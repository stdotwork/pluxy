package main

import (
	"encoding/json"
	"regexp"
	"strconv"
)

// Claude takes thinking in two forms. Newer models think adaptively: thinking {"type":
// "adaptive"} with output_config.effort low, medium, high or max. Older models take a token budget:
// thinking {"type": "enabled", "budget_tokens": n}, at least 1024 and below max_tokens. Clients in
// other formats name an effort (OpenAI's reasoning_effort, from none to xhigh), which applyEffort
// turns into whichever form the model takes.

// minBudget is the smallest thinking budget Messages accepts.
const minBudget = 1024

// budgets are the thinking budgets for each effort on models that take a budget, as today's core
// maps them (minimal raised to the smallest budget Messages accepts).
var budgets = map[string]int{
	"minimal": minBudget,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
	"max":     128000,
}

// adaptiveEfforts maps each effort to output_config.effort on adaptive models.
var adaptiveEfforts = map[string]string{
	"minimal": "low",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "max",
	"max":     "max",
}

// claudeModel reads a model name such as claude-opus-4-6 or claude-sonnet-5-5-20261001: its
// family, major and minor version.
var claudeModel = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable)-(\d+)(?:-(\d{1,2}))?(?:$|-)`)

// adaptive reports whether model thinks adaptively: Claude 4.6 and later.
//
// shortcut: decided from the model name, since protocol v0 has no model capabilities; unknown
// names take a budget, as today's core does for models missing from its catalog. Move this to
// per-model capabilities when describe carries them.
func adaptive(model string) bool {
	m := claudeModel.FindStringSubmatch(model)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor := 0
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major > 4 || (major == 4 && minor >= 6)
}

// applyEffort sets the thinking settings for effort on a Messages body built for model. It reports
// false, leaving the body as it was, for an effort it does not know or a body whose max_tokens
// leaves no room for the smallest budget; the effort then stays reported as dropped.
func applyEffort(body []byte, model, effort string) ([]byte, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false, err
	}
	set := func(key string, v any) {
		b, _ := json.Marshal(v)
		m[key] = b
	}
	switch {
	case effort == "none":
		set("thinking", map[string]any{"type": "disabled"})
	case effort == "auto" && adaptive(model):
		set("thinking", map[string]any{"type": "adaptive"})
	case effort == "auto":
		// Thinking with no budget named: the smallest one.
		if !setBudget(m, minBudget, set) {
			return body, false, nil
		}
	case adaptive(model):
		e, ok := adaptiveEfforts[effort]
		if !ok {
			return body, false, nil
		}
		set("thinking", map[string]any{"type": "adaptive"})
		set("output_config", map[string]any{"effort": e})
	default:
		budget, ok := budgets[effort]
		if !ok || !setBudget(m, budget, set) {
			return body, false, nil
		}
	}
	out, err := json.Marshal(m)
	return out, true, err
}

// setBudget sets a thinking budget, kept below max_tokens as Messages requires. It reports false
// when max_tokens leaves no room for the smallest budget.
func setBudget(m map[string]json.RawMessage, budget int, set func(string, any)) bool {
	var maxTokens int
	if err := json.Unmarshal(m["max_tokens"], &maxTokens); err == nil && budget >= maxTokens {
		budget = maxTokens - 1
	}
	if budget < minBudget {
		return false
	}
	set("thinking", map[string]any{"type": "enabled", "budget_tokens": budget})
	return true
}
