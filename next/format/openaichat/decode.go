package openaichat

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/stdotwork/pluxy/next/llm"
)

type chatUsage struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u chatUsage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func decodeFinish(raw string) llm.Finish {
	reason := llm.FinishOther
	switch raw {
	case "stop":
		reason = llm.FinishStop
	case "length":
		reason = llm.FinishLength
	case "tool_calls", "function_call":
		reason = llm.FinishToolCalls
	case "content_filter":
		reason = llm.FinishContentFilter
	}
	return llm.Finish{Reason: reason, RawReason: raw}
}

type streamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// StreamDecoder turns the data of an upstream Chat Completions stream into events. Only the
// first choice (index 0) is read. Use one decoder per stream.
type StreamDecoder struct {
	started   bool
	done      bool
	next      int
	text      int // open message item, or -1
	reasoning int // open reasoning item, or -1
	tools     map[int]int
	finish    *llm.Finish
	usage     *llm.Usage
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{text: -1, reasoning: -1, tools: map[int]int{}}
}

func (d *StreamDecoder) open(kind string, ev *[]llm.Event, item llm.ItemStart) int {
	item.Kind = kind
	i := d.next
	d.next++
	*ev = append(*ev, llm.Event{Type: llm.EventItemStart, Index: i, Item: &item})
	return i
}

func (d *StreamDecoder) closeItem(idx *int, ev *[]llm.Event) {
	if *idx >= 0 {
		*ev = append(*ev, llm.Event{Type: llm.EventItemEnd, Index: *idx})
		*idx = -1
	}
}

func (d *StreamDecoder) closeAll(ev *[]llm.Event) {
	d.closeItem(&d.reasoning, ev)
	d.closeItem(&d.text, ev)
	open := make([]int, 0, len(d.tools))
	for _, idx := range d.tools {
		open = append(open, idx)
	}
	slices.Sort(open)
	for _, idx := range open {
		*ev = append(*ev, llm.Event{Type: llm.EventItemEnd, Index: idx})
	}
	clear(d.tools)
}

// Decode takes the data of one SSE event and returns the events it produces. After "[DONE]" it
// returns the finish event and the decoder is done. An upstream error object becomes an
// *llm.Error.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openaichat: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
		return d.end(), nil
	}
	var c streamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream sent a malformed stream chunk", Raw: append(json.RawMessage(nil), data...)}
	}
	if len(c.Error) > 0 && string(c.Error) != "null" {
		return nil, upstreamStreamError(c.Error)
	}
	var out []llm.Event
	if !d.started {
		d.started = true
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: c.ID, Model: c.Model, Created: c.Created}})
	}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		if r := ch.Delta.ReasoningContent; r != nil && *r != "" {
			d.closeItem(&d.text, &out)
			if d.reasoning < 0 {
				d.reasoning = d.open(llm.ItemReasoning, &out, llm.ItemStart{})
			}
			out = append(out, llm.Event{Type: llm.EventReasoningDelta, Index: d.reasoning, Delta: *r})
		}
		if t := ch.Delta.Content; t != nil && *t != "" {
			d.closeItem(&d.reasoning, &out)
			if d.text < 0 {
				d.text = d.open(llm.ItemMessage, &out, llm.ItemStart{})
			}
			out = append(out, llm.Event{Type: llm.EventTextDelta, Index: d.text, Delta: *t})
		}
		for _, tc := range ch.Delta.ToolCalls {
			idx, ok := d.tools[tc.Index]
			if !ok {
				d.closeItem(&d.reasoning, &out)
				d.closeItem(&d.text, &out)
				id := tc.ID
				if id == "" {
					id = "call_" + strconv.Itoa(tc.Index)
				}
				idx = d.open(llm.ItemToolCall, &out, llm.ItemStart{CallID: id, Name: tc.Function.Name})
				d.tools[tc.Index] = idx
			}
			if tc.Function.Arguments != "" {
				out = append(out, llm.Event{Type: llm.EventToolInputDelta, Index: idx, Delta: tc.Function.Arguments})
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			f := decodeFinish(*ch.FinishReason)
			d.finish = &f
			d.closeAll(&out)
		}
	}
	if u := decodeUsage(c.Usage); u != nil {
		d.usage = u
	}
	return out, nil
}

func (d *StreamDecoder) end() []llm.Event {
	d.done = true
	var out []llm.Event
	if !d.started {
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}})
	}
	d.closeAll(&out)
	f := llm.Finish{Reason: llm.FinishOther}
	if d.finish != nil {
		f = *d.finish
	}
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: d.usage})
}

// Close is called when the upstream stream ends. A stream that ended without "[DONE]" counts
// as finished only if the upstream already sent a finish reason; otherwise it is an error.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	if d.finish == nil {
		d.done = true
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"}
	}
	return d.end(), nil
}

func upstreamStreamError(raw json.RawMessage) *llm.Error {
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &llm.Error{Code: llm.CodeUpstream, Message: e.Message, Raw: append(json.RawMessage(nil), raw...)}
	if out.Message == "" {
		out.Message = "upstream reported an error mid-stream"
	}
	if e.Code != nil {
		out.ProviderCode = fmt.Sprint(e.Code)
	}
	return out
}

type completion struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Created int64  `json:"created"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Content          *string        `json:"content"`
			ReasoningContent *string        `json:"reasoning_content"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// DecodeResponse turns an upstream non-streamed Chat Completions response into the events a
// stream of it would have produced. Only the first choice is read.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var c completion
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream sent a malformed response", Raw: append(json.RawMessage(nil), body...)}
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: c.ID, Model: c.Model, Created: c.Created}}}
	next := 0
	item := func(start llm.ItemStart, delta string, deltaType string) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next})
		next++
	}
	finish := llm.Finish{Reason: llm.FinishOther}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		m := ch.Message
		if m.ReasoningContent != nil && *m.ReasoningContent != "" {
			item(llm.ItemStart{Kind: llm.ItemReasoning}, *m.ReasoningContent, llm.EventReasoningDelta)
		}
		if m.Content != nil && *m.Content != "" {
			item(llm.ItemStart{Kind: llm.ItemMessage}, *m.Content, llm.EventTextDelta)
		}
		for i, tc := range m.ToolCalls {
			id := tc.ID
			if id == "" {
				id = "call_" + strconv.Itoa(i)
			}
			item(llm.ItemStart{Kind: llm.ItemToolCall, CallID: id, Name: tc.Function.Name}, tc.Function.Arguments, llm.EventToolInputDelta)
		}
		finish = decodeFinish(ch.FinishReason)
	}
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &finish, Usage: decodeUsage(c.Usage)}), nil
}
