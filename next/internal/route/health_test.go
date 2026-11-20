package route

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The reset rules and the account health they show, step by step on a fake clock: the scenarios
// of the prototype that pinned the rules, each a reported case of an account blocked for days.

// fakeReadings answers Reading with what read returns for the account; nil read: no reading.
type fakeReadings struct {
	read  func(id string) (*protocol.Quota, *llm.Error)
	calls []string
}

func (f *fakeReadings) Reading(_ context.Context, a Account) (*protocol.Quota, *llm.Error) {
	f.calls = append(f.calls, a.ID)
	if f.read == nil {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "no reading"}
	}
	return f.read(a.ID)
}

// quotaOut is a 429 for a used-up allowance on the whole account, naming its reset.
func quotaOut(reset time.Duration) script {
	return fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: reset.Milliseconds()})
}

func pct(v float64) *float64 { return &v }

// windows is a reading with a five-hour and a weekly window, each used to the given percent and
// reset after the given time from now.
func windows(now time.Time, fiveHour float64, fiveHourReset time.Duration, weekly float64, weeklyReset time.Duration) *protocol.Quota {
	return &protocol.Quota{Limits: []protocol.QuotaLimit{
		{ID: "five_hour", UsedPercent: pct(fiveHour), ResetsAt: now.Add(fiveHourReset).Unix(), WindowSeconds: 5 * 3600},
		{ID: "weekly", UsedPercent: pct(weekly), ResetsAt: now.Add(weeklyReset).Unix(), WindowSeconds: 7 * 24 * 3600},
		// A balance and a count, which are not usage windows: never what holds an account.
		{ID: "credits", Remaining: pct(0), Unit: "credits"},
		{ID: "extra_usage", UsedPercent: pct(100), Unit: "usd"},
	}}
}

type healthStep struct {
	after time.Duration
	note  string
	do    func(h *harness, rd *fakeReadings) // nil: time passes
	// What the account's health is after the step: its state, the time left until its block ends
	// and to its next check (0: none), and the block's source.
	state  string
	left   time.Duration
	check  time.Duration
	source string
}

func (h *harness) healthOf(id string) Health {
	return h.r.state.Health(Account{ID: id}, h.now, Hold{})
}

func runSteps(t *testing.T, h *harness, rd *fakeReadings, steps []healthStep) {
	t.Helper()
	for i, s := range steps {
		h.now = h.now.Add(s.after)
		if s.do != nil {
			s.do(h, rd)
		}
		got := h.healthOf("a")
		var left, check time.Duration
		if got.Until != nil {
			left = got.Until.Sub(h.now)
		}
		if got.NextCheck != nil {
			check = got.NextCheck.Sub(h.now)
		}
		if got.State != s.state || left != s.left || check != s.check || got.Source != s.source {
			t.Fatalf("step %d (%s): health %s left=%v check=%v source=%q (%s), want %s left=%v check=%v source=%q",
				i+1, s.note, got.State, left, check, got.Source, got.Why, s.state, s.left, s.check, s.source)
		}
	}
}

// request sends one request for model m.
func request(h *harness, _ *fakeReadings) { h.run(h.req("m")) }

// check runs the checks that are due, with the reading read gives the account.
func check(read func(now time.Time) (*protocol.Quota, *llm.Error)) func(h *harness, rd *fakeReadings) {
	return func(h *harness, rd *fakeReadings) {
		rd.read = func(string) (*protocol.Quota, *llm.Error) { return read(h.now) }
		h.r.Check(context.Background())
	}
}

// checkUnread runs the checks that are due for an account that has no usage reading.
func checkUnread(h *harness, rd *fakeReadings) {
	rd.read = nil
	h.r.Check(context.Background())
}

// backedOff sends a request that the account refuses for a used-up quota without naming a reset,
// so core backs off: 1s for the first step, held half of it with the harness's jitter.
func backedOff(h *harness, _ *fakeReadings) {
	h.on("a", fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential}))
	h.run(h.req("m"))
}

func refused(reset time.Duration) func(h *harness, rd *fakeReadings) {
	return func(h *harness, rd *fakeReadings) {
		h.on("a", quotaOut(reset))
		h.run(h.req("m"))
	}
}

