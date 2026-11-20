package route

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Fallback chains: a request for a chain's name goes through its steps in order, each a model
// with, optionally, one provider and named accounts. With no chain, a name is served by the
// accounts of one provider.

const (
	claudeExt = "pluxy/claude"
	codexExt  = "pluxy/codex"
)

// chainAccounts are two Claude accounts serving opus and sonnet and one Codex account serving
// sol.
func chainAccounts() []Account {
	claude := []ModelEntry{{Name: "opus"}, {Name: "sonnet"}}
	return []Account{
		{ID: "c1", Extension: claudeExt, Models: claude},
		{ID: "c2", Extension: claudeExt, Models: claude},
		{ID: "x1", Extension: codexExt, Models: []ModelEntry{{Name: "sol"}}},
	}
}

func opusChain() map[string][]Step {
	return map[string][]Step{"opus": {{Model: "opus", Provider: claudeExt}, {Model: "sol"}}}
}

// chainHarness is a harness whose requests record each attempt's step and model.
type chainHarness struct {
	*harness
	steps []Attempt
}

func newChainHarness(t *testing.T, cfg Config, accounts ...Account) *chainHarness {
	return &chainHarness{harness: newHarness(t, cfg, accounts...)}
}

func (h *chainHarness) req(model, session string) Request {
	r := h.harness.req(model)
	r.Session = session
	r.Attempting = func(a Attempt) { h.steps = append(h.steps, a) }
	return r
}

// served sends a request and returns the attempt that served it, failing the test unless it
// finished.
func (h *chainHarness) served(model, session string) Attempt {
	h.t.Helper()
	evs := h.run(h.req(model, session))
	if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
		h.t.Fatalf("request for %s failed: %+v", model, last.Error)
	}
	return h.steps[len(h.steps)-1]
}

// A chain's steps are tried in order: the first step's accounts while one can serve, the next
// step once none can, and the request names the step's model upstream. Moving on needs no wait.
func TestChainStepOrder(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain(), MaxWaitMS: 600_000, Strategy: StrategyFillFirst}, chainAccounts()...)
	if a := h.served("opus", ""); a.Account != "c1" || a.Model != "opus" || a.Step != 1 {
		t.Errorf("first request served by %+v, want c1 with opus at step 1", a)
	}
	h.on("c1", rateLimited(time.Minute))
	h.on("c2", rateLimited(time.Minute))
	if a := h.served("opus", ""); a.Account != "x1" || a.Model != "sol" || a.Step != 2 || a.Extension != codexExt {
		t.Errorf("with Claude refusing: served by %+v, want x1 with sol at step 2", a)
	}
	// Both Claude accounts are blocked now: straight to step 2, without waiting for them.
	if a := h.served("opus", ""); a.Account != "x1" {
		t.Errorf("with Claude blocked: served by %+v, want x1", a)
	}
	if len(h.slept) != 0 {
		t.Errorf("slept %v; a step that can serve now is used at once", h.slept)
	}
	// Once Claude is back, step 1 serves again.
	h.now = h.now.Add(2 * time.Minute)
	h.on("c1", ok())
	h.on("c2", ok())
	if a := h.served("opus", ""); a.Step != 1 {
		t.Errorf("after the block: served by %+v, want step 1", a)
	}
	h.wantAttempts("c1", "c1", "c2", "x1", "x1", "c1")
}

// When no step can serve now, the request waits for the first account of any step to come back,
// as a request for a model waits for its accounts.
func TestChainWaitsForAnyStep(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain(), MaxWaitMS: 600_000}, chainAccounts()...)
	h.on("c1", rateLimited(time.Minute), ok())
	h.on("c2", rateLimited(time.Minute), ok())
	h.on("x1", rateLimited(30*time.Second), ok())
	a := h.served("opus", "")
	if a.Account != "x1" || len(h.slept) != 1 || h.slept[0] < 30*time.Second || h.slept[0] >= time.Minute {
		t.Errorf("served by %+v after sleeping %v, want x1 after its 30s", a, h.slept)
	}
}

