package geminigenerate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

var budgets = map[string]int{
	"minimal": 512,
	"low":     1024,
	"medium":  8192,
	"high":    24576,
	"xhigh":   32768,
}

var levelOrder = []string{"minimal", "low", "medium", "high", "xhigh"}

var effortLevels = map[string]string{
	"none":    "minimal",
	"minimal": "minimal",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "xhigh",
}

var (
	geminiModel   = regexp.MustCompile(`^gemini-(\d+)(?:\.(\d+))?-`)
	roboticsModel = regexp.MustCompile(`^gemini-robotics-er-(\d+)(?:\.(\d+))?`)
)

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
	if m := roboticsModel.FindStringSubmatch(model); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		switch {
		case major >= 2:
			return []string{"minimal", "low", "medium", "high"}, 0, 0, false, true
		case major == 1 && minor >= 6:
			return nil, 0, 24576, true, true
		}
		return nil, 0, 0, false, false
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
	case major >= 4:
		if strings.Contains(model, "-neon") || strings.Contains(model, "-hydrogen") {
			levels = []string{"low", "medium", "high", "xhigh"}
		} else {
			levels = []string{"low", "medium", "high"}
		}
		return levels, 0, 0, false, true
	}
	switch {
	case major == 3 && minor == 0 && strings.Contains(model, "-pro"):
		levels = []string{"low", "high"}
	case strings.Contains(model, "-pro"), minor >= 8:
		levels = []string{"low", "medium", "high"}
	case strings.Contains(model, "-image"):
		levels = []string{"minimal", "high"}
	default:
		levels = []string{"minimal", "low", "medium", "high"}
	}
	return levels, 0, 0, false, true
}

func nearestLevel(want string, levels []string) string {
	i := slices.Index(levelOrder, want)
	for _, l := range levelOrder[max(i, 0):] {
		if slices.Contains(levels, l) {
			return l
		}
	}
	return levels[len(levels)-1]
}

func ApplyEffort(body []byte, model, effort string) ([]byte, bool, error) {
	out, _, applied, err := ApplyEffortReport(body, model, effort)
	return out, applied, err
}

