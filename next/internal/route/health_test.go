package route

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

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

func quotaOut(reset time.Duration) script {
	return fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: reset.Milliseconds()})
}

func pct(v float64) *float64 { return &v }

func windows(now time.Time, fiveHour float64, fiveHourReset time.Duration, weekly float64, weeklyReset time.Duration) *protocol.Quota {
	return &protocol.Quota{Limits: []protocol.QuotaLimit{
		{ID: "five_hour", UsedPercent: pct(fiveHour), ResetsAt: now.Add(fiveHourReset).Unix(), WindowSeconds: 5 * 3600},
		{ID: "weekly", UsedPercent: pct(weekly), ResetsAt: now.Add(weeklyReset).Unix(), WindowSeconds: 7 * 24 * 3600},
		{ID: "credits", Remaining: pct(0), Unit: "credits"},
		{ID: "extra_usage", UsedPercent: pct(100), Unit: "usd"},
	}}
}

type healthStep struct {
	after  time.Duration
	note   string
	do     func(h *harness, rd *fakeReadings)
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

func request(h *harness, _ *fakeReadings) { h.run(h.req("m")) }

func check(read func(now time.Time) (*protocol.Quota, *llm.Error)) func(h *harness, rd *fakeReadings) {
	return func(h *harness, rd *fakeReadings) {
		rd.read = func(string) (*protocol.Quota, *llm.Error) { return read(h.now) }
		h.r.Check(context.Background())
	}
}

func checkUnread(h *harness, rd *fakeReadings) {
	rd.read = nil
	h.r.Check(context.Background())
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
		name     string
		steps    []healthStep
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

func TestTurnedOffAccountIgnoresLateOutcomes(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", ok(), script{start(), {Type: llm.EventError, Error: &llm.Error{Code: llm.CodeQuota, Status: 429, Scope: llm.ScopeCredential, RetryAfterMS: time.Hour.Milliseconds()}}})
	served := h.r.Generate(context.Background(), h.req("m"))
	served.Next()
	limited := h.r.Generate(context.Background(), h.req("m"))
	limited.Next()
	h.setAccounts()
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
	h.setAccounts(accts("a")...)
	if got := h.healthOf("a"); got.State != HealthOK {
		t.Fatalf("turned on again: %+v, want ok", got)
	}
	h.on("a", ok())
	h.runOK("m")
}

func TestHealthNeedsLogin(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", quotaOut(time.Hour))
	h.run(h.req("m"))
	got := h.r.state.Health(Account{ID: "a"}, h.now, Hold{NeedsLogin: true, Why: "invalid_grant"})
	if got.State != HealthNeedsLogin || got.Why != "invalid_grant" || got.Until != nil {
		t.Fatalf("health = %+v", got)
	}
}

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
	st.failure(a, "m", 1, limit(5_000), now, 0)
	if got := left(); got != 2*time.Minute {
		t.Errorf("after an older attempt's sooner word: %v, want 2m", got)
	}
	st.failure(a, "m", 5, &llm.Error{Code: llm.CodeRateLimit}, now, 0)
	if got := left(); got != 2*time.Minute {
		t.Errorf("after a backoff: %v, want 2m", got)
	}
}

func TestResetBounds(t *testing.T) {
	const day = 24 * time.Hour
	for _, c := range []struct {
		ms           int64
		window       int64
		left, check  time.Duration
		source, kind string
	}{
		{1, 0, time.Second, 0, SourceProvider, HealthCooling},
		{(8 * day).Milliseconds(), 0, 8 * day, 15 * time.Minute, SourceProvider, HealthExhausted},
		{(8 * day).Milliseconds() + 1, 0, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
		{1 << 62, 0, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
		{(29 * day).Milliseconds(), 30 * 24 * 60, 29 * day, 15 * time.Minute, SourceProvider, HealthExhausted},
		{(29 * day).Milliseconds(), 5 * 60, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
		{(30*day + time.Hour).Milliseconds(), 30 * 24 * 60, 30*day + time.Hour, 15 * time.Minute, SourceProvider, HealthExhausted},
		{(31 * day).Milliseconds(), 30 * 24 * 60, 30 * time.Minute, 15 * time.Minute, SourceUnverified, HealthExhausted},
	} {
		st := NewState()
		now := time.Unix(1_700_000_000, 0)
		st.failure(&Account{ID: "a"}, "m", 1, &llm.Error{Code: llm.CodeQuota, Scope: llm.ScopeCredential, RetryAfterMS: c.ms, QuotaWindowMinutes: c.window}, now, 0)
		got := st.Health(Account{ID: "a"}, now, Hold{})
		var check time.Duration
		if got.NextCheck != nil {
			check = got.NextCheck.Sub(now)
		}
		if got.Until == nil || got.Until.Sub(now) != c.left || check != c.check || got.Source != c.source || got.State != c.kind {
			t.Errorf("reset of %d ms on a %d-minute window: %+v, want %s for %v from %s, checked in %v", c.ms, c.window, got, c.kind, c.left, c.source, c.check)
		}
	}
}

func TestCheckBelievesAResetItsWindowAllows(t *testing.T) {
	const day = 24 * time.Hour
	for _, c := range []struct {
		name   string
		window int64
		left   time.Duration
		source string
	}{
		{"a monthly window", 30 * 24 * 60 * 60, 29 * day, SourceProvider},
		{"a five-hour window", 5 * 60 * 60, 30 * time.Minute, SourceUnverified},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, Config{}, accts("a")...)
			rd := &fakeReadings{}
			h.r.UseReadings(rd)
			h.on("a", quotaOut(5*time.Hour))
			h.run(h.req("m"))
			h.now = h.now.Add(15 * time.Minute)
			check(func(now time.Time) (*protocol.Quota, *llm.Error) {
				return &protocol.Quota{Limits: []protocol.QuotaLimit{
					{ID: "monthly", UsedPercent: pct(100), ResetsAt: now.Add(29 * day).Unix(), WindowSeconds: c.window},
				}}, nil
			})(h, rd)
			got := h.healthOf("a")
			var left, next time.Duration
			if got.Until != nil {
				left = got.Until.Sub(h.now)
			}
			if got.NextCheck != nil {
				next = got.NextCheck.Sub(h.now)
			}
			if got.State != HealthExhausted || left != c.left || next != 15*time.Minute || got.Source != c.source {
				t.Errorf("after the check: %+v, want exhausted for %v from %s, checked in 15m", got, c.left, c.source)
			}
		})
	}
}

func TestCheckRequestIsOnlyWithoutAReading(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "down"}
	}}
	h.r.UseReadings(rd)
	h.on("a", quotaOut(5*time.Hour))
	h.runOK("m")
	h.now = h.now.Add(15 * time.Minute)
	h.r.Check(context.Background())
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "b")
	if !slices.Equal(rd.calls, []string{"a"}) {
		t.Errorf("readings = %v, want one of a", rd.calls)
	}
	rd.read = nil
	h.now = h.now.Add(15 * time.Minute)
	h.r.Check(context.Background())
	h.on("a", quotaOut(4*time.Hour))
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("a", "b", "b", "b", "a", "b", "b")
}

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