// Once the reply has started, a failure goes to the client: no other account and no other
// step.
func TestChainNoMoveAfterReplyStarted(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain()}, Account{ID: "c1", Extension: claudeExt}, Account{ID: "x1", Extension: codexExt})
	limit := llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 60_000}
	h.on("c1", script{start(), {Type: llm.EventError, Error: &limit}})
	evs := h.run(h.req("opus", ""))
	if len(evs) != 2 || evs[0].Type != llm.EventResponseStart || evs[1].Error == nil || evs[1].Error.Code != llm.CodeRateLimit {
		t.Fatalf("events = %+v, want the reply's start and then its error", evs)
	}
	h.wantAttempts("c1")
}

// An error about the request itself (a 400, a refusal of its content) ends the request: the
// next step would refuse it too.
func TestChainNoMoveForRequestError(t *testing.T) {
	for _, e := range []llm.Error{
		{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Retryable: true},
		{Code: llm.CodeAccountClosed, Status: 403, Scope: llm.ScopeRequest, Retryable: true},
		{Code: llm.CodeUpstream, Status: 500, Scope: llm.ScopeEndpoint}, // the upstream may have run it
	} {
		t.Run(e.Code, func(t *testing.T) {
			h := newChainHarness(t, Config{Chains: opusChain()}, Account{ID: "c1", Extension: claudeExt}, Account{ID: "x1", Extension: codexExt})
			h.on("c1", fail(e))
			evs := h.run(h.req("opus", ""))
			if got := evs[0].Error; got == nil || got.Code != e.Code {
				t.Errorf("got %+v, want the %s error", evs[0], e.Code)
			}
			h.wantAttempts("c1")
		})
	}
}

// A chain has at most 8 steps, at least one, and its steps name models, never another chain. A
// step may name the chain's own name: the model of that name, as the first step of a chain that
// keeps a model's name usually does.
func TestChainLimits(t *testing.T) {
	steps := func(n int) []Step {
		var out []Step
		for i := range n {
			out = append(out, Step{Model: fmt.Sprintf("m%d", i)})
		}
		return out
	}
	for _, c := range []struct {
		name   string
		chains map[string][]Step
		want   string // a word of the error; "" for none
	}{
		{"eight steps", map[string][]Step{"x": steps(8)}, ""},
		{"nine steps", map[string][]Step{"x": steps(9)}, "8"},
		{"no steps", map[string][]Step{"x": {}}, "step"},
		{"empty model", map[string][]Step{"x": {{Model: " "}}}, "model"},
		{"empty name", map[string][]Step{"": steps(1)}, "name"},
		{"own name", map[string][]Step{"opus": {{Model: "opus"}, {Model: "sol"}}}, ""},
		{"another chain", map[string][]Step{"fast": {{Model: "sol"}}, "best": {{Model: "opus"}, {Model: "fast"}}}, "chain"},
		{"chains naming each other", map[string][]Step{"opus": {{Model: "opus"}, {Model: "sol"}}, "sol": {{Model: "sol"}, {Model: "opus"}}}, "chain"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{Chains: c.chains}
			err := cfg.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Errorf("Validate: %v, want no error", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("Validate: %v, want an error naming %q", err, c.want)
			}
			if _, err := New(cfg, nil, nil, nil); (err != nil) != (c.want != "") {
				t.Errorf("New: %v", err)
			}
		})
	}
}

// A session bound to an account of a later step stays there while it can serve, even once an
// earlier step could serve again: its conversation keeps the prompt cache it has there.
// Requests without the session go back to step 1.
func TestChainAffinityAcrossSteps(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain()}, chainAccounts()...)
	h.on("c1", rateLimited(time.Minute), ok())
	h.on("c2", rateLimited(time.Minute), ok())
	if a := h.served("opus", "s1"); a.Account != "x1" || a.Step != 2 {
		t.Fatalf("served by %+v, want x1 at step 2", a)
	}
	h.now = h.now.Add(2 * time.Minute)
	if a := h.served("opus", "s1"); a.Account != "x1" || a.Step != 2 {
		t.Errorf("the session's next request: served by %+v, want x1 at step 2", a)
	}
	if a := h.served("opus", ""); a.Step != 1 {
		t.Errorf("a request without the session: served by %+v, want step 1", a)
	}
	// When the bound account cannot serve, the session moves and is bound again.
	h.on("x1", rateLimited(time.Minute))
	if a := h.served("opus", "s1"); a.Step != 1 {
		t.Errorf("with x1 refusing: served by %+v, want step 1", a)
	}
	bound := h.steps[len(h.steps)-1].Account
	if a := h.served("opus", "s1"); a.Account != bound {
		t.Errorf("after moving: served by %+v, want %s", a, bound)
	}
}

