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

// The effort is read with one scan of the body: under the name written with escapes, from the
// last of two names, and as none where the body holds no effort it can read. A body whose effort
// is already the one it would be given comes back as it was.
func TestApplyReasoningFastPath(t *testing.T) {
	for _, c := range []struct {
		name, target, body, suffix string
		levels                     []string
		want                       string
	}{
		{"same effort", llm.FormatOpenAIChat, `{"model":"m","reasoning_effort":"high"}`, "high", nil, `{"model":"m","reasoning_effort":"high"}`},
		{"no effort", llm.FormatOpenAIChat, `{"model":"m"}`, "", nil, `{"model":"m"}`},
		{"escaped name", llm.FormatOpenAIChat, `{"reasoning_effor\u0074":"high"}`, "high", nil, `{"reasoning_effor\u0074":"high"}`},
		{"last name wins", llm.FormatOpenAIChat, `{"reasoning_effort":"low","reasoning_effort":"high"}`, "high", nil, `{"reasoning_effort":"low","reasoning_effort":"high"}`},
		{"null then level", llm.FormatOpenAIChat, `{"reasoning_effort":null,"reasoning_effort":"low"}`, "low", nil, `{"reasoning_effort":null,"reasoning_effort":"low"}`},
		{"number is no effort", llm.FormatOpenAIChat, `{"reasoning_effort":3}`, "", nil, `{"reasoning_effort":3}`},
		{"clamped level", llm.FormatOpenAIChat, `{"model":"m","reasoning_effort":"xhigh"}`, "high", []string{"low", "high"}, `{"model":"m","reasoning_effort":"high"}`},
		{"responses same effort", llm.FormatOpenAIResponses, `{"model":"m","reasoning":{"effort":"low","summary":"auto"}}`, "low", nil, `{"model":"m","reasoning":{"effort":"low","summary":"auto"}}`},
		{"responses upper-case name", llm.FormatOpenAIResponses, `{"model":"m","reasoning":{"EFFORT":"low"}}`, "low", nil, `{"model":"m","reasoning":{"EFFORT":"low"}}`},
		{"responses last name wins", llm.FormatOpenAIResponses, `{"model":"m","reasoning":{"effort":"high","effort":null}}`, "high", nil, `{"model":"m","reasoning":{"effort":"high","effort":null}}`},
		{"not an object", llm.FormatOpenAIChat, `[1,2]`, "low", nil, `[1,2]`},
		{"null body", llm.FormatOpenAIChat, `null`, "low", nil, `null`},
		{"invalid", llm.FormatOpenAIChat, `{"model":`, "low", nil, `{"model":`},
		{"spaced", llm.FormatOpenAIChat, `{ "model" : "m" , "reasoning_effort" : "high" }`, "high", nil, `{ "model" : "m" , "reasoning_effort" : "high" }`},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := llm.Request{Model: "m", Options: llm.Options{ThinkingSuffix: c.suffix}}
			body := []byte(c.body)
			out, err := applyReasoning(body, req, c.target, c.levels)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Errorf("body = %s, want %s", out, c.want)
			}
			// A body that does not change is the bytes it came as, not a decode and an encode.
			if c.want == c.body && len(out) > 0 && &out[0] != &body[0] {
				t.Errorf("unchanged body was rebuilt: %s", out)
			}
		})
	}
}
