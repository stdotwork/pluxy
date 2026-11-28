package route

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type script []llm.Event

type fakeStream struct {
	events script
	i      int
	closed bool
}

func (s *fakeStream) Next() llm.Event {
	ev := s.events[min(s.i, len(s.events)-1)]
	s.i++
	return ev
}

func (s *fakeStream) Close() { s.closed = true }

type harness struct {
	t         *testing.T
	r         *Router
	now       time.Time
	slept     []time.Duration
	attempts  []string
	scripts   map[string][]script
	onAttempt func(id string)
}

func newHarness(t *testing.T, cfg Config, accounts ...Account) *harness {
	h := &harness{t: t, now: time.Unix(1_700_000_000, 0), scripts: map[string][]script{}}
	r, err := New(cfg, accounts, h.attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return h.now }
	r.sleep = func(_ context.Context, d time.Duration) error {
		h.slept = append(h.slept, d)
		h.now = h.now.Add(d)
		return nil
	}
	r.rand = func() float64 { return 0 }
	r.state.rand = func() float64 { return 0 }
	h.r = r
	return h
}

func (h *harness) attempt(_ context.Context, _ string, a protocol.Account, _ llm.Request) Stream {
	h.attempts = append(h.attempts, a.ID)
	if h.onAttempt != nil {
		h.onAttempt(a.ID)
	}
	ss := h.scripts[a.ID]
	if len(ss) == 0 {
		return &fakeStream{events: ok()}
	}
	s := ss[0]
	if len(ss) > 1 {
		h.scripts[a.ID] = ss[1:]
	}
	return &fakeStream{events: s}
}

func (h *harness) on(id string, replies ...script) { h.scripts[id] = replies }

func (h *harness) req(model string) Request { return Request{LLM: llm.Request{Model: model}} }

func (h *harness) run(req Request) []llm.Event {
	s := h.r.Generate(context.Background(), req)
	defer s.Close()
	var out []llm.Event
	for {
		ev := s.Next()
		out = append(out, ev)
		if ev.Terminal() {
			return out
		}
	}
}

func (h *harness) runOK(m string) {
	h.t.Helper()
	evs := h.run(h.req(m))
	if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
		h.t.Fatalf("request failed: %+v", last.Error)
	}
}

func (h *harness) wantAttempts(want ...string) {
	h.t.Helper()
	if !slices.Equal(h.attempts, want) {
		h.t.Errorf("attempts = %v, want %v", h.attempts, want)
	}
}

func start() llm.Event {
	return llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m"}}
}

func ok() script {
	return script{
		start(),
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "hi"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
}

func fail(e llm.Error) script { return script{{Type: llm.EventError, Error: &e}} }

func rateLimited(retryAfter time.Duration) script {
	return fail(llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: retryAfter.Milliseconds()})
}

func accts(ids ...string) []Account {
	out := make([]Account, len(ids))
	for i, id := range ids {
		out[i] = Account{ID: id}
	}
	return out
}

func TestRoundRobinFollowsLastAccount(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b", "c")...)
	h.runOK("m")
	h.on("b", rateLimited(10*time.Second), ok())
	h.runOK("m")
	h.runOK("m")
	h.now = h.now.Add(11 * time.Second)
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "c", "a", "b", "c")
}

func TestRoundRobinPerModel(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.runOK("m1")
	h.runOK("m2")
	h.runOK("m1")
	h.wantAttempts("a", "a", "b")
}

func TestWeighted(t *testing.T) {
	w := func(n int) *int { return &n }
	h := newHarness(t, Config{Strategy: StrategyWeighted},
		Account{ID: "a", Weight: w(5)}, Account{ID: "b", Weight: w(1)}, Account{ID: "c", Weight: w(1)}, Account{ID: "off", Weight: w(0)})
	for range 7 {
		h.runOK("m")
	}
	h.wantAttempts("a", "a", "b", "a", "c", "a", "a")
}

func TestFillFirst(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.runOK("m")
	h.runOK("m")
	h.on("a", rateLimited(time.Minute))
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "a", "a", "b", "b")
}

func TestLeastActive(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyLeastActive}, accts("a", "b")...)
	held := h.r.Generate(context.Background(), h.req("m"))
	held.Next()
	h.runOK("m")
	h.runOK("m")
	held.Close()
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "a")
}

func TestPriority(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "low"}, Account{ID: "high", Priority: 1})
	h.runOK("m")
	h.runOK("m")
	h.on("high", rateLimited(time.Minute))
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("high", "high", "high", "low", "low")
}

func TestCapabilityAndPin(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a", Models: []ModelEntry{{Name: "m1"}}}, Account{ID: "b", Models: []ModelEntry{{Name: "m2"}}}, Account{ID: "c"})
	h.runOK("m2")
	req := h.req("m1")
	req.Pin = "c"
	h.run(req)
	req.Pin = "b"
	evs := h.run(req)
	if e := evs[0].Error; e == nil || e.Code != llm.CodeNotFound {
		t.Errorf("pin to an account without the model: %+v, want not_found", evs[0])
	}
	h.wantAttempts("b", "c")
}

func TestPinnedAccountBlocked(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", rateLimited(time.Minute))
	req := h.req("m")
	req.Pin = "a"
	h.run(req)
	evs := h.run(req)
	if e := evs[0].Error; e == nil || e.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want a rate_limit error", evs[0])
	}
	h.wantAttempts("a")
}

func TestNoRetryAfterReplyStarted(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	limit := llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 60_000}
	h.on("a", script{start(), {Type: llm.EventError, Error: &limit}})
	evs := h.run(h.req("m"))
	if len(evs) != 2 || evs[0].Type != llm.EventResponseStart || evs[1].Error == nil || evs[1].Error.Code != llm.CodeRateLimit {
		t.Fatalf("events = %+v, want the reply's start and then its error", evs)
	}
	h.wantAttempts("a")
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b")
}

func TestNotRetrySafe(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeUpstream, Status: 500, Scope: llm.ScopeEndpoint}))
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.Status != 500 {
		t.Errorf("got %+v, want the upstream 500", evs[0])
	}
	h.wantAttempts("a")
}

func TestTimeoutRotatesOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		e    llm.Error
	}{
		{"extension_timeout", llm.Error{Code: llm.CodeExtensionTimeout, Retryable: true, Scope: llm.ScopeEndpoint, Message: "the extension sent nothing for 6m0s"}},
		{"upstream header timeout", llm.Error{Code: llm.CodeUnavailable, Retryable: true, Scope: llm.ScopeEndpoint, Message: `upstream unreachable: Post "https://u.example/v1": upstream response header timeout after 1m0s`}},
		{"upstream request timeout", llm.Error{Code: llm.CodeUnavailable, Retryable: true, Scope: llm.ScopeEndpoint, Message: `upstream unreachable: Post "https://u.example/v1": upstream request timeout after 10m0s`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{}, accts("a", "b", "c", "d")...)
			for _, id := range []string{"a", "b", "c", "d"} {
				h.on(id, fail(tc.e))
			}
			evs := h.run(h.req("m"))
			if last := evs[len(evs)-1]; last.Error == nil || last.Error.Code != tc.e.Code || last.Error.Message != tc.e.Message {
				t.Errorf("got %+v, want the timeout error", last)
			}
			if len(h.attempts) != 2 {
				t.Errorf("attempts = %v, want exactly 2", h.attempts)
			}
		})
	}
}

func TestExtensionTimeoutBlocksTheEndpoint(t *testing.T) {
	kind, scope, fixed, base, max := classify(&llm.Error{Code: llm.CodeExtensionTimeout, Retryable: true})
	if kind != blockCooldown || scope != scopeEndpoint || fixed != 0 || base != failureBackoffBase || max != failureBackoffMax {
		t.Errorf("classify = %q %q %v %v %v, want a cooldown of the endpoint on the failure backoff", kind, scope, fixed, base, max)
	}
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeExtensionTimeout, Retryable: true}), ok())
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b")
}