// One account can be in two steps with two models. A session bound to it at step 2 stays on
// step 2's model.
func TestChainAffinityKeepsTheStepsModel(t *testing.T) {
	chains := map[string][]Step{"opus": {{Model: "opus"}, {Model: "sonnet"}}}
	h := newChainHarness(t, Config{Chains: chains}, Account{ID: "c1", Extension: claudeExt, Models: []ModelEntry{{Name: "opus"}, {Name: "sonnet"}}})
	h.on("c1", fail(llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: 60_000}), ok())
	if a := h.served("opus", "s1"); a.Account != "c1" || a.Model != "sonnet" || a.Step != 2 {
		t.Fatalf("served by %+v, want c1 with sonnet at step 2", a)
	}
	h.now = h.now.Add(2 * time.Minute)
	if a := h.served("opus", "s1"); a.Model != "sonnet" {
		t.Errorf("the session's next request: served by %+v, want sonnet", a)
	}
	if a := h.served("opus", ""); a.Model != "opus" {
		t.Errorf("a request without the session: served by %+v, want opus", a)
	}
}

// A step can name its provider and its accounts: only those serve it.
func TestChainStepProviderAndAccounts(t *testing.T) {
	accounts := append(chainAccounts(), Account{ID: "o1", Extension: "pluxy/openai-compatible", Models: []ModelEntry{{Name: "opus"}}, Priority: 5})
	chains := map[string][]Step{"best": {{Model: "opus", Accounts: []string{"c2"}}, {Model: "opus", Provider: "pluxy/openai-compatible"}}}
	h := newChainHarness(t, Config{Chains: chains}, accounts...)
	if a := h.served("best", ""); a.Account != "c2" {
		t.Errorf("served by %+v, want c2, the step's one account", a)
	}
	h.on("c2", rateLimited(time.Minute))
	if a := h.served("best", ""); a.Account != "o1" || a.Step != 2 {
		t.Errorf("with c2 refusing: served by %+v, want o1 at step 2", a)
	}
}

// With no chain, a name is served by one provider's accounts: the provider of the
// highest-priority account that serves it. When they cannot serve, the request fails; it does
// not go to another provider that serves the same name.
func TestDefaultIsOneProvider(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	h := newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m}, Account{ID: "q1", Extension: "Q", Models: m, Priority: 1},
		Account{ID: "q2", Extension: "Q", Models: m})
	if a := h.served("m", ""); a.Account != "q1" || a.Step != 0 {
		t.Errorf("served by %+v, want q1 (the highest priority) and no step", a)
	}
	h.on("q1", rateLimited(time.Minute))
	if a := h.served("m", ""); a.Account != "q2" {
		t.Errorf("with q1 refusing: served by %+v, want q2, the provider's other account", a)
	}
	h.on("q2", rateLimited(time.Minute))
	evs := h.run(h.req("m", ""))
	if e := evs[0].Error; e == nil || e.Code != llm.CodeRateLimit {
		t.Errorf("with Q's accounts refusing: %+v, want the rate limit", evs[0])
	}
	if slices.Contains(h.attempts, "p1") {
		t.Errorf("attempts %v went to P, another provider", h.attempts)
	}
}

