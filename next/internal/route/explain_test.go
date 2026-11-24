package route

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func lastError(t *testing.T, evs []llm.Event) *llm.Error {
	t.Helper()
	last := evs[len(evs)-1]
	if last.Type != llm.EventError {
		t.Fatalf("request ended with %+v, want an error", last)
	}
	return last.Error
}

// A public ID is the same for one key and one account ID, differs between accounts and between
// keys, and holds nothing of the ID.
func TestPublicID(t *testing.T) {
	s := NewState()
	s.SetPublicKey([]byte("key-one"))
	id := s.PublicID("jane@example.com")
	if id != s.PublicID("jane@example.com") || id == s.PublicID("jane@example.org") {
		t.Errorf("public IDs %q, %q", id, s.PublicID("jane@example.org"))
	}
	if len(id) != len("acc_")+8 || !strings.HasPrefix(id, "acc_") || strings.ContainsAny(id, "@.") {
		t.Errorf("public ID %q", id)
	}
	other := NewState()
	other.SetPublicKey([]byte("key-two"))
	if other.PublicID("jane@example.com") == id {
		t.Error("two keys give one public ID")
	}
}

// An error that ends a request says what it tried, by public ID, provider, upstream model and a
// result code, and which accounts it left out and why. It never holds the upstream's words.
func TestErrorExplainsTheRequest(t *testing.T) {
	h := newHarness(t, Config{}, accts("a@example.com", "b", "c")...)
	h.on("a@example.com", rateLimited(10*time.Minute))
	h.on("c", rateLimited(30*time.Minute))
	h.on("b", fail(llm.Error{Code: llm.CodeUpstream, Status: 502, Retryable: true, Scope: llm.ScopeEndpoint, Message: "SECRET upstream words",
		Raw: []byte(`{"error":"SECRET raw"}`)}))
	e := lastError(t, h.run(h.req("m")))
	x := e.Explain
	if x == nil {
		t.Fatal("the error has no explanation")
	}
	pub := h.r.state.PublicID
	if len(x.Tried) < 2 || x.Tried[0].Account != pub("a@example.com") || x.Tried[0].Result != llm.CodeRateLimit || x.Tried[1].Result != llm.CodeUpstream {
		t.Errorf("tried = %+v", x.Tried)
	}
	for _, tr := range x.Tried {
		if tr.Model != "m" {
			t.Errorf("tried %+v, want the upstream model m", tr)
		}
	}
	if b, _ := jsonOf(x); strings.Contains(b, "SECRET") || strings.Contains(b, "example.com") {
		t.Errorf("the explanation holds upstream words or an account ID: %s", b)
	}
	// The log's copy names the accounts by their config IDs; the client's holds none of them.
	if e.Routed == nil || len(e.Routed.Tried) < 2 || e.Routed.Tried[0].ID != "a@example.com" || e.Routed.Tried[1].ID != "b" || e.Routed.Tried[0].Account != pub("a@example.com") {
		t.Errorf("explanation for the log = %+v, want config IDs beside the public ones", e.Routed)
	}
	for _, tr := range x.Tried {
		if tr.ID != "" {
			t.Errorf("the client's explanation carries the config ID of %+v", tr)
		}
	}

	// Next request: every account is blocked until its reset.
	e = lastError(t, h.run(h.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 0 || len(e.Explain.LeftOut) != 3 {
		t.Fatalf("explanation = %+v", e.Explain)
	}
	for _, l := range e.Explain.LeftOut {
		if l.State != HealthCooling && l.State != HealthExhausted || l.Until == nil || !l.Until.After(h.now) {
			t.Errorf("left out %+v, want a block that ends later", l)
		}
		if l.ID != "" {
			t.Errorf("the client's explanation carries the config ID of %+v", l)
		}
	}
	var ids []string
	for _, l := range e.Routed.LeftOut {
		ids = append(ids, l.ID)
	}
	if slices.Sort(ids); !slices.Equal(ids, []string{"a@example.com", "b", "c"}) {
		t.Errorf("the log's left_out names accounts %v, want their config IDs", ids)
	}
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// A banned account is named as banned where the request found no account to try.
func TestBannedAccountIsLeftOut(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential}))
	e := lastError(t, h.run(h.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 1 || e.Explain.Tried[0].Result != llm.CodeAccountClosed {
		t.Fatalf("first request: %+v", e.Explain)
	}
	e = lastError(t, h.run(h.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 0 || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].State != HealthBanned {
		t.Errorf("second request: %+v", e.Explain)
	}
}

// An account at its concurrency limit is left out as busy, and one the request's limits kept from
// its turn as skipped.
func TestBusyAndSkippedAccounts(t *testing.T) {
	accounts := accts("a", "b")
	accounts[0].MaxConcurrency = 1
	h := newHarness(t, Config{MaxAttempts: 1}, accounts...)
	h.on("b", rateLimited(0))
	// A request holds a's only slot while another fails on b.
	hold := h.r.Generate(context.Background(), h.req("m"))
	defer hold.Close()
	hold.Next()
	e := lastError(t, h.run(h.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 1 || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].State != LeftBusy {
		t.Errorf("explanation = %+v", e.Explain)
	}
	// With a free, MaxAttempts 1 stops the request after its first failed attempt.
	h2 := newHarness(t, Config{MaxAttempts: 1, Strategy: StrategyFillFirst}, accts("x", "y")...)
	h2.on("x", rateLimited(time.Second))
	e = lastError(t, h2.run(h2.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 1 || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].State != LeftSkipped {
		t.Errorf("explanation = %+v", e.Explain)
	}
}

// routing.explain false gives errors no explanation for the client; core's log still has it.
func TestExplainOff(t *testing.T) {
	off := false
	h := newHarness(t, Config{Explain: &off}, accts("a")...)
	h.on("a", rateLimited(time.Minute))
	e := lastError(t, h.run(h.req("m")))
	if e.Explain != nil {
		t.Errorf("explanation = %+v with routing.explain false", e.Explain)
	}
	if e.Routed == nil || e.Routed.Model != "m" || len(e.Routed.Tried) != 1 || e.Routed.Tried[0].Result != llm.CodeRateLimit {
		t.Errorf("explanation for the log = %+v, want the model and one rate_limit attempt", e.Routed)
	}
	// The model asked for is for the log: the client's `pluxy` member is as it was.
	var m map[string]json.RawMessage
	if b, _ := json.Marshal(e.Routed); json.Unmarshal(b, &m) != nil || len(m) != 2 || m["tried"] == nil || m["left_out"] == nil {
		t.Errorf("explanation as JSON has members %v, want tried and left_out only", m)
	}
}

// The error an attempt gave is not changed: routing state may hold it.
func TestExplainingCopiesTheError(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(time.Minute))
	_ = h.run(h.req("m"))
	rt := h.r.state.Runtime(Account{ID: "a"}, h.now)
	if rt.LastError == nil || rt.LastError.Explain != nil {
		t.Errorf("the error routing state holds carries an explanation: %+v", rt.LastError)
	}
}

// A pin may name the account by the public ID a reply gave.
func TestPinByPublicID(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	req := h.req("m")
	req.Pin = h.r.state.PublicID("b")
	h.run(req)
	h.wantAttempts("b")
}

// The attempt a request announces carries the public ID.
func TestAttemptingNamesThePublicID(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	var got Attempt
	req := h.req("m")
	req.Attempting = func(a Attempt) { got = a }
	h.run(req)
	if got.Account != "a" || got.Public != h.r.state.PublicID("a") || got.Model != "m" {
		t.Errorf("attempt = %+v", got)
	}
}

func planIDs(p Plan) []string {
	var out []string
	for _, s := range p.Order {
		out = append(out, s.ID)
	}
	return out
}

// A plan lists the accounts in the order requests would take them, with the same priority and
// strategy rules, and asking changes nothing: the requests that follow take that order.
func TestPlanFollowsTheRouter(t *testing.T) {
	accounts := accts("a", "b", "c", "d")
	accounts[2].Priority = 5
	h := newHarness(t, Config{}, accounts...)
	p := h.r.Plan(context.Background(), "m")
	if got, want := planIDs(p), []string{"c", "d", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if again := planIDs(h.r.Plan(context.Background(), "m")); !slices.Equal(again, planIDs(p)) {
		t.Errorf("a second plan = %v, the first %v: asking moved the rotation", again, planIDs(p))
	}
	h.runOK("m")
	h.wantAttempts("c") // the plan's first
	// With c blocked, round robin takes turns among the rest, from the account after the last.
	h.on("c", rateLimited(time.Hour))
	_ = h.run(h.req("m")) // c fails, d serves
	p = h.r.Plan(context.Background(), "m")
	if got, want := planIDs(p), []string{"a", "b", "d"}; !slices.Equal(got, want) {
		t.Errorf("order after turns = %v, want %v", got, want)
	}
	if len(p.LeftOut) != 1 || p.LeftOut[0].ID != "c" || p.LeftOut[0].State != HealthCooling || p.LeftOut[0].Until == nil {
		t.Errorf("left out = %+v", p.LeftOut)
	}
	if p.Order[0].Account != h.r.state.PublicID("a") || p.Order[0].Model != "m" {
		t.Errorf("first step = %+v", p.Order[0])
	}
	h.attempts = nil
	h.runOK("m")
	h.wantAttempts("a")
}

// A result code an extension made up reads as "error": nothing an extension chose goes to a
// client.
func TestResultCode(t *testing.T) {
	for code, want := range map[string]string{llm.CodeQuota: "quota", llm.CodeAuth: "auth", "made-up": "error", "": "error"} {
		if got := ResultCode(&llm.Error{Code: code}); got != want {
			t.Errorf("ResultCode(%q) = %q, want %q", code, got, want)
		}
	}
}

// Accounts a person turned off or that wait for a login are named in what a plan leaves out, when
// their own models say they would have served it.
func TestPlanNamesHeldAccounts(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a"}, Account{ID: "off", Models: []ModelEntry{{Name: "m"}}, Held: HealthDisabled},
		Account{ID: "other", Models: []ModelEntry{{Name: "other-model"}}, Held: HealthNeedsLogin})
	p := h.r.Plan(context.Background(), "m")
	if len(p.LeftOut) != 1 || p.LeftOut[0].ID != "off" || p.LeftOut[0].State != HealthDisabled {
		t.Errorf("left out = %+v", p.LeftOut)
	}
}

// A model that every account serving it holds says why: turned off, waiting for a new sign-in, or
// a mix of both. A pinned request names only the pinned account. The code, status and provider
// code of the reply do not change, and a model no held account serves keeps today's message.
func TestHeldModelErrorSaysWhy(t *testing.T) {
	served := func(id string, held string) Account {
		return Account{ID: id, Models: []ModelEntry{{Name: "m"}}, Held: held}
	}
	for _, c := range []struct {
		name     string
		accounts []Account
		pin      string
		want     string
	}{
		{"all turned off", []Account{served("a", HealthDisabled), served("b", HealthDisabled)}, "",
			`no account can serve model "m": every account that serves it is turned off`},
		{"all need a sign-in", []Account{served("a", HealthNeedsLogin)}, "",
			`no account can serve model "m": every account that serves it needs a new sign-in`},
		{"a mix", []Account{served("a", HealthDisabled), served("b", HealthNeedsLogin)}, "",
			`no account can serve model "m": every account that serves it is turned off or needs a new sign-in`},
		{"pinned turned off", []Account{served("a", HealthDisabled), served("b", HealthNeedsLogin)}, "a",
			`account "a" cannot serve model "m": it is turned off`},
		{"pinned needs a sign-in", []Account{served("a", HealthDisabled), served("b", HealthNeedsLogin)}, "b",
			`account "b" cannot serve model "m": it needs a new sign-in`},
		{"no held account serves it", []Account{{ID: "a", Models: []ModelEntry{{Name: "other"}}}}, "",
			`no account serves model "m"`},
		{"pinned account that does not exist", []Account{served("a", HealthDisabled)}, "nobody",
			`account "nobody" does not exist or does not serve model "m"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{}, c.accounts...)
			req := h.req("m")
			req.Pin = c.pin
			e := lastError(t, h.run(req))
			if e.Message != c.want {
				t.Errorf("message = %q, want %q", e.Message, c.want)
			}
			if e.Code != llm.CodeNotFound || e.ProviderCode != "model_not_found" {
				t.Errorf("code %q, provider code %q; want %q and model_not_found", e.Code, e.ProviderCode, llm.CodeNotFound)
			}
		})
	}
}
