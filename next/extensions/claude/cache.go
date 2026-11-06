package main

import (
	"bytes"
	"encoding/json"
)

// Prompt caching. A Messages client places its own cache_control markers, and its body is sent as
// it wrote it. A client in another format cannot place them, so a request translated to Messages
// gets the breakpoints today's core gives it: the end of the system prompt (or, without one, the
// last tool) and the last block of the last message that can hold one. Each breakpoint caches
// everything before it (https://docs.claude.com/en/docs/build-with-claude/prompt-caching); three
// stay within Messages' limit of four.

// ephemeral is the default cache marker: Anthropic's five-minute cache.
var ephemeral = json.RawMessage(`{"type":"ephemeral"}`)

// uncacheable are block types that cannot carry cache_control.
var uncacheable = map[string]bool{"thinking": true, "redacted_thinking": true}

// addCacheBreakpoints adds cache breakpoints to a translated Messages body that has none.
func addCacheBreakpoints(body []byte) ([]byte, error) {
	if bytes.Contains(body, []byte(`"cache_control"`)) {
		return body, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	var system, tools []map[string]json.RawMessage
	_ = json.Unmarshal(m["system"], &system)
	_ = json.Unmarshal(m["tools"], &tools)
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(m["messages"], &messages); err != nil {
		return body, nil
	}

	set := func(key string, v any) {
		b, _ := json.Marshal(v)
		m[key] = b
	}
	if len(system) > 0 {
		system[len(system)-1]["cache_control"] = ephemeral
		set("system", system)
	} else if len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = ephemeral
		set("tools", tools)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		var role string
		_ = json.Unmarshal(messages[i]["role"], &role)
		var content []map[string]json.RawMessage
		if (role != "user" && role != "assistant") || json.Unmarshal(messages[i]["content"], &content) != nil {
			// A message given as a string is not one EncodeRequest writes; leave it.
			continue
		}
		j := len(content) - 1
		for ; j >= 0; j-- {
			var typ string
			_ = json.Unmarshal(content[j]["type"], &typ)
			if !uncacheable[typ] {
				break
			}
		}
		if j < 0 {
			continue
		}
		content[j]["cache_control"] = ephemeral
		b, _ := json.Marshal(content)
		messages[i]["content"] = b
		set("messages", messages)
		break
	}
	return json.Marshal(m)
}
