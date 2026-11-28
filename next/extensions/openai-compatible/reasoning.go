package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/internal/jsonscan"
	"github.com/stdotwork/pluxy/next/llm"
)

var defaultReasoningLevels = []string{"low", "medium", "high"}

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func applyReasoning(body []byte, req llm.Request, target string, levels []string) ([]byte, []llm.Loss, error) {
	suffix := req.Options.ThinkingSuffix
	switch target {
	case llm.FormatAnthropicMessages:
		if suffix == "" {
			return body, nil, nil
		}
		out, _, err := anthropicmessages.ApplyEffort(body, req.Model, suffix)
		return out, nil, err
	case llm.FormatGeminiGenerate:
		if suffix == "" {
			return body, nil, nil
		}
		out, _, err := geminigenerate.ApplyEffort(body, req.Model, suffix)
		return out, nil, err
	}
	have, ok := bodyEffort(body, target)
	if !ok {
		return body, nil, nil
	}
	effort := have
	if suffix != "" {
		effort = llm.ThinkingEffort(suffix)
	}
	clamped := clampEffort(effort, levels)
	path := "reasoning.effort"
	if target == llm.FormatOpenAIChat {
		path = "reasoning_effort"
	}
	var losses []llm.Loss
	if effort != "" && clamped != effort {
		losses = []llm.Loss{{Kind: llm.LossApproximated, Path: path,
			Detail: fmt.Sprintf("reasoning effort %q is not one the account takes; %q was sent", effort, clamped)}}
	}
	if clamped == have {
		return body, losses, nil
	}
	out, err := setEffort(body, target, clamped)
	return out, losses, err
}

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

func names(key []byte, name string) bool {
	if bytes.IndexByte(key, '\\') < 0 {
		return string(key[1:len(key)-1]) == name
	}
	k, ok := jsonscan.Unquote(key)
	return ok && k == name
}

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
	if r == nil {
		r = map[string]json.RawMessage{}
	}
	r["effort"] = v
	m["reasoning"], _ = json.Marshal(r)
	return json.Marshal(m)
}

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
		if d < bestDist || (d == bestDist && j > slices.Index(effortOrder, best)) {
			best, bestDist = l, d
		}
	}
	return best
}