// The model lists describe a name two providers list as the model of the provider that serves it,
// with that provider's accounts.
func TestModelListNamesTheServingProvider(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	h := newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m}, Account{ID: "q1", Extension: "Q", Models: m, Priority: 1},
		Account{ID: "p2", Extension: "P", Models: m, Priority: 1}, Account{ID: "q2", Extension: "Q", Models: m})
	models := h.r.ServedModels(context.Background())
	if len(models) != 1 || models[0].Extension != "Q" || !slices.Equal(models[0].Accounts, []string{"q1", "q2"}) {
		t.Errorf("models = %+v, want m on Q with q1 and q2", models)
	}
}

// A priority tie goes to the provider of the account first in config order.
func TestDefaultProviderTieByConfigOrder(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	h := newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m}, Account{ID: "q1", Extension: "Q", Models: m},
		Account{ID: "p2", Extension: "P", Models: m})
	for range 4 {
		h.served("m", "")
	}
	h.wantAttempts("p1", "p2", "p1", "p2")
}

// An account whose model list cannot be read serves any model, but it does not choose the
// provider of a name another account lists: it would take every name from the providers that
// list them.
func TestDefaultProviderFromAccountsThatListTheModel(t *testing.T) {
	h := newChainHarness(t, Config{}, Account{ID: "u1", Extension: "U"}, Account{ID: "k1", Extension: "K", Models: []ModelEntry{{Name: "m"}}})
	h.r.UseCatalog(func(context.Context, Account, *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "list unreadable"}
	})
	for range 2 {
		if a := h.served("m", ""); a.Account != "k1" {
			t.Errorf("served by %+v, want k1, the account that lists m", a)
		}
	}
	// A name no account lists goes to the accounts that serve any model.
	if a := h.served("other", ""); a.Account != "u1" {
		t.Errorf("other served by %+v, want u1", a)
	}
}

// GET /route's plan: each step's accounts in the order they would be tried.
func TestChainPlan(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain(), Strategy: StrategyFillFirst}, chainAccounts()...)
	h.on("c2", rateLimited(time.Minute))
	h.on("c1", rateLimited(time.Minute), ok())
	h.served("opus", "")
	h.now = h.now.Add(30 * time.Second)
	p := h.r.Plan(context.Background(), "opus")
	if len(p.Chain) != 2 || p.Chain[1].Model != "sol" {
		t.Errorf("plan chain = %+v, want the chain's two steps", p.Chain)
	}
	var order []string
	for _, s := range p.Order {
		order = append(order, fmt.Sprintf("%d:%s:%s", s.Step, s.ID, s.Model))
	}
	if want := []string{"2:x1:sol"}; !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
	var left []string
	for _, l := range p.LeftOut {
		left = append(left, fmt.Sprintf("%d:%s:%s:%s", l.Step, l.ID, l.Model, l.State))
	}
	if want := []string{"1:c1:opus:cooling", "1:c2:opus:cooling"}; !slices.Equal(left, want) {
		t.Errorf("left_out = %v, want %v", left, want)
	}
	// A name with no chain has one step and no step numbers.
	p = h.r.Plan(context.Background(), "sol")
	if len(p.Chain) != 0 || len(p.Order) != 1 || p.Order[0].Step != 0 {
		t.Errorf("plan for sol = %+v", p)
	}
}

// The error of a chain that no step could serve names each step's accounts with their
// provider and model.
func TestChainErrorNamesEachStep(t *testing.T) {
	h := newChainHarness(t, Config{Chains: opusChain()}, chainAccounts()...)
	h.on("c1", rateLimited(time.Minute))
	h.on("c2", rateLimited(time.Minute))
	h.on("x1", rateLimited(time.Minute))
	h.run(h.req("opus", ""))
	evs := h.run(h.req("opus", ""))
	e := evs[0].Error
	if e == nil || e.Explain == nil {
		t.Fatalf("got %+v, want an error that explains", evs[0])
	}
	var left []string
	for _, l := range e.Explain.LeftOut {
		left = append(left, l.Provider+":"+l.Model+":"+l.State)
	}
	if want := []string{claudeExt + ":opus:cooling", claudeExt + ":opus:cooling", codexExt + ":sol:cooling"}; !slices.Equal(left, want) {
		t.Errorf("left_out = %v, want %v", left, want)
	}
}

