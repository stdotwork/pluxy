package anthropicmessages

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const minBudget = 1024

var budgets = map[string]int{
	"minimal": minBudget,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
	"max":     128000,
}

var adaptiveEfforts = map[string]string{
	"minimal": "low",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "max",
}

var (
	claudeModel    = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable|mythos)-(\d+)(?:-(\d{1,2})(?:$|-)|-\d{8}(?:$|-)|-\D|$)`)
	oldClaudeModel = regexp.MustCompile(`^claude-(\d+)(?:-(\d))?-(?:opus|sonnet|haiku|fable|mythos)(?:$|-)`)
)

var bedrockPrefixes = []string{"global.anthropic.", "apac.anthropic.", "us.anthropic.", "eu.anthropic.", "anthropic."}

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

func claudeVersion(model string) (major, minor int, ok bool) {
	major, minor, ok = parseClaudeVersion(model)
	return major, minor, ok && (major > 3 || (major == 3 && minor >= 7))
}

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

func adaptive(major, minor int) bool { return major > 4 || (major == 4 && minor >= 6) }

func ApplyEffort(body []byte, model, effort string) ([]byte, bool, error) {
	out, _, applied, err := ApplyEffortReport(body, model, effort)
	return out, applied, err
}

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
		cfg := rawjson.Object(m["output_config"])
		if cfg == nil {
			cfg = map[string]json.RawMessage{}
		}
		cfg["effort"], _ = json.Marshal(e)
		set("output_config", cfg)
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
