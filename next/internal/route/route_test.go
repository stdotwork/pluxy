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

// The provider extension is the edge of core, so these tests replace it with scripted replies:
// each attempt on an account plays that account's next script. Time is a fake clock that
// waits advance.

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
	t        *testing.T
	r        *Router
	now      time.Time
	slept    []time.Duration
	attempts []string            // account IDs in attempt order
	scripts  map[string][]script // per account: one script per attempt; the last repeats
	// onAttempt, when set, runs as an attempt starts, before its reply exists.
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

// on sets the replies of account id, one per attempt; the last one repeats.
func (h *harness) on(id string, replies ...script) { h.scripts[id] = replies }

func (h *harness) req(model string) Request { return Request{LLM: llm.Request{Model: model}} }

// run sends a request and reads it to its terminal event.
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

// runOK sends a request for model m and fails the test unless it finishes.
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

// rateLimited is a 429 as the openai-compatible extension reports it.
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
	h.runOK("m") // a
	// b is now rate-limited; with a picked last, the turn passes over b to c, then back to a
	// and b in order once b's limit has passed.
	h.on("b", rateLimited(10*time.Second), ok())
	h.runOK("m") // b refuses, c serves
	h.runOK("m") // a
	h.now = h.now.Add(11 * time.Second)
	h.runOK("m") // b
	h.runOK("m") // c
	h.wantAttempts("a", "b", "c", "a", "b", "c")
}

// Rotation state is per model: requests for one model do not move another model's turn.
func TestRoundRobinPerModel(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.runOK("m1")
	h.runOK("m2")
	h.runOK("m1")
	h.wantAttempts("a", "a", "b")
}

// Smooth weighted round robin with weights 5, 1, 1 gives nginx's documented sequence
// a a b a c a a.
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
	h.runOK("m") // a refuses, b serves
	h.runOK("m") // a is blocked: b
	h.wantAttempts("a", "a", "a", "b", "b")
}

// Least-active counts attempts still running: an account holding an open reply is passed over.
func TestLeastActive(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyLeastActive}, accts("a", "b")...)
	held := h.r.Generate(context.Background(), h.req("m"))
	held.Next() // a's reply has started and is still open
	h.runOK("m")
	h.runOK("m")
	held.Close()
	h.runOK("m")
	// b, b while a is busy; then both idle, and the turn after b is a.
	h.wantAttempts("a", "b", "b", "a")
}

func TestPriority(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "low"}, Account{ID: "high", Priority: 1})
	h.runOK("m")
	h.runOK("m")
	h.on("high", rateLimited(time.Minute))
	h.runOK("m") // high refuses, low serves
	h.runOK("m") // high blocked: low
	h.wantAttempts("high", "high", "high", "low", "low")
}

func TestCapabilityAndPin(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a", Models: []ModelEntry{{Name: "m1"}}}, Account{ID: "b", Models: []ModelEntry{{Name: "m2"}}}, Account{ID: "c"})
	h.runOK("m2") // b or c; round robin starts at the first: b
	req := h.req("m1")
	req.Pin = "c"
	h.run(req)
	req.Pin = "b" // b does not serve m1
	evs := h.run(req)
	if e := evs[0].Error; e == nil || e.Code != llm.CodeNotFound {
		t.Errorf("pin to an account without the model: %+v, want not_found", evs[0])
	}
	h.wantAttempts("b", "c")
}

// A pinned account that is blocked fails the request; it does not go to another account.
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

// The slice's check: once the reply has started, a failure goes to the caller and is not
// retried on another account, even when the extension says it would be retry-safe.
func TestNoRetryAfterReplyStarted(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	limit := llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 60_000}
	h.on("a", script{start(), {Type: llm.EventError, Error: &limit}})
	evs := h.run(h.req("m"))
	if len(evs) != 2 || evs[0].Type != llm.EventResponseStart || evs[1].Error == nil || evs[1].Error.Code != llm.CodeRateLimit {
		t.Fatalf("events = %+v, want the reply's start and then its error", evs)
	}
	h.wantAttempts("a")
	// The outcome still counts: a is rate-limited now.
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b")
}

// A failure the upstream may have acted on (a 5xx) is not retried elsewhere.
func TestNotRetrySafe(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeUpstream, Status: 500, Scope: llm.ScopeEndpoint}))
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.Status != 500 {
		t.Errorf("got %+v, want the upstream 500", evs[0])
	}
	h.wantAttempts("a")
}