// A chain's name may be new: it is then in the model lists, as its first served step's model.
func TestChainNameInModelList(t *testing.T) {
	chains := map[string][]Step{"my-fast": {{Model: "nobody-serves"}, {Model: "sol"}}, "opus": {{Model: "opus"}, {Model: "sol"}}}
	h := newChainHarness(t, Config{Chains: chains}, chainAccounts()...)
	var ids []string
	for _, m := range h.r.ServedModels(context.Background()) {
		ids = append(ids, m.ID)
		if m.ID == "my-fast" && (m.Extension != codexExt || m.Upstream != "sol") {
			t.Errorf("my-fast listed as %+v, want sol on %s", m, codexExt)
		}
	}
	if want := []string{"my-fast", "opus", "sol", "sonnet"}; !slices.Equal(ids, want) {
		t.Errorf("models = %v, want %v", ids, want)
	}
	if a := h.served("my-fast", ""); a.Account != "x1" || a.Step != 2 {
		t.Errorf("my-fast served by %+v, want x1 at step 2", a)
	}
}

// CheckSteps refuses a step no account can serve, naming it.
func TestCheckSteps(t *testing.T) {
	h := newChainHarness(t, Config{}, chainAccounts()...)
	for _, c := range []struct {
		steps []Step
		bad   string
	}{
		{[]Step{{Model: "opus"}, {Model: "sol"}}, ""},
		{[]Step{{Model: "opus", Provider: claudeExt, Accounts: []string{"c2"}}}, ""},
		{[]Step{{Model: "opus"}, {Model: "nobody-serves"}}, "nobody-serves"},
		{[]Step{{Model: "sol", Provider: claudeExt}}, "sol"},
		{[]Step{{Model: "opus", Accounts: []string{"x1"}}}, "opus"},
		{[]Step{{Model: "opus", Accounts: []string{"no-such-account"}}}, "opus"},
	} {
		err := h.r.CheckSteps(context.Background(), c.steps)
		if (err != nil) != (c.bad != "") || err != nil && !strings.Contains(err.Error(), c.bad) {
			t.Errorf("CheckSteps(%+v) = %v, want an error naming %q", c.steps, err, c.bad)
		}
	}
}

// The model lists name the provider and accounts routing uses: a same-provider account of higher
// priority than another provider's top account keeps the name on its provider, and the entry
// lists that provider's accounts, the one routing tries first leading.
func TestModelListAgreesWithRouting(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	h := newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m}, Account{ID: "p2", Extension: "P", Models: m, Priority: 5},
		Account{ID: "q1", Extension: "Q", Models: m, Priority: 3})
	a := h.served("m", "")
	if a.Account != "p2" || a.Extension != "P" {
		t.Fatalf("served by %+v, want p2 on P", a)
	}
	models := h.r.ServedModels(context.Background())
	if len(models) != 1 || models[0].Extension != "P" || !slices.Equal(models[0].Accounts, []string{"p2", "p1"}) {
		t.Errorf("models = %+v, want m on P with p2 and p1", models)
	}
}

// heldHarness has p1, an account of P that serves no requests (held as state), and q1, a usable
// account of Q that also lists m. p1 has the higher priority, or the same and comes first.
func heldHarness(t *testing.T, state string, priority int) *chainHarness {
	m := []ModelEntry{{Name: "m"}}
	return newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m, Priority: priority, Held: state},
		Account{ID: "q1", Extension: "Q", Models: m})
}

