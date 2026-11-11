package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Repairs to every Messages body before it is sent, a client's own Messages body included, for
// parts Anthropic refuses that a client may still send. Today's core makes the same two.

// fixBody removes empty domain lists from built-in web search tools and replaces tool call IDs
// Anthropic does not take. It returns body unchanged when there is nothing to repair.
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

// toolIDPrefix starts a tool call ID made from one Anthropic does not take; today's core uses the
// same, so a conversation keeps its IDs when it moves between the two cores.
const toolIDPrefix = "cpa_tid_v1_"

// portableID reports whether Anthropic takes id as a tool call ID: one or more letters, digits,
// underscores and hyphens.
func portableID(id string) bool {
	if id == "" {
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

// repairToolIDs replaces each tool_use id and tool_result tool_use_id Anthropic does not take
// (calls replayed from another provider can have dots, colons or other characters) with the
// prefix and the ID in unpadded URL-safe base64. The same ID becomes the same new ID, so each
// result still names its call, and two different IDs never become one. A new ID that is already
// in use gets underscores in front until it is not. Valid IDs are left alone.
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
			id = toolIDPrefix + base64.RawURLEncoding.EncodeToString([]byte(t.id))
			for used[id] {
				id = "_" + id
			}
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