func TestOtherRetrySafeFailuresTryEveryAccount(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b", "c")...)
	down := fail(llm.Error{Code: llm.CodeUnavailable, Retryable: true, Scope: llm.ScopeEndpoint, Message: "upstream unreachable: dial tcp: connection refused"})
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, down)
	}
	h.run(h.req("m"))
	h.wantAttempts("a", "b", "c")

	h = newHarness(t, Config{}, accts("a", "b", "c")...)
	h.on("a", fail(llm.Error{Code: llm.CodeExtensionTimeout, Retryable: true, Scope: llm.ScopeEndpoint}))
	h.on("b", down)
	h.runOK("m")
	h.wantAttempts("a", "b", "c")
}

func TestRequestErrorLeavesAccountAlone(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Retryable: true}), ok())
	h.run(h.req("m"))
	h.runOK("m")
	h.wantAttempts("a", "a")
}

func TestUnsupportedAccountPassesRequestOn(t *testing.T) {
	refuse := fail(llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "other format"})
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b", "c")...)
	h.on("a", refuse)
	h.on("b", refuse)
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "c", "a", "b", "c")

	h = newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", refuse)
	h.on("b", refuse)
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.Code != llm.CodeUnsupported || e.Message != "other format" {
		t.Errorf("got %+v, want the refusal", evs[0])
	}
	h.wantAttempts("a", "b")

	h = newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", refuse)
	h.on("b", refuse)
	req := h.req("m")
	req.Endpoint = true
	evs = h.run(req)
	if e := evs[0].Error; e == nil || e.Code != llm.CodeUnavailable || e.Status != http.StatusServiceUnavailable ||
		e.ProviderCode != "auth_not_found" || e.Message != "no auth available" {
		t.Errorf("got %+v, want 503 auth_not_found: no auth available", evs[0])
	}
	h.wantAttempts("a", "b")

	featureRefuse := fail(llm.Error{Code: llm.CodeUnsupported, Status: http.StatusBadRequest, Scope: llm.ScopeCredential, Retryable: true, Message: "input: token arrays unsupported"})
	h = newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", featureRefuse)
	req = h.req("m")
	req.Endpoint = true
	if evs = h.run(req); len(evs) == 0 || evs[0].Type == llm.EventError {
		t.Fatalf("got %+v, want b to serve after a refused feature", evs)
	}
	h.wantAttempts("a", "b")

	h = newHarness(t, Config{}, accts("a")...)
	h.on("a", featureRefuse)
	req = h.req("m")
	req.Endpoint = true
	evs = h.run(req)
	if e := evs[0].Error; e == nil || e.Code != llm.CodeUnsupported || e.Status != http.StatusBadRequest || e.Message != "input: token arrays unsupported" {
		t.Errorf("got %+v, want the 400 unsupported refusal preserved", evs[0])
	}
	h.wantAttempts("a")
}

func TestOlderSuccessKeepsNewerCooldown(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	older := h.r.Generate(context.Background(), h.req("m"))
	older.Next()
	h.on("a", rateLimited(time.Minute))
	h.runOK("m")
	for ev := older.Next(); !ev.Terminal(); ev = older.Next() {
	}
	older.Close()
	h.runOK("m")
	h.wantAttempts("a", "a", "b", "b")
}

func TestStaleFailureAfterNewerSuccess(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	quota := llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 3_600_000}
	h.on("a", script{start(), {Type: llm.EventError, Error: &quota}}, ok())
	older := h.r.Generate(context.Background(), h.req("m"))
	if ev := older.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event = %+v", ev)
	}
	h.runOK("m")
	if ev := older.Next(); ev.Type != llm.EventError {
		t.Fatalf("older request ended with %+v, want its quota error", ev)
	}
	older.Close()
	if rt := h.r.state.Runtime(Account{ID: "a"}, h.now); len(rt.Cooldowns) != 0 || rt.LastError != nil {
		t.Errorf("a stale failure left %+v", rt)
	}
	h.runOK("m")
	h.wantAttempts("a", "a", "a")
	if n := len(h.r.state.scopes); n != 0 {
		t.Errorf("state holds %d scopes once no older attempt runs, want none", n)
	}
}

func TestStaleFailureOnlyOnScopesTheSuccessCovers(t *testing.T) {
	st := NewState()
	a, b := &Account{ID: "a", Endpoint: "e"}, &Account{ID: "b", Endpoint: "e"}
	now := time.Unix(1_800_000_000, 0)
	notFound := &llm.Error{Code: llm.CodeNotFound, Scope: llm.ScopeModel}
	down := &llm.Error{Code: llm.CodeUnavailable, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}

	older, _ := st.reserve(a, "m1", now)
	newer, _ := st.reserve(a, "m2", now)
	st.success(a, "m2", newer)
	st.end(a, newer)
	st.failure(a, "m1", older, notFound, now, 0)
	st.end(a, older)
	if until, _, _ := st.availability(a, "m1", now); until.IsZero() {
		t.Error("an older failure on m1 was turned away by a success on m2")
	}
	if rt := st.Runtime(*a, now); rt.LastError != nil {
		t.Errorf("last error = %+v, want none: a newer attempt on the account succeeded", rt.LastError)
	}

	older, _ = st.reserve(a, "m2", now)
	other, _ := st.reserve(b, "m2", now)
	st.success(b, "m2", other)
	st.end(b, other)
	st.failure(a, "m2", older, down, now, 0)
	st.end(a, older)
	if until, _, _ := st.availability(a, "m2", now); !until.IsZero() {
		t.Errorf("an older endpoint failure blocked the endpoint until %v after a newer success on it", until)
	}
}

func TestStaleFailureMarkLifetime(t *testing.T) {
	st := NewState()
	a := &Account{ID: "a"}
	now := time.Unix(1_800_000_000, 0)
	older, _ := st.reserve(a, "m", now)
	for range 1000 {
		seq, _ := st.reserve(a, "m", now)
		st.success(a, "m", seq)
		st.end(a, seq)
	}
	if n := len(st.order); n > 2*len(st.running)+64 {
		t.Errorf("%d attempt numbers kept for %d running attempts", n, len(st.running))
	}
	later := now.Add(stateIdle + 2*sweepEvery)
	st.maybeSweep(later)
	st.failure(a, "m", older, &llm.Error{Code: llm.CodeRateLimit, Retryable: true, RetryAfterMS: 60_000}, later, 0)
	st.end(a, older)
	if until, _, _ := st.availability(a, "m", later); !until.IsZero() {
		t.Errorf("a stale failure blocked the account after the sweep, until %v", until)
	}
	st.maybeSweep(later.Add(2 * sweepEvery))
	if n := len(st.scopes); n != 0 {
		t.Errorf("state holds %d scopes once no older attempt runs, want none", n)
	}
}

func TestOutcomesUnderRace(t *testing.T) {
	st := NewState()
	accounts := []*Account{{ID: "a", Endpoint: "e"}, {ID: "b", Endpoint: "e"}}
	now := time.Unix(1_800_000_000, 0)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				a := accounts[(g+i)%2]
				model := fmt.Sprintf("m%d", i%3)
				seq, ok := st.reserve(a, model, now)
				if !ok {
					st.Reset(*a, now)
					continue
				}
				if i%4 == 0 {
					st.failure(a, model, seq, &llm.Error{Code: llm.CodeUpstream, Retryable: true}, now, -1)
				} else {
					st.success(a, model, seq)
				}
				st.end(a, seq)
			}
		}()
	}
	wg.Wait()
	if len(st.running) != 0 || len(st.active) != 0 || st.oldest() != 0 {
		t.Errorf("after every attempt ended: running %d, active %v, oldest %d", len(st.running), st.active, st.oldest())
	}
}