// An account that is out of service keeps its name on its provider: the request fails and names
// it, rather than going to another provider that lists the name. Each held kind alike.
func TestHeldAccountKeepsTheProvider(t *testing.T) {
	for _, c := range []struct {
		state    string
		priority int
	}{{HealthNeedsLogin, 1}, {HealthDisabled, 1}, {HealthNeedsLogin, 0}} {
		state := c.state
		h := heldHarness(t, state, c.priority)
		evs := h.run(h.req("m", ""))
		e := evs[len(evs)-1].Error
		if e == nil || e.Explain == nil {
			t.Errorf("%s: got %+v, want an error that explains", state, evs[len(evs)-1])
			continue
		}
		if len(h.attempts) != 0 {
			t.Errorf("%s: attempts %v, want none: Q is another provider", state, h.attempts)
		}
		if l := e.Explain.LeftOut; len(l) != 1 || l[0].Account != h.r.state.PublicID("p1") || l[0].State != state {
			t.Errorf("%s: left_out = %+v, want p1 %s", state, l, state)
		}
		for _, sm := range h.r.ServedModels(context.Background()) {
			if sm.ID == "m" {
				t.Errorf("%s: model list has m as %+v; requests for it go to P, which cannot serve now", state, sm)
			}
		}
	}
}

// A held account keeps the model list read while it was in service, and its extension is not
// asked again while it is held.
func TestHeldAccountKeepsTheModelListReadInService(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	accounts := []Account{
		{ID: "p1", Extension: "P", Managed: true, Priority: 1},
		{ID: "q1", Extension: "Q", Models: m},
	}
	h := newChainHarness(t, Config{}, accounts...)
	h.r.UseCredentials(&fakeCredentials{version: 3})
	reads := 0
	h.r.UseCatalog(func(_ context.Context, a Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		reads++
		if a.ID == "p1" && credential != nil {
			return &protocol.ModelList{Models: []protocol.Model{{ID: "m"}}}, nil
		}
		return &protocol.ModelList{Models: []protocol.Model{{ID: "other"}}}, nil
	})
	h.runOK("m")
	h.wantAttempts("p1")
	if reads != 1 {
		t.Fatalf("catalog reads = %d, want 1 with p1 in service", reads)
	}

	accounts[0].Held = HealthDisabled
	if err := h.r.SetAccounts(accounts, func(old, cur *Account) bool { return string(old.Settings) == string(cur.Settings) }); err != nil {
		t.Fatal(err)
	}
	known, creds := h.r.knownSnapshot()
	var held *Account
	for _, a := range known {
		if a.ID == "p1" {
			held = a
			break
		}
	}
	if held == nil {
		t.Fatal("p1 is not among the known accounts")
	}
	list := h.r.listOf(context.Background(), held, creds, h.r.catalogFunc(), h.now)
	if list == nil || !slices.Equal(modelIDs(list), []string{"m"}) {
		t.Errorf("held account's list = %+v, want the [m] read in service", list)
	}
	if reads != 1 {
		t.Errorf("catalog reads = %d after p1 became held, want its kept list and no new read", reads)
	}

	h.attempts = nil
	evs := h.run(h.req("m", ""))
	e := evs[len(evs)-1].Error
	if e == nil || e.Explain == nil || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].Account != h.r.state.PublicID("p1") ||
		e.Explain.LeftOut[0].State != HealthDisabled {
		t.Errorf("request = %+v, want the held p1 named as disabled", evs[len(evs)-1])
	}
	h.wantAttempts()
}

// A banned account keeps its name on its provider too.
func TestBannedAccountKeepsTheProvider(t *testing.T) {
	m := []ModelEntry{{Name: "m"}}
	h := newChainHarness(t, Config{}, Account{ID: "p1", Extension: "P", Models: m, Priority: 1}, Account{ID: "q1", Extension: "Q", Models: m})
	h.on("p1", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential}))
	h.run(h.req("m", ""))
	evs := h.run(h.req("m", ""))
	e := evs[len(evs)-1].Error
	if e == nil || e.Explain == nil {
		t.Fatalf("got %+v, want an error that explains", evs[len(evs)-1])
	}
	if !slices.Equal(h.attempts, []string{"p1"}) {
		t.Errorf("attempts %v, want p1 once and never q1", h.attempts)
	}
	if l := e.Explain.LeftOut; len(l) != 1 || l[0].State != HealthBanned {
		t.Errorf("left_out = %+v, want p1 banned", l)
	}
}

