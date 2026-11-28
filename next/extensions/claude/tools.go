package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

func fixBody(body []byte) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	tools, toolsChanged := dropEmptyDomains(m["tools"])
	messages, messagesChanged := repairToolIDs(m["messages"])
	if !toolsChanged && !messagesChanged {
		return body, nil
	}
	if toolsChanged {
		m["tools"] = tools
	}
	if messagesChanged {
		m["messages"] = messages
	}
	return json.Marshal(m)
}

// dropEmptyDomains removes an empty allowed_domains or blocked_domains list from each built-in
// web search tool (type web_search_*). Anthropic refuses an empty list as ambiguous; leaving the
// field out means the same as the empty list meant to the client: no restriction.
func dropEmptyDomains(raw json.RawMessage) (json.RawMessage, bool) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return raw, false
	}
	changed := false
	for _, t := range tools {
		var typ string
		if json.Unmarshal(t["type"], &typ) != nil || !strings.HasPrefix(typ, "web_search_") {
			continue
		}
		for _, field := range []string{"allowed_domains", "blocked_domains"} {
			var list []json.RawMessage
			if v, ok := t[field]; ok && json.Unmarshal(v, &list) == nil && list != nil && len(list) == 0 {
				delete(t, field)
				changed = true
			}
		}
	}
	if !changed {
		return raw, false
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return raw, false
	}
	return b, true
}

const toolIDPrefix = "cpa_tid_v1_"

// maxToolID is the longest tool call ID Anthropic takes, and the upstreams that carry a Messages
// body under their own limits: letters, digits, underscores and hyphens, at most 64.
const maxToolID = 64

func portableID(id string) bool {
	if id == "" || len(id) > maxToolID {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func repairToolIDs(raw json.RawMessage) (json.RawMessage, bool) {
	var messages []map[string]json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return raw, false
	}
	type target struct {
		msg   int
		block map[string]json.RawMessage
		field string
		id    string
	}
	var targets []target
	contents := make([][]map[string]json.RawMessage, len(messages))
	used := map[string]bool{}
	for i, msg := range messages {
		if json.Unmarshal(msg["content"], &contents[i]) != nil {
			continue
		}
		for _, b := range contents[i] {
			var typ string
			_ = json.Unmarshal(b["type"], &typ)
			field := map[string]string{"tool_use": "id", "tool_result": "tool_use_id"}[typ]
			var id string
			if field == "" || json.Unmarshal(b[field], &id) != nil || id == "" {
				continue
			}
			if portableID(id) {
				used[id] = true
			} else {
				targets = append(targets, target{i, b, field, id})
			}
		}
	}
	if len(targets) == 0 {
		return raw, false
	}
	mapped := map[string]string{}
	dirty := map[int]bool{}
	for _, t := range targets {
		id, ok := mapped[t.id]
		if !ok {
			id = encodeToolID(t.id, used)
			mapped[t.id] = id
			used[id] = true
		}
		t.block[t.field], _ = json.Marshal(id)
		dirty[t.msg] = true
	}
	for i := range dirty {
		messages[i]["content"], _ = json.Marshal(contents[i])
	}
	b, err := json.Marshal(messages)
	if err != nil {
		return raw, false
	}
	return b, true
}

func encodeToolID(id string, used map[string]bool) string {
	enc := toolIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(id))
	for used[enc] {
		enc = "_" + enc
		if len(enc) > maxToolID {
			break
		}
	}
	if len(enc) <= maxToolID && !used[enc] {
		return enc
	}
	for attempt := 0; ; attempt++ {
		seed := id
		if attempt > 0 {
			seed += "\x00" + strconv.Itoa(attempt)
		}
		sum := sha256.Sum256([]byte(seed))
		candidate := toolIDPrefix + hex.EncodeToString(sum[:])[:maxToolID-len(toolIDPrefix)]
		if !used[candidate] {
			return candidate
		}
	}
}
