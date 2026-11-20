package route

import (
	"context"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The harness's clock starts at 2023-11-14 22:13:20 UTC; the resets below are chosen against it.

const (
	day  = 24 * time.Hour
	week = 7 * day
)

// weeklyReading is a reading with a five-hour window that resets in an hour and a weekly window
// that resets weekly from now, neither full.
func weeklyReading(now time.Time, weekly time.Duration) *protocol.Quota {
	return windows(now, 40, time.Hour, 10, weekly)
}

// soonestHarness is a harness on soonest-reset whose accounts read as reads gives, after one
// refresh; an account reads gives nothing for has no reading.
func soonestHarness(t *testing.T, reads map[string]func(now time.Time) *protocol.Quota, cfg Config, accounts ...Account) (*harness, *fakeReadings) {
	t.Helper()
	cfg.Strategy = StrategySoonestReset
	h := newHarness(t, cfg, accounts...)
	rd := &fakeReadings{read: func(id string) (*protocol.Quota, *llm.Error) {
		if f := reads[id]; f != nil {
			return f(h.now), nil
		}
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "no reading"}
	}}
	h.r.UseReadings(rd)
	h.r.Refresh(context.Background())
	return h, rd
}

func weeklyIn(d time.Duration) func(time.Time) *protocol.Quota {
	return func(now time.Time) *protocol.Quota { return weeklyReading(now, d) }
}

// planOrder is the IDs of the accounts a request for m would try, in order.
func planOrder(h *harness, m string) []string {
	var out []string
	for _, s := range h.r.Plan(context.Background(), m).Order {
		out = append(out, s.ID)
	}
	return out
}

func wantOrder(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

// New requests go to the account whose longest window resets soonest, whatever its shorter
// windows say; the plan ranks every account that way, with the account that has no reading last.
func TestSoonestResetOrder(t *testing.T) {
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"a": weeklyIn(5 * day),
		"b": weeklyIn(2 * day),
		"d": weeklyIn(3 * day),
	}, Config{}, accts("a", "b", "c", "d")...)
	h.runOK("m")
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("b", "b", "b")
	wantOrder(t, planOrder(h, "m"), "b", "d", "a", "c")

	p := h.r.Plan(context.Background(), "m")
	b := p.Order[0].Rank
	wantReset := h.now.Add(2 * day).UTC().Round(time.Hour)
	if b == nil || b.Reset == nil || !b.Reset.Equal(wantReset) || b.WindowSeconds != int64(week/time.Second) || b.FullUntil != nil {
		t.Fatalf("b's rank %+v, want its weekly reset %v", b, wantReset)
	}
	if c := p.Order[3].Rank; c == nil || c.Reset != nil || c.FullUntil != nil {
		t.Fatalf("c's rank %+v, want none known", c)
	}
}

// Resets rounded to the hour tie: two accounts whose weeks end 20 minutes apart take turns,
// and one whose week ends three hours later waits.
func TestSoonestResetHourRoundingTies(t *testing.T) {
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"a": weeklyIn(2*day + 70*time.Minute), // 23:23:20, the hour 23:00
		"b": weeklyIn(2*day + 50*time.Minute), // 23:03:20, the hour 23:00
		"c": weeklyIn(2*day + 3*time.Hour),
	}, Config{}, accts("a", "b", "c")...)
	for range 4 {
		h.runOK("m")
	}
	h.wantAttempts("a", "b", "a", "b")
}

// An account with no known reset comes after every account with one, and an account with a
// full window after both, however soon its week ends.
func TestSoonestResetUnknownAndFullLast(t *testing.T) {
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"full": func(now time.Time) *protocol.Quota { return windows(now, 100, time.Hour, 10, day) },
		"late": weeklyIn(6 * day),
	}, Config{}, accts("full", "unknown", "late")...)
	h.runOK("m")
	h.wantAttempts("late")
	wantOrder(t, planOrder(h, "m"), "late", "unknown", "full")
	p := h.r.Plan(context.Background(), "m")
	if f := p.Order[2].Rank; f == nil || f.FullUntil == nil || !f.FullUntil.Equal(h.now.Add(time.Hour).UTC()) {
		t.Fatalf("full's rank %+v, want full until its five-hour reset", f)
	}

	// Once the full window has reset, the account ranks by its week again, and goes first.
	h.now = h.now.Add(time.Hour + time.Second)
	wantOrder(t, planOrder(h, "m"), "full", "late", "unknown")
}

// A blocked account and a held one are never picked, however soon their weeks end.
func TestSoonestResetNeverPicksBlockedOrHeld(t *testing.T) {
	held := Account{ID: "held", Held: HealthNeedsLogin}
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"blocked": weeklyIn(day),
		"held":    weeklyIn(day),
		"mid":     weeklyIn(3 * day),
		"late":    weeklyIn(6 * day),
	}, Config{}, Account{ID: "blocked"}, held, Account{ID: "mid"}, Account{ID: "late"})
	h.on("blocked", rateLimited(time.Hour))
	h.runOK("m") // blocked refuses, mid serves
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("blocked", "mid", "mid", "mid")
	wantOrder(t, planOrder(h, "m"), "mid", "late")
}