func TestResetRuleScenarios(t *testing.T) {
	const m, hr = time.Minute, time.Hour
	failed := func(time.Time) (*protocol.Quota, *llm.Error) {
		return nil, &llm.Error{Code: llm.CodeUpstream, Status: 502, Message: "usage endpoint down"}
	}
	cases := []struct {
		name  string
		steps []healthStep
		// attempts are the attempts the scenario sends to the account, in all.
		attempts int
	}{
		{"1. a reset 292 years off (milliseconds read as seconds); the reading says 3h", []healthStep{
			{0, "429 quota, reset far off", refused(292 * 365 * 24 * hr), HealthExhausted, 30 * m, 15 * m, SourceUnverified},
			{15 * m, "check: full window resets in 3h", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 100, 3*hr, 40, 100*hr), nil
			}), HealthExhausted, 3 * hr, 15 * m, SourceProvider},
			{3 * hr, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"2. the 5h window ran out, the upstream names the weekly reset (68h)", []healthStep{
			{0, "429 quota, reset in 68h", refused(68 * hr), HealthExhausted, 68 * hr, 15 * m, SourceProvider},
			{15 * m, "check: 5h window resets in 4h37m", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 100, 4*hr+37*m, 60, 68*hr-15*m), nil
			}), HealthExhausted, 4*hr + 37*m, 15 * m, SourceProvider},
			{4*hr + 37*m, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"3. the provider resets the quota early", []healthStep{
			{0, "429 quota, reset in 96h", refused(96 * hr), HealthExhausted, 96 * hr, 15 * m, SourceProvider},
			{15 * m, "check: still full", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 50, 2*hr, 100, 96*hr-15*m), nil
			}), HealthExhausted, 96*hr - 15*m, 15 * m, SourceProvider},
			{15 * m, "check: no window is full", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 0, 5*hr, 0, 7*24*hr), nil
			}), HealthOK, 0, 0, ""},
		}, 1},
		{"4. the reading cannot be made: the block holds, checks go on", []healthStep{
			{0, "429 quota, reset in 5h", refused(5 * hr), HealthExhausted, 5 * hr, 15 * m, SourceProvider},
			{15 * m, "check: failed", check(failed), HealthExhausted, 4*hr + 45*m, 15 * m, SourceProvider},
			{0, "a request while it holds", request, HealthExhausted, 4*hr + 45*m, 15 * m, SourceProvider},
			{15 * m, "check: failed", check(failed), HealthExhausted, 4*hr + 30*m, 15 * m, SourceProvider},
			{4*hr + 30*m, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"5. a 708.7 ms reset hint is held for 1s", []healthStep{
			{0, "429 rate limit, 708 ms", func(h *harness, _ *fakeReadings) {
				h.on("a", rateLimited(708*time.Millisecond))
				h.run(h.req("m"))
			}, HealthCooling, time.Second, 0, SourceProvider},
			{time.Second, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"7. a believable 5h reset is not tried early; a later, sooner word from the provider wins", []healthStep{
			{0, "429 quota, reset in 5h", refused(5 * hr), HealthExhausted, 5 * hr, 15 * m, SourceProvider},
			{15 * m, "check: same reset", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 100, 4*hr+45*m, 10, 100*hr), nil
			}), HealthExhausted, 4*hr + 45*m, 15 * m, SourceProvider},
			{0, "a request while it holds", request, HealthExhausted, 4*hr + 45*m, 15 * m, SourceProvider},
			{15 * m, "check: reset moved to 20m", check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return windows(now, 100, 20*m, 10, 100*hr), nil
			}), HealthExhausted, 20 * m, 15 * m, SourceProvider},
			{20 * m, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"8. no usage reading (an API key): at the check one request is let through", []healthStep{
			{0, "429 quota, reset in 96h", refused(96 * hr), HealthExhausted, 96 * hr, 15 * m, SourceProvider},
			{5 * m, "a request before the check", request, HealthExhausted, 96*hr - 5*m, 10 * m, SourceProvider},
			{10 * m, "check: no reading, so a request may pass", checkUnread, HealthExhausted, 96*hr - 15*m, 0, SourceProvider},
			{0, "the check request: 429 again", refused(96*hr - 15*m), HealthExhausted, 96*hr - 15*m, 15 * m, SourceProvider},
			{0, "a request after it", request, HealthExhausted, 96*hr - 15*m, 15 * m, SourceProvider},
			{15 * m, "check: a request may pass", checkUnread, HealthExhausted, 96*hr - 30*m, 0, SourceProvider},
			{0, "the check request: served", func(h *harness, _ *fakeReadings) {
				h.on("a", ok())
				h.runOK("m")
			}, HealthOK, 0, 0, ""},
		}, 3},
		{"9. a check with no more than an interval left after it is the last", []healthStep{
			{0, "429 quota, reset in 20m", refused(20 * m), HealthExhausted, 20 * m, 15 * m, SourceProvider},
			{15 * m, "check: failed", check(failed), HealthExhausted, 5 * m, 0, SourceProvider},
			{5 * m, "time passes", nil, HealthOK, 0, 0, ""},
		}, 1},
		{"10. the request a check lets through is the last check when no more than an interval is left", []healthStep{
			{0, "429 quota, reset in 20m", refused(20 * m), HealthExhausted, 20 * m, 15 * m, SourceProvider},
			{15 * m, "check: no reading, so a request may pass", checkUnread, HealthExhausted, 5 * m, 0, SourceProvider},
			{0, "the check request: refused for the request itself", func(h *harness, _ *fakeReadings) {
				h.on("a", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "bad request"}))
				h.run(h.req("m"))
			}, HealthExhausted, 5 * m, 0, SourceProvider},
			{5 * m, "time passes", nil, HealthOK, 0, 0, ""},
		}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{}, accts("a")...)
			rd := &fakeReadings{}
			h.r.UseReadings(rd)
			runSteps(t, h, rd, c.steps)
			if len(h.attempts) != c.attempts {
				t.Errorf("attempts on the account = %v, want %d", h.attempts, c.attempts)
			}
		})
	}
}