// A request-scoped refusal (the request itself is bad) is neither retried nor held against
// the account.
func TestRequestErrorLeavesAccountAlone(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Retryable: true}), ok())
	h.run(h.req("m"))
	h.runOK("m")
	h.wantAttempts("a", "a")
}

// An account that cannot serve the request (unsupported, about the account) passes it on
// without being blocked; when no account can serve it, the request fails after one try each
// instead of looping: an endpoint call as today's core does when it finds no usable auth (503
// auth_not_found), any other request with the last refusal.
func TestUnsupportedAccountPassesRequestOn(t *testing.T) {
	refuse := fail(llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "other format"})
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b", "c")...)
	h.on("a", refuse)
	h.on("b", refuse)
	h.runOK("m") // a and b refuse, c serves
	h.runOK("m") // a and b are not blocked: they are tried again first
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
}

// The slice's check: an older success never clears a newer cooldown. A reply that started
// before the account was rate-limited finishes after it; the account stays blocked.
func TestOlderSuccessKeepsNewerCooldown(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	older := h.r.Generate(context.Background(), h.req("m"))
	older.Next() // attempt 1 on a: reply started
	h.on("a", rateLimited(time.Minute))
	h.runOK("m") // attempt 2 on a: refused; b serves
	for ev := older.Next(); !ev.Terminal(); ev = older.Next() {
	} // attempt 1 finishes successfully, after the refusal
	older.Close()
	h.runOK("m") // a must still be blocked
	h.wantAttempts("a", "a", "b", "b")
}

// The newest result wins: a failure from an attempt that started before a success on the same
// account, and ends after it, leaves no block and no last error. Here the older request's reply
// has started and then breaks with a quota error after a newer request on the account succeeded.
func TestStaleFailureAfterNewerSuccess(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	quota := llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 3_600_000}
	h.on("a", script{start(), {Type: llm.EventError, Error: &quota}}, ok())
	older := h.r.Generate(context.Background(), h.req("m"))
	if ev := older.Next(); ev.Type != llm.EventResponseStart { // attempt 1 on a: reply started
		t.Fatalf("first event = %+v", ev)
	}
	h.runOK("m") // attempt 2 on a starts after attempt 1 and succeeds
	if ev := older.Next(); ev.Type != llm.EventError {
		t.Fatalf("older request ended with %+v, want its quota error", ev)
	} // attempt 1 fails after the newer success
	older.Close()
	if rt := h.r.state.Runtime(Account{ID: "a"}, h.now); len(rt.Cooldowns) != 0 || rt.LastError != nil {
		t.Errorf("a stale failure left %+v", rt)
	}
	h.runOK("m") // a still serves
	h.wantAttempts("a", "a", "a")
	if n := len(h.r.state.scopes); n != 0 {
		t.Errorf("state holds %d scopes once no older attempt runs, want none", n)
	}
}

// A newer success clears what an older failure would set only on the scopes the success covers:
// a success on one model says nothing about an older failure on another model of the account,
// and a success on another account that shares the endpoint turns away an older endpoint
// failure.
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

// The mark that turns away a stale failure outlives the idle sweep while the older attempt runs,
// and goes once no attempt older than it runs; ended attempt numbers do not pile up behind a
// long-running one.
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

// Under concurrent attempts the routing state stays consistent: every reservation is released and
// no attempt number is left running.
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

// A success from an attempt that started after the block clears it (and the backoff step).
func TestNewerSuccessClearsBlock(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", rateLimited(0), ok()) // no Retry-After: backoff of 0.5s at the first step
	h.runOK("m")                    // a refuses, b serves
	h.now = h.now.Add(time.Second)
	h.runOK("m") // a again, succeeds
	if n := len(h.r.state.scopes); n != 0 {
		t.Errorf("state holds %d scopes after the success, want none", n)
	}
	h.wantAttempts("a", "b", "a")
}

// Without a Retry-After, a rate limit backs off 1s, 2s, 4s… with equal jitter (here the
// lowest: half the step); failures while the block is live do not climb the ladder.
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
	st.failure(a, "m", 0, e, now, 0) // step 4 starts at now: 8s step, 4s block
	st.failure(a, "m", 0, e, now, 0) // while live: same step, deadline unchanged
	until, _, _ := st.availability(a, "m", now)
	got = append(got, until.Sub(now))
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("block lengths = %v, want %v", got, want)
	}
}

