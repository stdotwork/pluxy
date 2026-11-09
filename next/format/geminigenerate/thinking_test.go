package geminigenerate

import (
	"encoding/json"
	"testing"
)

func TestApplyEffort(t *testing.T) {
	for _, c := range []struct {
		model, effort string
		want          string // thinkingConfig as JSON, "" when not applied
	}{
		{"gemini-2.5-pro", "high", `{"includeThoughts":true,"thinkingBudget":24576}`},
		// Token budgets, as a thinking suffix names them: kept within the model's range, or the
		// level they fall in on a model with levels.
		{"gemini-2.5-pro", "16000", `{"includeThoughts":true,"thinkingBudget":16000}`},
		{"gemini-2.5-pro", "50", `{"includeThoughts":true,"thinkingBudget":128}`},
		{"gemini-2.5-flash", "99999", `{"includeThoughts":true,"thinkingBudget":24576}`},
		{"gemini-2.5-flash", "0", `{"thinkingBudget":0}`},
		{"gemini-3-pro-preview", "1000", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-2.5-pro", "none", `{"thinkingBudget":128}`},
		{"gemini-2.5-flash", "none", `{"thinkingBudget":0}`},
		{"gemini-2.5-flash", "xhigh", `{"includeThoughts":true,"thinkingBudget":24576}`},
		{"gemini-2.5-flash-lite", "minimal", `{"includeThoughts":true,"thinkingBudget":512}`},
		{"gemini-2.5-pro", "auto", `{"includeThoughts":true,"thinkingBudget":-1}`},
		{"gemini-3-pro-preview", "low", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-pro-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3.1-pro-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-3-flash-preview", "minimal", `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{"gemini-3-flash-preview", "none", `{"thinkingLevel":"minimal"}`},
		{"gemini-3.8-flash", "minimal", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-corpus", "high", ""},
		{"gemini-2.5-pro", "turbo", ""},
	} {
		body := []byte(`{"contents":[],"generationConfig":{"temperature":1}}`)
		out, ok, err := ApplyEffort(body, c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		if c.want == "" {
			if ok {
				t.Errorf("%s %s: applied %s, want nothing", c.model, c.effort, out)
			}
			continue
		}
		var got struct {
			GenerationConfig struct {
				Temperature    float64         `json:"temperature"`
				ThinkingConfig json.RawMessage `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if !ok || json.Unmarshal(out, &got) != nil || string(got.GenerationConfig.ThinkingConfig) != c.want || got.GenerationConfig.Temperature != 1 {
			t.Errorf("%s %s: got %s (applied %v), want thinkingConfig %s next to the temperature", c.model, c.effort, out, ok, c.want)
		}
	}
}