func TestNewerSuccessClearsBlock(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", rateLimited(0), ok())
	h.runOK("m")
	h.now = h.now.Add(time.Second)
	h.runOK("m")
	if n := len(h.r.state.scopes); n != 0 {
		t.Errorf("state holds %d scopes after the success, want none", n)
	}
	h.wantAttempts("a", "b", "a")
}

func TestBackoffWithoutRetryAfter(t *testing.T) {
	st := NewState()
	st.rand = func() float64 { return 0 }
	a := &Account{ID: "a"}
	now := time.Unix(0, 0)
	e := &llm.Error{Code: llm.CodeRateLimit, Retryable: true}
	var got []time.Duration
	for range 3 {
		st.failure(a, "m", 0, e, now, 0)
		until, _, _ := st.availability(a, "m", now)
		got = append(got, until.Sub(now))
		now = until
	}
	st.failure(a, "m", 0, e, now, 0)
	st.failure(a, "m", 0, e, now, 0)
	until, _, _ := st.availability(a, "m", now)
	got = append(got, until.Sub(now))
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("block lengths = %v, want %v", got, want)
	}
}

func TestScopes(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, Account{ID: "a", Endpoint: "e"}, Account{ID: "b", Endpoint: "e"}, Account{ID: "c"})
	h.on("a", fail(llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: 60_000}), ok())
	h.runOK("m1")
	h.runOK("m2")
	h.wantAttempts("a", "b", "a")

	h.attempts = nil
	h.on("a", fail(llm.Error{Code: llm.CodeUnavailable, Status: 503, Retryable: true, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}))
	h.runOK("m2")
	h.wantAttempts("a", "c")

	h.attempts = nil
	h.now = h.now.Add(2 * time.Minute)
	h.on("c", fail(llm.Error{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential}))
	h.on("a", ok())
	h.run(Request{LLM: llm.Request{Model: "m1"}, Pin: "c"})
	evs := h.run(Request{LLM: llm.Request{Model: "m2"}, Pin: "c"})
	if e := evs[0].Error; e == nil || e.ProviderCode != "model_cooldown" {
		t.Errorf("got %+v, want model_cooldown", evs[0])
	}
	h.wantAttempts("c")
}

func TestRateLimitWithoutScopeBlocksTheModel(t *testing.T) {
	for _, code := range []string{llm.CodeRateLimit, llm.CodeQuota} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
			h.on("a", fail(llm.Error{Code: code, Status: 429, Retryable: true, RetryAfterMS: 60_000}), ok())
			h.runOK("m1")
			h.runOK("m2")
			h.wantAttempts("a", "b", "a")
		})
	}
}

func TestAllBlocked(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(30*time.Second))
	h.run(h.req("m"))
	h.attempts = nil
	evs := h.run(h.req("m"))
	e := evs[0].Error
	if e == nil || e.Code != llm.CodeRateLimit || e.Status != 429 || e.RetryAfterMS != 30_000 {
		t.Errorf("got %+v, want 429 with retry_after_ms 30000", e)
	}
	h.wantAttempts()
}

func TestWaitHonoursRetryAfter(t *testing.T) {
	h := newHarness(t, Config{MaxWaitMS: 10_000}, accts("a")...)
	h.on("a", rateLimited(5*time.Second), ok())
	h.runOK("m")
	h.wantAttempts("a", "a")
	if !slices.Equal(h.slept, []time.Duration{5 * time.Second}) {
		t.Errorf("slept %v, want [5s]", h.slept)
	}
}

func TestWaitLongerThanBudgetFails(t *testing.T) {
	h := newHarness(t, Config{MaxWaitMS: 2_000}, accts("a")...)
	h.on("a", rateLimited(5*time.Second), ok())
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.Code != llm.CodeRateLimit || e.RetryAfterMS != 5_000 {
		t.Errorf("got %+v, want the 429 with retry_after_ms 5000", evs[0])
	}
	h.wantAttempts("a")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want no wait", h.slept)
	}
}

func TestAttemptAndAccountLimits(t *testing.T) {
	refuse := rateLimited(time.Minute)
	h := newHarness(t, Config{MaxAttempts: 2}, accts("a", "b", "c")...)
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, refuse)
	}
	h.run(h.req("m"))
	h.wantAttempts("a", "b")

	h = newHarness(t, Config{MaxAccounts: 1, MaxWaitMS: 120_000}, accts("a", "b")...)
	h.on("a", rateLimited(time.Second), ok())
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want a's rate limit", evs[0])
	}
	h.wantAttempts("a")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want no wait", h.slept)
	}
}

func TestMaxAccountsIsPerRound(t *testing.T) {
	refuse := rateLimited(0)
	h := newHarness(t, Config{MaxAccounts: 2, RequestRetry: rounds(1), DisableCooling: true}, accts("a", "b", "c")...)
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, refuse)
	}
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want the last rate limit", evs[0])
	}
	h.wantAttempts("a", "b", "c", "a")

	retry := func(id string, n *int) Account { return Account{ID: id, RequestRetry: n} }
	h = newHarness(t, Config{Strategy: StrategyFillFirst, MaxAccounts: 2, RequestRetry: rounds(1), DisableCooling: true},
		retry("a", rounds(2)), retry("b", rounds(0)), retry("c", nil))
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, refuse)
	}
	h.run(h.req("m"))
	h.wantAttempts("a", "b", "a", "c", "a")

	h = newHarness(t, Config{Strategy: StrategyFillFirst, MaxAccounts: 1, DisableCooling: true},
		retry("a", rounds(0)), retry("b", rounds(1)))
	h.on("a", refuse)
	h.on("b", ok())
	h.runOK("m")
	h.wantAttempts("a", "b")
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, Account{ID: "a", MaxConcurrency: 1}, Account{ID: "b", MaxConcurrency: 1})
	held := h.r.Generate(context.Background(), h.req("m"))
	held.Next()
	h.runOK("m")
	held2 := h.r.Generate(context.Background(), h.req("m"))
	held2.Next()
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.ProviderCode != "accounts_busy" {
		t.Errorf("got %+v, want accounts_busy", evs[0])
	}
	held.Close()
	held2.Close()
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "a")
	if n := h.r.state.activeCount("a") + h.r.state.activeCount("b"); n != 0 {
		t.Errorf("%d attempts still counted as active", n)
	}
}