// 6. A person turns an account off while requests on it still run; their late outcomes, a
// success and a used-up quota, leave nothing behind, so it is ok once it is turned on again.
func TestTurnedOffAccountIgnoresLateOutcomes(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", ok(), script{start(), {Type: llm.EventError, Error: &llm.Error{Code: llm.CodeQuota, Status: 429, Scope: llm.ScopeCredential, RetryAfterMS: time.Hour.Milliseconds()}}})
	served := h.r.Generate(context.Background(), h.req("m"))
	served.Next()
	limited := h.r.Generate(context.Background(), h.req("m"))
	limited.Next()
	h.setAccounts() // turned off: routing leaves it out
	if got := h.r.state.Health(Account{ID: "a"}, h.now, Hold{Disabled: true}); got.State != HealthDisabled {
		t.Fatalf("turned off: %+v", got)
	}
	for _, s := range []Stream{served, limited} {
		for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
		}
		s.Close()
	}
	if got := h.r.state.Health(Account{ID: "a"}, h.now, Hold{Disabled: true}); got.State != HealthDisabled {
		t.Fatalf("after the late outcomes: %+v", got)
	}
	h.now = h.now.Add(time.Minute)
	h.setAccounts(accts("a")...) // turned on
	if got := h.healthOf("a"); got.State != HealthOK {
		t.Fatalf("turned on again: %+v, want ok", got)
	}
	h.on("a", ok())
	h.runOK("m")
}

// A sign-in the provider refused shows as needs_login, whatever routing holds.
func TestHealthNeedsLogin(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", quotaOut(time.Hour))
	h.run(h.req("m"))
	got := h.r.state.Health(Account{ID: "a"}, h.now, Hold{NeedsLogin: true, Why: "invalid_grant"})
	if got.State != HealthNeedsLogin || got.Why != "invalid_grant" || got.Until != nil {
		t.Fatalf("health = %+v", got)
	}
}

// The provider's newest word replaces the one before it, sooner or later; a backoff core chose
// itself only moves a block later, and the word of an attempt that started before the one that
// set the block only moves it later too.
func TestNewestWordReplaces(t *testing.T) {
	st := NewState()
	st.rand = func() float64 { return 0 }
	a := &Account{ID: "a"}
	now := time.Unix(0, 0)
	limit := func(ms int64) *llm.Error { return &llm.Error{Code: llm.CodeRateLimit, RetryAfterMS: ms} }
	left := func() time.Duration { until, _, _ := st.availability(a, "m", now); return until.Sub(now) }
	st.failure(a, "m", 2, limit(60_000), now, 0)
	st.failure(a, "m", 3, limit(1_000), now, 0)
	if got := left(); got != time.Second {
		t.Errorf("after a sooner word: %v, want 1s", got)
	}
	st.failure(a, "m", 4, limit(120_000), now, 0)
	if got := left(); got != 2*time.Minute {
		t.Errorf("after a later word: %v, want 2m", got)
	}
	st.failure(a, "m", 1, limit(5_000), now, 0) // an attempt older than the block's
	if got := left(); got != 2*time.Minute {
		t.Errorf("after an older attempt's sooner word: %v, want 2m", got)
	}
	st.failure(a, "m", 5, &llm.Error{Code: llm.CodeRateLimit}, now, 0) // backoff: 1s step, 0.5s block
	if got := left(); got != 2*time.Minute {
		t.Errorf("after a backoff: %v, want 2m", got)
	}
}

