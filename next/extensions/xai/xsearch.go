package main

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

func toolType(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["type"].(string)
	return strings.TrimSpace(s)
}
func textField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
func hasSearch(tools []any) bool {
	for _, v := range tools {
		if toolType(v) == "x_search" {
			return true
		}
	}
	return false
}
func requestHasSearch(m map[string]any) bool {
	tools, _ := m["tools"].([]any)
	if hasSearch(tools) {
		return true
	}
	input, _ := m["input"].([]any)
	for _, v := range input {
		if toolType(v) == "additional_tools" {
			item, _ := v.(map[string]any)
			tools, _ := item["tools"].([]any)
			if hasSearch(tools) {
				return true
			}
		}
	}
	return false
}

// Match today's core: a forced hosted tool must not compete with injected search.
// allowed_tools must also allow the injected tool, without losing the client's entries.
func injectSearch(m map[string]any) {
	tools, _ := m["tools"].([]any)
	choice, _ := m["tool_choice"].(map[string]any)
	for _, hosted := range []string{"web_search", "image_generation"} {
		if choice != nil {
			if textField(choice, "type") == hosted {
				var kept []any
				for _, v := range tools {
					if toolType(v) == hosted {
						kept = append(kept, v)
					}
				}
				if len(kept) > 0 {
					tools = kept
					m["tools"] = tools
					m["tool_choice"] = "required"
					choice = nil
				}
			} else if textField(choice, "type") == "allowed_tools" {
				allowed, _ := choice["tools"].([]any)
				var kept []any
				removed := false
				for _, v := range allowed {
					if toolType(v) == hosted {
						removed = true
					} else {
						kept = append(kept, v)
					}
				}
				if removed {
					if len(kept) == 0 {
						var hostedTools []any
						for _, v := range tools {
							if toolType(v) == hosted {
								hostedTools = append(hostedTools, v)
							}
						}
						if len(hostedTools) > 0 {
							tools = hostedTools
							m["tools"] = tools
							mode := "required"
							if textField(choice, "mode") == "auto" {
								mode = "auto"
							}
							m["tool_choice"] = mode
							choice = nil
						}
					} else {
						choice["tools"] = kept
					}
				}
			}
		}
		mode, _ := m["tool_choice"].(string)
		if (mode == "required" || mode == "auto") && len(tools) > 0 {
			only := true
			for _, v := range tools {
				if toolType(v) != hosted {
					only = false
					break
				}
			}
			if only {
				return
			}
		}
	}
	if !requestHasSearch(m) {
		m["tools"] = append(tools, map[string]any{"type": "x_search"})
	}
	if choice != nil && textField(choice, "type") == "allowed_tools" {
		allowed, _ := choice["tools"].([]any)
		if !hasSearch(allowed) {
			choice["tools"] = append(allowed, map[string]any{"type": "x_search"})
		}
	}
}

type searchFilter struct {
	enabled  bool
	declared map[string]bool
	indexes  map[int]bool
	ids      map[string]bool
}

func newSearchFilter(body []byte) *searchFilter {
	m, _ := decodeObject(body)
	f := &searchFilter{enabled: requestHasSearch(m), declared: map[string]bool{}, indexes: map[int]bool{}, ids: map[string]bool{}}
	collect := func(tools []any) {
		for _, v := range tools {
			t, _ := v.(map[string]any)
			if kind := toolType(t); kind == "function" || kind == "custom" {
				f.declared[textField(t, "name")] = true
			}
		}
	}
	tools, _ := m["tools"].([]any)
	collect(tools)
	input, _ := m["input"].([]any)
	for _, v := range input {
		if toolType(v) == "additional_tools" {
			item, _ := v.(map[string]any)
			tools, _ := item["tools"].([]any)
			collect(tools)
		}
	}
	return f
}

func (f *searchFilter) internal(item map[string]any) bool {
	kind := toolType(item)
	if kind != "function_call" && kind != "custom_tool_call" {
		return false
	}
	if textField(item, "namespace") != "" {
		return false
	}
	name := textField(item, "name")
	switch name {
	case "x_user_search", "x_semantic_search", "x_keyword_search", "x_thread_fetch":
	default:
		return false
	}
	return strings.HasPrefix(textField(item, "call_id"), "xs_call") || kind == "custom_tool_call" || !f.declared[name]
}

func (f *searchFilter) output(m map[string]any) bool {
	list, ok := m["output"].([]any)
	if !ok {
		return false
	}
	kept := make([]any, 0, len(list))
	for _, v := range list {
		item, _ := v.(map[string]any)
		if !f.internal(item) {
			kept = append(kept, v)
		}
	}
	if len(kept) == len(list) {
		return false
	}
	m["output"] = kept
	return true
}

// Filter before the Responses decoder so all client formats see the same items and finish reason.
func (f *searchFilter) response(raw []byte) []byte {
	if !f.enabled {
		return raw
	}
	m, err := decodeObject(raw)
	if err != nil || m == nil || !f.output(m) {
		return raw
	}
	out, err := marshal(m)
	if err != nil {
		return raw
	}
	return out
}

func (f *searchFilter) event(raw []byte) []byte {
	if !f.enabled {
		return raw
	}
	m, err := decodeObject(raw)
	if err != nil || m == nil {
		return raw
	}
	index, hasIndex := m["output_index"].(json.Number)
	n, _ := index.Int64()
	item, _ := m["item"].(map[string]any)
	if f.internal(item) {
		if hasIndex {
			f.indexes[int(n)] = true
		}
		for _, key := range []string{"id", "call_id"} {
			if id := textField(item, key); id != "" {
				f.ids[id] = true
			}
		}
		return nil
	}
	if hasIndex && f.indexes[int(n)] {
		return nil
	}
	for _, key := range []string{"item_id", "call_id"} {
		if f.ids[textField(m, key)] {
			return nil
		}
	}
	changed := false
	if response, ok := m["response"].(map[string]any); ok {
		changed = f.output(response)
	}
	if hasIndex {
		removed := 0
		for dropped := range f.indexes {
			if dropped < int(n) {
				removed++
			}
		}
		if removed > 0 {
			m["output_index"] = int(n) - removed
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := marshal(m)
	if err != nil {
		return raw
	}
	return out
}

type searchDecoder struct {
	streamDecoder
	filter *searchFilter
}

func (d *searchDecoder) Decode(raw []byte) ([]llm.Event, error) {
	raw = d.filter.event(raw)
	if raw == nil {
		return nil, nil
	}
	return d.streamDecoder.Decode(raw)
}
