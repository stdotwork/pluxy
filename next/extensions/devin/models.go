package main

import (
	"regexp"
	"slices"
	"strings"
)

var modelLevels = map[string][]string{
	"swe-2":                    {"medium", "high", "max"},
	"swe-2-lightning":          {"medium", "high", "max"},
	"swe-1-7":                  {"medium"},
	"swe-1-6":                  nil,
	"claude-fable-5-1":         {"low", "medium", "high", "xhigh", "max"},
	"claude-5-fable":           {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-5-5":          {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-5":            {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-8":          {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-7":          {"low", "medium", "high", "xhigh", "max"},
	"claude-opus-4-6":          nil,
	"claude-opus-4-6-1m":       nil,
	"claude-sonnet-5-5":        {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-5":          {"low", "medium", "high", "xhigh", "max"},
	"claude-sonnet-4-6":        nil,
	"claude-sonnet-4-6-1m":     nil,
	"claude-haiku-5-5":         {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-1-sol":              {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-astra":              {"low", "medium", "high", "xhigh", "max"},
	"gpt-6-sol":                {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-6-luna":               {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5-6-sol":              {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5-6-terra":            {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5-6-luna":             {"none", "low", "medium", "high", "xhigh", "max"},
	"gpt-5-5":                  {"none", "low", "medium", "high", "xhigh"},
	"gpt-5-4":                  {"none", "low", "medium", "high", "xhigh"},
	"gpt-5-4-mini":             {"low", "medium", "high", "xhigh"},
	"gemini-4-argon":           {"low", "medium", "high"},
	"gemini-3-8-flash":         {"low", "medium", "high"},
	"gemini-3-7-flash":         {"low", "medium", "high"},
	"gemini-3-6-flash":         {"minimal", "low", "medium", "high"},
	"gemini-3-5-flash":         {"minimal", "low", "medium", "high"},
	"grok-4-7":                 {"low", "medium", "high", "xhigh"},
	"grok-4-6":                 {"low", "medium", "high", "xhigh"},
	"kimi-k3":                  {"low", "high", "max"},
	"kimi-k2-7":                nil,
	"kimi-k2-6":                nil,
	"glm-5-3":                  {"low", "high", "max"},
	"glm-5-3-flash":            {"low", "high", "max"},
	"glm-5-2":                  {"none", "max"},
	"glm-5-2-1m":               {"none", "max"},
	"deepseek-v4-pro":          {"high", "max"},
	"deepseek-v4-flash":        {"high", "max"},
	"deepseek-v4-1-flash-1017": {"none", "low", "high", "max"},
	"muse-wonder-1-0":          {"minimal", "low", "medium", "high", "xhigh", "max"},
	"nemotron-3-ultra":         {"none", "medium", "high"},
}

const defaultModel = "swe-2"

var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

var effortSuffix = regexp.MustCompile(`(?:[-_](?:none|minimal|low|medium|high|xhigh|max)(?:-(?:fast|priority|lightning|1m))?|-(?:fast|slow|priority|thinking|thinking-1m))$`)

func modelID(model, effort string) string {
	model = strings.TrimSpace(model)
	if len(model) >= 6 && strings.EqualFold(model[:6], "devin/") {
		model = model[6:]
	}
	if model == "" {
		model = defaultModel
	}
	if effortSuffix.MatchString(model) {
		return model
	}
	effort = normalEffort(effort)
	if id, ok := variantID(model, effort); ok {
		return id
	}
	levels, known := modelLevels[model]
	if !known {
		if effort == "" {
			return model
		}
		return model + "-" + effort
	}
	if len(levels) == 0 {
		return model
	}
	if stem, ok := strings.CutSuffix(model, "-lightning"); ok {
		return stem + "-" + nearestEffort(effort, levels, defaultEffort(stem, levels)) + "-lightning"
	}
	return model + "-" + nearestEffort(effort, levels, defaultEffort(model, levels))
}

func variantID(model, effort string) (string, bool) {
	thinking := effort != "" && effort != "none"
	switch model {
	case "swe-1-7":
		if effort == "medium" {
			return "swe-1-7-medium", true
		}
		return model, true
	case "swe-1-6":
		if effort == "fast" {
			return "swe-1-6-fast", true
		}
		return model, true
	case "glm-5-2", "glm-5-2-1m":
		stem, long := strings.CutSuffix(model, "-1m")
		suffix := ""
		if long {
			suffix = "-1m"
		}
		if effort == "none" || effort == "max" {
			return stem + "-" + effort + suffix, true
		}
		return model, true
	case "claude-opus-4-6", "claude-sonnet-4-6":
		if thinking {
			return model + "-thinking", true
		}
		return model, true
	case "claude-opus-4-6-1m", "claude-sonnet-4-6-1m":
		if thinking {
			return strings.TrimSuffix(model, "-1m") + "-thinking-1m", true
		}
		return model, true
	}
	return "", false
}

func normalEffort(e string) string {
	switch e = strings.ToLower(strings.TrimSpace(e)); e {
	case "off", "disabled":
		return "none"
	case "auto", "adaptive":
		return "high"
	}
	return e
}

func defaultEffort(model string, levels []string) string {
	has := func(l string) bool { return slices.Contains(levels, l) }
	switch {
	case strings.Contains(model, "swe-2"):
		return "high"
	case strings.HasPrefix(model, "gpt-5") && has("none") && has("low"):
		return "low"
	case has("high") && containsAny(model, "gemini", "grok", "glm", "deepseek", "kimi", "nemotron"):
		return "high"
	}
	for _, l := range []string{"medium", "high", "low"} {
		if has(l) {
			return l
		}
	}
	return levels[0]
}

func nearestEffort(effort string, levels []string, def string) string {
	if slices.Contains(levels, effort) {
		return effort
	}
	want := slices.Index(effortOrder, effort)
	if want < 0 {
		return def
	}
	best, bestDist := def, len(effortOrder)
	for _, l := range levels {
		i := slices.Index(effortOrder, l)
		if i < 0 {
			continue
		}
		d := i - want
		if d < 0 {
			d = -d
		}
		if d < bestDist || d == bestDist && i > slices.Index(effortOrder, best) {
			best, bestDist = l, d
		}
	}
	return best
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