// A reset a provider names beyond 8 days is not believed, and does not overflow: the account is
// held 30 minutes and checked at 15. One under a second is held a second.
func TestResetBounds(t *testing.T) {
	for _, c := range []struct {
		ms           int64
		left, check  time.Duration
		source, kind string
	}{
		{1, time.Second, 0, SourceProvider, HealthCooling},
		{(8 * 24 * time.Hour).Milliseconds(), 8 * 24 * time.Hour, 15 * time.Minute, SourceProvider, HealthExhausted},
		{(8 * 24 * time.Hour).Milliseconds() + 1, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
		{1 << 62, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
	} {
		st := NewState()
		now := time.Unix(1_700_000_000, 0)
		st.failure(&Account{ID: "a"}, "m", 1, &llm.Error{Code: llm.CodeQuota, Scope: llm.ScopeCredential, RetryAfterMS: c.ms}, now, 0)
		got := st.Health(Account{ID: "a"}, now, Hold{})
		var check time.Duration
		if got.NextCheck != nil {
			check = got.NextCheck.Sub(now)
		}
		if got.Until == nil || got.Until.Sub(now) != c.left || check != c.check || got.Source != c.source || got.State != c.kind {
			t.Errorf("reset of %d ms: %+v, want %s for %v from %s, checked in %v", c.ms, got, c.kind, c.left, c.source, c.check)
		}
	}
}

// A check made with a reading never lets a request through, and only one request passes at a
// check where there is no reading.
func TestCheckRequestIsOnlyWithoutAReading(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "down"}
	}}
	h.r.UseReadings(rd)
	h.on("a", quotaOut(5*time.Hour))
	h.runOK("m") // a refuses, b serves
	h.now = h.now.Add(15 * time.Minute)
	h.r.Check(context.Background())
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "b")
	if !slices.Equal(rd.calls, []string{"a"}) {
		t.Errorf("readings = %v, want one of a", rd.calls)
	}
	// No reading: the next check lets the next request through, and only it.
	rd.read = nil
	h.now = h.now.Add(15 * time.Minute)
	h.r.Check(context.Background())
	h.on("a", quotaOut(4*time.Hour))
	h.runOK("m") // a, refused again; b
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "b", "a", "b", "b")
}

// A block is checked only while it has more than the check interval left, and a short block is
// never checked.
func TestShortBlocksAreNotChecked(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	rd := &fakeReadings{}
	h.r.UseReadings(rd)
	h.on("a", quotaOut(10*time.Minute))
	h.run(h.req("m"))
	h.now = h.now.Add(9 * time.Minute)
	h.r.Check(context.Background())
	if len(rd.calls) != 0 {
		t.Errorf("readings = %v, want none", rd.calls)
	}
	if got := h.healthOf("a"); got.State != HealthExhausted || got.NextCheck != nil {
		t.Errorf("health = %+v", got)
	}
}

