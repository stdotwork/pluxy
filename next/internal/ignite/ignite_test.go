package ignite

import (
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

var loc = time.FixedZone("test", 2*60*60)

func at(day, hour, min, sec int) time.Time { return time.Date(2026, 11, day, hour, min, sec, 0, loc) }

func ip(v int) *int         { return &v }
func i64(v int64) *int64    { return &v }
func pf(v float64) *float64 { return &v }
func bp(v bool) *bool       { return &v }

func owner() *Config {
	return &Config{Enabled: true, StartHour: ip(7), EndHour: ip(22), EndGraceMS: i64(30 * 60 * 1000), GraceMS: i64(2000),
		RetryMS: i64(300000), BackoffMultiplier: pf(3), MaxFailures: ip(3), HoldMS: i64(60000),
		Extensions: map[string]Extension{"pluxy/claude": {}, "pluxy/codex": {}}}
}

func TestInHours(t *testing.T) {
	cases := []struct {
		name       string
		start, end int
		graceMin   int64
		now, want  time.Time
	}{
		{"before the start", 7, 22, 30, at(10, 6, 59, 59), at(10, 7, 0, 0)},
		{"at the start", 7, 22, 30, at(10, 7, 0, 0), at(10, 7, 0, 0)},
		{"after the end hour, in the grace", 7, 22, 30, at(10, 22, 29, 59), at(10, 22, 29, 59)},
		{"after the grace", 7, 22, 30, at(10, 22, 30, 0), at(11, 7, 0, 0)},
		{"before midnight", 7, 22, 30, at(10, 23, 50, 0), at(11, 7, 0, 0)},
		{"over midnight: before it", 22, 6, 0, at(10, 23, 0, 0), at(10, 23, 0, 0)},
		{"over midnight: after it", 22, 6, 0, at(11, 5, 59, 0), at(11, 5, 59, 0)},
		{"over midnight: the day", 22, 6, 0, at(11, 12, 0, 0), at(11, 22, 0, 0)},
		{"over midnight: the grace past the end", 22, 6, 15, at(11, 6, 10, 0), at(11, 6, 10, 0)},
		{"the whole day", 0, 0, 0, at(10, 3, 0, 0), at(10, 3, 0, 0)},
		{"end before start, grace past the start: the whole day", 7, 6, 90, at(10, 6, 59, 0), at(10, 6, 59, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Enabled: true, StartHour: ip(tc.start), EndHour: ip(tc.end), EndGraceMS: i64(tc.graceMin * 60 * 1000)}
			if got := c.InHours(tc.now); !got.Equal(tc.want) {
				t.Fatalf("InHours(%s) = %s, want %s", tc.now, got, tc.want)
			}
		})
	}
	var c Config
	if got := c.InHours(at(10, 22, 30, 0)); !got.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("default hours: InHours(22:30) = %s, want 07:00 the next day", got)
	}
}

func claudeReading(fiveReset int64, fiveUsed float64, weekUsed float64) *protocol.Quota {
	week := at(14, 0, 0, 0).Unix()
	return &protocol.Quota{Limits: []protocol.QuotaLimit{
		{ID: "five_hour", UsedPercent: pf(fiveUsed), ResetsAt: fiveReset, WindowSeconds: FiveHours},
		{ID: "seven_day", UsedPercent: pf(weekUsed), ResetsAt: week, WindowSeconds: 7 * 86400},
		{ID: "seven_day_sonnet", UsedPercent: pf(100), ResetsAt: week, WindowSeconds: 7 * 86400, Models: []string{"claude-sonnet-5"}},
		{ID: "seven_day_cowork", UsedPercent: pf(3), ResetsAt: week, WindowSeconds: 7 * 86400, Feature: true},
		{ID: "extra_usage", UsedPercent: pf(10), Unit: "usd"},
	}}
}

