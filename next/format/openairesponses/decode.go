package openairesponses

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
)

// maxErrorRaw limits the upstream bytes an error keeps for diagnostics.
const maxErrorRaw = 1 << 20

func malformed(msg string, data []byte) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Raw: llm.RawJSON(data[:min(len(data), maxErrorRaw)])}
}

type usage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u usage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.InputTokensDetails != nil {
		out.CachedInputTokens = u.InputTokensDetails.CachedTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out
}

// response is the part of a Response object the decoder reads.
type response struct {
	ID                string            `json:"id"`
	Model             string            `json:"model"`
	CreatedAt         int64             `json:"created_at"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             json.RawMessage   `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type outputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

// kindOf returns the model's item kind for a Responses output item, or "" when the model has no
// such item.
func kindOf(it outputItem) string {
	switch it.Type {
	case "message":
		return llm.ItemMessage
	case "reasoning":
		return llm.ItemReasoning
	case "function_call":
		return llm.ItemToolCall
	}
	return ""
}

// finishOf says why a terminal Response ended.
func finishOf(r response) llm.Finish {
	switch r.Status {
	case "incomplete":
		reason := ""
		if r.IncompleteDetails != nil {
			reason = r.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			return llm.Finish{Reason: llm.FinishLength, RawReason: reason}
		case "content_filter":
			return llm.Finish{Reason: llm.FinishContentFilter, RawReason: reason}
		}
		return llm.Finish{Reason: llm.FinishOther, RawReason: reason}
	case "completed":
		for _, raw := range r.Output {
			var it outputItem
			if json.Unmarshal(raw, &it) == nil && it.Type == "function_call" {
				return llm.Finish{Reason: llm.FinishToolCalls, RawReason: r.Status}
			}
		}
		return llm.Finish{Reason: llm.FinishStop, RawReason: r.Status}
	}
	return llm.Finish{Reason: llm.FinishOther, RawReason: r.Status}
}

func failure(r response, raw []byte) *llm.Error {
	e := &llm.Error{Code: llm.CodeUpstream, Message: "upstream reported the response failed", Raw: llm.RawJSON(raw), Usage: decodeUsage(r.Usage)}
	if r.Error != nil {
		if r.Error.Message != "" {
			e.Message = r.Error.Message
		}
		e.ProviderCode = r.Error.Code
	}
	return e
}

type streamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
	Response    json.RawMessage `json:"response"`
	Code        any             `json:"code"`
	Message     string          `json:"message"`
}

// StreamDecoder turns the events of an upstream Responses stream into model events. Every model
// event carries the upstream event it came from in Raw; upstream events the model has no place
// for (response.in_progress, content parts, done events, hosted tool items) become native
// events. Use one decoder per stream.
type StreamDecoder struct {
	started bool
	done    bool
	next    int
	items   map[int]int // upstream output_index -> open model item index
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{items: map[int]int{}}
}

