package main

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/internal/jsonscan"
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
	have, ok := bodyEffort(body, target)
	if !ok {
		return body, nil // not one JSON object: today's decode leaves it as it is too
	}
	effort := have
	if suffix != "" {
		effort = llm.ThinkingEffort(suffix)
	}
	clamped := clampEffort(effort, levels)
	if clamped == have {
		return body, nil // the usual case: the body is sent as it was built
	}
	return setEffort(body, target, clamped)
}

// bodyEffort reads the reasoning effort a built body carries: reasoning_effort for Chat,
// reasoning.effort for Responses. It finds it without decoding the body, so the usual case, a
// body that already has the effort it is given, costs one scan. A name written twice counts as
// the last one, as a map read takes it, and a value the effort cannot be read from counts as
// none, as decoding it into a string leaves it. ok is false when body is not one JSON object.
func bodyEffort(body []byte, target string) (effort string, ok bool) {
	members, ok := jsonscan.Object(body)
	if !ok {
		return "", false
	}
	name := "reasoning_effort"
	if target != llm.FormatOpenAIChat {
		name = "reasoning"
	}
	var value []byte
	for _, m := range members {
		if names(m.Key, name) {
			value = m.Value
		}
	}
	if value == nil {
		return "", true
	}
	if target == llm.FormatOpenAIChat {
		var s string
		_ = jsonscan.Unmarshal(value, &s)
		return s, true
	}
	var r struct {
		Effort string `json:"effort"`
	}
	_ = jsonscan.Unmarshal(value, &r)
	return r.Effort, true
}

// names reports whether a member's name, as written, is name in the sense a map key has: as it
// stands, or with its escapes decoded. A name encoding/json repairs (invalid UTF-8) names
// nothing here, since no such name is name.
func names(key []byte, name string) bool {
	if bytes.IndexByte(key, '\\') < 0 {
		return string(key[1:len(key)-1]) == name
	}
	k, ok := jsonscan.Unquote(key)
	return ok && k == name
}

// setEffort sets the body's reasoning effort to e and returns the body as today's core writes it:
// decoded into a map and encoded again. It is only reached for a body whose effort changes, so
// its bytes, and their cost, stay what they were.
func setEffort(body []byte, target string, e string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return body, nil
	}
	v, _ := json.Marshal(e)
	if target == llm.FormatOpenAIChat {
		m["reasoning_effort"] = v
		return json.Marshal(m)
	}
	r := map[string]json.RawMessage{}
	_ = json.Unmarshal(m["reasoning"], &r)
	r["effort"] = v
	m["reasoning"], _ = json.Marshal(r)
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