func TestWindows(t *testing.T) {
	now := at(10, 12, 0, 0)
	t.Run("claude: the five-hour window only; a full family window blocks nothing", func(t *testing.T) {
		ws := Windows(claudeReading(now.Add(time.Hour).Unix(), 30, 20).Limits, now)
		if len(ws) != 1 || ws[0].ID != "five_hour" || !ws[0].Running || !ws[0].Reset.Equal(now.Add(time.Hour)) || !ws[0].BlockedUntil.IsZero() {
			t.Fatalf("windows = %+v", ws)
		}
	})
	t.Run("a window that has not started has no reset", func(t *testing.T) {
		ws := Windows(claudeReading(0, 0, 20).Limits, now)
		if len(ws) != 1 || ws[0].Running {
			t.Fatalf("windows = %+v", ws)
		}
	})
	t.Run("a reset in the past", func(t *testing.T) {
		ws := Windows(claudeReading(now.Add(-time.Second).Unix(), 80, 20).Limits, now)
		if len(ws) != 1 || ws[0].Running {
			t.Fatalf("windows = %+v", ws)
		}
	})
	t.Run("a full weekly window blocks it", func(t *testing.T) {
		ws := Windows(claudeReading(0, 0, 100).Limits, now)
		if len(ws) != 1 || !ws[0].BlockedUntil.Equal(at(14, 0, 0, 0)) {
			t.Fatalf("windows = %+v", ws)
		}
	})
	t.Run("an unused window whose reset moves with the clock is not running", func(t *testing.T) {
		l := []protocol.QuotaLimit{{ID: "five_hour", UsedPercent: pf(0), ResetsAt: now.Add(5*time.Hour + 3*time.Second).Unix(), WindowSeconds: FiveHours}}
		if ws := Windows(l, now); len(ws) != 1 || ws[0].Running {
			t.Fatalf("placeholder: windows = %+v", ws)
		}
		l[0].ResetsAt = now.Add(5*time.Hour - 15*time.Second).Unix()
		if ws := Windows(l, now); len(ws) != 1 || !ws[0].Running {
			t.Fatalf("started: windows = %+v", ws)
		}
		l[0].ResetsAt, l[0].UsedPercent = now.Add(5*time.Hour).Unix(), pf(1)
		if ws := Windows(l, now); len(ws) != 1 || !ws[0].Running {
			t.Fatalf("used: windows = %+v", ws)
		}
	})
	t.Run("antigravity: a five-hour window per group, each blocked by its own group's", func(t *testing.T) {
		gem, cg := []string{"Gemini 3 Flash"}, []string{"Claude Sonnet 4.6", "GPT-OSS 120B"}
		l := []protocol.QuotaLimit{
			{ID: "gemini_models_five_hour", UsedPercent: pf(0), WindowSeconds: FiveHours, Models: gem},
			{ID: "gemini_models_weekly", UsedPercent: pf(100), ResetsAt: at(12, 0, 0, 0).Unix(), WindowSeconds: 7 * 86400, Models: gem},
			{ID: "claude_and_gpt_models_five_hour", UsedPercent: pf(0), WindowSeconds: FiveHours, Models: cg},
			{ID: "claude_and_gpt_models_weekly", UsedPercent: pf(40), ResetsAt: at(12, 0, 0, 0).Unix(), WindowSeconds: 7 * 86400, Models: cg},
		}
		ws := Windows(l, now)
		if len(ws) != 2 || ws[0].ID != "gemini_models_five_hour" || ws[1].ID != "claude_and_gpt_models_five_hour" {
			t.Fatalf("windows = %+v", ws)
		}
		if !ws[0].BlockedUntil.Equal(at(12, 0, 0, 0)) || !ws[1].BlockedUntil.IsZero() {
			t.Fatalf("blocks: %s, %s", ws[0].BlockedUntil, ws[1].BlockedUntil)
		}
	})
	t.Run("a provider with one window: that one, of any length", func(t *testing.T) {
		l := []protocol.QuotaLimit{{ID: "daily", UsedPercent: pf(0), WindowSeconds: 86400}, {ID: "credits", Remaining: pf(3), Unit: "credits"}}
		if ws := Windows(l, now); len(ws) != 1 || ws[0].ID != "daily" {
			t.Fatalf("windows = %+v", ws)
		}
	})
	t.Run("two windows, neither of five hours: none", func(t *testing.T) {
		l := []protocol.QuotaLimit{{ID: "daily", WindowSeconds: 86400}, {ID: "weekly", WindowSeconds: 7 * 86400}}
		if ws := Windows(l, now); len(ws) != 0 {
			t.Fatalf("windows = %+v", ws)
		}
	})
}