func TestSessionAffinity(t *testing.T) {
	h := newHarness(t, Config{SessionAffinity: &AffinityConfig{}}, accts("a", "b")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	s2 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s2"}
	other := Request{LLM: llm.Request{Model: "m"}, Tenant: "u", Session: "s1"}
	h.run(s1)
	h.run(s2)
	h.run(s1)
	h.run(s2)
	h.run(other)
	h.wantAttempts("a", "b", "a", "b", "a")

	h.attempts = nil
	h.on("a", rateLimited(time.Minute))
	h.run(s1)
	h.now = h.now.Add(2 * time.Minute)
	h.run(s1)
	h.wantAttempts("a", "b", "b")
}

func affinityConfig(t *testing.T, routing string) Config {
	t.Helper()
	var cfg Config
	if err := json.Unmarshal([]byte(routing), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSessionAffinityDefault(t *testing.T) {
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	for name, c := range map[string]struct {
		routing string
		want    []string
	}{
		"no section":    {`{}`, []string{"a", "a", "a"}},
		"empty section": {`{"session_affinity": {}}`, []string{"a", "a", "a"}},
		"ttl only":      {`{"session_affinity": {"ttl_ms": 60000}}`, []string{"a", "a", "a"}},
		"enabled":       {`{"session_affinity": {"enabled": true}}`, []string{"a", "a", "a"}},
		"disabled":      {`{"session_affinity": {"enabled": false}}`, []string{"a", "b", "a"}},
		"disabled, ttl": {`{"session_affinity": {"enabled": false, "ttl_ms": 60000}}`, []string{"a", "b", "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, affinityConfig(t, c.routing), accts("a", "b")...)
			h.run(s1)
			h.run(s1)
			h.run(s1)
			h.wantAttempts(c.want...)
		})
	}
}

func TestSessionAffinityDefaultSticks(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	s2 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s2"}
	h.run(s1)
	h.run(s2)
	h.run(s1)
	h.run(s1)
	h.run(s2)
	h.wantAttempts("a", "b", "a", "a", "b")

	h.attempts = nil
	none := Request{LLM: llm.Request{Model: "m"}, Tenant: "t"}
	h.run(none)
	h.run(none)
	if len(h.attempts) != 2 || h.attempts[0] == h.attempts[1] {
		t.Errorf("sessionless attempts = %v, want two different accounts", h.attempts)
	}

	h.attempts = nil
	h.now = h.now.Add(time.Hour + time.Minute)
	h.run(s1)
	h.run(s1)
	if len(h.attempts) != 2 || h.attempts[0] != h.attempts[1] {
		t.Errorf("after the hour: attempts = %v, want one account", h.attempts)
	}
}

func TestSessionAffinityTTLKept(t *testing.T) {
	h := newHarness(t, affinityConfig(t, `{"session_affinity": {"ttl_ms": 60000}}`), accts("a", "b")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.run(s1)
	h.now = h.now.Add(30 * time.Second)
	h.run(s1)
	h.now = h.now.Add(2 * time.Minute)
	h.run(s1)
	h.wantAttempts("a", "a", "b")
}

type heldStream struct {
	n       int
	onClose func()
	closed  sync.Once
}

func (s *heldStream) Next() llm.Event {
	s.n++
	if s.n == 1 {
		return start()
	}
	return llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}
}

func (s *heldStream) Close() { s.closed.Do(s.onClose) }

func TestConcurrencyLimitHoldsUnderRace(t *testing.T) {
	var mu sync.Mutex
	active, peak := map[string]int{}, map[string]int{}
	attempt := func(_ context.Context, _ string, a protocol.Account, _ llm.Request) Stream {
		mu.Lock()
		active[a.ID]++
		peak[a.ID] = max(peak[a.ID], active[a.ID])
		mu.Unlock()
		return &heldStream{onClose: func() {
			mu.Lock()
			active[a.ID]--
			mu.Unlock()
		}}
	}
	r, err := New(Config{Strategy: StrategyFillFirst}, []Account{{ID: "a", MaxConcurrency: 1}, {ID: "b"}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	var barrier sync.WaitGroup
	barrier.Add(2)
	var arrivals atomic.Int32
	r.beforeReserve = func(string) {
		if arrivals.Add(1) <= 2 {
			barrier.Done()
			barrier.Wait()
		}
	}
	streams := make([]Stream, 2)
	var wg sync.WaitGroup
	for i := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			streams[i] = r.Generate(context.Background(), Request{LLM: llm.Request{Model: "m"}})
			if ev := streams[i].Next(); ev.Type != llm.EventResponseStart {
				t.Errorf("request %d: %+v", i, ev)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	gotA, gotB := peak["a"], peak["b"]
	mu.Unlock()
	if gotA != 1 || gotB != 1 {
		t.Errorf("peak attempts: a %d (max_concurrency 1), b %d; want 1 and 1", gotA, gotB)
	}
	for _, s := range streams {
		s.Close()
	}
}

func TestModelStateIsBounded(t *testing.T) {
	defer func(n int) { maxModelKeys = n }(maxModelKeys)
	maxModelKeys = 100
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(time.Hour))
	h.run(h.req("keep"))
	h.on("a", fail(llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: time.Hour.Milliseconds()}))
	h.now = h.now.Add(2 * time.Hour)
	h.run(h.req("keep"))
	notFound := fail(llm.Error{Code: llm.CodeNotFound, Status: 404, Retryable: true, Scope: llm.ScopeModel, ProviderCode: "model_not_found"})
	h.on("a", notFound)
	for i := range 1000 {
		h.run(h.req(fmt.Sprintf("no-such-model-%d", i)))
	}
	h.r.state.mu.Lock()
	n := len(h.r.state.scopes)
	h.r.state.mu.Unlock()
	h.r.picker.mu.Lock()
	cursors := len(h.r.picker.last)
	h.r.picker.mu.Unlock()
	if n > maxModelKeys+2 || cursors > maxModelKeys {
		t.Errorf("after 1,000 model names: %d scopes, %d model cursors; want at most %d", n, cursors, maxModelKeys)
	}
	if until, _, _ := h.r.state.availability(h.r.accounts[0], "keep", h.now); until.IsZero() {
		t.Error("the live cooldown on model \"keep\" was dropped")
	}

	h.now = h.now.Add(24 * time.Hour)
	h.on("a", ok())
	h.runOK("m")
	h.r.state.mu.Lock()
	n = len(h.r.state.scopes)
	h.r.state.mu.Unlock()
	h.r.picker.mu.Lock()
	cursors = len(h.r.picker.last)
	h.r.picker.mu.Unlock()
	if n != 0 || cursors > 1 {
		t.Errorf("24h later: %d scopes, %d model cursors; want 0 and at most 1", n, cursors)
	}
}

type fakeCredentials struct{ version uint64 }

func (f *fakeCredentials) Credential(context.Context, string, string) (*protocol.Credential, uint64, *llm.Error) {
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: fmt.Sprint("access-", f.version)}, f.version, nil
}

func (f *fakeCredentials) Rejected(_ context.Context, _ string, version uint64) bool {
	if version == f.version {
		f.version++
	}
	return true
}

func TestRenewedAccountIsTriedAgain(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a", Managed: true}, Account{ID: "b"})
	h.r.UseCredentials(&fakeCredentials{})
	h.on("a", fail(llm.Error{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential}), ok())
	h.on("b", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Message: "b rejected request"}))
	h.runOK("m")
	h.wantAttempts("a", "a")
}

func (h *harness) reload(cfg Config, accounts ...Account) {
	h.t.Helper()
	r, err := New(cfg, accounts, h.attempt, h.r.state)
	if err != nil {
		h.t.Fatal(err)
	}
	h.r.state.Retain(h.r, r, func(old, cur *Account) bool { return string(old.Settings) == string(cur.Settings) })
	r.now, r.sleep, r.rand = h.r.now, h.r.sleep, h.r.rand
	h.r = r
}

func withSettings(a Account, s string) Account { a.Settings = []byte(s); return a }

func TestRetainIgnoresOutcomesFromBeforeAChange(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", script{start(), rateLimited(time.Minute)[0]})
	older := h.r.Generate(context.Background(), h.req("m"))
	older.Next()
	h.reload(Config{Strategy: StrategyFillFirst}, withSettings(Account{ID: "a"}, `{"base_url":"elsewhere"}`), Account{ID: "b"})
	older.Next()
	older.Close()
	h.on("a", ok())
	h.runOK("m")
	h.wantAttempts("a", "a")
}

func TestRetainDropsWhatBelongedToChangedAccounts(t *testing.T) {
	ep := func(id, endpoint string) Account { return Account{ID: id, Endpoint: endpoint} }
	h := newHarness(t, Config{SessionAffinity: &AffinityConfig{}}, ep("a", "shared"), ep("b", "solo"))
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.run(s1)
	h.r.state.failure(h.r.accounts[0], "m", 0, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}, h.now, 0)

	c := ep("c", "shared")
	c.Priority = 1
	h.reload(Config{SessionAffinity: &AffinityConfig{}}, withSettings(ep("a", "shared"), `{"x":1}`), ep("b", "solo"), c)
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("c")

	big := 1_000_000_000
	w := func(id string, n int) Account { return Account{ID: id, Weight: &n} }
	h = newHarness(t, Config{Strategy: StrategyWeighted}, w("a", big), w("b", big))
	h.runOK("m")
	h.reload(Config{Strategy: StrategyWeighted}, w("a", 1), w("b", 1))
	h.attempts = nil
	for range 4 {
		h.runOK("m")
	}
	h.wantAttempts("a", "b", "a", "b")
}

