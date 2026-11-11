package main

import (
	"encoding/json"
	"strings"
)

// moveSystemMessages moves the text of every message with role system into the top-level system
// prompt, after what it already holds, and removes those messages: for a Claude-compatible
// upstream that refuses system messages in the conversation (the account setting
// rebuild_mid_system_message). Text blocks keep their fields (cache_control); a plain string
// becomes a text block; blank text and blocks that are not text are left out. A body with no
// system message, or whose system messages hold no text, is returned unchanged, as today's core
// does. Only a Messages client's own body can have such messages: a translated request already
// has all its system and developer messages in the system prompt.
func moveSystemMessages(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var messages []json.RawMessage
	if json.Unmarshal(m["messages"], &messages) != nil {
		return body, nil
	}
	var moved []json.RawMessage
	kept := make([]json.RawMessage, 0, len(messages))
	for _, raw := range messages {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &msg) == nil && strings.EqualFold(strings.TrimSpace(msg.Role), "system") {
			moved = append(moved, textBlocks(msg.Content)...)
			continue
		}
		kept = append(kept, raw)
	}
	if len(moved) == 0 {
		return body, nil
	}
	system := append(textBlocks(m["system"]), moved...)
	var err error
	if m["system"], err = json.Marshal(system); err != nil {
		return nil, err
	}
	if m["messages"], err = json.Marshal(kept); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// textBlocks returns the non-blank text of a system prompt or message content as text blocks: a
// string becomes one block, a list keeps its text blocks as they are (and turns strings in it into
// blocks).
func textBlocks(raw json.RawMessage) []json.RawMessage {
	block := func(s string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"type": "text", "text": s})
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []json.RawMessage{block(s)}
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var out []json.RawMessage
	for _, item := range list {
		if json.Unmarshal(item, &s) == nil {
			if strings.TrimSpace(s) != "" {
				out = append(out, block(s))
			}
			continue
		}
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(item, &b) == nil && b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			out = append(out, item)
		}
	}
	return out
}