func run(t *testing.T, p *Planner, q *protocol.Quota, h Health, now time.Time) []Fire {
	t.Helper()
	due := p.Due(now)
	if len(due) == 0 {
		return nil
	}
	if len(due) != 1 || due[0] != "a" {
		t.Fatalf("due = %v", due)
	}
	return p.Plan("a", q, h, now)
}

var ok = Health{OK: true}

func newOwnerPlanner(now time.Time) *Planner {
	p := NewPlanner()
	p.Set(owner(), []Ref{{ID: "a", Extension: "pluxy/claude"}}, now)
	return p
}

func TestPlanStartsWindowAfterItsReset(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	reset := at(10, 14, 30, 0)
	if f := run(t, p, claudeReading(reset.Unix(), 40, 10), ok, now); len(f) != 0 {
		t.Fatalf("a running window was started: %+v", f)
	}
	if got := p.Next(); !got.Equal(reset.Add(2 * time.Second)) {
		t.Fatalf("next reading %s, want %s", got, reset.Add(2*time.Second))
	}
	if d := p.Due(reset.Add(time.Second)); len(d) != 0 {
		t.Fatalf("due before the grace ends: %v", d)
	}
	v := p.View("a")
	if len(v.Windows) != 1 || v.Windows[0].State != StateRunning || !v.Windows[0].Next.Equal(reset.Add(2*time.Second)) {
		t.Fatalf("view = %+v", v)
	}
	now = reset.Add(2 * time.Second)
	f := run(t, p, claudeReading(0, 0, 10), ok, now)
	if len(f) != 1 || f[0].Window.ID != "five_hour" || f[0].Model != "" {
		t.Fatalf("fires = %+v", f)
	}
	p.Done("a", "five_hour", "claude-haiku-5-5", nil, now)
	if v := p.View("a"); v.Windows[0].State != StateStarting || v.Windows[0].Model != "claude-haiku-5-5" {
		t.Fatalf("view after the request = %+v", v.Windows[0])
	}
	if got := p.Next(); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("next reading %s, want a minute on", got)
	}
	now = now.Add(time.Minute)
	if f := run(t, p, claudeReading(now.Add(5*time.Hour-time.Minute).Unix(), 0, 10), ok, now); len(f) != 0 {
		t.Fatalf("started twice: %+v", f)
	}
	if v := p.View("a"); v.Windows[0].State != StateRunning || v.Windows[0].Error != "" {
		t.Fatalf("view once started = %+v", v.Windows[0])
	}
}

func TestPlanWaitsForTheHours(t *testing.T) {
	now := at(10, 23, 10, 2)
	p := newOwnerPlanner(now)
	if f := run(t, p, claudeReading(0, 0, 10), ok, now); len(f) != 0 {
		t.Fatalf("started outside the hours: %+v", f)
	}
	if got := p.Next(); !got.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("next reading %s, want 07:00", got)
	}
	v := p.View("a").Windows[0]
	if v.State != StateWaiting || !v.Next.Equal(at(11, 7, 0, 0)) || v.Why != "the start of the hours" {
		t.Fatalf("view = %+v", v)
	}
	if f := run(t, p, claudeReading(0, 0, 10), ok, at(11, 7, 0, 0)); len(f) != 1 {
		t.Fatalf("not started at 07:00: %+v", f)
	}
	p = newOwnerPlanner(at(10, 22, 20, 0))
	if f := run(t, p, claudeReading(0, 0, 10), ok, at(10, 22, 20, 0)); len(f) != 1 {
		t.Fatalf("not started in the end grace: %+v", f)
	}
	p = newOwnerPlanner(at(10, 20, 0, 0))
	run(t, p, claudeReading(at(11, 1, 0, 0).Unix(), 50, 10), ok, at(10, 20, 0, 0))
	if v := p.View("a").Windows[0]; !v.Next.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("next ignition %s, want 07:00", v.Next)
	}
}