func (h *harness) runOn(r *Router, req Request) []llm.Event {
	cur := h.r
	h.r = r
	defer func() { h.r = cur }()
	return h.run(req)
}

func TestOldRouterRecordsNothingOnChangedAccounts(t *testing.T) {
	cfg := Config{Strategy: StrategyFillFirst}
	h := newHarness(t, cfg, accts("a", "b")...)
	old := h.r
	h.on("a", rateLimited(time.Hour))
	h.on("b", rateLimited(time.Hour), ok())
	h.onAttempt = func(id string) {
		if id == "a" && h.r == old {
			h.reload(cfg, Account{ID: "a"}, withSettings(Account{ID: "b"}, `{"base_url":"elsewhere"}`))
		}
	}
	h.runOn(old, h.req("m"))
	h.onAttempt = nil
	h.attempts = nil
	h.runOK("m")
	h.wantAttempts("b")
}

func TestOldRouterBindsNoSessionToChangedAccounts(t *testing.T) {
	cfg := Config{SessionAffinity: &AffinityConfig{}}
	h := newHarness(t, cfg, accts("a", "b")...)
	old := h.r
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.onAttempt = func(id string) {
		if id == "a" && h.r == old {
			b := Account{ID: "b", Priority: 1}
			h.reload(cfg, withSettings(Account{ID: "a"}, `{"x":1}`), b)
		}
	}
	h.runOn(old, s1)
	h.onAttempt = nil
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("b")
}

func TestOldWeightsDoNotDisturbNewRotation(t *testing.T) {
	big := 1_000_000_000
	w := func(id string, n int) Account { return Account{ID: id, Weight: &n} }
	cfg := Config{Strategy: StrategyWeighted}
	h := newHarness(t, cfg, w("a", big), w("b", big))
	h.runOK("m")
	old := h.r
	h.reload(cfg, w("a", 1), w("b", 1))
	h.runOn(old, h.req("m"))
	h.attempts = nil
	for range 4 {
		h.runOK("m")
	}
	h.wantAttempts("a", "b", "a", "b")
}

func (h *harness) setAccounts(accounts ...Account) {
	h.t.Helper()
	if err := h.r.SetAccounts(accounts, func(old, cur *Account) bool { return string(old.Settings) == string(cur.Settings) }); err != nil {
		h.t.Fatal(err)
	}
}

func TestSetAccountsRestartsChangedAccounts(t *testing.T) {
	for _, between := range []struct {
		name string
		do   func(h *harness)
	}{
		{"nothing", func(*harness) {}},
		{"unchanged SetAccounts", func(h *harness) { h.setAccounts(accts("a", "b", "c")...) }},
		{"unchanged reload", func(h *harness) { h.reload(Config{Strategy: StrategyFillFirst}, accts("a", "b", "c")...) }},
	} {
		t.Run(between.name, func(t *testing.T) {
			h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b", "c")...)
			h.on("a", rateLimited(time.Hour))
			h.on("b", rateLimited(time.Hour))
			h.on("c", script{start(), rateLimited(time.Hour)[0]})
			older := h.r.Generate(context.Background(), h.req("m"))
			older.Next()
			h.wantAttempts("a", "b", "c")
			between.do(h)
			h.setAccounts(Account{ID: "a"}, withSettings(Account{ID: "b"}, `{"x":1}`), withSettings(Account{ID: "c"}, `{"x":1}`))
			older.Next()
			older.Close()
			h.on("b", rateLimited(time.Hour))
			h.on("c", ok())
			h.attempts = nil
			h.runOK("m")
			h.wantAttempts("b", "c")
		})
	}
}

func TestSetAccountsUnbindsChangedAccounts(t *testing.T) {
	h := newHarness(t, Config{SessionAffinity: &AffinityConfig{}}, accts("a", "b")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.run(s1)
	h.setAccounts(Account{ID: "a"}, Account{ID: "b", Priority: 1})
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("a")
	h.setAccounts(withSettings(Account{ID: "a"}, `{"x":1}`), Account{ID: "b", Priority: 1})
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("b")
}

func TestRequestAttempt(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	var own []string
	req := h.req("m")
	req.Attempt = func(_ context.Context, _ string, a protocol.Account, _ llm.Request) Stream {
		own = append(own, a.ID)
		if a.ID == "a" {
			return &fakeStream{events: rateLimited(time.Minute)}
		}
		return &fakeStream{events: ok()}
	}
	if evs := h.run(req); evs[len(evs)-1].Type != llm.EventFinish {
		t.Fatalf("request failed: %+v", evs[len(evs)-1])
	}
	if !slices.Equal(own, []string{"a", "b"}) {
		t.Errorf("own attempts = %v, want a then b", own)
	}
	h.runOK("m")
	h.wantAttempts("b")
}

func rounds(n int) *int { return &n }

func upstreamDown() script {
	return fail(llm.Error{Code: llm.CodeUpstream, Status: 503, Retryable: true, Scope: llm.ScopeEndpoint})
}

func TestRequestRetryRounds(t *testing.T) {
	h := newHarness(t, Config{RequestRetry: rounds(2), DisableCooling: true}, accts("a")...)
	h.on("a", rateLimited(0), rateLimited(0), ok())
	h.runOK("m")
	h.wantAttempts("a", "a", "a")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want no wait without blocks", h.slept)
	}

	h = newHarness(t, Config{RequestRetry: rounds(1), DisableCooling: true}, accts("a")...)
	h.on("a", rateLimited(0), rateLimited(0), ok())
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("after the last round: %+v, want the upstream's rate limit", evs[0])
	}
	h.wantAttempts("a", "a")

	h = newHarness(t, Config{DisableCooling: true}, accts("a")...)
	h.on("a", rateLimited(0), ok())
	h.run(h.req("m"))
	h.wantAttempts("a")
}

func TestRequestRetryStopsOnAuthAndNotFound(t *testing.T) {
	for _, e := range []llm.Error{
		{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential},
		{Code: llm.CodeNotFound, Status: 404, Retryable: true, Scope: llm.ScopeModel},
	} {
		h := newHarness(t, Config{RequestRetry: rounds(3), DisableCooling: true}, accts("a")...)
		h.on("a", fail(e), ok())
		h.run(h.req("m"))
		h.wantAttempts("a")
	}
}

func TestRequestRetryWaits(t *testing.T) {
	h := newHarness(t, Config{RequestRetry: rounds(2), MaxWaitMS: 3000, TransientErrorCooldownMS: 2000}, accts("a")...)
	h.on("a", upstreamDown(), upstreamDown(), ok())
	h.runOK("m")
	h.wantAttempts("a", "a", "a")
	if !slices.Equal(h.slept, []time.Duration{2 * time.Second, 2 * time.Second}) {
		t.Errorf("slept %v, want 2s before each round", h.slept)
	}

	h = newHarness(t, Config{RequestRetry: rounds(1), MaxWaitMS: 5000}, accts("a")...)
	h.on("a", rateLimited(time.Second), ok())
	h.run(h.req("m"))
	h.wantAttempts("a")

	h = newHarness(t, Config{RequestRetry: rounds(1), MaxWaitMS: 15000}, accts("a")...)
	h.on("a", rateLimited(time.Second), ok())
	h.runOK("m")
	h.wantAttempts("a", "a")
	if len(h.slept) != 1 || h.slept[0] != 10*time.Second {
		t.Errorf("slept %v, want the 10s floor", h.slept)
	}

	h = newHarness(t, Config{RequestRetry: rounds(0), MaxWaitMS: 60000}, accts("a")...)
	h.on("a", rateLimited(time.Second), ok())
	h.run(h.req("m"))
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.ProviderCode != "model_cooldown" || len(h.slept) != 0 {
		t.Errorf("blocked at the start: %+v, slept %v; want model_cooldown at once", evs[0], h.slept)
	}
}

