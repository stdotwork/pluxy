package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	advisorBeta                    = "advisor-tool-2026-03-01"
	extendedCacheTTLBeta           = "extended-cache-ttl-2025-04-11"
	thinkingBindingBeta            = "thinking-binding-controls-2026-08-01"
	midConversationToolChangesBeta = "mid-conversation-tool-changes-2026-07-01"
	inlineToolsBeta                = "inline-tools-2026-09-15"
)

func bodyBetas(body []byte, count bool) ([]byte, []string) {
	if !bytes.Contains(body, []byte(`"tools"`)) && !bytes.Contains(body, []byte(`"betas"`)) &&
		!bytes.Contains(body, []byte(`"1h"`)) && !bytes.Contains(body, []byte(`"block_binding"`)) &&
		!bytes.Contains(body, []byte(`"tool_addition"`)) && !bytes.Contains(body, []byte(`"tool_removal"`)) {
		return body, nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body, nil
	}
	var betas []string
	var tools []cacheable
	_ = json.Unmarshal(m["tools"], &tools)
	for _, t := range tools {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Type)), "advisor_") {
			betas = append(betas, advisorBeta)
			break
		}
	}
	if raw, ok := m["betas"]; ok {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			list = []json.RawMessage{raw}
		}
		for _, item := range list {
			if b := betaText(item); b != "" {
				betas = append(betas, b)
			}
		}
		delete(m, "betas")
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(m) == nil {
			body = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		}
	}
	if !count && hasHourTTL(m, tools) {
		betas = append(betas, extendedCacheTTLBeta)
	}
	var thinking map[string]json.RawMessage
	if json.Unmarshal(m["thinking"], &thinking) == nil {
		if _, ok := thinking["block_binding"]; ok {
			betas = append(betas, thinkingBindingBeta)
		}
	}
	if b := toolChangeBeta(m); b != "" {
		betas = append(betas, b)
	}
	return body, betas
}

func betaText(item json.RawMessage) string {
	var t string
	var v any
	if json.Unmarshal(item, &v) != nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		t = strings.TrimSpace(v)
	case float64, bool:
		t = strings.TrimSpace(string(item))
	}
	if strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return t
}

type cacheable struct {
	Type         string          `json:"type"`
	CacheControl json.RawMessage `json:"cache_control"`
}

func hasHourTTL(m map[string]json.RawMessage, tools []cacheable) bool {
	hour := func(cc json.RawMessage) bool {
		var c struct {
			TTL string `json:"ttl"`
		}
		return json.Unmarshal(cc, &c) == nil && c.TTL == "1h"
	}
	for _, t := range tools {
		if hour(t.CacheControl) {
			return true
		}
	}
	var system []cacheable
	_ = json.Unmarshal(m["system"], &system)
	for _, b := range system {
		if hour(b.CacheControl) {
			return true
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(m["messages"], &messages)
	for _, msg := range messages {
		var content []cacheable
		_ = json.Unmarshal(msg.Content, &content)
		for _, b := range content {
			if hour(b.CacheControl) {
				return true
			}
		}
	}
	return false
}

func toolChangeBeta(m map[string]json.RawMessage) string {
	raw := m["messages"]
	if !bytes.Contains(raw, []byte(`"tool_addition"`)) && !bytes.Contains(raw, []byte(`"tool_removal"`)) {
		return ""
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(raw, &messages)
	hasChange := false
	for _, msg := range messages {
		var content []struct {
			Type string          `json:"type"`
			Tool json.RawMessage `json:"tool"`
		}
		if json.Unmarshal(msg.Content, &content) != nil {
			continue
		}
		for _, b := range content {
			switch b.Type {
			case "tool_removal":
				hasChange = true
			case "tool_addition":
				hasChange = true
				var tool struct {
					Type       string          `json:"type"`
					Definition json.RawMessage `json:"definition"`
				}
				if json.Unmarshal(b.Tool, &tool) == nil {
					def := bytes.TrimSpace(tool.Definition)
					if tool.Type == "tool_definition" || (len(def) > 0 && !bytes.Equal(def, []byte("null"))) {
						return inlineToolsBeta
					}
				}
			}
		}
	}
	if hasChange {
		return midConversationToolChangesBeta
	}
	return ""
}

func addBetas(list string, betas []string) string {
	for _, b := range betas {
		if b = strings.TrimSpace(b); b != "" {
			list = withBeta(list, b)
		}
	}
	return list
}