func TestPlanRetriesWithBackoffThenPauses(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	fail := &Failure{Message: "the provider is overloaded"}
	waits := []time.Duration{5 * time.Minute, 15 * time.Minute}
	for i, wait := range waits {
		if f := run(t, p, claudeReading(0, 0, 10), ok, now); len(f) != 1 {
			t.Fatalf("try %d: fires = %+v", i+1, f)
		}
		p.Done("a", "five_hour", "m", fail, now)
		if got := p.Next(); !got.Equal(now.Add(wait)) {
			t.Fatalf("after failure %d: next reading %s, want %s later", i+1, got, wait)
		}
		v := p.View("a").Windows[0]
		if v.State != StateWaiting || v.Failures != i+1 || v.Error != fail.Message {
			t.Fatalf("after failure %d: view = %+v", i+1, v)
		}
		if f := p.Plan("a", claudeReading(0, 0, 10), ok, now.Add(wait-time.Second)); len(f) != 0 {
			t.Fatalf("started during the wait: %+v", f)
		}
		now = now.Add(wait)
	}
	if f := run(t, p, claudeReading(0, 0, 10), ok, now); len(f) != 1 {
		t.Fatalf("third try: fires = %+v", f)
	}
	p.Done("a", "five_hour", "m", fail, now)
	if got := p.Next(); !got.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("paused: next reading %s, want 07:00", got)
	}
	if v := p.View("a").Windows[0]; v.State != StatePaused || !v.Next.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("paused: view = %+v", v)
	}
	if f := run(t, p, claudeReading(0, 0, 10), ok, at(11, 7, 0, 0)); len(f) != 1 {
		t.Fatalf("not tried after the pause: %+v", f)
	}
	p.Done("a", "five_hour", "m", fail, at(11, 7, 0, 0))
	if got := p.Next(); !got.Equal(at(11, 7, 5, 0)) {
		t.Fatalf("after the pause: next reading %s, want 07:05", got)
	}
}

func TestPlanHardFailurePausesAtOnce(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	run(t, p, claudeReading(0, 0, 10), ok, now)
	p.Done("a", "five_hour", "m", &Failure{Message: "no such model", Hard: true}, now)
	if got := p.Next(); !got.Equal(at(11, 7, 0, 0)) {
		t.Fatalf("next reading %s, want 07:00 the next day", got)
	}
}

func TestPlanCountsAWindowThatDidNotStart(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	run(t, p, claudeReading(0, 0, 10), ok, now)
	p.Done("a", "five_hour", "m", nil, now)
	if f := p.Plan("a", claudeReading(0, 0, 10), ok, now.Add(30*time.Second)); len(f) != 0 {
		t.Fatalf("started again during the hold: %+v", f)
	}
	now = now.Add(time.Minute)
	if f := run(t, p, claudeReading(0, 0, 10), ok, now); len(f) != 0 {
		t.Fatalf("started again at once: %+v", f)
	}
	v := p.View("a").Windows[0]
	if v.Failures != 1 || !strings.Contains(v.Error, "had not started") || !v.Next.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("view = %+v", v)
	}
}

func TestPlanSkipsAccountsThatCannotServe(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	until := now.Add(20 * time.Minute)
	if f := run(t, p, claudeReading(0, 0, 10), Health{Until: until, Why: "a rate limit"}, now); len(f) != 0 {
		t.Fatalf("started a cooling account: %+v", f)
	}
	if got := p.Next(); !got.Equal(until.Add(2 * time.Second)) {
		t.Fatalf("next reading %s, want the block's end and the grace", got)
	}
	p = newOwnerPlanner(now)
	if f := run(t, p, claudeReading(0, 0, 10), Health{Why: "banned"}, now); len(f) != 0 {
		t.Fatalf("started a banned account: %+v", f)
	}
	if got := p.Next(); !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("next reading %s, want 5 minutes on", got)
	}
	p = newOwnerPlanner(now)
	if f := run(t, p, claudeReading(0, 0, 100), ok, now); len(f) != 0 {
		t.Fatalf("started under a full weekly window: %+v", f)
	}
	if got := p.Next(); !got.Equal(at(14, 7, 0, 0)) {
		t.Fatalf("next reading %s, want 07:00 after the weekly reset (00:00 is outside the hours)", got)
	}
	p.Set(owner(), nil, now)
	if d, v := p.Due(now.Add(48*time.Hour)), p.View("a"); len(d) != 0 || v != nil {
		t.Fatalf("a dropped account: due %v, view %+v", d, v)
	}
}