func TestDisableCooling(t *testing.T) {
	off, on := true, false
	h := newHarness(t, Config{DisableCooling: true}, Account{ID: "a"}, Account{ID: "b", DisableCooling: &on})
	h.on("a", rateLimited(time.Minute), ok())
	h.on("b", rateLimited(time.Minute), ok())
	h.run(Request{LLM: llm.Request{Model: "m"}, Pin: "a"})
	h.run(Request{LLM: llm.Request{Model: "m"}, Pin: "b"})
	h.attempts = nil
	h.runOK("m")
	h.wantAttempts("a")
	if evs := h.run(Request{LLM: llm.Request{Model: "m"}, Pin: "b"}); evs[0].Error == nil {
		t.Errorf("account b, cooling on for it: %+v, want its block", evs[0])
	}

	h = newHarness(t, Config{}, Account{ID: "a", DisableCooling: &off})
	h.on("a", upstreamDown(), ok())
	h.run(h.req("m"))
	h.runOK("m")
}

func TestTransientErrorCooldown(t *testing.T) {
	h := newHarness(t, Config{TransientErrorCooldownMS: 5000, MaxWaitMS: 60000}, accts("a")...)
	h.on("a", upstreamDown(), ok())
	h.run(h.req("m"))
	h.runOK("m")
	if len(h.slept) != 1 || h.slept[0] < 5*time.Second || h.slept[0] > 5*time.Second+2*time.Second {
		t.Errorf("slept %v, want the 5s block", h.slept)
	}

	h = newHarness(t, Config{TransientErrorCooldownMS: -1}, accts("a")...)
	h.on("a", fail(llm.Error{Code: llm.CodeUpstream, Status: 503, Retryable: true, Scope: llm.ScopeEndpoint, RetryAfterMS: 60000}), ok())
	h.run(h.req("m"))
	h.runOK("m")

	h = newHarness(t, Config{TransientErrorCooldownMS: -1}, accts("a")...)
	h.on("a", rateLimited(time.Minute), ok())
	h.run(h.req("m"))
	if evs := h.run(h.req("m")); evs[0].Error == nil {
		t.Errorf("rate limit with transient cooldown off: %+v, want its block", evs[0])
	}
}

func TestDroppedBlockSetsNoLastError(t *testing.T) {
	defer func(n int) { maxModelKeys = n }(maxModelKeys)
	maxModelKeys = 1
	st := NewState()
	a := &Account{ID: "a"}
	now := time.Unix(1_800_000_000, 0)
	modelLimit := func(msg string) *llm.Error {
		return &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, RetryAfterMS: time.Hour.Milliseconds(), Message: msg}
	}
	st.failure(a, "m1", 0, modelLimit("m1 limited"), now, 0)
	st.failure(a, "m2", 0, modelLimit("m2 limited"), now, 0)
	if until, _, _ := st.availability(a, "m2", now); !until.IsZero() {
		t.Fatalf("m2 blocked until %v with the table full", until)
	}
	if rt := st.Runtime(*a, now); rt.LastError == nil || rt.LastError.Message != "m1 limited" {
		t.Errorf("last error = %+v, want m1's: m2's failure set no block", rt.LastError)
	}
}

func usageLimited(reset time.Duration) script {
	return fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, ProviderCode: "usage_limit_reached",
		Message: "The usage limit has been reached", RetryAfterMS: reset.Milliseconds(), QuotaWindowMinutes: 300,
		Raw: json.RawMessage(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)})
}

func TestUsageLimitWhenEveryAccountIsOutOfQuota(t *testing.T) {
	h := newHarness(t, Config{QuotaResponseMode: QuotaUsageLimit},
		Account{ID: "a", Extension: "pluxy/codex"}, Account{ID: "b", Extension: "pluxy/codex"})
	h.on("a", usageLimited(3*time.Hour))
	h.on("b", usageLimited(2*time.Hour))
	for i := range 2 {
		evs := h.run(h.req("gpt-x"))
		e := evs[0].Error
		if e == nil || e.UsageLimit == nil {
			t.Fatalf("request %d: %+v, want the usage-limit reply", i, evs[0])
		}
		u := e.UsageLimit
		if !u.ResetsAt.Equal(h.now.Add(2*time.Hour)) || u.WindowMinutes != 300 || u.Provider != "codex" || u.Model != "gpt-x" ||
			u.LastError != "usage_limit_reached: The usage limit has been reached" {
			t.Errorf("request %d: usage limit = %+v, want the first reset (b's), its window, provider codex and the last error", i, u)
		}
		if e.Retryable || e.Status != 429 || e.Code != llm.CodeQuota || e.RetryAfterMS != (2*time.Hour).Milliseconds() {
			t.Errorf("request %d: error = %+v", i, e)
		}
	}
	h.wantAttempts("a", "b")
}

func TestUsageLimitOnlyWhenEveryAccountQualifies(t *testing.T) {
	quotaNoReset := fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential})
	for _, tc := range []struct {
		name string
		mode string
		a, b script
	}{
		{"retryable mode", QuotaRetryable, usageLimited(2 * time.Hour), usageLimited(2 * time.Hour)},
		{"default mode", "", usageLimited(2 * time.Hour), usageLimited(2 * time.Hour)},
		{"near reset", QuotaUsageLimit, usageLimited(2 * time.Hour), usageLimited(5 * time.Minute)},
		{"one only rate-limited", QuotaUsageLimit, usageLimited(2 * time.Hour), rateLimited(2 * time.Hour)},
		{"quota without a reset", QuotaUsageLimit, usageLimited(2 * time.Hour), quotaNoReset},
		{"one serves", QuotaUsageLimit, usageLimited(2 * time.Hour), ok()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{QuotaResponseMode: tc.mode, Strategy: StrategyFillFirst}, accts("a", "b")...)
			h.on("a", tc.a)
			h.on("b", tc.b)
			for i := range 2 {
				evs := h.run(h.req("m"))
				if e := evs[0].Error; e != nil && e.UsageLimit != nil {
					t.Fatalf("request %d got the usage-limit reply: %+v", i, e.UsageLimit)
				}
			}
		})
	}
}

func TestUsageLimitAfterTheReplyStarted(t *testing.T) {
	chain := map[string][]Step{"m": {{Model: "m", Provider: "pluxy/codex"}, {Model: "m", Provider: "pluxy/openai-compatible"}}}
	h := newHarness(t, Config{QuotaResponseMode: QuotaUsageLimit, Strategy: StrategyFillFirst, Chains: chain},
		Account{ID: "a", Extension: "pluxy/codex"}, Account{ID: "b", Extension: "pluxy/openai-compatible"})
	h.r.state.failure(h.r.accounts[1], "m", 0, &llm.Error{Code: llm.CodeQuota, RetryAfterMS: time.Hour.Milliseconds(), Message: "used up"}, h.now, 0)
	quota := llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: (3 * time.Hour).Milliseconds(),
		ProviderCode: "usage_limit_reached", Message: "The usage limit has been reached"}
	h.on("a", script{start(), {Type: llm.EventError, Error: &quota}})
	evs := h.run(h.req("m"))
	last := evs[len(evs)-1]
	if last.Type != llm.EventError || last.Error.UsageLimit == nil {
		t.Fatalf("stream ended with %+v, want the usage-limit reply", last)
	}
	if u := last.Error.UsageLimit; u.Provider != "" || u.WindowMinutes != 0 || !u.ResetsAt.Equal(h.now.Add(time.Hour)) ||
		u.LastError != "usage_limit_reached: The usage limit has been reached" {
		t.Errorf("usage limit = %+v, want no provider, no window, b's reset and a's failure", u)
	}
	h.wantAttempts("a")
}

