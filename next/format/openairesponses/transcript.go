package openairesponses

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Transcript keeps the conversation of one Responses WebSocket client whose turns go to an
// upstream without socket state (each turn is a separate HTTP request), so each turn can carry
// the whole conversation: a turn that continues the last response (previous_response_id, or a
// response.append) gets the last request's input and the last response's output in front of its
// own input. The rules follow today's core (its WebSocket handler's transcript merge), which the
// corpus records.
type Transcript struct {
	last   map[string]json.RawMessage // the last turn's request as sent upstream; nil before one
	output json.RawMessage            // the last response's output items
	warmup string                     // the ID of a warmup answered without the upstream, until the next turn
}

// Message types a Responses WebSocket client sends.
const (
	MessageCreate    = "response.create"
	MessageAppend    = "response.append"
	MessageInterrupt = "response.interrupt"
)

// TranscriptTurn is one client message made into an upstream request.
type TranscriptTurn struct {
	// Body is the Responses request body to send upstream.
	Body []byte
	// Warmup is set for a warmup (generate: false), which the caller answers without the
	// upstream (WarmupEvents) and records with Warmup.
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

// IsWarmup reports whether a client message only warms the socket up: a response.create with
// generate set to false.
func IsWarmup(msg map[string]json.RawMessage) bool {
	g, ok := msg["generate"]
	return ok && jsonString(msg["type"]) == MessageCreate && string(bytes.TrimSpace(g)) == "false"
}

// Next makes a client message (response.create or response.append) into the next turn's
// request, or returns the error the client gets for it. It does not change the transcript; the
// caller records a turn that completed with Commit, a warmup with Warmup.
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
		// The warmup's input never reached the upstream: send it with this turn's.
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
		// A warmup, or a create after one, that names no parent starts the conversation again.
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
	// From here on the message names a parent or appends to one (a warmup without a parent is
	// handled above): it goes upstream as a turn, warmup or not, as in today's core.
	if replacesTranscript(typ, msg, input) {
		return TranscriptTurn{Body: marshalObject(t.replacement(msg))}, nil
	}
	m := t.replacement(msg)
	if !containsCompaction(input) {
		m["input"] = mergeInput(t.last, t.output, input)
	}
	// Input with a compaction item already holds the whole conversation, compacted: merging
	// the stale turns into it would break its tool call pairs. Today's core sends it as it is
	// to an upstream that takes compaction items (the Codex endpoint, which made them).
	return TranscriptTurn{Body: marshalObject(m)}, nil
}

// Commit records a turn that completed: the request sent upstream and the response's output.
func (t *Transcript) Commit(body []byte, output json.RawMessage) {
	_ = json.Unmarshal(body, &t.last)
	t.output = output
	t.warmup = ""
}

// Warmup records a warmup answered without the upstream, with the response ID the client got.
func (t *Transcript) Warmup(body []byte, id string) {
	_ = json.Unmarshal(body, &t.last)
	delete(t.last, "generate")
	t.output = nil
	t.warmup = id
}

// Reset forgets the conversation.
func (t *Transcript) Reset() { *t = Transcript{} }

// Started reports whether a turn has completed on the transcript.
func (t *Transcript) Started() bool { return t.last != nil }

// replacement returns msg as a request that stands alone: without type and
// previous_response_id, with the last request's model and instructions where msg has none,
// streamed.
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

// createRequest finishes the request of a first turn: input a list (empty when absent), a
// model, streamed.
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

// replacesTranscript reports whether a message with no previous_response_id carries the whole
// conversation itself: its input holds a model's output (a tool call or an assistant message),
// or the Codex client's own compaction summary.
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
		case "function_call", "custom_tool_call":
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

// mergeInput returns the last request's input, then the last response's output (as input
// items, without fields only output has), then the new input; a tool call whose call_id came
// earlier is left out, and of the items sharing an id only one stays.
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

// replayItem removes from a response item replayed as input the content_token_count fields of
// its internal_chat_message_metadata_passthrough objects, which only output has, and such
// objects left empty.
func replayItem(r json.RawMessage) json.RawMessage {
	// The key may be spelled with JSON escapes, which only decoding resolves.
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

func isToolCall(typ string) bool { return typ == "function_call" || typ == "custom_tool_call" }
func isToolCallOutput(typ string) bool {
	return typ == "function_call_output" || typ == "custom_tool_call_output"
}

// dedupeCalls keeps the first tool call of each call_id.
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

// dedupeByID keeps one item per id: the last, unless an earlier one is the call a tool output
// answers and the later one is not.
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
