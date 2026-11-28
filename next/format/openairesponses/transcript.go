package openairesponses

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

type Transcript struct {
	last   map[string]json.RawMessage
	output json.RawMessage
	warmup string
}

const (
	MessageCreate    = "response.create"
	MessageAppend    = "response.append"
	MessageInterrupt = "response.interrupt"
)

type TranscriptTurn struct {
	Body   []byte
	Warmup bool
}

func badRequest(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: msg}
}

func jsonString(v json.RawMessage) string {
	var s string
	_ = json.Unmarshal(v, &s)
	return strings.TrimSpace(s)
}

func isArray(v json.RawMessage) bool {
	v = bytes.TrimSpace(v)
	return len(v) > 0 && v[0] == '['
}

func IsWarmup(msg map[string]json.RawMessage) bool {
	g, ok := msg["generate"]
	return ok && jsonString(msg["type"]) == MessageCreate && string(bytes.TrimSpace(g)) == "false"
}

func (t *Transcript) Next(msg map[string]json.RawMessage) (TranscriptTurn, *llm.Error) {
	typ := jsonString(msg["type"])
	prev := jsonString(msg["previous_response_id"])
	warmup := IsWarmup(msg)
	input, hasInput := msg["input"]
	switch {
	case t.warmup != "" && prev != "":
		if prev != t.warmup {
			return TranscriptTurn{}, PreviousResponseNotFound()
		}
		if typ != MessageCreate && typ != MessageAppend {
			return TranscriptTurn{}, badRequest("unsupported websocket request type: " + typ)
		}
		if !isArray(input) {
			return TranscriptTurn{}, badRequest("websocket request requires array field: input")
		}
		merged := mergeInput(t.last, nil, input)
		m := t.replacement(msg)
		m["input"] = merged
		return TranscriptTurn{Body: marshalObject(m)}, nil
	case warmup && prev == "", t.warmup != "" && typ == MessageCreate:
		if hasInput && !isArray(input) {
			return TranscriptTurn{}, badRequest("websocket request requires array field: input")
		}
		body, e := createRequest(t.replacement(msg))
		return TranscriptTurn{Body: body, Warmup: warmup}, e
	case t.last == nil && prev != "":
		return TranscriptTurn{}, PreviousResponseNotFound()
	}
	switch typ {
	case MessageCreate, MessageAppend:
	default:
		return TranscriptTurn{}, badRequest("unsupported websocket request type: " + typ)
	}
	if t.last == nil {
		if typ == MessageAppend {
			return TranscriptTurn{}, badRequest("websocket request received before response.create")
		}
		body, e := createRequest(clone(msg))
		return TranscriptTurn{Body: body, Warmup: warmup}, e
	}
	if !isArray(input) {
		return TranscriptTurn{}, badRequest("websocket request requires array field: input")
	}
	if replacesTranscript(typ, msg, input) {
		return TranscriptTurn{Body: marshalObject(t.replacement(msg))}, nil
	}
	m := t.replacement(msg)
	if !containsCompaction(input) {
		m["input"] = mergeInput(t.last, t.output, input)
	}
	return TranscriptTurn{Body: marshalObject(m)}, nil
}

func (t *Transcript) Commit(body []byte, output json.RawMessage) {
	_ = json.Unmarshal(body, &t.last)
	t.output = output
	t.warmup = ""
}

func (t *Transcript) Warmup(body []byte, id string) {
	_ = json.Unmarshal(body, &t.last)
	delete(t.last, "generate")
	t.output = nil
	t.warmup = id
}

func (t *Transcript) Reset() { *t = Transcript{} }

func (t *Transcript) Started() bool { return t.last != nil }

func (t *Transcript) replacement(msg map[string]json.RawMessage) map[string]json.RawMessage {
	m := clone(msg)
	delete(m, "type")
	delete(m, "previous_response_id")
	if _, ok := m["model"]; !ok {
		if model := jsonString(t.last["model"]); model != "" {
			m["model"] = raw(model)
		}
	}
	if _, ok := m["instructions"]; !ok {
		if v, ok := t.last["instructions"]; ok {
			m["instructions"] = v
		}
	}
	m["stream"] = raw(true)
	return m
}

func createRequest(m map[string]json.RawMessage) ([]byte, *llm.Error) {
	input, ok := m["input"]
	if ok && !isArray(input) {
		return nil, badRequest("websocket request requires array field: input")
	}
	delete(m, "type")
	m["stream"] = raw(true)
	if !ok {
		m["input"] = json.RawMessage("[]")
	}
	if jsonString(m["model"]) == "" {
		return nil, badRequest("missing model in response.create request")
	}
	return marshalObject(m), nil
}

// compactionSummaryPrefix starts the summary the Codex client puts in a user message when it
// compacts the conversation on its own side.
const compactionSummaryPrefix = "Another language model started to solve this problem and produced a summary of its thinking process. You also have access to the state of the tools that were used by that language model. Use this to build on the work that has already been done and avoid duplicating work. Here is the summary produced by the other language model, use the information in this summary to assist with your own analysis:"

type transcriptItem struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	ID      string          `json:"id"`
	CallID  string          `json:"call_id"`
	Content json.RawMessage `json:"content"`
	Tools   json.RawMessage `json:"tools"`
}

func items(v json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	_ = json.Unmarshal(v, &out)
	return out
}

