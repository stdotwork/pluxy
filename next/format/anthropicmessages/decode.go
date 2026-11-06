package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"slices"

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
	if len(have) == 0 {
		return next
	}
	m := rawjson.Object(have)
	n := rawjson.Object(next)
	if m == nil || n == nil {
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
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// StreamDecoder turns the events of an upstream Messages stream into model events. Every model
// event carries the upstream event it came from in Raw; upstream events the model has no event
// for (ping, message_delta, signature deltas, redacted thinking, server tool blocks, unknown
// events) become native events, so a Messages client still gets every upstream event once. Use
// one decoder per stream.
type StreamDecoder struct {
	started bool
	done    bool
	next    int
	items   map[int]int // upstream block index -> open model item index
	stop    string
	usage   json.RawMessage
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{items: map[int]int{}}
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
	if !d.started {
		d.started = true
		info := llm.ResponseInfo{Format: format}
		if ev.Message != nil {
			info.ID, info.Model = ev.Message.ID, ev.Message.Model
			d.usage = ev.Message.Usage
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
		// signature_delta, citations_delta and deltas the model has no place for.
		return native(), nil
	case "content_block_stop":
		idx, open := d.items[ev.Index]
		if !open {
			return native(), nil
		}
		delete(d.items, ev.Index)
		return append(out, llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: raw}), nil
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

// DecodeResponse turns an upstream non-streamed Message into the events a stream of it would have
// produced. The finish event carries the whole body in Raw, so a Messages client gets it
// unchanged. A body of type error becomes an *llm.Error.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var m message
	if err := json.Unmarshal(body, &m); err != nil || rawjson.Object(body) == nil {
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
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: m.ID, Model: m.Model, Format: format}}}
	next := 0
	add := func(start llm.ItemStart, deltaType, delta string) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next})
		next++
	}
	for _, raw := range m.Content {
		var b contentBlock
		if json.Unmarshal(raw, &b) != nil {
			return nil, malformed("upstream sent a malformed response", body)
		}
		switch kindOf(b.Type) {
		case llm.ItemMessage:
			add(llm.ItemStart{Kind: llm.ItemMessage}, llm.EventTextDelta, b.Text)
		case llm.ItemReasoning:
			add(llm.ItemStart{Kind: llm.ItemReasoning}, llm.EventReasoningDelta, b.Think)
		case llm.ItemToolCall:
			if b.ID == "" || b.Name == "" {
				out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
				continue
			}
			args, err := compactJSON(b.Input)
			if err != nil {
				return nil, malformed("upstream sent a malformed response", body)
			}
			add(llm.ItemStart{Kind: llm.ItemToolCall, CallID: b.ID, Name: b.Name}, llm.EventToolInputDelta, args)
		default:
			out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
		}
	}
	f := finishOf(m.StopReason)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(m.Usage), Raw: append(json.RawMessage(nil), body...)}), nil
}