// Quota blocks are saved and read back at start, where each is checked at once: a restart sends
// no request to an exhausted account, and a block that no longer holds ends at that check.
func TestSavedBlocks(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b", "c")...)
	changes := h.r.state.Changes()
	h.on("a", quotaOut(3*time.Hour))
	h.on("b", rateLimited(time.Hour)) // not a quota block: not saved
	h.on("c", fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: (30 * 24 * time.Hour).Milliseconds()}), ok())
	h.run(h.req("m")) // a, b and c refuse
	select {
	case <-changes:
	default:
		t.Fatal("no change signalled after a quota block")
	}
	saved := h.r.state.Saved(h.now)
	b, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var back []SavedBlock
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 {
		t.Fatalf("saved %s, want a's and c's quota blocks", b)
	}

	// A new core, ten minutes later, with a, c and d; a's and c's blocks hold, until checked.
	h2 := newHarness(t, Config{}, accts("a", "c", "d")...)
	h2.now = h.now.Add(10 * time.Minute)
	back = append(back, SavedBlock{Account: "gone", Scope: "credential", Until: h2.now.Add(time.Hour), Source: SourceProvider})
	h2.r.state.Load(back, h2.now, func(id string) bool { return id != "gone" })
	if got := h2.healthOf("a"); got.State != HealthExhausted || got.Until == nil || got.Until.Sub(h2.now) != 3*time.Hour-10*time.Minute ||
		got.NextCheck == nil || !got.NextCheck.Equal(h2.now) || got.Source != SourceProvider {
		t.Fatalf("a after the restart: %+v", got)
	}
	h2.runOK("m")
	h2.runOK("m")
	h2.wantAttempts("d", "d")
	if s := h2.r.state.Saved(h2.now); len(s) != 2 {
		t.Errorf("saved after the restart: %+v, want a's and c's", s)
	}
	rd := &fakeReadings{read: func(id string) (*protocol.Quota, *llm.Error) {
		if id == "a" {
			return windows(h2.now, 100, 2*time.Hour, 5, 100*time.Hour), nil
		}
		return windows(h2.now, 10, 2*time.Hour, 5, 100*time.Hour), nil
	}}
	h2.r.UseReadings(rd)
	h2.r.Check(context.Background())
	slices.Sort(rd.calls)
	if !slices.Equal(rd.calls, []string{"a", "c"}) {
		t.Errorf("checks at start = %v, want a and c", rd.calls)
	}
	if got := h2.healthOf("a"); got.State != HealthExhausted || got.Until.Sub(h2.now) != 2*time.Hour {
		t.Errorf("a after its check: %+v, want exhausted for 2h", got)
	}
	h2.runOK("m") // c's model block ended: c serves
	h2.wantAttempts("d", "d", "c")
	if s := h2.r.state.Saved(h2.now); len(s) != 1 || s[0].Account != "a" {
		t.Errorf("saved after the checks: %+v, want a's", s)
	}
	// Clearing the account (the panel's reset) removes its saved block.
	h2.r.state.Reset(Account{ID: "a"}, h2.now)
	if s := h2.r.state.Saved(h2.now); len(s) != 0 {
		t.Errorf("saved after a reset: %+v", s)
	}
}

// A check holds a block only on a full usage window that covers it: a block on the account's
// credential on a window of the whole account, a block on one model on that or a window that
// names the model, and no block on a window of a feature of the plan (code review, another
// app's allowance). Here only a window of model m1 and a feature's window are full.
func TestCheckHoldsOnlyOnCoveringWindows(t *testing.T) {
	const m, hr = time.Minute, time.Hour
	reading := func(now time.Time) (*protocol.Quota, *llm.Error) {
		return &protocol.Quota{Limits: []protocol.QuotaLimit{
			{ID: "five_hour", UsedPercent: pct(40), ResetsAt: now.Add(2 * hr).Unix(), WindowSeconds: 5 * 3600},
			{ID: "weekly", UsedPercent: pct(60), ResetsAt: now.Add(50 * hr).Unix(), WindowSeconds: 7 * 24 * 3600},
			{ID: "weekly_m1", UsedPercent: pct(100), ResetsAt: now.Add(70 * hr).Unix(), WindowSeconds: 7 * 24 * 3600, Models: []string{"m1"}},
			{ID: "code_review_weekly", UsedPercent: pct(100), ResetsAt: now.Add(90 * hr).Unix(), WindowSeconds: 7 * 24 * 3600, Feature: true},
		}}, nil
	}
	for _, c := range []struct {
		name, model, scope string
		state              string
		left               time.Duration
	}{
		{"the whole account, refused on m1", "m1", llm.ScopeCredential, HealthOK, 0},
		{"another model", "m2", llm.ScopeModel, HealthOK, 0},
		{"the model the full window names", "m1", llm.ScopeModel, HealthExhausted, 70 * hr},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{}, accts("a")...)
			rd := &fakeReadings{}
			h.r.UseReadings(rd)
			h.on("a", fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: c.scope, RetryAfterMS: (5 * hr).Milliseconds()}))
			h.run(h.req(c.model))
			h.now = h.now.Add(15 * m)
			check(reading)(h, rd)
			got := h.healthOf("a")
			var left time.Duration
			if got.Until != nil {
				left = got.Until.Sub(h.now)
			}
			if got.State != c.state || left != c.left {
				t.Errorf("after the check: %s for %v (%s), want %s for %v", got.State, left, got.Why, c.state, c.left)
			}
		})
	}
}