// A later failure never shortens a live block.
func TestBlockOnlyExtends(t *testing.T) {
	st := NewState()
	a := &Account{ID: "a"}
	now := time.Unix(0, 0)
	st.failure(a, "m", 0, &llm.Error{Code: llm.CodeRateLimit, RetryAfterMS: 60_000}, now, 0)
	st.failure(a, "m", 0, &llm.Error{Code: llm.CodeRateLimit, RetryAfterMS: 1_000}, now, 0)
	if until, _, _ := st.availability(a, "m", now); until.Sub(now) != time.Minute {
		t.Errorf("block = %v, want 1m", until.Sub(now))
	}
}

// Scopes are separate: a model limit leaves the account's other models alone; a credential
// limit blocks them all; an endpoint outage blocks every account on that endpoint.
func TestScopes(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, Account{ID: "a", Endpoint: "e"}, Account{ID: "b", Endpoint: "e"}, Account{ID: "c"})
	h.on("a", fail(llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: 60_000}), ok())
	h.runOK("m1") // a refuses m1 → b
	h.runOK("m2") // a still serves m2
	h.wantAttempts("a", "b", "a")

	h.attempts = nil
	h.on("a", fail(llm.Error{Code: llm.CodeUnavailable, Status: 503, Retryable: true, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}))
	h.runOK("m2") // a's endpoint is down; b shares it, so c serves
	h.wantAttempts("a", "c")

	h.attempts = nil
	h.now = h.now.Add(2 * time.Minute)
	h.on("c", fail(llm.Error{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential}))
	h.on("a", ok())
	h.run(Request{LLM: llm.Request{Model: "m1"}, Pin: "c"})
	evs := h.run(Request{LLM: llm.Request{Model: "m2"}, Pin: "c"}) // credential blocked for every model
	if e := evs[0].Error; e == nil || e.ProviderCode != "model_cooldown" {
		t.Errorf("got %+v, want model_cooldown", evs[0])
	}
	h.wantAttempts("c")
}

// A rate limit or quota that names no scope holds back only the model on the account, as today's
// core cools only the model that answered a 429 unless the provider says the whole credential is
// limited.
func TestRateLimitWithoutScopeBlocksTheModel(t *testing.T) {
	for _, code := range []string{llm.CodeRateLimit, llm.CodeQuota} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
			h.on("a", fail(llm.Error{Code: code, Status: 429, Retryable: true, RetryAfterMS: 60_000}), ok())
			h.runOK("m1") // a refuses m1 → b
			h.runOK("m2") // a still serves m2
			h.wantAttempts("a", "b", "a")
		})
	}
}

// When every account is blocked and the wait does not fit, the request fails at once with
// the time the first block ends, and no attempt.
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

// With a wait budget, a request waits for a blocked account as long as the upstream asked, and
// never less, then tries it again.
func TestWaitHonoursRetryAfter(t *testing.T) {
	h := newHarness(t, Config{MaxWaitMS: 10_000}, accts("a")...)
	h.on("a", rateLimited(5*time.Second), ok())
	h.runOK("m")
	h.wantAttempts("a", "a")
	if !slices.Equal(h.slept, []time.Duration{5 * time.Second}) {
		t.Errorf("slept %v, want [5s]", h.slept)
	}
}

// A wait longer than the budget is not shortened to fit: the request fails with the refusal.
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

	// The account limit is for one round (today's max-retry-credentials). A request without
	// rounds ends with it, though a is blocked for only a second and the wait fits.
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