func TestSavedBlocks(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b", "c")...)
	changes := h.r.state.Changes()
	h.on("a", quotaOut(3*time.Hour))
	h.on("b", rateLimited(20*time.Second))
	h.on("c", fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: (30 * 24 * time.Hour).Milliseconds()}), ok())
	h.run(h.req("m"))
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
	h2.runOK("m")
	h2.wantAttempts("d", "d", "c")
	if s := h2.r.state.Saved(h2.now); len(s) != 1 || s[0].Account != "a" {
		t.Errorf("saved after the checks: %+v, want a's", s)
	}
	h2.r.state.Reset(Account{ID: "a"}, h2.now)
	if s := h2.r.state.Saved(h2.now); len(s) != 0 {
		t.Errorf("saved after a reset: %+v", s)
	}
}

func TestSavedBlocksOfEveryKind(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b", "c", "d", "e")...)
	changes := h.r.state.Changes()
	long := strings.Repeat("é", 200)
	h.on("a", rateLimited(6831*time.Second))
	h.on("b", fail(llm.Error{Code: llm.CodeAuth, Status: 401, Retryable: true, Scope: llm.ScopeCredential, Message: "Incorrect API key provided"}))
	h.on("c", fail(llm.Error{Code: llm.CodeNotFound, Status: 404, Retryable: true, Scope: llm.ScopeModel, Message: long}))
	h.on("d", rateLimited(20*time.Second))
	h.runOK("m")
	h.wantAttempts("a", "b", "c", "d", "e")
	select {
	case <-changes:
	default:
		t.Fatal("no change signalled after a rate limit and cooldowns")
	}
	b, err := json.Marshal(h.r.state.Saved(h.now))
	if err != nil {
		t.Fatal(err)
	}
	var back []SavedBlock
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	at := h.now.UTC()
	want := []SavedBlock{
		{Account: "a", Scope: "credential", Kind: "rate_limit", Until: h.now.Add(6831 * time.Second).UTC(), Source: SourceProvider,
			Error: &SavedError{Code: llm.CodeRateLimit, Status: 429, At: &at}},
		{Account: "b", Scope: "credential", Kind: "cooldown", Until: h.now.Add(30 * time.Minute).UTC(), Source: SourceBackoff,
			Error: &SavedError{Code: llm.CodeAuth, Status: 401, Message: "Incorrect API key provided", At: &at}},
		{Account: "c", Scope: "model", Model: "m", Kind: "cooldown", Until: h.now.Add(12 * time.Hour).UTC(), Source: SourceBackoff,
			Error: &SavedError{Code: llm.CodeNotFound, Status: 404, Message: strings.Repeat("é", 128), At: &at}},
	}
	if g, w := indented(t, back), indented(t, want); g != w {
		t.Fatalf("saved\n%s\nwant\n%s", g, w)
	}

	h2 := newHarness(t, Config{}, accts("a", "b", "c", "d", "e")...)
	h2.now = h.now.Add(10 * time.Minute)
	changes = h2.r.state.Changes()
	h2.r.state.Load(back, h2.now, nil)
	for _, w := range []struct {
		id, scope, model, kind, code, message string
		until                                 time.Duration
		health                                string
	}{
		{"a", "credential", "", "rate_limit", llm.CodeRateLimit, "", 6831 * time.Second, HealthCooling},
		{"b", "credential", "", "cooldown", llm.CodeAuth, "Incorrect API key provided", 30 * time.Minute, HealthCooling},
		{"c", "model", "m", "cooldown", llm.CodeNotFound, strings.Repeat("é", 128), 12 * time.Hour, HealthOK},
	} {
		rt := h2.r.state.Runtime(Account{ID: w.id}, h2.now)
		if len(rt.Cooldowns) != 1 {
			t.Fatalf("%s after the restart: cooldowns %+v, want one", w.id, rt.Cooldowns)
		}
		cd := rt.Cooldowns[0]
		if cd.Scope != w.scope || cd.Model != w.model || cd.Kind != w.kind || !cd.Until.Equal(h.now.Add(w.until)) ||
			cd.Error == nil || cd.Error.Code != w.code || cd.Error.Message != w.message {
			t.Errorf("%s after the restart: cooldown %+v (error %+v), want %s %s %s until %v with %s %q", w.id, cd, cd.Error, w.scope, w.model, w.kind, h.now.Add(w.until), w.code, w.message)
		}
		if rt.LastError == nil || rt.LastError.Code != w.code || rt.LastError.Message != w.message || !rt.LastErrorAt.Equal(h.now) {
			t.Errorf("%s after the restart: last error %+v at %v, want %s %q at %v", w.id, rt.LastError, rt.LastErrorAt, w.code, w.message, h.now)
		}
		if got := h2.healthOf(w.id); got.State != w.health {
			t.Errorf("%s after the restart: health %+v, want %s", w.id, got, w.health)
		}
	}
	if got := h2.healthOf("b"); got.Why != "Incorrect API key provided" {
		t.Errorf("b after the restart: health %+v, want the provider's words", got)
	}
	if rt := h2.r.state.Runtime(Account{ID: "d"}, h2.now); len(rt.Cooldowns) != 0 || rt.LastError != nil {
		t.Errorf("d after the restart: %+v, want nothing (its block had 20 seconds left)", rt)
	}
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return windows(h2.now, 10, 2*time.Hour, 5, 100*time.Hour), nil
	}}
	h2.r.UseReadings(rd)
	h2.r.Check(context.Background())
	if len(rd.calls) != 0 {
		t.Errorf("checks at start = %v, want none", rd.calls)
	}
	h2.runOK("m")
	h2.runOK("m")
	h2.wantAttempts("d", "e")

	select {
	case <-changes:
	default:
	}
	h2.r.state.Reset(Account{ID: "a"}, h2.now)
	select {
	case <-changes:
	default:
		t.Error("no change signalled after a's cooldown was cleared")
	}
	if s := h2.r.state.Saved(h2.now); len(s) != 2 || s[0].Account != "b" || s[1].Account != "c" {
		t.Errorf("saved after clearing a: %+v, want b's and c's", s)
	}

	h3 := newHarness(t, Config{}, accts("a")...)
	var old []SavedBlock
	if err := json.Unmarshal([]byte(`[{"account": "a", "scope": "credential", "until": "`+h3.now.Add(3*time.Hour).UTC().Format(time.RFC3339)+`", "source": "provider"}]`), &old); err != nil {
		t.Fatal(err)
	}
	h3.r.state.Load(old, h3.now, nil)
	rt := h3.r.state.Runtime(Account{ID: "a"}, h3.now)
	if len(rt.Cooldowns) != 1 || rt.Cooldowns[0].Kind != "quota" || rt.Cooldowns[0].Error == nil ||
		rt.Cooldowns[0].Error.Message != "a usage window was used up before pluxy restarted" || rt.LastError != nil {
		t.Errorf("a from an older file: %+v, want a quota block with the restart's reason and no last error", rt)
	}
	if got := h3.healthOf("a"); got.State != HealthExhausted {
		t.Errorf("a from an older file: health %+v, want exhausted", got)
	}
}