// A check that ends a block starts its backoff over, as a success does: the next refusal without
// a reset time is held for the first step again. Here the account's state is not swept between
// the check and that refusal: a request on the other account swept it just before the check.
func TestCheckEndingABlockStartsTheBackoffOver(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return windows(h.now, 0, 5*time.Hour, 0, 7*24*time.Hour), nil
	}}
	h.r.UseReadings(rd)
	backoff := fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential})
	h.on("a", backoff)
	h.runOK("m") // a refuses: backoff, its first step; b serves
	if got := h.healthOf("a"); got.State != HealthCooling || got.Until == nil || got.Until.Sub(h.now) != 500*time.Millisecond {
		t.Fatalf("after the first refusal: %+v, want cooling for 500ms", got)
	}
	h.now = h.now.Add(time.Second)
	h.on("a", quotaOut(5*time.Hour))
	h.runOK("m") // a refuses until a reset in 5h; b serves
	h.now = h.now.Add(15 * time.Minute)
	h.runOK("m") // b serves, and the state is swept
	h.r.Check(context.Background())
	if got := h.healthOf("a"); got.State != HealthOK {
		t.Fatalf("after the check: %+v, want ok", got)
	}
	h.on("a", backoff)
	h.runOK("m") // a refuses again without a reset; b serves
	h.wantAttempts("a", "b", "a", "b", "b", "a", "b")
	if got := h.healthOf("a"); got.State != HealthCooling || got.Until == nil || got.Until.Sub(h.now) != 500*time.Millisecond {
		t.Errorf("after the refusal that followed the check: %+v, want cooling for 500ms, the first step", got)
	}
}

// A model the account does not have (a 404) is kept off the account for 12 hours, but says
// nothing about the account itself: its health stays ok, and the block shows in its cooldowns.
func TestMissingModelLeavesHealthOK(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", fail(llm.Error{Code: llm.CodeNotFound, Status: 404, Scope: llm.ScopeModel, Message: "no such model"}))
	h.run(h.req("m"))
	if got := h.healthOf("a"); got.State != HealthOK || got.Until != nil {
		t.Errorf("health = %+v, want ok", got)
	}
	rt := h.r.state.Runtime(Account{ID: "a"}, h.now)
	if len(rt.Cooldowns) != 1 || rt.Cooldowns[0].Scope != llm.ScopeModel || rt.Cooldowns[0].Model != "m" || rt.Cooldowns[0].Until.Sub(h.now) != 12*time.Hour {
		t.Errorf("cooldowns = %+v, want model m's for 12h", rt.Cooldowns)
	}
}

// A saved block on an account that does not route now (turned off, or waiting for a sign-in) is
// not checked, and its check stays due: once the account routes again, the next run of the
// checks reads it, not one an interval later.
func TestCheckSkipsAccountsThatDoNotRoute(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return windows(h.now, 0, 5*time.Hour, 0, 7*24*time.Hour), nil
	}}
	h.r.UseReadings(rd)
	h.r.state.Load([]SavedBlock{{Account: "x", Scope: "credential", Until: h.now.Add(3 * time.Hour), Source: SourceProvider}}, h.now, nil)
	h.r.Check(context.Background())
	if len(rd.calls) != 0 {
		t.Errorf("readings = %v, want none: x does not route", rd.calls)
	}
	h.now = h.now.Add(time.Minute)
	h.setAccounts(accts("a", "x")...)
	h.r.Check(context.Background())
	if !slices.Equal(rd.calls, []string{"x"}) {
		t.Errorf("readings once x routes = %v, want x's", rd.calls)
	}
	if got := h.healthOf("x"); got.State != HealthOK {
		t.Errorf("x after its check: %+v, want ok", got)
	}
}