// Two steps that resolve to the same account and model try it once, even when its refusal set no
// block.
func TestOverlappingStepsTryAnAccountOnce(t *testing.T) {
	h := newChainHarness(t, Config{DisableCooling: true, Chains: map[string][]Step{"m": {{Model: "m", Provider: "A"}, {Model: "m"}}}},
		Account{ID: "a1", Extension: "A", Models: []ModelEntry{{Name: "m"}}})
	h.on("a1", rateLimited(time.Minute))
	h.run(h.req("m", ""))
	h.wantAttempts("a1")
}

// A chain may not repeat a step.
func TestChainRefusesIdenticalSteps(t *testing.T) {
	for _, steps := range [][]Step{
		{{Model: "m"}, {Model: "m"}},
		{{Model: "m", Provider: "A", Accounts: []string{"a", "b"}}, {Model: "s"}, {Model: "m", Provider: "A", Accounts: []string{"b", "a"}}},
	} {
		if err := ValidateChain("c", steps, nil); err == nil || !strings.Contains(err.Error(), "step") {
			t.Errorf("ValidateChain(%+v) = %v, want an error", steps, err)
		}
	}
	if err := ValidateChain("c", []Step{{Model: "m", Provider: "A"}, {Model: "m"}}, nil); err != nil {
		t.Errorf("steps that differ: %v", err)
	}
}

// A chain's entry in the model lists has the accounts of each step as routing finds them.
func TestChainModelListFollowsTheSteps(t *testing.T) {
	accounts := []Account{{ID: "p1", Extension: "P", Models: []ModelEntry{{Name: "m"}}, Priority: 1},
		{ID: "q1", Extension: "Q", Models: []ModelEntry{{Name: "m"}, {Name: "s"}}}}
	list := func(chains map[string][]Step) map[string]ServedModel {
		h := newChainHarness(t, Config{Chains: chains}, accounts...)
		got := map[string]ServedModel{}
		for _, sm := range h.r.ServedModels(context.Background()) {
			got[sm.ID] = sm
		}
		return got
	}
	// A new name whose step names a provider other than the model's default one.
	if my := list(map[string][]Step{"my": {{Model: "m", Provider: "Q"}}})["my"]; my.Extension != "Q" || !slices.Equal(my.Accounts, []string{"q1"}) {
		t.Errorf("my = %+v, want Q with q1", my)
	}
	// A chain named like a model it serves.
	if m := list(map[string][]Step{"m": {{Model: "m"}, {Model: "s"}}})["m"]; m.Extension != "P" || !slices.Equal(m.Accounts, []string{"p1", "q1"}) {
		t.Errorf("m = %+v, want P with p1 and q1 (step 2, s)", m)
	}
}

// The Codex client's catalogue marks a chain that can reach another provider non_codex, and has
// a new chain name whose steps Codex serves.
func TestCodexNamesKnowChains(t *testing.T) {
	names := func(chains map[string][]Step) map[string]Name {
		h := newChainHarness(t, Config{Chains: chains}, chainAccounts()...)
		got := map[string]Name{}
		for _, n := range h.r.ModelNames(context.Background(), codexExt) {
			got[n.Public] = n
		}
		return got
	}
	if n, ok := names(map[string][]Step{"sol": {{Model: "sol"}, {Model: "opus"}}})["sol"]; !ok || !n.NonCodex {
		t.Errorf("sol = %+v, %v; want non_codex: its chain goes on to Claude", n, ok)
	}
	got := names(map[string][]Step{"my-sol": {{Model: "sol"}}, "my-mix": {{Model: "opus"}, {Model: "sol"}}, "my-opus": {{Model: "opus"}}})
	if n, ok := got["my-sol"]; !ok || n.NonCodex || n.Upstream != "sol" {
		t.Errorf("my-sol = %+v, %v; want listed, as sol, and not non_codex", n, ok)
	}
	if n, ok := got["my-mix"]; !ok || !n.NonCodex || n.Upstream != "sol" {
		t.Errorf("my-mix = %+v, %v; want listed, as sol, and non_codex", n, ok)
	}
	if n, ok := got["my-opus"]; ok {
		t.Errorf("my-opus = %+v; Codex serves no step of it", n)
	}
}
