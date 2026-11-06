package route

import (
	"context"
	"fmt"
	"slices"
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
	h := newHarness(t, Config{}, Account{ID: "a", Models: []string{"m1"}}, Account{ID: "b", Models: []string{"m2"}}, Account{ID: "c"})
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
// without being blocked; when no account can serve it, the request fails with the refusal
// after one try each instead of looping.
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
	if e := evs[0].Error; e == nil || e.Code != llm.CodeUnsupported {
		t.Errorf("got %+v, want the refusal", evs[0])
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
		st.failure(a, "m", e, now)
		until, _, _ := st.availability(a, "m", now)
		got = append(got, until.Sub(now))
		now = until
	}
	st.failure(a, "m", e, now) // step 4 starts at now: 8s step, 4s block
	st.failure(a, "m", e, now) // while live: same step, deadline unchanged
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
	st.failure(a, "m", &llm.Error{Code: llm.CodeRateLimit, RetryAfterMS: 60_000}, now)
	st.failure(a, "m", &llm.Error{Code: llm.CodeRateLimit, RetryAfterMS: 1_000}, now)
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

	h = newHarness(t, Config{MaxAccounts: 1, MaxWaitMS: 120_000}, accts("a", "b")...)
	h.on("a", rateLimited(time.Second), ok())
	h.runOK("m") // a refuses; b is beyond the account limit, so wait for a
	h.wantAttempts("a", "a")
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
	h.r.state.failure(h.r.accounts[0], "m", &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, RetryAfterMS: 60_000}, h.now)

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
