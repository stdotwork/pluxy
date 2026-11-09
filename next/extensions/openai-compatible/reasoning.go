package main

import (
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// defaultReasoningLevels are the reasoning efforts a Chat or Responses upstream is taken to
// accept when the account names none: the three every OpenAI-compatible service knows, as today's
// core assumes for a model it has no thinking settings for.
var defaultReasoningLevels = []string{"low", "medium", "high"}

// effortOrder orders the reasoning efforts that clamping moves between; none is below them all.
var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// applyReasoning sets the upstream body's reasoning settings, after the body is built (sent
// unchanged or translated), as today's core does for an API-key upstream:
//
//   - A thinking suffix on the model name (llm.Options.ThinkingSuffix) wins over the body's own
//     settings: reasoning_effort for Chat, reasoning.effort for Responses, and for Messages and
//     GenerateContent the model's thinking settings (Claude's and Gemini's rules).
//   - A Chat or Responses effort outside the levels the account accepts (levels) moves to the
//     nearest one it does, as today's core moves it: by default xhigh and max to high, minimal
//     and none to low, and auto to medium.
func applyReasoning(body []byte, req llm.Request, target string, levels []string) ([]byte, error) {
	suffix := req.Options.ThinkingSuffix
	switch target {
	case llm.FormatAnthropicMessages:
		if suffix == "" {
			return body, nil
		}
		out, _, err := anthropicmessages.ApplyEffort(body, req.Model, suffix)
		return out, err
	case llm.FormatGeminiGenerate:
		if suffix == "" {
			return body, nil
		}
		out, _, err := geminigenerate.ApplyEffort(body, req.Model, suffix)
		return out, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, nil
	}
	// Where the effort lives: reasoning_effort for Chat, reasoning.effort for Responses.
	get := func() string {
		var e string
		if target == llm.FormatOpenAIChat {
			_ = json.Unmarshal(m["reasoning_effort"], &e)
			return e
		}
		var r struct {
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(m["reasoning"], &r)
		return r.Effort
	}
	set := func(e string) {
		v, _ := json.Marshal(e)
		if target == llm.FormatOpenAIChat {
			m["reasoning_effort"] = v
			return
		}
		r := map[string]json.RawMessage{}
		_ = json.Unmarshal(m["reasoning"], &r)
		r["effort"] = v
		m["reasoning"], _ = json.Marshal(r)
	}
	effort := get()
	if suffix != "" {
		effort = llm.ThinkingEffort(suffix)
	}
	clamped := clampEffort(effort, levels)
	if clamped == get() {
		return body, nil
	}
	set(clamped)
	return json.Marshal(m)
}

// clampEffort returns effort when levels has it or it is a name clamping does not know (or
// empty); otherwise the nearest level levels has, the highest for an effort above them all and
// the lowest for one below. auto, without a level of its own in levels, is taken as medium.
func clampEffort(effort string, levels []string) string {
	if len(levels) == 0 {
		levels = defaultReasoningLevels
	}
	if slices.Contains(levels, effort) {
		return effort
	}
	at := effort
	if at == "auto" {
		at = "medium"
	}
	i := slices.Index(effortOrder, at)
	if i < 0 {
		return effort
	}
	best, bestDist := effort, len(effortOrder)
	for _, l := range levels {
		j := slices.Index(effortOrder, l)
		if j < 0 {
			continue
		}
		d := j - i
		if d < 0 {
			d = -d
		}
		// The nearer level wins; between two as near, the higher one.
		if d < bestDist || (d == bestDist && j > slices.Index(effortOrder, best)) {
			best, bestDist = l, d
		}
	}
	return best
}