// 6 (H2). An account_closed failure bans the account with the provider's words; a stale success
// and a stale 429 from requests already in flight leave it banned, Reset leaves it banned, and
// only Unban returns it to ok (ignoring outcomes of attempts started before Unban).
func TestBannedAccountScenario6AndPersistence(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst, DisableCooling: true}, accts("a", "b")...)
	// Start three in-flight attempts on a before the ban lands.
	h.on("a",
		ok(),
		script{start(), {Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: time.Hour.Milliseconds()}}},
		fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential, Message: "Your account has been suspended."}),
	)
	h.on("b", ok())
	sOK := h.r.Generate(context.Background(), h.req("m"))
	s429 := h.r.Generate(context.Background(), h.req("m"))
	sBan := h.r.Generate(context.Background(), h.req("m"))
	// Start sOK (seq 1) and s429 (seq 2) on a so both are in flight before sBan lands.
	if ev := sOK.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("stale ok first event = %+v", ev)
	}
	if ev := s429.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("stale 429 first event = %+v", ev)
	}
	// sBan (seq 3) hits account_closed before ResponseStart, bans a, and rotates to b.
	if ev := sBan.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("sBan should have rotated to b and started: %+v", ev)
	}
	for ev := sBan.Next(); !ev.Terminal(); ev = sBan.Next() {
	}
	sBan.Close()

	// DisableCooling did NOT stop account_closed from banning a.
	got := h.healthOf("a")
	if got.State != HealthBanned || got.Why != "Your account has been suspended." || got.Source != SourceProvider || got.Until != nil || got.NextCheck != nil {
		t.Fatalf("after account_closed: %+v, want banned with provider words and source provider", got)
	}

	// Stale success arrives.
	for ev := sOK.Next(); !ev.Terminal(); ev = sOK.Next() {
	}
	sOK.Close()
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("after stale success: %+v, want still banned", got)
	}

	// Stale 429 arrives.
	for ev := s429.Next(); !ev.Terminal(); ev = s429.Next() {
	}
	s429.Close()
	if got := h.healthOf("a"); got.State != HealthBanned || got.Until != nil || got.Why != "Your account has been suspended." {
		t.Fatalf("after stale 429: %+v, want still banned without cooldown", got)
	}
	if rt := h.r.state.Runtime(Account{ID: "a"}, h.now); len(rt.Cooldowns) != 0 || rt.LastError == nil || rt.LastError.Code != llm.CodeAccountClosed {
		t.Fatalf("runtime after stale 429: %+v, want no cooldowns and LastError account_closed", rt)
	}

	// Reset (cooldown clear) does NOT clear a ban.
	if cleared := h.r.state.Reset(Account{ID: "a"}, h.now); cleared != 0 {
		t.Errorf("Reset cleared = %d, want 0", cleared)
	}
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("after Reset: %+v, want still banned", got)
	}

	// SavedBans and LoadBans preserve the ban across a restart, and skip unknown accounts.
	bans := h.r.state.SavedBans()
	if len(bans) != 1 || bans[0].Account != "a" || bans[0].Why != "Your account has been suspended." {
		t.Fatalf("SavedBans = %+v", bans)
	}
	h2 := newHarness(t, Config{}, accts("a", "b")...)
	h2.now = h.now.Add(time.Hour)
	h2.r.state.LoadBans(append(bans, SavedBan{Account: "gone", Why: "gone"}), func(id string) bool { return id != "gone" })
	if got := h2.healthOf("a"); got.State != HealthBanned || got.Why != "Your account has been suspended." || got.Source != SourceProvider {
		t.Fatalf("after LoadBans: %+v", got)
	}
	if len(h2.r.state.SavedBans()) != 1 {
		t.Fatalf("SavedBans after LoadBans = %+v, want gone skipped", h2.r.state.SavedBans())
	}
	// Requests after restart skip banned a completely.
	h2.on("a", ok())
	h2.on("b", ok())
	h2.runOK("m")
	h2.wantAttempts("b")

	// A person turns a back on (Unban): a is ok and serves again; a late failure from an
	// attempt started before Unban is ignored.
	h.r.state.failure(&Account{ID: "a"}, "m", 1, &llm.Error{Code: llm.CodeQuota, Status: 429, RetryAfterMS: time.Hour.Milliseconds()}, h.now, 0)
	if !h.r.state.Unban("a") {
		t.Fatal("Unban returned false, want true")
	}
	if h.r.state.Unban("a") {
		t.Fatal("second Unban returned true, want false")
	}
	if got := h.healthOf("a"); got.State != HealthOK || got.Why != "" {
		t.Fatalf("after Unban: %+v, want ok", got)
	}
	// Late 429 from seq 3 (reserved before Unban) cannot re-block or pollute LastError.
	h.r.state.failure(&Account{ID: "a"}, "m", 2, &llm.Error{Code: llm.CodeAccountClosed, Message: "stale ban"}, h.now, 0)
	if got := h.healthOf("a"); got.State != HealthOK {
		t.Fatalf("after stale failure pre-Unban: %+v, want ok", got)
	}
	h.on("a", ok())
	h.runOK("m")
	if !slices.Contains(h.attempts, "a") {
		t.Errorf("attempts after Unban = %v, want a included", h.attempts)
	}
}

