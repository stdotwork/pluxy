package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// An effort outside the account's levels moves to the nearest level it has, the higher of two as
// near; auto without a level of its own is taken as medium; a name clamping does not know is left.
func TestClampEffort(t *testing.T) {
	for _, c := range []struct {
		effort string
		levels []string
		want   string
	}{
		{"high", nil, "high"},
		{"xhigh", nil, "high"},
		{"max", nil, "high"},
		{"minimal", nil, "low"},
		{"none", nil, "low"},
		{"auto", nil, "medium"},
		{"", nil, ""},
		{"turbo", nil, "turbo"},
		{"none", []string{"none", "low", "high"}, "none"},
		{"auto", []string{"low", "high"}, "high"},
		{"medium", []string{"low", "high"}, "high"},
		{"xhigh", []string{"low", "medium", "high", "xhigh"}, "xhigh"},
		{"max", []string{"minimal", "low", "medium", "high", "xhigh"}, "xhigh"},
		{"high", []string{"bogus"}, "high"},
	} {
		if got := clampEffort(c.effort, c.levels); got != c.want {
			t.Errorf("clampEffort(%q, %q) = %q, want %q", c.effort, c.levels, got, c.want)
		}
	}
}

// The suffix wins over the body's effort; the result is clamped; a body without an effort and no
// suffix is sent as it came.
func TestApplyReasoning(t *testing.T) {
	for _, c := range []struct {
		name, target, body, suffix, want string
	}{
		{"chat suffix", llm.FormatOpenAIChat, `{"model":"m","reasoning_effort":"high"}`, "low", `{"model":"m","reasoning_effort":"low"}`},
		{"chat budget", llm.FormatOpenAIChat, `{"model":"m"}`, "8192", `{"model":"m","reasoning_effort":"medium"}`},
		{"chat clamp", llm.FormatOpenAIChat, `{"model":"m","reasoning_effort":"xhigh"}`, "", `{"model":"m","reasoning_effort":"high"}`},
		{"chat none", llm.FormatOpenAIChat, `{"model":"m"}`, "", `{"model":"m"}`},
		{"responses suffix", llm.FormatOpenAIResponses, `{"model":"m","reasoning":{"summary":"auto"}}`, "xhigh", `{"model":"m","reasoning":{"effort":"high","summary":"auto"}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := llm.Request{Model: "m", Options: llm.Options{ThinkingSuffix: c.suffix}}
			out, err := applyReasoning([]byte(c.body), req, c.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			_ = json.Unmarshal(out, &got)
			_ = json.Unmarshal([]byte(c.want), &want)
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Errorf("body = %s, want %s", out, c.want)
			}
		})
	}
}