func ApplyEffortReport(body []byte, model, effort string) ([]byte, []llm.Loss, bool, error) {
	levels, minB, maxB, zero, ok := ThinkingOf(model)
	if !ok {
		return body, nil, false, nil
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	budget, isBudget := llm.ThinkingBudget(effort)
	if isBudget && (budget == 0 || levels != nil) {
		effort, isBudget = llm.ThinkingEffort(effort), false
	}
	var losses []llm.Loss
	cfg := map[string]any{}
	include := false
	switch {
	case isBudget:
		clamped := min(max(budget, minB), maxB)
		if clamped != budget {
			losses = append(losses, llm.Loss{
				Kind:   llm.LossApproximated,
				Path:   "generationConfig.thinkingConfig.thinkingBudget",
				Detail: fmt.Sprintf("thinking budget %d is outside %s's range %d..%d; sent as %d", budget, model, minB, maxB, clamped),
			})
		}
		cfg["thinkingBudget"] = clamped
		include = true
	case effort == "auto":
		if levels == nil {
			cfg["thinkingBudget"] = -1
		}
		include = true
	case levels != nil:
		want, known := effortLevels[effort]
		if !known {
			return body, nil, false, nil
		}
		got := nearestLevel(want, levels)
		cfg["thinkingLevel"] = got
		if effort == "none" {
			losses = append(losses, llm.Loss{
				Kind:   llm.LossApproximated,
				Path:   "generationConfig.thinkingConfig.thinkingLevel",
				Detail: fmt.Sprintf("%s cannot turn thinking off; sent as %s, its lowest", model, got),
			})
		} else {
			if got != effort {
				losses = append(losses, llm.Loss{
					Kind:   llm.LossApproximated,
					Path:   "generationConfig.thinkingConfig.thinkingLevel",
					Detail: fmt.Sprintf("%s has no thinking level %s; sent as %s", model, effort, got),
				})
			}
			include = true
		}
	case effort == "none":
		if zero {
			cfg["thinkingBudget"] = 0
		} else {
			cfg["thinkingBudget"] = minB
			losses = append(losses, llm.Loss{
				Kind:   llm.LossApproximated,
				Path:   "generationConfig.thinkingConfig.thinkingBudget",
				Detail: fmt.Sprintf("%s cannot turn thinking off; sent with its minimum budget %d", model, minB),
			})
		}
	default:
		budget, known := budgets[effort]
		if effort == "max" {
			budget, known = maxB, true
		}
		if !known {
			return body, nil, false, nil
		}
		clamped := min(max(budget, minB), maxB)
		if clamped != budget {
			losses = append(losses, llm.Loss{
				Kind:   llm.LossApproximated,
				Path:   "generationConfig.thinkingConfig.thinkingBudget",
				Detail: fmt.Sprintf("%s thinking budget capped from %d to %d", model, budget, clamped),
			})
		}
		cfg["thinkingBudget"] = clamped
		include = true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, false, err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	genKey, genRaw := fieldForWrite(m, "generationConfig", "generation_config")
	gen := map[string]json.RawMessage{}
	if len(genRaw) > 0 && !isNull(genRaw) {
		if err := json.Unmarshal(genRaw, &gen); err != nil {
			return nil, nil, false, err
		}
	}
	if prev, ok := existingIncludeThoughts(gen); ok {
		cfg["includeThoughts"] = prev
	} else if include {
		cfg["includeThoughts"] = true
	}
	tcKey, _ := fieldForWrite(gen, "thinkingConfig", "thinking_config")
	b, _ := json.Marshal(cfg)
	gen[tcKey] = b
	m[genKey], _ = json.Marshal(gen)
	out, err := json.Marshal(m)
	return out, losses, err == nil, err
}

func fieldForWrite(m map[string]json.RawMessage, camelName, snakeName string) (string, json.RawMessage) {
	if raw, ok := m[camelName]; ok && !isNull(raw) {
		delete(m, snakeName)
		return camelName, raw
	}
	if raw, ok := m[snakeName]; ok {
		delete(m, camelName)
		return snakeName, raw
	}
	return camelName, m[camelName]
}

func existingIncludeThoughts(gen map[string]json.RawMessage) (bool, bool) {
	tc := object(gen["thinkingConfig"])
	if tc == nil {
		tc = object(gen["thinking_config"])
	}
	raw, ok := tc["includeThoughts"]
	if !ok || isNull(raw) {
		return false, false
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return false, false
	}
	return v, true
}

func ApplySummary(body []byte, model string, req llm.Request) ([]byte, []llm.Loss, bool, error) {
	o := req.Origin
	if o == nil {
		return body, nil, false, nil
	}
	if _, _, _, _, ok := ThinkingOf(model); !ok {
		return body, nil, false, nil
	}
	var wantThoughts, hasSetting, only bool
	var losses []llm.Loss
	switch o.Format {
	case llm.FormatAnthropicMessages:
		thinking := rawjson.Object(rawjson.Object(o.Raw)["thinking"])
		raw, exists := thinking["display"]
		if !exists {
			return body, nil, false, nil
		}
		var display string
		if json.Unmarshal(raw, &display) != nil {
			return body, nil, false, nil
		}
		switch strings.ToLower(strings.TrimSpace(display)) {
		case "summarized":
			wantThoughts = true
		case "omitted":
			wantThoughts = false
		default:
			return body, nil, false, nil
		}
		hasSetting = true
		only = onlyKeys(thinking, "display")
	case llm.FormatOpenAIResponses:
		reasoning := rawjson.Object(rawjson.Object(o.Raw)["reasoning"])
		key := "summary"
		raw, exists := reasoning[key]
		if !exists {
			key = "generate_summary"
			raw, exists = reasoning[key]
		}
		if !exists {
			return body, nil, false, nil
		}
		if isNull(raw) {
			wantThoughts = false
		} else {
			var summary string
			if json.Unmarshal(raw, &summary) != nil {
				return body, nil, false, nil
			}
			switch strings.ToLower(strings.TrimSpace(summary)) {
			case "auto":
				wantThoughts = true
			case "concise", "detailed":
				wantThoughts = true
				losses = append(losses, llm.Loss{
					Kind:   llm.LossApproximated,
					Path:   "reasoning." + key,
					Detail: "Gemini has one thought-summary toggle; sent with includeThoughts true",
				})
			case "none":
				wantThoughts = false
			default:
				return body, nil, false, nil
			}
		}
		hasSetting = true
		only = onlyKeys(reasoning, "summary", "generate_summary")
	}
	if !hasSetting {
		return body, nil, false, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, false, err
	}
	gen := map[string]json.RawMessage{}
	if raw, exists := m["generationConfig"]; exists && !isNull(raw) {
		if err := json.Unmarshal(raw, &gen); err != nil {
			return nil, nil, false, err
		}
	}
	tc := map[string]json.RawMessage{}
	if raw, exists := gen["thinkingConfig"]; exists && !isNull(raw) {
		if err := json.Unmarshal(raw, &tc); err != nil {
			return nil, nil, false, err
		}
	}
	if wantThoughts && hasZeroBudget(tc) {
		return body, nil, false, nil
	}
	b, _ := json.Marshal(wantThoughts)
	tc["includeThoughts"] = b
	gen["thinkingConfig"], _ = json.Marshal(tc)
	m["generationConfig"], _ = json.Marshal(gen)
	out, err := json.Marshal(m)
	return out, losses, only, err
}

func hasZeroBudget(tc map[string]json.RawMessage) bool {
	raw, ok := tc["thinkingBudget"]
	if !ok || isNull(raw) {
		return false
	}
	var b int
	return json.Unmarshal(raw, &b) == nil && b == 0
}

func onlyKeys(m map[string]json.RawMessage, allowed ...string) bool {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return len(m) > 0
}
