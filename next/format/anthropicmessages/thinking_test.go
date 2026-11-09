package anthropicmessages

import (
	"encoding/json"
	"testing"
)

func TestApplyEffort(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		maxTokens     int
		want          string // thinking and output_config as JSON, "" when not applied
	}{
		{"claude-opus-4-6", "high", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`},
		{"claude-sonnet-5-5-20261001", "xhigh", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`},
		{"claude-fable-5-1", "minimal", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`},
		{"claude-opus-4-6", "auto", 32000, `{"thinking":{"type":"adaptive"}}`},
		{"claude-sonnet-4-5", "medium", 32000, `{"thinking":{"type":"enabled","budget_tokens":8192}}`},
		{"claude-test", "high", 32000, ""},
		{"claude-opus-4-1", "high", 32000, `{"thinking":{"type":"enabled","budget_tokens":24576}}`},
		{"claude-3-7-sonnet-latest", "low", 32000, `{"thinking":{"type":"enabled","budget_tokens":1024}}`},
		{"claude-sonnet-4", "max", 32000, `{"thinking":{"type":"enabled","budget_tokens":31999}}`},
		{"claude-sonnet-4", "auto", 32000, `{"thinking":{"type":"enabled","budget_tokens":1024}}`},
		{"claude-sonnet-4", "none", 32000, `{"thinking":{"type":"disabled"}}`},
		{"claude-sonnet-4", "low", 1024, ""},
		{"claude-opus-4-6", "unheard-of", 32000, ""},
		{"claude-3-5-sonnet-latest", "high", 32000, ""},
		{"gpt-5", "high", 32000, ""},
		// Token budgets, as a thinking suffix names them.
		{"claude-sonnet-4-5", "16000", 32000, `{"thinking":{"type":"enabled","budget_tokens":16000}}`},
		{"claude-sonnet-4-5", "40000", 32000, `{"thinking":{"type":"enabled","budget_tokens":31999}}`},
		{"claude-sonnet-4-5", "100", 32000, `{"thinking":{"type":"enabled","budget_tokens":1024}}`},
		{"claude-sonnet-4-5", "0", 32000, `{"thinking":{"type":"disabled"}}`},
		{"claude-opus-4-6", "16000", 32000, `{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`},
	} {
		body := []byte(`{"model":"` + c.model + `","max_tokens":` + itoa(c.maxTokens) + `}`)
		out, applied, err := ApplyEffort(body, c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if applied || string(out) != string(body) {
				t.Errorf("%s %s: applied %v, body %s; want left alone", c.model, c.effort, applied, out)
			}
			continue
		}
		var got, want map[string]json.RawMessage
		_ = json.Unmarshal(out, &got)
		_ = json.Unmarshal([]byte(c.want), &want)
		if !applied || len(got) != len(want)+2 {
			t.Errorf("%s %s: applied %v, body %s; want %s", c.model, c.effort, applied, out, c.want)
			continue
		}
		for k, v := range want {
			if !jsonEqual(got[k], v) {
				t.Errorf("%s %s: %s = %s, want %s", c.model, c.effort, k, got[k], v)
			}
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return string(ja) == string(jb)
}