// max_accounts limits the accounts one round tries, and every round gets its own count
// (today's max-retry-credentials).
func TestMaxAccountsIsPerRound(t *testing.T) {
	refuse := rateLimited(0)
	// Each of the two rounds tries two of the three accounts: round 0 a and b, round 1 the
	// next in turn, c, and a.
	h := newHarness(t, Config{MaxAccounts: 2, RequestRetry: rounds(1), DisableCooling: true}, accts("a", "b", "c")...)
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, refuse)
	}
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want the last rate limit", evs[0])
	}
	h.wantAttempts("a", "b", "c", "a")

	// An account that leaves a round (its own request_retry is spent) takes no place in it and
	// is not counted: round 1 tries a and c, round 2 only a.
	retry := func(id string, n *int) Account { return Account{ID: id, RequestRetry: n} }
	h = newHarness(t, Config{Strategy: StrategyFillFirst, MaxAccounts: 2, RequestRetry: rounds(1), DisableCooling: true},
		retry("a", rounds(2)), retry("b", rounds(0)), retry("c", nil))
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, refuse)
	}
	h.run(h.req("m"))
	h.wantAttempts("a", "b", "a", "c", "a")

	// An account the limit left out of a round still takes part in the next one: b's own count
	// runs a second round for it alone, though a's round used up the limit.
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
	held.Next() // a busy
	h.runOK("m")
	held2 := h.r.Generate(context.Background(), h.req("m"))
	held2.Next() // b busy
	evs := h.run(h.req("m"))
	if e := evs[0].Error; e == nil || e.ProviderCode != "accounts_busy" {
		t.Errorf("got %+v, want accounts_busy", evs[0])
	}
	held.Close()
	held2.Close()
	h.runOK("m") // a free again
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
	h.run(s1)    // a, bound
	h.run(s2)    // b, bound
	h.run(s1)    // a
	h.run(s2)    // b
	h.run(other) // another tenant's s1 is not bound: round robin, a
	h.wantAttempts("a", "b", "a", "b", "a")

	// The bound account blocked: the session moves to b and stays there.
	h.attempts = nil
	h.on("a", rateLimited(time.Minute))
	h.run(s1)
	h.now = h.now.Add(2 * time.Minute)
	h.run(s1)
	h.wantAttempts("a", "b", "b")
}

// heldStream is a reply that has started and stays open until closed.
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

// Choosing an account and reserving it are one step: two requests that both find an account
// under its concurrency limit cannot both run on it. The hook holds both requests after they
// chose and before they reserved, the moment the race needs.
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

// Routing state keyed by a model name the client chose does not grow without bound: entries
// that have been idle a while are dropped, and while many are live the table stays within its
// limit without dropping a live cooldown.
func TestModelStateIsBounded(t *testing.T) {
	defer func(n int) { maxModelKeys = n }(maxModelKeys)
	maxModelKeys = 100
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(time.Hour))
	h.run(h.req("keep")) // a credential-wide block first
	h.on("a", fail(llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: time.Hour.Milliseconds()}))
	h.now = h.now.Add(2 * time.Hour) // the credential-wide block has passed
	h.run(h.req("keep"))             // now a model-scoped block on "keep"
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

// fakeCredentials stands in for core's credential store at the router's boundary: every
// refused credential is renewed.
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

// After the upstream refuses an account's credential and it is renewed, the request goes back
// to that account, not to the next one in rotation.
func TestRenewedAccountIsTriedAgain(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "a", Managed: true}, Account{ID: "b"})
	h.r.UseCredentials(&fakeCredentials{})
	h.on("a", fail(llm.Error{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential}), ok())
	h.on("b", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Message: "b rejected request"}))
	h.runOK("m")
	h.wantAttempts("a", "a")
}

// reload builds the router a config reload would: new accounts over the same state, carried
// over by Retain with "same" meaning same settings.
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

// After a reload, an attempt that began on an account before the account changed records
// nothing on the changed account.
func TestRetainIgnoresOutcomesFromBeforeAChange(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, accts("a", "b")...)
	h.on("a", script{start(), rateLimited(time.Minute)[0]}) // a 429 after the reply started
	older := h.r.Generate(context.Background(), h.req("m"))
	older.Next() // attempt on a, reply started
	h.reload(Config{Strategy: StrategyFillFirst}, withSettings(Account{ID: "a"}, `{"base_url":"elsewhere"}`), Account{ID: "b"})
	older.Next() // the old attempt now fails with a 429
	older.Close()
	h.on("a", ok())
	h.runOK("m")
	h.wantAttempts("a", "a")
}

// Changed accounts lose their session bindings and the blocks on an endpoint label only they
// named; weighted rotation starts afresh when weights change.
func TestRetainDropsWhatBelongedToChangedAccounts(t *testing.T) {
	ep := func(id, endpoint string) Account { return Account{ID: id, Endpoint: endpoint} }
	h := newHarness(t, Config{SessionAffinity: &AffinityConfig{}}, ep("a", "shared"), ep("b", "solo"))
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	h.run(s1) // a, bound
	h.r.state.failure(h.r.accounts[0], "m", 0, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}, h.now, 0)

	// a changes; a new account c joins the "shared" label. The outage on "shared" was a's
	// endpoint, so c starts unblocked, and s1 is no longer bound to the changed a.
	c := ep("c", "shared")
	c.Priority = 1
	h.reload(Config{SessionAffinity: &AffinityConfig{}}, withSettings(ep("a", "shared"), `{"x":1}`), ep("b", "solo"), c)
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("c")

	// Weighted: a big weight scale, then a small one; the balances must not carry over.
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