func itemOf(raw json.RawMessage) transcriptItem {
	var it transcriptItem
	_ = json.Unmarshal(raw, &it)
	it.Type, it.Role = strings.TrimSpace(it.Type), strings.TrimSpace(it.Role)
	return it
}

func replacesTranscript(typ string, msg map[string]json.RawMessage, input json.RawMessage) bool {
	if jsonString(msg["previous_response_id"]) != "" {
		return false
	}
	_, hasPrev := msg["previous_response_id"]
	list := items(input)
	if typ == MessageCreate && !hasPrev && hasCompactionSummary(list) {
		return true
	}
	for _, raw := range list {
		it := itemOf(raw)
		switch it.Type {
		case "function_call", "custom_tool_call", "tool_search_call":
			return true
		case "message":
			if it.Role == "assistant" {
				return true
			}
		}
	}
	return false
}

func hasCompactionSummary(list []json.RawMessage) bool {
	found := false
	for i, raw := range list {
		it := itemOf(raw)
		if it.Type == "additional_tools" {
			var tools []map[string]json.RawMessage
			if i != 0 || it.Role != "developer" || json.Unmarshal(it.Tools, &tools) != nil || !isArray(it.Tools) {
				return false
			}
			for _, tool := range tools {
				if tool == nil || jsonString(tool["type"]) == "" {
					return false
				}
			}
			continue
		}
		if it.Type != "" && it.Type != "message" {
			return false
		}
		if it.Role != "user" && it.Role != "developer" {
			return false
		}
		if it.Role == "user" && strings.HasPrefix(inputText(it.Content), compactionSummaryPrefix+"\n") {
			found = true
		}
	}
	return found
}

func inputText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &parts)
	var b strings.Builder
	for _, p := range parts {
		if strings.TrimSpace(p.Type) == "input_text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func isCompaction(typ string) bool { return typ == "compaction" || typ == "compaction_summary" }

func containsCompaction(input json.RawMessage) bool {
	return slices.ContainsFunc(items(input), func(r json.RawMessage) bool { return isCompaction(itemOf(r).Type) })
}

func mergeInput(last map[string]json.RawMessage, output, appendInput json.RawMessage) json.RawMessage {
	list := items(last["input"])
	if isArray(output) {
		out := items(output)
		if containsCompaction(output) {
			list = slices.DeleteFunc(list, func(r json.RawMessage) bool { return itemOf(r).Type == "compaction_trigger" })
		}
		for _, r := range out {
			list = append(list, replayItem(r))
		}
	}
	list = append(list, items(appendInput)...)
	return marshalList(dedupeByID(dedupeCalls(list)))
}

func replayItem(r json.RawMessage) json.RawMessage {
	if !bytes.Contains(r, []byte("internal_chat_message_metadata_passthrough")) && !bytes.Contains(r, []byte(`\u`)) {
		return r
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(r))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return r
	}
	if !stripTokenCounts(v) {
		return r
	}
	b, err := json.Marshal(v)
	if err != nil {
		return r
	}
	return b
}

func stripTokenCounts(v any) bool {
	changed := false
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if k == "internal_chat_message_metadata_passthrough" {
				if p, ok := child.(map[string]any); ok {
					if _, ok := p["content_token_count"]; ok {
						delete(p, "content_token_count")
						changed = true
					}
					if len(p) == 0 {
						delete(x, k)
						changed = true
						continue
					}
				}
			}
			if stripTokenCounts(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range x {
			if stripTokenCounts(child) {
				changed = true
			}
		}
	}
	return changed
}

func isToolCall(typ string) bool {
	return typ == "function_call" || typ == "custom_tool_call" || typ == "tool_search_call"
}

func isToolCallOutput(typ string) bool {
	return typ == "function_call_output" || typ == "custom_tool_call_output" || typ == "tool_search_output"
}

func dedupeCalls(list []json.RawMessage) []json.RawMessage {
	seen := map[string]bool{}
	var out []json.RawMessage
	for _, r := range list {
		it := itemOf(r)
		if isToolCall(it.Type) && it.CallID != "" {
			if seen[it.CallID] {
				continue
			}
			seen[it.CallID] = true
		}
		out = append(out, r)
	}
	return out
}

func dedupeByID(list []json.RawMessage) []json.RawMessage {
	its := make([]transcriptItem, len(list))
	answered := map[string]bool{}
	for i, r := range list {
		its[i] = itemOf(r)
		if isToolCallOutput(its[i].Type) && its[i].CallID != "" {
			answered[its[i].CallID] = true
		}
	}
	keep := map[string]int{}
	keepAnswered := map[string]bool{}
	for i, it := range its {
		if it.ID == "" {
			continue
		}
		a := it.CallID != "" && answered[it.CallID]
		if _, seen := keep[it.ID]; !seen || a || !keepAnswered[it.ID] {
			keep[it.ID], keepAnswered[it.ID] = i, a
		}
	}
	var out []json.RawMessage
	for i, r := range list {
		if id := its[i].ID; id != "" && keep[id] != i {
			continue
		}
		out = append(out, r)
	}
	return out
}

func clone(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func marshalObject(m map[string]json.RawMessage) []byte {
	b, _ := json.Marshal(m)
	return b
}

func marshalList(list []json.RawMessage) json.RawMessage {
	if list == nil {
		list = []json.RawMessage{}
	}
	b, _ := json.Marshal(list)
	return b
}