func TestPlanReadings(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	p.ReadFailed("a", "upstream unreachable", now)
	if got := p.Next(); !got.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("after a failed reading: next %s", got)
	}
	if v := p.View("a"); v.Error != "upstream unreachable" {
		t.Fatalf("view = %+v", v)
	}
	p.NoReading("a", "an API key has no plan")
	if d := p.Due(now.Add(48 * time.Hour)); len(d) != 0 {
		t.Fatalf("an account with no reading is read again: %v", d)
	}
	c := owner()
	c.StartHour = ip(6)
	p.Set(c, []Ref{{ID: "a", Extension: "pluxy/claude"}}, now)
	if d := p.Due(now); len(d) != 1 {
		t.Fatalf("not read after a change of the settings: %v", d)
	}
	p.Plan("a", claudeReading(at(10, 14, 0, 0).Unix(), 10, 10), ok, now)
	p.Set(c, []Ref{{ID: "a", Extension: "pluxy/claude"}}, now)
	if d := p.Due(now); len(d) != 0 {
		t.Fatalf("read again with the same settings: %v", d)
	}
}

func TestSettingsChooseAccountsWindowsAndModels(t *testing.T) {
	c := &Config{Enabled: true,
		Extensions: map[string]Extension{
			"pluxy/claude":      {},
			"pluxy/codex":       {Enabled: bp(false), Model: "gpt-6-luna"},
			"pluxy/antigravity": {Model: "gemini-3-flash", Windows: map[string]string{"claude_and_gpt_models_five_hour": "gpt-oss-120b-medium", "gemini_models_five_hour": ""}},
		},
		Accounts: map[string]Account{"c2": {Enabled: bp(true), Model: "gpt-5.6-luna"}, "k1": {Enabled: bp(false)}}}
	for _, tc := range []struct {
		id, ext string
		want    bool
	}{{"k1", "pluxy/claude", false}, {"k2", "pluxy/claude", true}, {"c1", "pluxy/codex", false}, {"c2", "pluxy/codex", true}, {"x", "pluxy/xai", false}} {
		if got := c.AccountOn(tc.id, tc.ext); got != tc.want {
			t.Errorf("AccountOn(%s, %s) = %v", tc.id, tc.ext, got)
		}
	}
	off := *c
	off.Enabled = false
	if off.AccountOn("k2", "pluxy/claude") {
		t.Error("an account is on with ignition off")
	}
	if !c.windowOn("pluxy/claude", "five_hour") || c.windowOn("pluxy/antigravity", "other_five_hour") || !c.windowOn("pluxy/antigravity", "gemini_models_five_hour") {
		t.Error("windowOn")
	}
	for _, tc := range []struct{ id, ext, w, want string }{
		{"g", "pluxy/antigravity", "claude_and_gpt_models_five_hour", "gpt-oss-120b-medium"},
		{"g", "pluxy/antigravity", "gemini_models_five_hour", "gemini-3-flash"},
		{"c2", "pluxy/codex", "five_hour", "gpt-5.6-luna"},
		{"c1", "pluxy/codex", "five_hour", "gpt-6-luna"},
		{"k2", "pluxy/claude", "five_hour", ""},
	} {
		if got := c.Model(tc.id, tc.ext, tc.w); got != tc.want {
			t.Errorf("Model(%s, %s) = %q, want %q", tc.id, tc.w, got, tc.want)
		}
	}
}