func indented(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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

func TestCheckEndingABlockStartsTheBackoffOver(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	rd := &fakeReadings{read: func(string) (*protocol.Quota, *llm.Error) {
		return windows(h.now, 0, 5*time.Hour, 0, 7*24*time.Hour), nil
	}}
	h.r.UseReadings(rd)
	backoff := fail(llm.Error{Code: llm.CodeQuota, Status: 429, Retryable: true, Scope: llm.ScopeCredential})
	h.on("a", backoff)
	h.runOK("m")
	if got := h.healthOf("a"); got.State != HealthCooling || got.Until == nil || got.Until.Sub(h.now) != 500*time.Millisecond {
		t.Fatalf("after the first refusal: %+v, want cooling for 500ms", got)
	}
	h.now = h.now.Add(time.Second)
	h.on("a", quotaOut(5*time.Hour))
	h.runOK("m")
	h.now = h.now.Add(15 * time.Minute)
	h.runOK("m")
	h.r.Check(context.Background())
	if got := h.healthOf("a"); got.State != HealthOK {
		t.Fatalf("after the check: %+v, want ok", got)
	}
	h.on("a", backoff)
	h.runOK("m")
	h.wantAttempts("a", "b", "a", "b", "b", "a", "b")
	if got := h.healthOf("a"); got.State != HealthCooling || got.Until == nil || got.Until.Sub(h.now) != 500*time.Millisecond {
		t.Errorf("after the refusal that followed the check: %+v, want cooling for 500ms, the first step", got)
	}
}

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

func TestBannedAccountScenario6AndPersistence(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst, DisableCooling: true}, accts("a", "b")...)
	h.on("a",
		ok(),
		script{start(), {Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: time.Hour.Milliseconds()}}},
		fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential, Message: "Your account has been suspended."}),
	)
	h.on("b", ok())
	sOK := h.r.Generate(context.Background(), h.req("m"))
	s429 := h.r.Generate(context.Background(), h.req("m"))
	sBan := h.r.Generate(context.Background(), h.req("m"))
	if ev := sOK.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("stale ok first event = %+v", ev)
	}
	if ev := s429.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("stale 429 first event = %+v", ev)
	}
	if ev := sBan.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("sBan should have rotated to b and started: %+v", ev)
	}
	for ev := sBan.Next(); !ev.Terminal(); ev = sBan.Next() {
	}
	sBan.Close()

	got := h.healthOf("a")
	if got.State != HealthBanned || got.Why != "Your account has been suspended." || got.Source != SourceProvider || got.Until != nil || got.NextCheck != nil {
		t.Fatalf("after account_closed: %+v, want banned with provider words and source provider", got)
	}

	for ev := sOK.Next(); !ev.Terminal(); ev = sOK.Next() {
	}
	sOK.Close()
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("after stale success: %+v, want still banned", got)
	}

	for ev := s429.Next(); !ev.Terminal(); ev = s429.Next() {
	}
	s429.Close()
	if got := h.healthOf("a"); got.State != HealthBanned || got.Until != nil || got.Why != "Your account has been suspended." {
		t.Fatalf("after stale 429: %+v, want still banned without cooldown", got)
	}
	if rt := h.r.state.Runtime(Account{ID: "a"}, h.now); len(rt.Cooldowns) != 0 || rt.LastError == nil || rt.LastError.Code != llm.CodeAccountClosed {
		t.Fatalf("runtime after stale 429: %+v, want no cooldowns and LastError account_closed", rt)
	}

	if cleared := h.r.state.Reset(Account{ID: "a"}, h.now); cleared != 0 {
		t.Errorf("Reset cleared = %d, want 0", cleared)
	}
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("after Reset: %+v, want still banned", got)
	}
	if rt := h.r.state.Runtime(Account{ID: "a"}, h.now); rt.LastError == nil || rt.LastError.Code != llm.CodeAccountClosed {
		t.Fatalf("runtime after Reset: %+v, want the ban's LastError kept", rt)
	}

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
	h2.on("a", ok())
	h2.on("b", ok())
	h2.runOK("m")
	h2.wantAttempts("b")

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

