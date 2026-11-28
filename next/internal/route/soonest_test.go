package route

import (
	"context"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const (
	day  = 24 * time.Hour
	week = 7 * day
)

func weeklyReading(now time.Time, weekly time.Duration) *protocol.Quota {
	return windows(now, 40, time.Hour, 10, weekly)
}

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

func TestSoonestResetHourRoundingTies(t *testing.T) {
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"a": weeklyIn(2*day + 70*time.Minute),
		"b": weeklyIn(2*day + 50*time.Minute),
		"c": weeklyIn(2*day + 3*time.Hour),
	}, Config{}, accts("a", "b", "c")...)
	for range 4 {
		h.runOK("m")
	}
	h.wantAttempts("a", "b", "a", "b")
}

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

	h.now = h.now.Add(time.Hour + time.Second)
	wantOrder(t, planOrder(h, "m"), "full", "late", "unknown")
}

func TestSoonestResetNeverPicksBlockedOrHeld(t *testing.T) {
	held := Account{ID: "held", Held: HealthNeedsLogin}
	h, _ := soonestHarness(t, map[string]func(time.Time) *protocol.Quota{
		"blocked": weeklyIn(day),
		"held":    weeklyIn(day),
		"mid":     weeklyIn(3 * day),
		"late":    weeklyIn(6 * day),
	}, Config{}, Account{ID: "blocked"}, held, Account{ID: "mid"}, Account{ID: "late"})
	h.on("blocked", rateLimited(time.Hour))
	h.runOK("m")
	h.runOK("m")
	h.runOK("m")
	h.wantAttempts("blocked", "mid", "mid", "mid")
	wantOrder(t, planOrder(h, "m"), "mid", "late")
}

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

func TestSoonestResetKeepsSessionAffinity(t *testing.T) {
	reads := map[string]func(time.Time) *protocol.Quota{"a": weeklyIn(2 * day), "b": weeklyIn(5 * day), "c": weeklyIn(6 * day)}
	h, _ := soonestHarness(t, reads, Config{}, accts("a", "b", "c")...)
	s1 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s1"}
	s2 := Request{LLM: llm.Request{Model: "m"}, Tenant: "t", Session: "s2"}
	h.run(s1)
	reads["c"] = weeklyIn(day)
	h.now = h.now.Add(defaultQuotaRefresh)
	h.r.Refresh(context.Background())
	h.run(s1)
	h.run(s2)
	h.wantAttempts("a", "a", "c")
}

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

	ms := int64(time.Minute / time.Millisecond)
	h2, rd2 := soonestHarness(t, reads, Config{QuotaRefreshMS: &ms}, accts("a")...)
	h2.now = h2.now.Add(time.Minute)
	h2.r.Refresh(context.Background())
	if len(rd2.calls) != 2 {
		t.Fatalf("one-minute interval: %d readings, want 2", len(rd2.calls))
	}

	zero := int64(0)
	h3, rd3 := soonestHarness(t, reads, Config{QuotaRefreshMS: &zero}, accts("a", "b")...)
	for range 3 {
		h3.now = h3.now.Add(time.Hour)
		h3.r.Refresh(context.Background())
	}
	if len(rd3.calls) != 0 {
		t.Fatalf("quota_refresh_ms 0: %d readings, want none", len(rd3.calls))
	}

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

func TestSoonestResetUsesCheckReadings(t *testing.T) {
	zero := int64(0)
	h, rd := soonestHarness(t, nil, Config{QuotaRefreshMS: &zero}, accts("a", "b", "c")...)
	h.r.state.SetCheckInterval(15 * time.Minute)
	h.on("a", quotaOut(3*time.Hour))
	h.runOK("m")
	h.wantAttempts("a", "b")
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
