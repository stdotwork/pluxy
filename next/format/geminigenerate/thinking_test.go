package geminigenerate

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestThinkingOf(t *testing.T) {
	for _, c := range []struct {
		model     string
		wantLevel []string
		minB      int
		maxB      int
		zero      bool
		ok        bool
	}{
		{"gemini-4-neon", []string{"low", "medium", "high", "xhigh"}, 0, 0, false, true},
		{"gemini-4-hydrogen", []string{"low", "medium", "high", "xhigh"}, 0, 0, false, true},
		{"models/gemini-4-carbon", []string{"low", "medium", "high"}, 0, 0, false, true},
		{"gemini-4-argon", []string{"low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.8-flash", []string{"low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.6-flash", []string{"minimal", "low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.5-flash-lite", []string{"minimal", "low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.1-pro-preview", []string{"low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.1-flash-lite", []string{"minimal", "low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3.1-flash-lite-image", []string{"minimal", "high"}, 0, 0, false, true},
		{"gemini-3-flash-preview", []string{"minimal", "low", "medium", "high"}, 0, 0, false, true},
		{"gemini-3-pro-preview", []string{"low", "high"}, 0, 0, false, true},
		{"gemini-3-pro-image-preview", []string{"low", "high"}, 0, 0, false, true},
		{"gemini-robotics-er-2-preview", []string{"minimal", "low", "medium", "high"}, 0, 0, false, true},
		{"gemini-robotics-er-1.6-preview", nil, 0, 24576, true, true},
		{"gemini-2.5-pro", nil, 128, 32768, false, true},
		{"gemini-2.5-flash", nil, 0, 24576, true, true},
		{"gemini-2.5-flash-lite", nil, 512, 24576, true, true},
		{"gemini-pro-latest", nil, 128, 32768, false, true},
		{"gemini-flash-latest", nil, 0, 24576, true, true},
		{"gemini-flash-lite-latest", nil, 512, 24576, true, true},
		{"gemini-2.0-flash", nil, 0, 0, false, false},
		{"gemini-corpus", nil, 0, 0, false, false},
	} {
		levels, minB, maxB, zero, ok := ThinkingOf(c.model)
		if !slices.Equal(levels, c.wantLevel) || minB != c.minB || maxB != c.maxB || zero != c.zero || ok != c.ok {
			t.Errorf("ThinkingOf(%q) = (%v, %d, %d, %v, %v), want (%v, %d, %d, %v, %v)",
				c.model, levels, minB, maxB, zero, ok, c.wantLevel, c.minB, c.maxB, c.zero, c.ok)
		}
	}
}

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
		{"gemini-4-neon", "32000", `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"gemini-2.5-pro", "none", `{"thinkingBudget":128}`},
		{"gemini-2.5-flash", "none", `{"thinkingBudget":0}`},
		{"gemini-2.5-flash", "xhigh", `{"includeThoughts":true,"thinkingBudget":24576}`},
		{"gemini-2.5-flash-lite", "minimal", `{"includeThoughts":true,"thinkingBudget":512}`},
		{"gemini-2.5-pro", "auto", `{"includeThoughts":true,"thinkingBudget":-1}`},
		{"gemini-3-pro-preview", "low", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-pro-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3.1-pro-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-3-flash-preview", "minimal", `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{"gemini-3-flash-preview", "low", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-3-flash-preview", "medium", `{"includeThoughts":true,"thinkingLevel":"medium"}`},
		{"gemini-3-flash-preview", "high", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3-flash-preview", "xhigh", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3-flash-preview", "none", `{"thinkingLevel":"minimal"}`},
		{"gemini-3.1-flash-lite-image", "low", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-3.8-flash", "minimal", `{"includeThoughts":true,"thinkingLevel":"low"}`},
		{"gemini-4-neon", "xhigh", `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"gemini-4-neon", "max", `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"gemini-4-neon", "none", `{"thinkingLevel":"low"}`},
		{"gemini-4-hydrogen", "xhigh", `{"includeThoughts":true,"thinkingLevel":"xhigh"}`},
		{"gemini-4-carbon", "xhigh", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-4-argon", "xhigh", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-robotics-er-2-preview", "minimal", `{"includeThoughts":true,"thinkingLevel":"minimal"}`},
		{"gemini-robotics-er-2-preview", "high", `{"includeThoughts":true,"thinkingLevel":"high"}`},
		{"gemini-robotics-er-1.6-preview", "medium", `{"includeThoughts":true,"thinkingBudget":8192}`},
		{"gemini-robotics-er-1.6-preview", "none", `{"thinkingBudget":0}`},
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

	// An explicit includeThoughts on the incoming thinkingConfig survives a thinking suffix.
	body := []byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"includeThoughts":false,"thinkingLevel":"low"}}}`)
	out, ok, err := ApplyEffort(body, "gemini-4-neon", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		GenerationConfig struct {
			ThinkingConfig json.RawMessage `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	if !ok || json.Unmarshal(out, &got) != nil || string(got.GenerationConfig.ThinkingConfig) != `{"includeThoughts":false,"thinkingLevel":"xhigh"}` {
		t.Errorf("preserve includeThoughts: got %s (applied %v)", out, ok)
	}
}

func TestApplyEffortSnakeCase(t *testing.T) {
	for _, c := range []struct {
		name      string
		model     string
		effort    string
		body      string
		wantKey   string
		absentKey string
		wantTCKey string
		absentTC  string
		wantTemp  float64
		wantTC    string
	}{
		{
			name:      "snake_case generation_config and thinking_config",
			model:     "gemini-4-neon",
			effort:    "xhigh",
			body:      `{"contents":[],"generation_config":{"temperature":0.5,"thinking_config":{"include_thoughts":false,"thinking_level":"low"}}}`,
			wantKey:   "generation_config",
			absentKey: "generationConfig",
			wantTCKey: "thinking_config",
			absentTC:  "thinkingConfig",
			wantTemp:  0.5,
			wantTC:    `{"includeThoughts":false,"thinkingLevel":"xhigh"}`,
		},
		{
			name:      "snake_case generation_config without thinking_config",
			model:     "gemini-4-neon",
			effort:    "high",
			body:      `{"contents":[],"generation_config":{"temperature":0.5}}`,
			wantKey:   "generation_config",
			absentKey: "generationConfig",
			wantTCKey: "thinkingConfig",
			absentTC:  "thinking_config",
			wantTemp:  0.5,
			wantTC:    `{"includeThoughts":true,"thinkingLevel":"high"}`,
		},
		{
			name:      "camelCase generationConfig with snake_case thinking_config",
			model:     "gemini-2.5-pro",
			effort:    "high",
			body:      `{"contents":[],"generationConfig":{"temperature":0.5,"thinking_config":{"include_thoughts":false,"thinking_budget":1024}}}`,
			wantKey:   "generationConfig",
			absentKey: "generation_config",
			wantTCKey: "thinking_config",
			absentTC:  "thinkingConfig",
			wantTemp:  0.5,
			wantTC:    `{"includeThoughts":false,"thinkingBudget":24576}`,
		},
		{
			name:      "null camelCase falls back to snake_case without duplicates",
			model:     "gemini-4-neon",
			effort:    "high",
			body:      `{"contents":[],"generationConfig":null,"generation_config":{"temperature":0.5,"thinkingConfig":null,"thinking_config":{"include_thoughts":false}}}`,
			wantKey:   "generation_config",
			absentKey: "generationConfig",
			wantTCKey: "thinking_config",
			absentTC:  "thinkingConfig",
			wantTemp:  0.5,
			wantTC:    `{"includeThoughts":false,"thinkingLevel":"high"}`,
		},
		{
			name:      "camelCase wins when both spellings are set",
			model:     "gemini-4-neon",
			effort:    "high",
			body:      `{"contents":[],"generationConfig":{"temperature":0.5,"thinkingConfig":{"includeThoughts":false}},"generation_config":{"temperature":0.9,"thinking_config":{"include_thoughts":true}}}`,
			wantKey:   "generationConfig",
			absentKey: "generation_config",
			wantTCKey: "thinkingConfig",
			absentTC:  "thinking_config",
			wantTemp:  0.5,
			wantTC:    `{"includeThoughts":false,"thinkingLevel":"high"}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, losses, ok, err := ApplyEffortReport([]byte(c.body), c.model, c.effort)
			if err != nil || !ok {
				t.Fatalf("ApplyEffortReport() ok=%v err=%v", ok, err)
			}
			if len(losses) != 0 {
				t.Errorf("losses = %+v, want none", losses)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(out, &top); err != nil {
				t.Fatalf("unmarshal out: %v (%s)", err, out)
			}
			if _, exists := top[c.absentKey]; exists {
				t.Errorf("out has duplicate key %q: %s", c.absentKey, out)
			}
			genRaw, exists := top[c.wantKey]
			if !exists {
				t.Fatalf("out missing %q: %s", c.wantKey, out)
			}
			var gen map[string]json.RawMessage
			if err := json.Unmarshal(genRaw, &gen); err != nil {
				t.Fatalf("unmarshal %s: %v", c.wantKey, err)
			}
			if _, exists := gen[c.absentTC]; exists {
				t.Errorf("%s has duplicate key %q: %s", c.wantKey, c.absentTC, genRaw)
			}
			var temp float64
			if err := json.Unmarshal(gen["temperature"], &temp); err != nil || temp != c.wantTemp {
				t.Errorf("temperature = %s, want %v", gen["temperature"], c.wantTemp)
			}
			if string(gen[c.wantTCKey]) != c.wantTC {
				t.Errorf("%s = %s, want %s (full out: %s)", c.wantTCKey, gen[c.wantTCKey], c.wantTC, out)
			}
		})
	}

	// Non-thinking model body stays byte-identical.
	raw := `{"contents":[],"generation_config":{"temperature":0.5}}`
	out, losses, ok, err := ApplyEffortReport([]byte(raw), "gemini-2.0-flash", "high")
	if err != nil || ok || len(losses) != 0 || string(out) != raw {
		t.Errorf("non-thinking model: out=%s losses=%+v ok=%v err=%v", out, losses, ok, err)
	}
}

func TestApplyEffortReport(t *testing.T) {
	body := []byte(`{"contents":[]}`)
	for _, c := range []struct {
		model, effort string
		wantLossPath  string // "" for no loss
	}{
		{"gemini-4-neon", "xhigh", ""},
		{"gemini-4-neon", "max", "generationConfig.thinkingConfig.thinkingLevel"},
		{"gemini-4-carbon", "xhigh", "generationConfig.thinkingConfig.thinkingLevel"},
		{"gemini-3.8-flash", "minimal", "generationConfig.thinkingConfig.thinkingLevel"},
		{"gemini-3-flash-preview", "none", "generationConfig.thinkingConfig.thinkingLevel"},
		{"gemini-3-flash-preview", "minimal", ""},
		{"gemini-2.5-pro", "none", "generationConfig.thinkingConfig.thinkingBudget"},
		{"gemini-2.5-pro", "50", "generationConfig.thinkingConfig.thinkingBudget"},
		{"gemini-2.5-pro", "xhigh", ""},
		{"gemini-2.5-flash", "xhigh", "generationConfig.thinkingConfig.thinkingBudget"},
		{"gemini-2.5-flash", "none", ""},
	} {
		_, losses, ok, err := ApplyEffortReport(body, c.model, c.effort)
		if err != nil || !ok {
			t.Fatalf("%s %s: ok=%v err=%v", c.model, c.effort, ok, err)
		}
		if c.wantLossPath == "" {
			if len(losses) != 0 {
				t.Errorf("%s %s: losses = %+v, want none", c.model, c.effort, losses)
			}
			continue
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossApproximated || losses[0].Path != c.wantLossPath {
			t.Errorf("%s %s: losses = %+v, want one approximated at %q", c.model, c.effort, losses, c.wantLossPath)
		}
	}
}

func TestApplySummary(t *testing.T) {
	for _, c := range []struct {
		name        string
		model       string
		body        string
		format      string
		raw         string
		wantTC      string
		wantCarried bool
		wantApprox  bool
	}{
		{
			name:        "Claude omitted removes includeThoughts",
			model:       "gemini-3-flash-preview",
			body:        `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}}}`,
			format:      llm.FormatAnthropicMessages,
			raw:         `{"thinking":{"display":"omitted"}}`,
			wantTC:      `{"includeThoughts":false,"thinkingLevel":"high"}`,
			wantCarried: true,
		},
		{
			name:        "Claude summarized sets includeThoughts without effort",
			model:       "gemini-3-flash-preview",
			body:        `{"contents":[]}`,
			format:      llm.FormatAnthropicMessages,
			raw:         `{"thinking":{"display":"summarized"}}`,
			wantTC:      `{"includeThoughts":true}`,
			wantCarried: true,
		},
		{
			name:        "Responses concise approximates summary",
			model:       "gemini-4-neon",
			body:        `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"xhigh"}}}`,
			format:      llm.FormatOpenAIResponses,
			raw:         `{"reasoning":{"summary":"concise"}}`,
			wantTC:      `{"includeThoughts":true,"thinkingLevel":"xhigh"}`,
			wantCarried: true,
			wantApprox:  true,
		},
		{
			name:        "Responses extra key keeps dropped loss",
			model:       "gemini-4-neon",
			body:        `{}`,
			format:      llm.FormatOpenAIResponses,
			raw:         `{"reasoning":{"summary":"none","extra":1}}`,
			wantTC:      `{"includeThoughts":false}`,
			wantCarried: false,
		},
		{
			name:        "Chat format untouched",
			model:       "gemini-4-neon",
			body:        `{}`,
			format:      llm.FormatOpenAIChat,
			raw:         `{"reasoning":{"summary":"auto"}}`,
			wantTC:      ``,
			wantCarried: false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := llm.Request{Model: c.model, Origin: &llm.Origin{Format: c.format, Raw: json.RawMessage(c.raw)}}
			out, losses, carried, err := ApplySummary([]byte(c.body), c.model, req)
			if err != nil {
				t.Fatal(err)
			}
			if carried != c.wantCarried {
				t.Errorf("carried = %v, want %v", carried, c.wantCarried)
			}
			hasApprox := slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Kind == llm.LossApproximated })
			if hasApprox != c.wantApprox {
				t.Errorf("losses = %+v, want approx=%v", losses, c.wantApprox)
			}
			var got struct {
				GenerationConfig struct {
					ThinkingConfig json.RawMessage `json:"thinkingConfig"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if string(got.GenerationConfig.ThinkingConfig) != c.wantTC {
				t.Errorf("thinkingConfig = %s, want %s", got.GenerationConfig.ThinkingConfig, c.wantTC)
			}
		})
	}
}
