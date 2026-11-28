package route

import (
	"slices"
	"testing"
	"time"
)

type chosen struct{ account, why string }

func (h *harness) runWhy(req Request) []chosen {
	var out []chosen
	req.Attempting = func(a Attempt) { out = append(out, chosen{a.Account, a.Why}) }
	h.run(req)
	return out
}

func TestWhyAccountChosen(t *testing.T) {
	type run struct {
		setup func(h *harness)
		req   func(h *harness) Request
		want  []chosen
	}
	plain := func(h *harness) Request { return h.req("m") }
	session := func(h *harness) Request {
		r := h.req("m")
		r.Tenant, r.Session = "t", "conversation"
		return r
	}
	cases := []struct {
		name     string
		cfg      Config
		accounts []Account
		runs     []run
	}{
		{"the only account", Config{}, accts("a"), []run{
			{req: plain, want: []chosen{{"a", WhyOnly}}},
		}},
		{"alone in the first group, then a backup", Config{}, []Account{{ID: "low"}, {ID: "high", Priority: 1}}, []run{
			{req: plain, want: []chosen{{"high", WhyFirst}}},
			{setup: func(h *harness) { h.on("high", rateLimited(time.Minute)) }, req: plain,
				want: []chosen{{"high", WhyFirst}, {"low", WhyBackup}}},
			{req: plain, want: []chosen{{"low", WhyBackup}}},
		}},
		{"turns in one group", Config{}, accts("a", "b"), []run{
			{req: plain, want: []chosen{{"a", StrategyRoundRobin}}},
			{req: plain, want: []chosen{{"b", StrategyRoundRobin}}},
		}},
		{"the only one of its group left", Config{}, accts("a", "b"), []run{
			{setup: func(h *harness) { h.on("a", rateLimited(time.Minute)) }, req: plain,
				want: []chosen{{"a", StrategyRoundRobin}, {"b", WhyOnlyFree}}},
		}},
		{"fill first", Config{Strategy: StrategyFillFirst}, accts("a", "b"), []run{
			{req: plain, want: []chosen{{"a", StrategyFillFirst}}},
		}},
		{"the conversation's account", Config{}, accts("a", "b"), []run{
			{req: session, want: []chosen{{"a", StrategyRoundRobin}}},
			{req: session, want: []chosen{{"a", WhySession}}},
		}},
		{"pinned", Config{}, accts("a", "b"), []run{
			{req: func(h *harness) Request { r := h.req("m"); r.Pin = "b"; return r },
				want: []chosen{{"b", WhyPinned}}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.cfg, tc.accounts...)
			for i, r := range tc.runs {
				if r.setup != nil {
					r.setup(h)
				}
				if got := h.runWhy(r.req(h)); !slices.Equal(got, r.want) {
					t.Errorf("request %d: attempts %v, want %v", i+1, got, r.want)
				}
			}
		})
	}
}
