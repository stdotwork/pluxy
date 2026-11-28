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

func TestPublicIDOfCoreMadeID(t *testing.T) {
	s := NewState()
	s.SetPublicKey([]byte("key-one"))
	if got := s.PublicID("acc_1a2b3c4d"); got != "acc_1a2b3c4d" {
		t.Errorf("PublicID(acc_1a2b3c4d) = %q, want it unchanged", got)
	}
	for _, id := range []string{"jane@example.com", "acc_1A2B3C4D", "acc_1a2b3c4", "acc_1a2b3c4d5", "acc_1a2b3c4g", "xacc_1a2b3c4d"} {
		got := s.PublicID(id)
		if got == id || len(got) != len("acc_")+8 || !strings.HasPrefix(got, "acc_") {
			t.Errorf("PublicID(%q) = %q, want a digest", id, got)
		}
	}
}

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
	if e.Routed == nil || len(e.Routed.Tried) < 2 || e.Routed.Tried[0].ID != "a@example.com" || e.Routed.Tried[1].ID != "b" || e.Routed.Tried[0].Account != pub("a@example.com") {
		t.Errorf("explanation for the log = %+v, want config IDs beside the public ones", e.Routed)
	}
	for _, tr := range x.Tried {
		if tr.ID != "" {
			t.Errorf("the client's explanation carries the config ID of %+v", tr)
		}
	}

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

func TestBusyAndSkippedAccounts(t *testing.T) {
	accounts := accts("a", "b")
	accounts[0].MaxConcurrency = 1
	h := newHarness(t, Config{MaxAttempts: 1}, accounts...)
	h.on("b", rateLimited(0))
	hold := h.r.Generate(context.Background(), h.req("m"))
	defer hold.Close()
	hold.Next()
	e := lastError(t, h.run(h.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 1 || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].State != LeftBusy {
		t.Errorf("explanation = %+v", e.Explain)
	}
	h2 := newHarness(t, Config{MaxAttempts: 1, Strategy: StrategyFillFirst}, accts("x", "y")...)
	h2.on("x", rateLimited(time.Second))
	e = lastError(t, h2.run(h2.req("m")))
	if e.Explain == nil || len(e.Explain.Tried) != 1 || len(e.Explain.LeftOut) != 1 || e.Explain.LeftOut[0].State != LeftSkipped {
		t.Errorf("explanation = %+v", e.Explain)
	}
}

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
	var m map[string]json.RawMessage
	if b, _ := json.Marshal(e.Routed); json.Unmarshal(b, &m) != nil || len(m) != 2 || m["tried"] == nil || m["left_out"] == nil {
		t.Errorf("explanation as JSON has members %v, want tried and left_out only", m)
	}
}

func TestExplainingCopiesTheError(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(time.Minute))
	_ = h.run(h.req("m"))
	rt := h.r.state.Runtime(Account{ID: "a"}, h.now)
	if rt.LastError == nil || rt.LastError.Explain != nil {
		t.Errorf("the error routing state holds carries an explanation: %+v", rt.LastError)
	}
}

func TestPinByPublicID(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	req := h.req("m")
	req.Pin = h.r.state.PublicID("b")
	h.run(req)
	h.wantAttempts("b")
}

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
	h.wantAttempts("c")
	h.on("c", rateLimited(time.Hour))
	_ = h.run(h.req("m"))
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

func TestResultCode(t *testing.T) {
	for code, want := range map[string]string{llm.CodeQuota: "quota", llm.CodeAuth: "auth", "made-up": "error", "": "error"} {
		if got := ResultCode(&llm.Error{Code: code}); got != want {
			t.Errorf("ResultCode(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestPlanNamesHeldAccounts(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a"}, Account{ID: "off", Models: []ModelEntry{{Name: "m"}}, Held: HealthDisabled},
		Account{ID: "other", Models: []ModelEntry{{Name: "other-model"}}, Held: HealthNeedsLogin})
	p := h.r.Plan(context.Background(), "m")
	if len(p.LeftOut) != 1 || p.LeftOut[0].ID != "off" || p.LeftOut[0].State != HealthDisabled {
		t.Errorf("left out = %+v", p.LeftOut)
	}
}

func TestNotServedModelSaysWhatIsServed(t *testing.T) {
	var many []ModelEntry
	for _, n := range []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9", "m10"} {
		many = append(many, ModelEntry{Name: n})
	}
	for _, c := range []struct {
		name     string
		accounts []Account
		want     string
	}{
		{"served models are named", []Account{{ID: "a", Models: []ModelEntry{{Name: "fake-gpt-1"}, {Name: "fake-gpt-mini"}}}},
			`no account serves model "claude-opus-5-5". pluxy serves: fake-gpt-1, fake-gpt-mini. Set the tool's model to one of them, or add a model alias to an account so that "claude-opus-5-5" goes to one of them.`},
		{"a long list is cut", []Account{{ID: "a", Models: many}},
			`no account serves model "claude-opus-5-5". pluxy serves: m1, m10, m2, m3, m4, m5, m6, m7 and 2 more (pluxy model list shows them all). Set the tool's model to one of them, or add a model alias to an account so that "claude-opus-5-5" goes to one of them.`},
		{"no account serves anything", nil,
			`no account serves model "claude-opus-5-5". pluxy serves no models yet: add an account first.`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{}, c.accounts...)
			e := lastError(t, h.run(h.req("claude-opus-5-5")))
			if e.Message != c.want {
				t.Errorf("message = %q, want %q", e.Message, c.want)
			}
			if e.Code != llm.CodeNotFound || e.ProviderCode != "model_not_found" {
				t.Errorf("code %q, provider code %q; want %q and model_not_found", e.Code, e.ProviderCode, llm.CodeNotFound)
			}
		})
	}
}

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
			`no account serves model "m". pluxy serves: other. Set the tool's model to one of them, or add a model alias to an account so that "m" goes to one of them.`},
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
