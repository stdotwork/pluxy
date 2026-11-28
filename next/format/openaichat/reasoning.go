package openaichat

import (
	"encoding/json"
	"strings"
)

// reasoningFields are the fields a Chat message or delta may carry reasoning in. Upstreams name
// it differently: reasoning_content (DeepSeek and others), reasoning (vLLM, GLM) and
// reasoning_details (OpenRouter's structured list).
type reasoningFields struct {
	ReasoningContent json.RawMessage `json:"reasoning_content"`
	Reasoning        json.RawMessage `json:"reasoning"`
	Details          json.RawMessage `json:"reasoning_details"`
}

func (f reasoningFields) text() string {
	if t := fieldText(f.ReasoningContent); t != "" {
		return t
	}
	if t := fieldText(f.Reasoning); t != "" {
		return t
	}
	return detailsText(f.Details)
}

func fieldText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(partText(p))
	}
	return b.String()
}

func partText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var p struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	switch p.Type {
	case "reasoning.encrypted", "encrypted":
		return ""
	}
	return p.Text
}

func detailsText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		var d struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Summary string `json:"summary"`
		}
		if json.Unmarshal(e, &d) != nil {
			continue
		}
		switch d.Type {
		case "reasoning.encrypted", "encrypted":
			continue
		}
		if d.Text != "" {
			b.WriteString(d.Text)
			continue
		}
		b.WriteString(d.Summary)
	}
	return b.String()
}
