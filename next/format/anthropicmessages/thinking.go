package anthropicmessages

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Claude takes thinking in two forms. Newer models think adaptively: thinking {"type":
// "adaptive"} with output_config.effort low, medium, high, xhigh or max. Older models take a token
// budget: thinking {"type": "enabled", "budget_tokens": n}, at least 1024 and below max_tokens.
// Clients in other formats name an effort (OpenAI's reasoning_effort, from none to xhigh), which
// ApplyEffort turns into whichever form the model takes, with the level and the way to turn
// thinking off that each model has (FitThinking).

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

// adaptiveEfforts maps each effort to output_config.effort on adaptive models; FitThinking then
// sends xhigh as max to a model without it. Messages has no minimal.
var adaptiveEfforts = map[string]string{
	"minimal": "low",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "max",
}

// claudeModel reads a model name such as claude-opus-4-6 or claude-sonnet-5-5-20261001: its
// major and minor version. The minor is left out only before an 8-digit date, the end of the name
// or a part that does not start with a digit, so claude-sonnet-4-20250514 is 4.0 and
// claude-opus-4-7x is no version. oldClaudeModel reads the older form, such as
// claude-3-7-sonnet-latest.
var (
	claudeModel    = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d{1,2})(?:$|-)|-\d{8}(?:$|-)|-\D|$)`)
	oldClaudeModel = regexp.MustCompile(`^claude-(\d+)(?:-(\d))?-(?:opus|sonnet|haiku|fable|mythos)(?:$|-)`)
)

// bedrockPrefixes are the prefixes Amazon Bedrock puts before a Claude model name: the provider,
// and before it a cross-region inference profile's geography.
var bedrockPrefixes = []string{"global.anthropic.", "apac.anthropic.", "us.anthropic.", "eu.anthropic.", "anthropic."}

// claudeName returns model as the parsers read it: lowercase, without a Bedrock prefix, and cut at
// a context window in brackets (claude-opus-4-7[1m]) or a Vertex AI version after @
// (claude-opus-4@20250514).
func claudeName(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, p := range bedrockPrefixes {
		if rest, ok := strings.CutPrefix(m, p); ok {
			m = rest
			break
		}
	}
	m, _, _ = strings.Cut(m, "@")
	m, _, _ = strings.Cut(m, "[")
	return m
}

// claudeVersion reads the version of a Claude model that can think (Claude 3.7 and later); ok is
// false for any other name, which then gets no thinking settings, as today's core gives none to
// models missing from its catalog.
//
// shortcut: decided from the model name, since protocol v0 has no model capabilities. Move this
// to per-model capabilities when describe carries them.
func claudeVersion(model string) (major, minor int, ok bool) {
	major, minor, ok = parseClaudeVersion(model)
	return major, minor, ok && (major > 3 || (major == 3 && minor >= 7))
}

// parseClaudeVersion reads the version in a Claude model name of either form (claudeName); ok is
// false for a name it cannot place.
func parseClaudeVersion(model string) (major, minor int, ok bool) {
	model = claudeName(model)
	m := claudeModel.FindStringSubmatch(model)
	if m == nil {
		m = oldClaudeModel.FindStringSubmatch(model)
	}
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major, minor, true
}

// adaptive reports whether a Claude model of this version thinks adaptively: Claude 4.6 and later.
func adaptive(major, minor int) bool { return major > 4 || (major == 4 && minor >= 6) }

// ApplyEffort sets the thinking settings for effort on a Messages body built for model. effort
// may also be a token budget, as a model name's thinking suffix gives it (llm.ThinkingBudget):
// a model that takes a budget gets that budget, an adaptive model the effort it falls in
// (llm.ThinkingEffort). It reports false, leaving the body as it was, for an effort it does not
// know or a body whose max_tokens leaves no room for the smallest budget, or a model that is not
// a Claude model; the effort then stays reported as dropped. The settings are then made ones the
// model takes, as FitThinking makes them (none, for example, becomes between_tools on Claude
// Sonnet 5.5); ApplyEffortReport also returns each difference from what was asked.
func ApplyEffort(body []byte, model, effort string) ([]byte, bool, error) {
	out, _, applied, err := ApplyEffortReport(body, model, effort)
	return out, applied, err
}

// ApplyEffortReport is ApplyEffort, and also reports each place the settings sent differ from
// the effort asked for, with its path in the body.
func ApplyEffortReport(body []byte, model, effort string) ([]byte, []llm.Loss, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, false, err
	}
	major, minor, ok := claudeVersion(model)
	if !ok {
		return body, nil, false, nil
	}
	var losses []llm.Loss
	set := func(key string, v any) {
		b, _ := json.Marshal(v)
		m[key] = b
	}
	budget, isBudget := llm.ThinkingBudget(effort)
	if isBudget && (budget == 0 || adaptive(major, minor)) {
		effort, isBudget = llm.ThinkingEffort(effort), false
	}
	switch {
	case isBudget:
		if !setBudget(m, max(budget, minBudget), set) {
			return body, nil, false, nil
		}
	case effort == "none":
		set("thinking", map[string]any{"type": "disabled"})
	case effort == "auto" && adaptive(major, minor):
		set("thinking", map[string]any{"type": "adaptive"})
	case effort == "auto":
		// Thinking with no budget named: the smallest one.
		if !setBudget(m, minBudget, set) {
			return body, nil, false, nil
		}
	case adaptive(major, minor):
		e, ok := adaptiveEfforts[effort]
		if !ok {
			return body, nil, false, nil
		}
		if e != effort {
			losses = append(losses, llm.Loss{Kind: llm.LossApproximated, Path: "output_config.effort",
				Detail: "Claude has no effort " + effort + "; sent as " + e + ", its lowest"})
		}
		set("thinking", map[string]any{"type": "adaptive"})
		set("output_config", map[string]any{"effort": e})
	default:
		budget, ok := budgets[effort]
		if !ok || !setBudget(m, budget, set) {
			return body, nil, false, nil
		}
	}
	if r, ok := rulesFor(model); ok {
		f := &fitter{m: m, r: r}
		f.config()
		losses = append(losses, f.losses...)
	}
	out, err := json.Marshal(m)
	return out, losses, true, err
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
