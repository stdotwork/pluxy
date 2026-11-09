package geminigenerate

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Gemini takes thinking in two forms (Google's thinking guide). Gemini 3 and later take a level:
// generationConfig.thinkingConfig.thinkingLevel, from the levels the model has. Gemini 2.5 takes a
// token budget: thinkingConfig.thinkingBudget, within the model's range, -1 for dynamic, 0 to turn
// thinking off where the model allows it. Clients in other formats name an effort (OpenAI's
// reasoning_effort, from none to xhigh), which ApplyEffort turns into whichever form the model
// takes, with includeThoughts so the thought summaries come back as reasoning.

// budgets are the thinking budgets for each effort on models that take a budget, as today's core
// maps them; each is then kept within the model's range.
var budgets = map[string]int{
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
}

// levelOrder orders Gemini's thinking levels.
var levelOrder = []string{"minimal", "low", "medium", "high"}

// effortLevels maps each effort to the thinking level it asks for.
var effortLevels = map[string]string{
	"none":    "minimal",
	"minimal": "minimal",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "high",
	"max":     "high",
}

var geminiModel = regexp.MustCompile(`^gemini-(\d+)(?:\.(\d+))?-`)

// ThinkingOf returns how a model takes thinking: its levels (Gemini 3 and later), or its budget
// range and whether 0 turns thinking off (Gemini 2.5 and the -latest names). ok is false for a
// model name the extension does not know as a thinking Gemini model, which gets no thinking
// settings.
//
// shortcut: decided from the model name with today's core's catalog as the reference, since
// protocol v0 has no model capabilities. Move this to per-model capabilities when describe
// carries them.
func ThinkingOf(model string) (levels []string, minBudget, maxBudget int, zero, ok bool) {
	model = strings.ToLower(strings.TrimPrefix(model, "models/"))
	switch model {
	case "gemini-pro-latest":
		return nil, 128, 32768, false, true
	case "gemini-flash-latest":
		return nil, 0, 24576, true, true
	case "gemini-flash-lite-latest":
		return nil, 512, 24576, true, true
	}
	m := geminiModel.FindStringSubmatch(model)
	if m == nil {
		return nil, 0, 0, false, false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	switch {
	case major == 2 && minor == 5:
		switch {
		case strings.Contains(model, "-pro"):
			return nil, 128, 32768, false, true
		case strings.Contains(model, "-flash-lite"):
			return nil, 512, 24576, true, true
		case strings.Contains(model, "-flash"):
			return nil, 0, 24576, true, true
		}
		return nil, 0, 0, false, false
	case major < 3:
		return nil, 0, 0, false, false
	}
	switch {
	case major == 3 && minor == 0 && strings.Contains(model, "-pro"):
		levels = []string{"low", "high"}
	case strings.Contains(model, "-pro"), major > 3 || minor >= 8:
		levels = []string{"low", "medium", "high"}
	case strings.Contains(model, "-image"):
		levels = []string{"minimal", "high"}
	default:
		levels = []string{"minimal", "low", "medium", "high"}
	}
	return levels, 0, 0, false, true
}

// nearestLevel returns want when the model has it, otherwise the next level up it has, otherwise
// its highest.
func nearestLevel(want string, levels []string) string {
	i := slices.Index(levelOrder, want)
	for _, l := range levelOrder[max(i, 0):] {
		if slices.Contains(levels, l) {
			return l
		}
	}
	return levels[len(levels)-1]
}

// ApplyEffort sets the thinking settings for effort on a GenerateContent body built for model.
// effort may also be a token budget, as a model name's thinking suffix gives it
// (llm.ThinkingBudget): a model that takes a budget gets that budget within its range, a model
// with levels the level the budget falls in (llm.ThinkingEffort). It reports false, leaving the
// body as it was, for an effort it does not know or a model that is not a thinking Gemini model;
// the effort then stays reported as dropped.
func ApplyEffort(body []byte, model, effort string) ([]byte, bool, error) {
	levels, minB, maxB, zero, ok := ThinkingOf(model)
	if !ok {
		return body, false, nil
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	budget, isBudget := llm.ThinkingBudget(effort)
	if isBudget && (budget == 0 || levels != nil) {
		effort, isBudget = llm.ThinkingEffort(effort), false
	}
	cfg := map[string]any{}
	switch {
	case isBudget:
		cfg["thinkingBudget"] = min(max(budget, minB), maxB)
		cfg["includeThoughts"] = true
	case effort == "auto":
		if levels == nil {
			cfg["thinkingBudget"] = -1
		}
		cfg["includeThoughts"] = true
	case levels != nil:
		want, known := effortLevels[effort]
		if !known {
			return body, false, nil
		}
		cfg["thinkingLevel"] = nearestLevel(want, levels)
		if effort != "none" {
			cfg["includeThoughts"] = true
		}
	case effort == "none":
		// A model that cannot turn thinking off thinks as little as it can.
		cfg["thinkingBudget"] = minB
		if zero {
			cfg["thinkingBudget"] = 0
		}
	default:
		budget, known := budgets[effort]
		if effort == "max" {
			budget, known = maxB, true
		}
		if !known {
			return body, false, nil
		}
		cfg["thinkingBudget"] = min(max(budget, minB), maxB)
		cfg["includeThoughts"] = true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false, err
	}
	gen := map[string]json.RawMessage{}
	if raw, ok := m["generationConfig"]; ok {
		if err := json.Unmarshal(raw, &gen); err != nil {
			return nil, false, err
		}
	}
	b, _ := json.Marshal(cfg)
	gen["thinkingConfig"] = b
	m["generationConfig"], _ = json.Marshal(gen)
	out, err := json.Marshal(m)
	return out, err == nil, err
}