func TestErrorSummary(t *testing.T) {
	for _, tc := range []struct {
		e    llm.Error
		want string
	}{
		{llm.Error{ProviderCode: "rate_limit_exceeded", Message: "Rate limit reached"}, "rate_limit_exceeded: Rate limit reached"},
		{llm.Error{ProviderCode: "x", Message: "m", Raw: json.RawMessage(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)},
			"usage_limit_reached: The usage limit has been reached"},
		{llm.Error{Raw: json.RawMessage(`{"error":"plain text"}`)}, "plain text"},
		{llm.Error{ProviderCode: "quota", Message: "quota exceeded"}, "quota exceeded"},
		{llm.Error{ProviderCode: "only_code"}, "only_code"},
		{llm.Error{Message: strings.Repeat("é", 300)}, strings.Repeat("é", 253) + "..."},
		{llm.Error{ProviderCode: "x", Raw: json.RawMessage(`{"code":"quota_exceeded","message":"Quota exceeded"}`)}, "quota_exceeded: Quota exceeded"},
		{llm.Error{ProviderCode: "x", Raw: json.RawMessage(`{"error":{},"type":"overloaded","message":"Try later"}`)}, "overloaded: Try later"},
		{llm.Error{ProviderCode: "RESOURCE_EXHAUSTED", Message: "m", Raw: json.RawMessage(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`)},
			"429: Resource has been exhausted"},
		{llm.Error{ProviderCode: "x", Message: "kept", Raw: json.RawMessage(`[1]`)}, "x: kept"},
	} {
		if got := errorSummary(&tc.e); got != tc.want {
			t.Errorf("errorSummary(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestQuotaOutFollowsTodaysPrecedence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	quota := func(scope string, d time.Duration) *llm.Error {
		return &llm.Error{Code: llm.CodeQuota, Scope: scope, RetryAfterMS: d.Milliseconds()}
	}
	limit := func(scope string, d time.Duration) *llm.Error {
		return &llm.Error{Code: llm.CodeRateLimit, Scope: scope, RetryAfterMS: d.Milliseconds()}
	}
	for _, c := range []struct {
		name  string
		fails []*llm.Error
		until time.Duration
	}{
		{"model quota ends last", []*llm.Error{quota(llm.ScopeModel, 3*time.Hour), limit(llm.ScopeModel, time.Hour)}, 3 * time.Hour},
		{"rate limit ends last", []*llm.Error{quota(llm.ScopeModel, time.Hour), limit(llm.ScopeModel, 3*time.Hour)}, 0},
		{"rate limit ends as late", []*llm.Error{quota(llm.ScopeModel, 2*time.Hour), limit(llm.ScopeModel, 2*time.Hour)}, 0},
		{"endpoint limit ends as late", []*llm.Error{quota(llm.ScopeModel, 2*time.Hour), limit(llm.ScopeEndpoint, 2*time.Hour)}, 0},
		{"endpoint cooldown ends last", []*llm.Error{quota(llm.ScopeModel, 2*time.Hour), limit(llm.ScopeEndpoint, 3*time.Hour)}, 0},
		{"credential quota over a later model limit", []*llm.Error{quota(llm.ScopeCredential, 2*time.Hour), limit(llm.ScopeModel, 3*time.Hour)}, 2 * time.Hour},
		{"credential quota over a later model quota", []*llm.Error{quota(llm.ScopeCredential, 2*time.Hour), quota(llm.ScopeModel, 3*time.Hour)}, 2 * time.Hour},
		{"credential limit as late as credential quota", []*llm.Error{quota(llm.ScopeCredential, 2*time.Hour), limit(llm.ScopeCredential, 2*time.Hour)}, 0},
		{"no reset named", []*llm.Error{{Code: llm.CodeQuota, Scope: llm.ScopeModel}}, 0},
	} {
		st := NewState()
		a := &Account{ID: "a"}
		for i, e := range c.fails {
			st.failure(a, "m", uint64(i+1), e, now, 0)
		}
		until, _, _, ok := st.quotaOut(a, "m", now)
		if want := c.until != 0; ok != want || (ok && !until.Equal(now.Add(c.until))) {
			t.Errorf("%s: quotaOut = %v, %v; want out %v until now+%v", c.name, until, ok, want, c.until)
		}
	}
}

func TestQuotaResponseModeValidates(t *testing.T) {
	for mode, ok := range map[string]bool{"": true, QuotaRetryable: true, QuotaUsageLimit: true, "Usage-Limit": false, "stop": false} {
		c := Config{QuotaResponseMode: mode}
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("mode %q: Validate = %v", mode, err)
		}
	}
}

func TestAccountRequestRetry(t *testing.T) {
	withRetry := func(id string, n *int) Account { return Account{ID: id, RequestRetry: n} }
	h := newHarness(t, Config{Strategy: StrategyFillFirst, RequestRetry: rounds(2), DisableCooling: true},
		withRetry("a", rounds(0)), withRetry("b", rounds(1)), withRetry("c", nil))
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, rateLimited(0))
	}
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want the last rate limit", evs[0])
	}
	h.wantAttempts("a", "b", "c", "b", "c", "c")

	h = newHarness(t, Config{Strategy: StrategyFillFirst, RequestRetry: rounds(1), DisableCooling: true},
		withRetry("a", rounds(3)), withRetry("b", nil))
	h.on("a", rateLimited(0))
	h.on("b", rateLimited(0))
	h.run(h.req("m"))
	h.wantAttempts("a", "b", "a", "b", "a", "a")

	h = newHarness(t, Config{Strategy: StrategyFillFirst, DisableCooling: true}, withRetry("a", rounds(2)), withRetry("b", nil))
	h.on("a", rateLimited(0), rateLimited(0), ok())
	h.on("b", rateLimited(0))
	h.runOK("m")
	h.wantAttempts("a", "b", "a", "a")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want no wait without blocks", h.slept)
	}
}

func TestAccountRequestRetryWaitsForTheNextRoundsAccounts(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst, RequestRetry: rounds(1), MaxWaitMS: time.Hour.Milliseconds()},
		Account{ID: "a", RequestRetry: rounds(0)}, Account{ID: "b"})
	h.on("a", rateLimited(10*time.Second))
	h.on("b", rateLimited(time.Minute), ok())
	h.runOK("m")
	h.wantAttempts("a", "b", "b")
	if len(h.slept) != 1 || h.slept[0] != time.Minute {
		t.Errorf("slept %v, want one wait of 1m0s for b", h.slept)
	}
}

func TestAccountRequestRetryMustNotBeNegative(t *testing.T) {
	a := Account{ID: "a", RequestRetry: rounds(-1)}
	if err := a.Check(); err == nil {
		t.Error("a negative request_retry passed the check")
	}
}

func TestAccountCheckRefusesRepeatedModelName(t *testing.T) {
	a := Account{ID: "pool", Models: []ModelEntry{{Name: "up-a", Alias: "pub"}, {Name: "up-b", Alias: "pub"}}}
	err := a.Check()
	if err == nil || !strings.Contains(err.Error(), `"pub"`) || !strings.Contains(err.Error(), `"pool"`) {
		t.Errorf("Check = %v, want a refusal naming the alias \"pub\" and the account \"pool\"", err)
	}
	a.Models = []ModelEntry{{Name: "m"}, {Name: "up-b", Alias: "m"}}
	if err := a.Check(); err == nil {
		t.Error("an alias equal to another entry's name passed the check")
	}
	for _, models := range [][]ModelEntry{
		{{Name: "up-a", Alias: "pub"}, {Name: "up-a", Alias: "pub"}},
		{{Name: "up-a", Alias: "pub-a"}, {Name: "up-b", Alias: "pub-b"}, {Name: "up-c"}},
	} {
		a.Models = models
		if err := a.Check(); err != nil {
			t.Errorf("models %v: Check = %v, want none", models, err)
		}
	}
}

func TestAnyModelServesRequestWithoutModel(t *testing.T) {
	newRouter := func() *harness {
		return newHarness(t, Config{Strategy: StrategyFillFirst},
			Account{ID: "a", Models: []ModelEntry{{Name: "m1"}}}, Account{ID: "b", Models: []ModelEntry{{Name: "m2"}}})
	}
	run := func(h *harness, req Request) (accounts, models []string, last llm.Event) {
		req.Attempt = func(ctx context.Context, ext string, a protocol.Account, r llm.Request) Stream {
			models = append(models, r.Model)
			return h.attempt(ctx, ext, a, r)
		}
		evs := h.run(req)
		return h.attempts, models, evs[len(evs)-1]
	}

	h := newRouter()
	h.on("a", rateLimited(time.Minute), ok())
	accounts, models, last := run(h, Request{AnyModel: true})
	if last.Type != llm.EventFinish {
		t.Fatalf("a request without a model failed: %+v", last.Error)
	}
	if want := []string{"a", "b"}; !slices.Equal(accounts, want) || !slices.Equal(models, []string{"", ""}) {
		t.Errorf("attempts %v with models %q, want %v with no model", accounts, models, want)
	}

	_, _, last = run(newRouter(), Request{})
	if last.Type != llm.EventError || last.Error.Code != llm.CodeNotFound {
		t.Errorf("without AnyModel: got %+v, want model not found", last)
	}

	h = newRouter()
	accounts, models, last = run(h, Request{AnyModel: true, LLM: llm.Request{Model: "m2"}})
	if last.Type != llm.EventFinish || !slices.Equal(accounts, []string{"b"}) || !slices.Equal(models, []string{"m2"}) {
		t.Errorf("a named model: attempts %v with models %q (%+v), want only b, for m2", accounts, models, last)
	}
	_, _, last = run(newRouter(), Request{AnyModel: true, LLM: llm.Request{Model: "m3"}})
	if last.Type != llm.EventError || last.Error.Code != llm.CodeNotFound {
		t.Errorf("an unknown model: got %+v, want model not found", last)
	}
}

func TestCoolingErrorWaitRoundsUp(t *testing.T) {
	for _, c := range []struct {
		name  string
		block time.Duration
		spent time.Duration
		want  string
		ms    int64
	}{
		{"a fifth of a second left", time.Second, 800 * time.Millisecond, "1s", 200},
		{"a whole second left", time.Second, 0, "1s", 1000},
		{"a second and a fifth left", 1200 * time.Millisecond, 0, "2s", 1200},
		{"just under a minute left", time.Minute, 100 * time.Millisecond, "1m0s", 59900},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{RequestRetry: rounds(0), MaxWaitMS: 120000}, accts("a")...)
			h.on("a", rateLimited(c.block), ok())
			h.run(h.req("m"))
			h.now = h.now.Add(c.spent)
			e := h.run(h.req("m"))[0].Error
			if e == nil || e.ProviderCode != "model_cooldown" {
				t.Fatalf("got %+v, want model_cooldown", e)
			}
			if want := fmt.Sprintf("every account for model %q is cooling down; try again in %s", "m", c.want); e.Message != want || e.RetryAfterMS != c.ms {
				t.Errorf("message %q, retry_after_ms %d; want %q and %d", e.Message, e.RetryAfterMS, want, c.ms)
			}
		})
	}
}

func TestCoolingErrorNeverSaysZero(t *testing.T) {
	h := newHarness(t, Config{RequestRetry: rounds(0), MaxWaitMS: 120000}, accts("a")...)
	h.on("a", rateLimited(time.Second), ok())
	h.run(h.req("m"))
	h.now = h.now.Add(time.Second - time.Millisecond)
	e := h.run(h.req("m"))[0].Error
	if e == nil || e.ProviderCode != "model_cooldown" || !strings.HasSuffix(e.Message, "try again in 1s") || e.RetryAfterMS != 1 {
		t.Errorf("1ms before the block ends: %+v, want model_cooldown, try again in 1s, retry_after_ms 1", e)
	}
	h.now = h.now.Add(time.Millisecond)
	h.runOK("m")

	for _, wait := range []time.Duration{0, -time.Second} {
		e := coolingError("m", wait, nil)
		if !strings.HasSuffix(e.Message, "try again in 1s") || e.RetryAfterMS != 1000 {
			t.Errorf("wait %v: %q, retry_after_ms %d; want try again in 1s and 1000", wait, e.Message, e.RetryAfterMS)
		}
	}
}

func TestCooledDownModelIsNotUnknown(t *testing.T) {
	serves := []ModelEntry{{Name: "m"}}
	h := newHarness(t, Config{}, Account{ID: "a", Models: serves}, Account{ID: "b", Models: serves})
	h.r.state.failure(h.r.accounts[0], "m", 0, &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, RetryAfterMS: (90 * time.Second).Milliseconds()}, h.now, 0)
	h.r.state.failure(h.r.accounts[1], "m", 0, &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, RetryAfterMS: (45 * time.Second).Milliseconds()}, h.now, 0)

	evs := h.run(h.req("m"))
	e := evs[0].Error
	if e == nil {
		t.Fatalf("got %+v, want the model_cooldown error", evs[0])
	}
	if e.ProviderCode == "model_not_found" || e.Code == llm.CodeNotFound || e.Status == http.StatusNotFound {
		t.Fatalf("a cooled-down model was answered as unknown: %+v", e)
	}
	if e.ProviderCode != "model_cooldown" || e.Status != http.StatusTooManyRequests || e.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want 429 rate_limit with provider code model_cooldown", e)
	}
	if e.RetryAfterMS != 45_000 {
		t.Errorf("retry_after_ms = %d, want 45000: the earliest block's end, in whole seconds", e.RetryAfterMS)
	}
	if want := `every account for model "m" is cooling down; try again in 45s`; e.Message != want {
		t.Errorf("message = %q, want %q", e.Message, want)
	}
	h.wantAttempts()

	evs = h.run(h.req("other"))
	if e := evs[0].Error; e == nil || e.ProviderCode != "model_not_found" || e.Code != llm.CodeNotFound {
		t.Errorf("unlisted model: got %+v, want model_not_found", evs[0])
	}

	h.now = h.now.Add(45 * time.Second)
	h.runOK("m")
	h.wantAttempts("b")
}

func TestCooldownWaitComesFromTheAccountThatServesTheModel(t *testing.T) {
	h := newHarness(t, Config{},
		Account{ID: "codex", Extension: "pluxy/codex"}, Account{ID: "b", Extension: "pluxy/codex", Models: []ModelEntry{{Name: "m"}}})
	h.r.UseCatalog(func(context.Context, Account, *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "list unreadable"}
	})
	h.r.state.failure(h.r.accounts[0], "m", 0, &llm.Error{Code: llm.CodeQuota, Scope: llm.ScopeCredential, RetryAfterMS: (72 * time.Hour).Milliseconds()}, h.now, 0)
	h.r.state.failure(h.r.accounts[1], "m", 0, &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeModel, RetryAfterMS: (2 * time.Minute).Milliseconds()}, h.now, 0)

	evs := h.run(h.req("m"))
	e := evs[0].Error
	if e == nil || e.ProviderCode != "model_cooldown" || e.Status != http.StatusTooManyRequests {
		t.Fatalf("got %+v, want 429 model_cooldown", evs[0])
	}
	if e.RetryAfterMS != (2 * time.Minute).Milliseconds() {
		t.Errorf("retry_after_ms = %d, want 120000: b serves m and unblocks first, not codex's 72h quota block", e.RetryAfterMS)
	}
	if e.Code != llm.CodeRateLimit {
		t.Errorf("code = %q, want %q: the reason is b's rate limit, not codex's quota", e.Code, llm.CodeRateLimit)
	}
	h.wantAttempts()
}

func TestRemovedAccountLosesSessionBindings(t *testing.T) {
	h := newHarness(t, Config{SessionAffinity: &AffinityConfig{}}, accts("a")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.run(s1)
	b := Account{ID: "b", Priority: 1}
	h.setAccounts(b)
	h.setAccounts(Account{ID: "a"}, b)
	h.run(s1)
	h.wantAttempts("a", "b")
}