func TestValidate(t *testing.T) {
	good := owner()
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	var none *Config
	if err := none.Validate(); err != nil || none.On() {
		t.Fatalf("no section: %v, on %v", err, none.On())
	}
	for _, bad := range []func(c *Config){
		func(c *Config) { c.StartHour = ip(24) },
		func(c *Config) { c.EndHour = ip(-1) },
		func(c *Config) { c.GraceMS = i64(-1) },
		func(c *Config) { c.HoldMS = i64(14999) },
		func(c *Config) { c.RetryMS = i64(0) },
		func(c *Config) { c.BackoffMultiplier = pf(0.5) },
		func(c *Config) { c.MaxFailures = ip(0) },
		func(c *Config) { c.Extensions[""] = Extension{} },
		func(c *Config) { c.Accounts = map[string]Account{"": {}} },
	} {
		c := owner()
		bad(c)
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestChooseModel(t *testing.T) {
	claude := []string{"claude-haiku-4-5-20251001", "claude-sonnet-4-6", "claude-opus-5", "claude-haiku-5-5", "claude-3-5-haiku-20241022"}
	codex := []string{"gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-luna", "codex-auto-review", "gpt-image-2"}
	ag := []string{"claude-opus-4-6-thinking", "claude-sonnet-4-6", "gemini-3.8-flash-high", "gemini-3-flash", "gemini-3.1-flash-image", "gpt-oss-120b-medium", "gemini-3.1-flash-lite", "gemini-3.5-flash-lite"}
	for _, tc := range []struct {
		name   string
		models []string
		counts []string
		want   string
	}{
		{"claude: the newest haiku", claude, nil, "claude-haiku-5-5"},
		{"codex: the newest luna", codex, nil, "gpt-6-luna"},
		{"antigravity gemini group: the newest flash-lite", ag, []string{"Gemini 3 Flash", "Gemini 3 Pro"}, "gemini-3.5-flash-lite"},
		{"antigravity claude and gpt group: gpt-oss, not a thinking model", ag, []string{"Claude Sonnet 4.6", "GPT-OSS 120B"}, "gpt-oss-120b-medium"},
		{"gemini is not mini", []string{"gemini-3-pro", "gemini-3-ultra"}, nil, "gemini-3-pro"},
		{"a preview is not a review model", []string{"gemini-3-pro-preview", "gemini-3.1-flash-lite-preview", "text-embedding-3", "gemini-embedding-001"}, nil, "gemini-3.1-flash-lite-preview"},
		{"an embedding model is not", []string{"text-embedding-3", "gemini-embedding-001"}, nil, ""},
		{"none fit", []string{"gpt-image-2"}, nil, ""},
	} {
		if got := ChooseModel(tc.models, tc.counts); got != tc.want {
			t.Errorf("%s: ChooseModel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSettingsChangeLiftsAPause(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	run(t, p, claudeReading(0, 0, 10), ok, now)
	p.Done("a", "five_hour", "", &Failure{Message: "no model", Hard: true}, now)
	c := owner()
	c.Extensions["pluxy/claude"] = Extension{Model: "claude-haiku-5-5"}
	now = now.Add(time.Minute)
	p.Set(c, []Ref{{ID: "a", Extension: "pluxy/claude"}}, now)
	if f := run(t, p, claudeReading(0, 0, 10), ok, now); len(f) != 1 || f[0].Model != "claude-haiku-5-5" {
		t.Fatalf("fires after the settings changed = %+v", f)
	}
}

func TestStateWhileTheRequestRuns(t *testing.T) {
	now := at(10, 12, 0, 0)
	p := newOwnerPlanner(now)
	run(t, p, claudeReading(0, 0, 10), ok, now)
	if v := p.View("a").Windows[0]; v.State != StateStarting || v.Next != nil {
		t.Fatalf("while the request runs: %+v", v)
	}
}

func TestInHoursAcrossDaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	for _, tc := range []struct {
		name       string
		start, end int
		now, want  time.Time
	}{
		{"0 to 23, the night the clocks go back", 0, 23, time.Date(2026, 10, 31, 23, 30, 0, 0, ny), time.Date(2026, 11, 1, 0, 0, 0, 0, ny)},
		{"0 to 2, the morning the clocks go forward", 0, 2, time.Date(2026, 3, 8, 1, 30, 0, 0, ny), time.Date(2026, 3, 8, 1, 30, 0, 0, ny)},
		{"7 to 22, the day the clocks go back", 7, 22, time.Date(2026, 11, 1, 6, 30, 0, 0, ny), time.Date(2026, 11, 1, 7, 0, 0, 0, ny)},
	} {
		c := &Config{Enabled: true, StartHour: ip(tc.start), EndHour: ip(tc.end), EndGraceMS: i64(0)}
		if got := c.InHours(tc.now); !got.Equal(tc.want) {
			t.Errorf("%s: InHours(%s) = %s, want %s", tc.name, tc.now, got, tc.want)
		}
	}
}