// runOn runs req on router r instead of the harness's current one.
func (h *harness) runOn(r *Router, req Request) []llm.Event {
	cur := h.r
	h.r = r
	defer func() { h.r = cur }()
	return h.run(req)
}

// A router a reload replaced records nothing about an account the reload changed, even for an
// attempt it begins after the reload: a request that started before it retries there.
func TestOldRouterRecordsNothingOnChangedAccounts(t *testing.T) {
	cfg := Config{Strategy: StrategyFillFirst}
	h := newHarness(t, cfg, accts("a", "b")...)
	old := h.r
	h.on("a", rateLimited(time.Hour))
	h.on("b", rateLimited(time.Hour), ok())
	h.onAttempt = func(id string) {
		if id == "a" && h.r == old {
			// The reload lands while the old request's attempt on a is running; it changes b.
			h.reload(cfg, Account{ID: "a"}, withSettings(Account{ID: "b"}, `{"base_url":"elsewhere"}`))
		}
	}
	h.runOn(old, h.req("m")) // a: 429; the old router retries on the old b: 429
	h.onAttempt = nil
	h.attempts = nil
	h.runOK("m") // a is still blocked; the changed b is not
	h.wantAttempts("b")
}

// A reply that starts on a replaced router after the reload does not bind its session to an
// account the reload changed.
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
	h.runOn(old, s1) // starts on a after the reload changed a
	h.onAttempt = nil
	h.attempts = nil
	h.run(s1)
	h.wantAttempts("b")
}

// A replaced router picking with its old weights does not disturb the new weights' rotation.
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

// A request's own attempt function runs its attempts in place of the router's, with the
// same retries and account state.
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
	h.runOK("m") // the router's attempt; a is still rate-limited
	h.wantAttempts("b")
}

func rounds(n int) *int { return &n }

func upstreamDown() script {
	return fail(llm.Error{Code: llm.CodeUpstream, Status: 503, Retryable: true, Scope: llm.ScopeEndpoint})
}

// With request_retry, a request runs at most that many rounds after the first; without blocks
// (cooling off) each round starts at once, as today's core's request-retry does.
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

	// Without request_retry, no round starts without a wait.
	h = newHarness(t, Config{DisableCooling: true}, accts("a")...)
	h.on("a", rateLimited(0), ok())
	h.run(h.req("m"))
	h.wantAttempts("a")
}

// A refused key or a missing model starts no round.
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

// A round waits for the first block to end when that is within max_wait_ms, each time; an
// account refused for rate in this request waits at least 10 seconds.
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

	// request_retry 0: a request that finds every account blocked fails at once.
	h = newHarness(t, Config{RequestRetry: rounds(0), MaxWaitMS: 60000}, accts("a")...)
	h.on("a", rateLimited(time.Second), ok())
	h.run(h.req("m"))
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.ProviderCode != "model_cooldown" || len(h.slept) != 0 {
		t.Errorf("blocked at the start: %+v, slept %v; want model_cooldown at once", evs[0], h.slept)
	}
}

// disable_cooling keeps every account eligible after failures; an account's own setting wins.
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

// transient_error_cooldown_ms sets how long a failing upstream is blocked; below 0, not at all.
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

	// Rate limits keep their own blocks.
	h = newHarness(t, Config{TransientErrorCooldownMS: -1}, accts("a")...)
	h.on("a", rateLimited(time.Minute), ok())
	h.run(h.req("m"))
	if evs := h.run(h.req("m")); evs[0].Error == nil {
		t.Errorf("rate limit with transient cooldown off: %+v, want its block", evs[0])
	}
}

// A failure whose block is not kept (the model table is full of live blocks) is not the
// account's last error either: the last error is the latest failure that set a block.
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
	st.failure(a, "m2", 0, modelLimit("m2 limited"), now, 0) // the table is full: no block kept
	if until, _, _ := st.availability(a, "m2", now); !until.IsZero() {
		t.Fatalf("m2 blocked until %v with the table full", until)
	}
	if rt := st.Runtime(*a, now); rt.LastError == nil || rt.LastError.Message != "m1 limited" {
		t.Errorf("last error = %+v, want m1's: m2's failure set no block", rt.LastError)
	}
}

