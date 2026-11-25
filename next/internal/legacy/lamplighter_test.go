package legacy

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/ignite"
)

// lampConfig is a config.yaml with the Lamplighter plugin set up as in a real CPA setup:
// ignition on for Claude, Codex and two Antigravity groups (one with its own model), the
// notifications off, and a few of the plugin's other keys.
const lampConfig = `
access:
  api-keys: ["sk-client-1"]
plugins:
  enabled: true
  dir: plugins
  configs:
    lamplighter:
      enabled: true
      bark_url: ""
      webhook:
        url: ""
      notice_threshold: 50
      timezone: ""
      models_api_key: sk-lamp-secret-value
      account_weights:
        claude-a.json: 2
      ignition:
        enabled: true
        start_hour: 7
        end_hour: 22
        end_grace_minutes: 30
        grace_seconds: 2
        failure_retry_seconds: 300
        failure_backoff_multiplier: 3
        max_transient_failures: 3
        post_success_hold_seconds: 60
      providers:
        claude:
          ignite: true
          model: ""
        codex:
          monitor: true
          ignite: true
          model: ""
        antigravity:
          ignite: true
          groups: ["Claude / GPT", "Gemini"]
          models:
            Claude / GPT: gpt-oss-120b-medium
        kimi:
          ignite: true
`

func rowsOf(t *testing.T, res *Result) map[string][2]string {
	t.Helper()
	lists, err := res.Ledger.Report()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]string{}
	for i, rows := range [][]Row{lists.Brought, lists.Changed, lists.NotBrought} {
		for _, r := range rows {
			out[r.What] = [2]string{[]string{"brought", "changed", "not brought"}[i], r.As + r.To + " " + r.Why}
		}
	}
	return out
}

func TestImportLamplighterIgnition(t *testing.T) {
	res, err := ImportConfig([]byte(lampConfig), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, _ := res.Config["routing"].(map[string]any)
	if r["ignition"] == nil {
		t.Fatalf("no routing.ignition in %v", res.Config)
	}
	b, _ := json.Marshal(r["ignition"])
	var got ignite.Config
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	ip := func(v int) *int { return &v }
	i64 := func(v int64) *int64 { return &v }
	pf := func(v float64) *float64 { return &v }
	on := true
	want := ignite.Config{Enabled: true, StartHour: ip(7), EndHour: ip(22), EndGraceMS: i64(1800000), GraceMS: i64(2000),
		RetryMS: i64(300000), BackoffMultiplier: pf(3), MaxFailures: ip(3), HoldMS: i64(60000),
		Extensions: map[string]ignite.Extension{
			"pluxy/claude": {Enabled: &on}, "pluxy/codex": {Enabled: &on},
			"pluxy/antigravity": {Enabled: &on, Windows: map[string]string{"claude_and_gpt_models_five_hour": "gpt-oss-120b-medium", "gemini_models_five_hour": ""}},
		}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routing.ignition = %s", b)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the imported settings do not validate: %v", err)
	}
	rows := rowsOf(t, res)
	for what, want := range map[string]string{
		"plugins.configs.lamplighter.enabled":                                   "changed",
		"plugins.configs.lamplighter.ignition.end_grace_minutes":                "brought",
		"plugins.configs.lamplighter.ignition.grace_seconds":                    "brought",
		"plugins.configs.lamplighter.providers.claude.ignite":                   "brought",
		"plugins.configs.lamplighter.providers.antigravity.ignite":              "changed",
		"plugins.configs.lamplighter.providers.antigravity.groups":              "changed",
		"plugins.configs.lamplighter.providers.kimi.ignite":                     "not brought",
		"plugins.configs.lamplighter.bark_url":                                  "not brought",
		"plugins.configs.lamplighter.webhook.url":                               "not brought",
		"plugins.configs.lamplighter.notice_threshold":                          "not brought",
		"plugins.configs.lamplighter.models_api_key":                            "not brought",
		"plugins.configs.lamplighter.account_weights.claude-a.json":             "not brought",
		"plugins.configs.lamplighter.providers.antigravity.models.Claude / GPT": "brought",
		"plugins.enabled": "not brought",
	} {
		if rows[what][0] != want {
			t.Errorf("%s: %q, want %s", what, rows[what], want)
		}
	}
	if r := rows["plugins.configs.lamplighter.notice_threshold"][1]; !strings.Contains(r, "no usage notifications") || strings.Contains(r, "nothing is lost") {
		t.Errorf("notice_threshold row: %s", r)
	}
	if r := rows["plugins.configs.lamplighter.bark_url"][1]; !strings.Contains(r, "nothing is lost") {
		t.Errorf("bark_url row: %s", r)
	}
	if r := rows["plugins.configs.lamplighter.providers.antigravity.ignite"][1]; !strings.Contains(r, "archived") {
		t.Errorf("antigravity row: %s", r)
	}
	// The plugin's key reaches no row and no config.
	all, _ := json.Marshal(res)
	if strings.Contains(string(all), "sk-lamp-secret-value") {
		t.Error("the plugin's API key is in the import's result")
	}
}