// Priority comes first: a higher-priority account is picked even when a lower one's week ends
// sooner.
func TestSoonestResetPriorityFirst(t *testing.T) {
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"low":   weeklyIn(day),
		"high6": weeklyIn(6 * day),
		"high4": weeklyIn(4 * day),
	}, Config{}, Account{ID: "low"}, Account{ID: "high6", Priority: 1}, Account{ID: "high4", Priority: 1})
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("high4", "high4")
	wantOrder(t, planOrder(h, "m"), "high4", "high6", "low")
}

// A session stays on its account when a new reading ranks another first; new sessions follow
// the new reading.
func TestSoonestResetKeepsSessionAffinity(t *testing.T) {
	reads := map[string]func(time.Time) *protocol.Quota{"a": weeklyIn(2 * day), "b": weeklyIn(5 * day), "c": weeklyIn(6 * day)}
	h, _ := soonestHarness(t, reads, Config{}, accts("a", "b", "c")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	s2 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s2"}
	h.run(s1) // a
	reads["c"] = weeklyIn(day)
	h.now = h.now.Add(defaultQuotaRefresh)
	h.r.Refresh(context.Background())
	h.run(s1) // still a
	h.run(s2) // c
	h.wantAttempts("a", "a", "c")
}

// Refresh reads every routed account once per routing.quota_refresh_ms (30 minutes when unset),
// and not at all when it is 0 or the strategy is another.
func TestQuotaRefreshInterval(t *testing.T) {
	reads := map[string]func(time.Time) *protocol.Quota{"a": weeklyIn(day), "b": weeklyIn(2 * day)}
	h, rd := soonestHarness(t, reads, Config{}, accts("a", "b")...)
	wantCalls := func(want int) {
		t.Helper()
		if len(rd.calls) != want {
			t.Fatalf("%d readings (%v), want %d", len(rd.calls), rd.calls, want)
		}
	}
	wantCalls(2)
	h.now = h.now.Add(29 * time.Minute)
	h.r.Refresh(context.Background())
	wantCalls(2)
	h.now = h.now.Add(time.Minute)
	h.r.Refresh(context.Background())
	wantCalls(4)

	// A shorter interval from the config.
	ms := int64(time.Minute / time.Millisecond)
	h2, rd2 := soonestHarness(t, reads, Config{QuotaRefreshMS: &ms}, accts("a")...)
	h2.now = h2.now.Add(time.Minute)
	h2.r.Refresh(context.Background())
	if len(rd2.calls) != 2 {
		t.Fatalf("one-minute interval: %d readings, want 2", len(rd2.calls))
	}

	// 0 turns the readings off.
	zero := int64(0)
	h3, rd3 := soonestHarness(t, reads, Config{QuotaRefreshMS: &zero}, accts("a", "b")...)
	for range 3 {
		h3.now = h3.now.Add(time.Hour)
		h3.r.Refresh(context.Background())
	}
	if len(rd3.calls) != 0 {
		t.Fatalf("quota_refresh_ms 0: %d readings, want none", len(rd3.calls))
	}

	// Another strategy reads nothing.
	h4 := newHarness(t, Config{}, accts("a")...)
	rd4 := &fakeReadings{}
	h4.r.UseReadings(rd4)
	h4.r.Refresh(context.Background())
	if len(rd4.calls) != 0 {
		t.Fatalf("round-robin: %d readings, want none", len(rd4.calls))
	}
}

func TestQuotaRefreshNegativeIsInvalid(t *testing.T) {
	n := int64(-1)
	if err := (&Config{QuotaRefreshMS: &n}).Validate(); err == nil {
		t.Fatal("a negative quota_refresh_ms passed validation")
	}
}

// A reading the checks of long blocks take ranks the account too.
func TestSoonestResetUsesCheckReadings(t *testing.T) {
	zero := int64(0)
	h, rd := soonestHarness(t, nil, Config{QuotaRefreshMS: &zero}, accts("a", "b", "c")...)
	h.r.state.SetCheckInterval(15 * time.Minute)
	h.on("a", quotaOut(3*time.Hour))
	h.runOK("m") // a refuses for 3 hours, b serves; with no readings the turn goes on to c
	h.wantAttempts("a", "b")
	// a's check reads its window free again, its week ending sooner than the others' (unknown).
	rd.read = func(id string) (*protocol.Quota, *llm.Error) {
		if id == "a" {
			return weeklyReading(h.now, 2*day), nil
		}
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "no reading"}
	}
	h.now = h.now.Add(15 * time.Minute)
	h.r.Check(context.Background())
	if len(rd.calls) != 1 {
		t.Fatalf("readings %v, want a's check alone", rd.calls)
	}
	wantOrder(t, planOrder(h, "m"), "a", "b", "c")
}
