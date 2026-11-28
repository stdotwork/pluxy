package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

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
			out, _, err := applyReasoning([]byte(c.body), req, c.target, nil)
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
			out, _, err := applyReasoning(body, req, c.target, c.levels)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != c.want {
				t.Errorf("body = %s, want %s", out, c.want)
			}
			if c.want == c.body && len(out) > 0 && &out[0] != &body[0] {
				t.Errorf("unchanged body was rebuilt: %s", out)
			}
		})
	}
}

func TestUpstreamBodyNullReasoning(t *testing.T) {
	t.Run("with thinking suffix", func(t *testing.T) {
		raw := []byte(`{"model":"gpt-5(high)","input":"hi","reasoning":null}`)
		req, err := openairesponses.ParseRequest(raw)
		if err != nil {
			t.Fatalf("ParseRequest: %v", err)
		}
		model, suffix := llm.SplitThinkingSuffix(req.Model)
		if model != "gpt-5" || suffix != "high" {
			t.Fatalf("SplitThinkingSuffix(%q) = (%q, %q), want (gpt-5, high)", req.Model, model, suffix)
		}
		req.Model = model
		req.Options.ThinkingSuffix = suffix
		req.Options.ReasoningEffort = llm.ThinkingEffort(suffix)
		var m map[string]json.RawMessage
		if err := json.Unmarshal(req.Native.Body, &m); err != nil {
			t.Fatalf("unmarshal native: %v", err)
		}
		m["model"], _ = json.Marshal(model)
		req.Native.Body, _ = json.Marshal(m)

		out, tr, err := upstreamBody(req, llm.FormatOpenAIResponses, settings{})
		if err != nil {
			t.Fatalf("upstreamBody: %v", err)
		}
		if tr.Passthrough {
			t.Errorf("Passthrough = true, want false after setting reasoning.effort")
		}
		var got struct {
			Model     string `json:"model"`
			Input     string `json:"input"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("unmarshal out %s: %v", out, err)
		}
		if got.Model != "gpt-5" || got.Input != "hi" || got.Reasoning.Effort != "high" {
			t.Errorf("got %+v (body %s), want model=gpt-5 input=hi reasoning.effort=high", got, out)
		}
	})

	t.Run("without thinking suffix stays byte-identical", func(t *testing.T) {
		raw := []byte(`{ "model" : "gpt-5" , "input" : "hi" , "reasoning" : null }`)
		req, err := openairesponses.ParseRequest(raw)
		if err != nil {
			t.Fatalf("ParseRequest: %v", err)
		}
		out, tr, err := upstreamBody(req, llm.FormatOpenAIResponses, settings{})
		if err != nil {
			t.Fatalf("upstreamBody: %v", err)
		}
		if !tr.Passthrough {
			t.Errorf("Passthrough = false, want true")
		}
		if !bytes.Equal(out, raw) {
			t.Errorf("body changed:\n got: %s\nwant: %s", out, raw)
		}
	})
}