// Sibling rule: account_closed always bans the account; when retryable is true it moves on to
// sibling accounts unless scope is request (the refusal is about this request's content), in
// which case it stops there.
func TestAccountClosedSiblingRule(t *testing.T) {
	t.Run("credential scope bans and moves on to sibling", func(t *testing.T) {
		h := newHarness(t, Config{}, accts("a", "b")...)
		h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 400, Retryable: true, Scope: llm.ScopeCredential, Message: "This organization has been disabled."}))
		h.on("b", ok())
		h.runOK("m")
		h.wantAttempts("a", "b")
		if got := h.healthOf("a"); got.State != HealthBanned {
			t.Errorf("a health = %+v, want banned", got)
		}
	})
	t.Run("request scope bans and stops before sibling", func(t *testing.T) {
		h := newHarness(t, Config{}, accts("a", "b")...)
		h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeRequest, Message: "Account suspended for this request's content."}))
		h.on("b", ok())
		evs := h.run(h.req("m"))
		last := evs[len(evs)-1]
		if last.Type != llm.EventError || last.Error.Code != llm.CodeAccountClosed {
			t.Fatalf("got %+v, want account_closed error", evs)
		}
		h.wantAttempts("a")
		if got := h.healthOf("a"); got.State != HealthBanned {
			t.Errorf("a health = %+v, want banned", got)
		}
	})
}

// A ban ends only when a person enables the account, signs in again, or deletes it: leaving
// the router (e.g. a failed refresh or an extension not running at reload) or a settings
// change keeps the account banned when it returns.
func TestBanSurvivesLeavingRouterSettingsChangeAndRetain(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 400, Retryable: true, Scope: llm.ScopeCredential, Message: "This organization has been disabled."}))
	h.on("b", ok())
	h.runOK("m")
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("a health after account_closed = %+v, want banned", got)
	}

	// 1. Account leaves the router (e.g. reauth_required) and returns.
	h.setAccounts(accts("b")...)
	if !h.r.state.IsBanned("a") {
		t.Errorf("after leaving router via SetAccounts: IsBanned(a) = false, want true")
	}
	h.setAccounts(accts("a", "b")...)
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Errorf("after returning to router: a health = %+v, want banned", got)
	}

	// 2. Settings change in place (restart).
	h.setAccounts(withSettings(Account{ID: "a"}, `{"base_url":"https://other.invalid"}`), Account{ID: "b"})
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Errorf("after settings change: a health = %+v, want banned", got)
	}

	// 3. Reload with its extension stopped (Retain drops a), then reload with it running again.
	r2, err := New(Config{}, accts("b"), h.r.attempt, h.r.state)
	if err != nil {
		t.Fatal(err)
	}
	same := func(old, cur *Account) bool { return string(old.Settings) == string(cur.Settings) }
	h.r.state.Retain(h.r, r2, same)
	if !h.r.state.IsBanned("a") {
		t.Errorf("after Retain without a: IsBanned(a) = false, want true")
	}
	r3, err := New(Config{}, accts("a", "b"), h.r.attempt, h.r.state)
	if err != nil {
		t.Fatal(err)
	}
	h.r.state.Retain(r2, r3, same)
	if got := r3.state.Health(Account{ID: "a"}, h.now, Hold{}); got.State != HealthBanned {
		t.Errorf("after Retain with a back: a health = %+v, want banned", got)
	}

	// Requests still skip a.
	h.on("a", ok())
	h.on("b", ok())
	h.attempts = nil
	h.runOK("m")
	h.wantAttempts("b")

	// 4. Deleting the account drops its ban (so re-adding it starts ok).
	r3.state.RetainBans(func(id string) bool { return id != "a" })
	if r3.state.IsBanned("a") {
		t.Errorf("after RetainBans without a: IsBanned(a) = true, want false")
	}
	if got := r3.state.Health(Account{ID: "a"}, h.now, Hold{}); got.State != HealthOK {
		t.Errorf("after deletion: a health = %+v, want ok", got)
	}
}
