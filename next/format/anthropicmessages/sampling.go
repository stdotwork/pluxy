package anthropicmessages

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

func DropRefusedSampling(body []byte, model string) ([]byte, []llm.Loss, error) {
	if !strings.HasPrefix(claudeName(model), "claude-") {
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
	case !placed:
		const why = "this Claude model name is not known as a Claude version, so it is treated as one that takes no sampling settings (Claude 4.7 and later take only the defaults, and not temperature and top_p together)"
		if hasTemperature {
			drop("temperature", why)
		}
		if hasTopP {
			drop("top_p", why)
		}
	case major > 4 || (major == 4 && minor >= 7):
		if hasTemperature && hasTopP {
			const why = "Claude 4.7 and later models refuse temperature and top_p together, even at their defaults"
			drop("temperature", why)
			drop("top_p", why)
		} else if hasTemperature && temperature != 1 {
			drop("temperature", "Claude 4.7 and later models take only the default temperature, 1")
		} else if hasTopP && topP != 0.99 {
			drop("top_p", "Claude 4.7 and later models take only the default top_p, 0.99")
		}
	default:
		if thinkingOn(m["thinking"]) {
			if hasTemperature && temperature != 1 {
				drop("temperature", "Claude takes no temperature other than 1 while thinking is on")
				hasTemperature = false
			}
			if hasTopP && topP < 0.95 {
				drop("top_p", "Claude takes top_p only from 0.95 to 1 while thinking is on")
				hasTopP = false
			}
		}
		if hasTemperature && hasTopP {
			drop("top_p", "Claude refuses temperature and top_p together; temperature is kept")
		}
	}
	if len(losses) == 0 {
		return body, nil, nil
	}
	out, err := json.Marshal(m)
	return out, losses, err
}

func number(v json.RawMessage) (n float64, ok bool) {
	var p *float64
	if v == nil || json.Unmarshal(v, &p) != nil || p == nil {
		return 0, false
	}
	return *p, true
}

func thinkingOn(v json.RawMessage) bool {
	var t struct {
		Type string `json:"type"`
	}
	return v != nil && json.Unmarshal(v, &t) == nil && (t.Type == "enabled" || t.Type == "adaptive")
}