func TestImportLamplighterDefaultsAndOff(t *testing.T) {
	// The plugin on with no ignition or providers block: its defaults, Claude and Codex.
	res, err := ImportConfig([]byte("access:\n  api-keys: [k]\nplugins:\n  enabled: true\n  configs:\n    lamplighter:\n      enabled: true\n"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Config["routing"].(map[string]any)["ignition"])
	if string(b) != `{"enabled":true,"extensions":{"pluxy/claude":{"enabled":true},"pluxy/codex":{"enabled":true}}}` {
		t.Fatalf("defaults: routing.ignition = %s", b)
	}
	// Values out of the plugin's ranges are held to them, as the plugin holds them.
	res, err = ImportConfig([]byte("access:\n  api-keys: [k]\nplugins:\n  enabled: true\n  configs:\n    lamplighter:\n      enabled: true\n      ignition:\n        failure_retry_seconds: 10\n        post_success_hold_seconds: 5\n      providers:\n        codex:\n          monitor: false\n"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(res.Config["routing"].(map[string]any)["ignition"])
	if string(b) != `{"enabled":true,"extensions":{"pluxy/claude":{"enabled":true}},"hold_ms":15000,"retry_ms":60000}` {
		t.Fatalf("held: routing.ignition = %s", b)
	}
	rows := rowsOf(t, res)
	if r := rows["plugins.configs.lamplighter.ignition.failure_retry_seconds"]; r[0] != "changed" {
		t.Errorf("retry row: %v", r)
	}
	if r := rows["plugins.configs.lamplighter.providers.codex.monitor"]; r[0] != "changed" {
		t.Errorf("monitor row: %v", r)
	}
	// The plugin off, or every plugin off: nothing is brought.
	for _, doc := range []string{
		"access:\n  api-keys: [k]\nplugins:\n  enabled: true\n  configs:\n    lamplighter:\n      ignition:\n        start_hour: 6\n",
		"access:\n  api-keys: [k]\nplugins:\n  enabled: false\n  configs:\n    lamplighter:\n      enabled: true\n      ignition:\n        start_hour: 6\n",
	} {
		res, err := ImportConfig([]byte(doc), t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if r, _ := res.Config["routing"].(map[string]any); r["ignition"] != nil {
			t.Errorf("brought from a plugin that was off: %v", r["ignition"])
		}
		if r := rowsOf(t, res)["plugins.configs.lamplighter.ignition.start_hour"]; r[0] != "not brought" || !strings.Contains(r[1], "nothing is lost") {
			t.Errorf("off: start_hour row %v", r)
		}
	}
}

func TestImportLamplighterOddShapes(t *testing.T) {
	doc := `access:
  api-keys: [k]
plugins:
  enabled: true
  configs:
    lamplighter:
      enabled: true
      ignition: {}
      providers:
        Claude: {ignite: false}
        claude: {model: claude-haiku-5-5}
        codex: false
        antigravity:
          ignite: true
          models: {"Claude / GPT": gpt-oss-120b-medium}
`
	res, err := ImportConfig([]byte(doc), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Config["routing"].(map[string]any)["ignition"])
	// claude is the key the plugin reads; codex: false keeps codex's defaults (on); antigravity
	// starts its default group only, so the model set for another group goes nowhere.
	want := `{"enabled":true,"extensions":{"pluxy/antigravity":{"enabled":true,"windows":{"gemini_models_five_hour":""}},"pluxy/claude":{"enabled":true,"model":"claude-haiku-5-5"},"pluxy/codex":{"enabled":true}}}`
	if string(b) != want {
		t.Fatalf("routing.ignition = %s\nwant %s", b, want)
	}
	rows := rowsOf(t, res)
	for what, want := range map[string]string{
		"plugins.configs.lamplighter.providers.Claude.ignite":                   "the file sets providers.claude too",
		"plugins.configs.lamplighter.providers.codex":                           "the plugin's defaults apply",
		"plugins.configs.lamplighter.ignition":                                  "the plugin's defaults apply",
		"plugins.configs.lamplighter.providers.antigravity.models.Claude / GPT": "groups does not name it",
	} {
		if r := rows[what]; r[0] != "not brought" || !strings.Contains(r[1], want) {
			t.Errorf("%s: %v, want not brought: %s", what, r, want)
		}
	}
}
