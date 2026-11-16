package anthropicmessages

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Claude models refuse some sampling settings with a 400 (Anthropic's Thinking page, "Sampling
// parameters"; OpenAI SDK compatibility, the temperature and top_p rows):
//   - Claude 4.7 and later take only the defaults, and refuse temperature and top_p together even
//     at their defaults;
//   - older models with thinking on take temperature only at 1 and top_p only from 0.95 to 1;
//   - temperature and top_p together are refused.
//
// The rules hold for anthropic-version 2023-06-01, which translated requests are sent with.

// DropRefusedSampling removes from a Messages body built for model the sampling settings that
// model refuses, and reports each as dropped:
//   - a Claude model of 4.7 or later, or a claude- name not known as an older model: temperature
//     and top_p;
//   - an older Claude model with thinking on (thinking.type enabled or adaptive): temperature
//     other than 1, and top_p below 0.95;
//   - an older Claude model without thinking: top_p when temperature is set too.
//
// A model name that does not start with claude- keeps its settings. The body is returned as it
// was when nothing is removed.
//
// shortcut: decided from the model name, as claudeVersion is.
func DropRefusedSampling(body []byte, model string) ([]byte, []llm.Loss, error) {
	if !strings.HasPrefix(model, "claude-") {
		return body, nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, err
	}
	temperature, hasTemperature := number(m["temperature"])
	topP, hasTopP := number(m["top_p"])
	if !hasTemperature && !hasTopP {
		return body, nil, nil
	}
	var losses []llm.Loss
	drop := func(key, why string) {
		delete(m, key)
		losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: key, Detail: why})
	}
	major, minor, placed := parseClaudeVersion(model)
	switch {
	case !placed || major > 4 || (major == 4 && minor >= 7):
		const why = "Claude 4.7 and later models refuse temperature and top_p other than their defaults, and both together; a Claude model name not known as an older model is treated as one of them"
		if hasTemperature {
			drop("temperature", why)
		}
		if hasTopP {
			drop("top_p", why)
		}
	case thinkingOn(m["thinking"]):
		if hasTemperature && temperature != 1 {
			drop("temperature", "Claude takes no temperature other than 1 while thinking is on")
		}
		if hasTopP && topP < 0.95 {
			drop("top_p", "Claude takes top_p only from 0.95 to 1 while thinking is on")
		}
	case hasTemperature && hasTopP:
		drop("top_p", "Claude refuses temperature and top_p together; temperature is kept")
	}
	if len(losses) == 0 {
		return body, nil, nil
	}
	out, err := json.Marshal(m)
	return out, losses, err
}

// number reads a JSON number; ok is false when v is absent or not a number.
func number(v json.RawMessage) (n float64, ok bool) {
	if v == nil {
		return 0, false
	}
	return n, json.Unmarshal(v, &n) == nil
}

// thinkingOn reports whether a Messages thinking setting turns thinking on.
func thinkingOn(v json.RawMessage) bool {
	var t struct {
		Type string `json:"type"`
	}
	return v != nil && json.Unmarshal(v, &t) == nil && (t.Type == "enabled" || t.Type == "adaptive")
}