func (d *StreamDecoder) start(out []llm.Event, info llm.ResponseInfo, raw json.RawMessage) []llm.Event {
	d.started = true
	info.Format = format
	return append(out, llm.Event{Type: llm.EventResponseStart, Response: &info, Raw: raw})
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

// Decode takes the data of one SSE event and returns the events it produces. A terminal
// upstream event (response.completed, response.incomplete, response.failed or error) ends the
// stream; response.failed and error become an *llm.Error.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openairesponses: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
		// Not part of Responses; some compatible servers send it after the terminal event.
		return nil, nil
	}
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.Type == "" {
		return nil, malformed("upstream sent a malformed stream event", data)
	}
	raw := append(json.RawMessage(nil), data...)
	var out []llm.Event
	if ev.Type == "error" {
		d.done = true
		e := &llm.Error{Code: llm.CodeUpstream, Message: ev.Message, Raw: raw}
		if e.Message == "" {
			e.Message = "upstream reported an error mid-stream"
		}
		if ev.Code != nil {
			e.ProviderCode = fmt.Sprint(ev.Code)
		}
		return nil, e
	}
	var r response
	if len(ev.Response) > 0 {
		_ = json.Unmarshal(ev.Response, &r)
	}
	if !d.started {
		if ev.Type == "response.created" {
			return d.start(out, llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt}, raw), nil
		}
		out = d.start(out, llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt}, nil)
	}
	native := llm.Event{Type: llm.EventNative, Raw: raw}
	switch ev.Type {
	case "response.output_item.added":
		var it outputItem
		_ = json.Unmarshal(ev.Item, &it)
		kind := kindOf(it)
		if kind == "" || (kind == llm.ItemToolCall && (it.CallID == "" || it.Name == "")) {
			return append(out, native), nil
		}
		if _, open := d.items[ev.OutputIndex]; open {
			return nil, malformed("upstream opened output item twice", data)
		}
		idx := d.next
		d.next++
		d.items[ev.OutputIndex] = idx
		start := llm.ItemStart{Kind: kind}
		if kind == llm.ItemToolCall {
			start.CallID, start.Name = it.CallID, it.Name
		}
		return append(out, llm.Event{Type: llm.EventItemStart, Index: idx, Item: &start, Raw: raw}), nil
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta":
		idx, open := d.items[ev.OutputIndex]
		if !open {
			return append(out, native), nil
		}
		t := map[string]string{
			"response.output_text.delta":             llm.EventTextDelta,
			"response.reasoning_summary_text.delta":  llm.EventReasoningDelta,
			"response.reasoning_text.delta":          llm.EventReasoningDelta,
			"response.function_call_arguments.delta": llm.EventToolInputDelta,
		}[ev.Type]
		return append(out, llm.Event{Type: t, Index: idx, Delta: ev.Delta, Raw: raw}), nil
	case "response.output_item.done":
		idx, open := d.items[ev.OutputIndex]
		if !open {
			return append(out, native), nil
		}
		delete(d.items, ev.OutputIndex)
		return append(out, llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: raw}), nil
	case "response.completed", "response.incomplete":
		d.done = true
		out = d.closeAll(out)
		f := finishOf(r)
		return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Raw: raw}), nil
	case "response.failed":
		d.done = true
		return out, failure(r, data)
	}
	return append(out, native), nil
}

// Usage returns the usage the upstream reported before the stream failed. A Responses stream
// reports usage only in its terminal event, which ends the stream, so there is none to return;
// a response.failed event's usage is on its error.
func (d *StreamDecoder) Usage() *llm.Usage { return nil }

// Close is called when the upstream stream ends. A stream that ends before its terminal event
// is an error: the reply may have been cut short.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"}
}

// DecodeResponse turns an upstream non-streamed Response object into the events a stream of it
// would have produced. The finish event carries the whole body in Raw, so a Responses client
// gets it unchanged.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, malformed("upstream sent a malformed response", body)
	}
	if r.Status == "failed" {
		return nil, failure(r, body)
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt, Format: format}}}
	next := 0
	add := func(start llm.ItemStart, deltaType, delta string) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next})
		next++
	}
	for _, raw := range r.Output {
		var it outputItem
		if json.Unmarshal(raw, &it) != nil {
			return nil, malformed("upstream sent a malformed response", body)
		}
		switch kindOf(it) {
		case llm.ItemMessage:
			text := ""
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
			add(llm.ItemStart{Kind: llm.ItemMessage}, llm.EventTextDelta, text)
		case llm.ItemReasoning:
			text := ""
			for _, s := range it.Summary {
				text += s.Text
			}
			add(llm.ItemStart{Kind: llm.ItemReasoning}, llm.EventReasoningDelta, text)
		case llm.ItemToolCall:
			if it.CallID == "" || it.Name == "" {
				out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
				continue
			}
			add(llm.ItemStart{Kind: llm.ItemToolCall, CallID: it.CallID, Name: it.Name}, llm.EventToolInputDelta, it.Arguments)
		default:
			out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
		}
	}
	f := finishOf(r)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Raw: append(json.RawMessage(nil), body...)}), nil
}