func TestEveryAccountBannedAnswersWithTheBan(t *testing.T) {
	h := newHarness(t, Config{}, accts("a")...)
	h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential, Message: "Your account has been suspended."}))
	first := h.run(h.req("m"))
	if last := first[len(first)-1]; last.Error == nil || last.Error.Code != llm.CodeAccountClosed {
		t.Fatalf("first request = %+v, want the ban", last.Error)
	}
	evs := h.run(h.req("m"))
	last := evs[len(evs)-1]
	if last.Type != llm.EventError || last.Error == nil || last.Error.Code != llm.CodeAccountClosed {
		t.Fatalf("second request = %+v, want the ban", last.Error)
	}
	if last.Error.Message != "Your account has been suspended." {
		t.Errorf("second request's message = %q, want the provider's words", last.Error.Message)
	}
	h.wantAttempts("a")
}

func TestBanSurvivesLeavingRouterSettingsChangeAndRetain(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 400, Retryable: true, Scope: llm.ScopeCredential, Message: "This organization has been disabled."}))
	h.on("b", ok())
	h.runOK("m")
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Fatalf("a health after account_closed = %+v, want banned", got)
	}

	h.setAccounts(accts("b")...)
	if !h.r.state.IsBanned("a") {
		t.Errorf("after leaving router via SetAccounts: IsBanned(a) = false, want true")
	}
	h.setAccounts(accts("a", "b")...)
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Errorf("after returning to router: a health = %+v, want banned", got)
	}

	h.setAccounts(withSettings(Account{ID: "a"}, `{"base_url":"https://other.invalid"}`), Account{ID: "b"})
	if got := h.healthOf("a"); got.State != HealthBanned {
		t.Errorf("after settings change: a health = %+v, want banned", got)
	}

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

	h.on("a", ok())
	h.on("b", ok())
	h.attempts = nil
	h.runOK("m")
	h.wantAttempts("b")

	r3.state.RetainBans(func(id string) bool { return id != "a" })
	if r3.state.IsBanned("a") {
		t.Errorf("after RetainBans without a: IsBanned(a) = true, want false")
	}
	if got := r3.state.Health(Account{ID: "a"}, h.now, Hold{}); got.State != HealthOK {
		t.Errorf("after deletion: a health = %+v, want ok", got)
	}
}