// usageLimited is a Codex usage_limit_reached refusal as the Codex extension reports it: a
// quota error whose reset the upstream named, with the five-hour window.
func usageLimited(reset time.Duration) script {
	return fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, ProviderCode: "usage_limit_reached",
		Message: "The usage limit has been reached", RetryAfterMS: reset.Milliseconds(), QuotaWindowMinutes: 300,
		Raw: json.RawMessage(`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)})
}

// In the usage-limit quota response mode, a request that finds every account out of quota until
// a reset more than 5 minutes away ends with the usage-limit reply: on the request that used up
// the last account and on the next one, which reaches no account.
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
	h.wantAttempts("a", "b") // the second request reached no account
}

// The usage-limit reply needs every account out of quota until a reset the upstream named,
// more than 5 minutes away, and the mode on; otherwise the retryable replies stay.
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

// A reply that started and then ends with the last account's quota refusal ends with the
// usage-limit reply; the provider is named only when every account belongs to one extension, and
// a pool without a known window has none.
func TestUsageLimitAfterTheReplyStarted(t *testing.T) {
	h := newHarness(t, Config{QuotaResponseMode: QuotaUsageLimit, Strategy: StrategyFillFirst},
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

// The summary of the last upstream failure is today's core's "code: message", from the upstream's
// error body when it has one.
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

// An account counts as out of quota as today's core counts it: a named quota block on the
// credential decides alone; otherwise the block that ends last must be a named quota block, and
// a block of another kind that ends as late keeps the account out of the count.
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
		until time.Duration // 0: not out of quota
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

// An account's own request_retry counts for it: it leaves a request's rounds past its own count,
// and a count above routing's runs more rounds for it alone (today's per-key request-retry).
func TestAccountRequestRetry(t *testing.T) {
	withRetry := func(id string, n *int) Account { return Account{ID: id, RequestRetry: n} }
	// Lower than routing's: a takes part in round 0, b in rounds 0-1, c (routing's 2) in 0-2.
	h := newHarness(t, Config{Strategy: StrategyFillFirst, RequestRetry: rounds(2), DisableCooling: true},
		withRetry("a", rounds(0)), withRetry("b", rounds(1)), withRetry("c", nil))
	for _, id := range []string{"a", "b", "c"} {
		h.on(id, rateLimited(0))
	}
	if evs := h.run(h.req("m")); evs[0].Error == nil || evs[0].Error.Code != llm.CodeRateLimit {
		t.Errorf("got %+v, want the last rate limit", evs[0])
	}
	h.wantAttempts("a", "b", "c", "b", "c", "c")

	// Higher than routing's: a runs rounds 0-3 while b stops after routing's 1.
	h = newHarness(t, Config{Strategy: StrategyFillFirst, RequestRetry: rounds(1), DisableCooling: true},
		withRetry("a", rounds(3)), withRetry("b", nil))
	h.on("a", rateLimited(0))
	h.on("b", rateLimited(0))
	h.run(h.req("m"))
	h.wantAttempts("a", "b", "a", "b", "a", "a")

	// Routing sets no rounds: an account's own count runs them, and the others take none.
	h = newHarness(t, Config{Strategy: StrategyFillFirst, DisableCooling: true}, withRetry("a", rounds(2)), withRetry("b", nil))
	h.on("a", rateLimited(0), rateLimited(0), ok())
	h.on("b", rateLimited(0))
	h.runOK("m")
	h.wantAttempts("a", "b", "a", "a")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want no wait without blocks", h.slept)
	}
}

// The wait before a round counts only the accounts that take part in it: a's block, shorter
// than b's, does not end the wait, as a has no rounds past the first.
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

// A request of an endpoint that takes no model and names none goes to every account, whatever
// models each lists, and its attempts name no upstream model. One that names a model is routed
// by it as any other, and without the flag an empty model serves nobody.
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
	// a is tried first although it lists only m1, refuses, and b (m2 only) serves it.
	if want := []string{"a", "b"}; !slices.Equal(accounts, want) || !slices.Equal(models, []string{"", ""}) {
		t.Errorf("attempts %v with models %q, want %v with no model", accounts, models, want)
	}

	// Without the flag, an empty model is served by nobody.
	_, _, last = run(newRouter(), Request{})
	if last.Type != llm.EventError || last.Error.Code != llm.CodeNotFound {
		t.Errorf("without AnyModel: got %+v, want model not found", last)
	}

	// With a model named, the flag changes nothing: only the account that lists it is asked.
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
