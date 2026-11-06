package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// maxErrorRaw limits the upstream bytes an error keeps for diagnostics.
const maxErrorRaw = 1 << 20

func malformed(msg string, data []byte) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Raw: llm.RawJSON(data[:min(len(data), maxErrorRaw)])}
}

type usage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
}

// decodeUsage reads Messages usage. Anthropic's input_tokens counts only the input after the last
// cache breakpoint; cache reads and writes are counted apart from it, so the model's InputTokens
// is their sum.
func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u usage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{OutputTokens: u.OutputTokens, CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteInputTokens: u.CacheCreationInputTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.InputTokens != nil || u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil {
		var total int64
		for _, p := range []*int64{u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens} {
			if p != nil {
				total += *p
			}
		}
		out.InputTokens = &total
	}
	return out
}

// mergeUsage overlays the usage of a message_delta on what the stream reported so far: Messages
// stream usage is cumulative, and a field a later event leaves out keeps its earlier value.
func mergeUsage(have, next json.RawMessage) json.RawMessage {
	if len(have) == 0 || string(have) == "null" {
		return next
	}
	m := rawjson.Object(have)
	n := rawjson.Object(next)
	if n == nil {
		return have
	}
	if m == nil {
		return next
	}
	for k, v := range n {
		if string(v) != "null" {
			m[k] = v
		}
	}
	return rawjson.Encode(m)
}

// finishOf maps a stop reason to the model's. The raw value always travels with it.
func finishOf(reason string) llm.Finish {
	f := llm.Finish{Reason: llm.FinishOther, RawReason: reason}
	switch reason {
	case "end_turn", "stop_sequence":
		f.Reason = llm.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		f.Reason = llm.FinishLength
	case "tool_use":
		f.Reason = llm.FinishToolCalls
	case "refusal":
		f.Reason = llm.FinishContentFilter
	}
	return f
}

// kindOf returns the model's item kind for a content block type, or "" when the model has no such
// item.
func kindOf(typ string) string {
	switch typ {
	case "text":
		return llm.ItemMessage
	case "thinking":
		return llm.ItemReasoning
	case "tool_use":
		return llm.ItemToolCall
	}
	return ""
}

// errorOf reads an Anthropic error object ({"type": ..., "message": ...}).
func errorOf(raw json.RawMessage, fallback string) *llm.Error {
	var e struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &llm.Error{Code: llm.CodeUpstream, Message: e.Message, ProviderCode: e.Type}
	if out.Message == "" {
		out.Message = fallback
	}
	return out
}

type contentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Think string          `json:"thinking"`
	Input json.RawMessage `json:"input"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	} `json:"message"`
	ContentBlock *contentBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// MaxSignature bounds the thinking signature the stream decoder collects for one block. Claude's
// signatures run to kilobytes; one this large means a broken stream.
const MaxSignature = 1 << 20

// StreamDecoder turns the events of an upstream Messages stream into model events. Every model
// event carries the upstream event it came from in Raw; upstream events the model has no event
// for (ping, message_delta, signature deltas, redacted thinking, server tool blocks, unknown
// events) become native events, so a Messages client still gets every upstream event once. Use
// one decoder per stream.
type StreamDecoder struct {
	started bool
	done    bool
	next    int
	items   map[int]int              // upstream block index -> open model item index
	sigs    map[int]*strings.Builder // model item index -> signature of an open thinking block
	stop    string
	usage   json.RawMessage
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{items: map[int]int{}, sigs: map[int]*strings.Builder{}}
}

// closeAll closes items the upstream left open; the events it adds carry no Raw.
func (d *StreamDecoder) closeAll(out []llm.Event) []llm.Event {
	open := make([]int, 0, len(d.items))
	for _, idx := range d.items {
		open = append(open, idx)
	}
	slices.Sort(open)
	for _, idx := range open {
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: idx})
	}
	clear(d.items)
	return out
}

// Decode takes the data of one SSE event and returns the events it produces. message_stop ends
// the stream; an error event becomes an *llm.Error that keeps the upstream event in Raw.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("anthropicmessages: data after the end of the stream")
	}
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.Type == "" {
		return nil, d.withUsage(malformed("upstream sent a malformed stream event", data))
	}
	raw := append(json.RawMessage(nil), data...)
	if ev.Type == "error" {
		d.done = true
		e := errorOf(ev.Error, "upstream reported an error mid-stream")
		e.Raw = raw
		return nil, d.withUsage(e)
	}
	var out []llm.Event
	if ev.Type == "message_start" && ev.Message != nil {
		// Read even when other events (a ping) came first.
		d.usage = mergeUsage(ev.Message.Usage, d.usage)
	}
	if !d.started {
		d.started = true
		info := llm.ResponseInfo{Format: format}
		if ev.Message != nil {
			info.ID, info.Model = ev.Message.ID, ev.Message.Model
		}
		// The first event's raw goes on response.start, whatever it is, so a Messages client
		// still gets every upstream event once.
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &info, Raw: raw})
		if ev.Type == "message_start" {
			return out, nil
		}
		raw = nil
	}
	native := func() []llm.Event {
		if raw == nil {
			return out
		}
		return append(out, llm.Event{Type: llm.EventNative, Raw: raw})
	}
	switch ev.Type {
	case "content_block_start":
		b := ev.ContentBlock
		if b == nil {
			return native(), nil
		}
		kind := kindOf(b.Type)
		if kind == "" || (kind == llm.ItemToolCall && (b.ID == "" || b.Name == "")) {
			return native(), nil
		}
		if _, open := d.items[ev.Index]; open {
			return nil, d.withUsage(malformed("upstream opened content block twice", data))
		}
		idx := d.next
		d.next++
		d.items[ev.Index] = idx
		start := llm.ItemStart{Kind: kind}
		if kind == llm.ItemToolCall {
			start.CallID, start.Name = b.ID, b.Name
		}
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: idx, Item: &start, Raw: raw})
		// A block may start with content in it; carry it as a delta that keeps no raw.
		switch {
		case b.Type == "text" && b.Text != "":
			out = append(out, llm.Event{Type: llm.EventTextDelta, Index: idx, Delta: b.Text})
		case b.Type == "thinking" && b.Think != "":
			out = append(out, llm.Event{Type: llm.EventReasoningDelta, Index: idx, Delta: b.Think})
		}
		return out, nil
	case "content_block_delta":
		idx, open := d.items[ev.Index]
		if !open || ev.Delta == nil {
			return native(), nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			return append(out, llm.Event{Type: llm.EventTextDelta, Index: idx, Delta: ev.Delta.Text, Raw: raw}), nil
		case "thinking_delta":
			return append(out, llm.Event{Type: llm.EventReasoningDelta, Index: idx, Delta: ev.Delta.Thinking, Raw: raw}), nil
		case "input_json_delta":
			return append(out, llm.Event{Type: llm.EventToolInputDelta, Index: idx, Delta: ev.Delta.PartialJSON, Raw: raw}), nil
		}
		if ev.Delta.Type == "signature_delta" {
			// Carried on the item's end as well, for clients in other formats.
			sig := d.sigs[idx]
			if sig == nil {
				sig = &strings.Builder{}
				d.sigs[idx] = sig
			}
			if sig.Len()+len(ev.Delta.Signature) > MaxSignature {
				d.done = true
				return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint,
					Message: fmt.Sprintf("upstream sent a thinking signature over %d bytes", MaxSignature)})
			}
			sig.WriteString(ev.Delta.Signature)
		}
		// signature_delta, citations_delta and deltas the model has no place for.
		return native(), nil
	case "content_block_stop":
		idx, open := d.items[ev.Index]
		if !open {
			return native(), nil
		}
		delete(d.items, ev.Index)
		end := llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: raw}
		if sig := d.sigs[idx]; sig != nil && sig.Len() > 0 {
			end.Opaque = &llm.Opaque{Format: format, Data: sig.String()}
		}
		delete(d.sigs, idx)
		return append(out, end), nil
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			d.stop = ev.Delta.StopReason
		}
		if len(ev.Usage) > 0 && string(ev.Usage) != "null" {
			d.usage = mergeUsage(d.usage, ev.Usage)
		}
		return native(), nil
	case "message_stop":
		d.done = true
		out = d.closeAll(out)
		f := finishOf(d.stop)
		return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(d.usage), Raw: raw}), nil
	}
	// ping, message_start after the first event, and event types added later.
	return native(), nil
}

// Close is called when the upstream stream ends. A stream that ends before message_stop is an
// error: the reply may have been cut short.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
}

// Usage returns the usage the upstream reported so far, or nil. A caller that fails the stream
// for its own reasons (the connection broke, say) puts it on the error.
func (d *StreamDecoder) Usage() *llm.Usage { return decodeUsage(d.usage) }

// withUsage puts the usage reported so far on an error that ends the stream.
func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.Usage()
	return e
}

type message struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      json.RawMessage   `json:"usage"`
	Error      json.RawMessage   `json:"error"`
}

func eventJSON(fields map[string]any) json.RawMessage {
	b, _ := json.Marshal(fields)
	return b
}

// DecodeResponse turns an upstream non-streamed Message into the events a stream of it would have
// produced, each carrying in Raw the Messages stream event it stands for, so a streaming Messages
// client gets every block whole: signatures as signature_delta, and blocks the model has no place
// for (redacted thinking, server tool results) as they were. The finish event carries the whole
// body in Raw, so a non-streaming Messages client gets it unchanged. A body of type error becomes
// an *llm.Error.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var m message
	top := rawjson.Object(body)
	if err := json.Unmarshal(body, &m); err != nil || top == nil {
		return nil, malformed("upstream sent a malformed response", body)
	}
	if m.Type == "error" {
		e := errorOf(m.Error, "upstream reported an error")
		e.Raw = append(json.RawMessage(nil), body...)
		return nil, e
	}
	if m.Type != "message" {
		return nil, malformed("upstream sent a response that is not a message", body)
	}
	head := map[string]json.RawMessage{}
	for k, v := range top {
		head[k] = v
	}
	// The stop fields come at the end of a stream, in message_delta.
	stopFields := []string{"stop_reason", "stop_sequence", "stop_details", "container"}
	head["content"] = json.RawMessage("[]")
	for _, k := range stopFields {
		if _, ok := head[k]; ok || k == "stop_reason" || k == "stop_sequence" {
			head[k] = json.RawMessage("null")
		}
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: m.ID, Model: m.Model, Format: format},
		Raw: eventJSON(map[string]any{"type": "message_start", "message": head})}}
	next := 0
	for i, raw := range m.Content {
		b := rawjson.Object(raw)
		var cb contentBlock
		if b == nil || json.Unmarshal(raw, &cb) != nil {
			return nil, malformed("upstream sent a malformed response", body)
		}
		kind := kindOf(cb.Type)
		if kind == llm.ItemToolCall && (cb.ID == "" || cb.Name == "") {
			kind = ""
		}
		stop := eventJSON(map[string]any{"type": "content_block_stop", "index": i})
		if kind == "" {
			out = append(out,
				llm.Event{Type: llm.EventNative, Raw: eventJSON(map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(raw)})},
				llm.Event{Type: llm.EventNative, Raw: stop})
			continue
		}
		start := llm.ItemStart{Kind: kind}
		startBlock := map[string]json.RawMessage{}
		for k, v := range b {
			startBlock[k] = v
		}
		var deltaType, delta string
		var deltaRaw json.RawMessage
		switch kind {
		case llm.ItemMessage:
			startBlock["text"] = json.RawMessage(`""`)
			deltaType, delta = llm.EventTextDelta, cb.Text
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "text_delta", "text": cb.Text}})
		case llm.ItemReasoning:
			startBlock["thinking"], startBlock["signature"] = json.RawMessage(`""`), json.RawMessage(`""`)
			deltaType, delta = llm.EventReasoningDelta, cb.Think
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "thinking_delta", "thinking": cb.Think}})
		case llm.ItemToolCall:
			start.CallID, start.Name = cb.ID, cb.Name
			args, err := compactJSON(cb.Input)
			if err != nil {
				return nil, malformed("upstream sent a malformed response", body)
			}
			startBlock["input"] = json.RawMessage("{}")
			deltaType, delta = llm.EventToolInputDelta, args
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "input_json_delta", "partial_json": args}})
		}
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start,
			Raw: eventJSON(map[string]any{"type": "content_block_start", "index": i, "content_block": startBlock})})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta, Raw: deltaRaw})
		}
		if sig, ok := b["signature"]; ok && kind == llm.ItemReasoning && string(sig) != `""` {
			out = append(out, llm.Event{Type: llm.EventNative, Raw: eventJSON(map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]json.RawMessage{"type": json.RawMessage(`"signature_delta"`), "signature": sig}})})
		}
		end := llm.Event{Type: llm.EventItemEnd, Index: next, Raw: stop}
		var sig string
		if kind == llm.ItemReasoning && json.Unmarshal(b["signature"], &sig) == nil && sig != "" {
			end.Opaque = &llm.Opaque{Format: format, Data: sig}
		}
		out = append(out, end)
		next++
	}
	md := map[string]json.RawMessage{"stop_reason": json.RawMessage("null"), "stop_sequence": json.RawMessage("null")}
	for _, k := range stopFields {
		if v, ok := top[k]; ok {
			md[k] = v
		}
	}
	delta := map[string]any{"type": "message_delta", "delta": md}
	if len(m.Usage) > 0 {
		delta["usage"] = m.Usage
	}
	out = append(out, llm.Event{Type: llm.EventNative, Raw: eventJSON(delta)})
	f := finishOf(m.StopReason)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(m.Usage), Raw: append(json.RawMessage(nil), body...)}), nil
}
