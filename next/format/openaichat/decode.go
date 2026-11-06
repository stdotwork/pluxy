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

// maxErrorRaw limits the upstream bytes an error keeps for diagnostics.
const maxErrorRaw = 1 << 20

// malformed returns the error for upstream data that is not the JSON it should be. The data is
// kept in Raw as a JSON string when it is not JSON, so the error can still be sent to core.
func malformed(msg string, data []byte) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Raw: llm.RawJSON(data[:min(len(data), maxErrorRaw)])}
}

// replyFields returns the reply-level fields of a response or chunk that the model has no place
// for (system_fingerprint, service_tier and the like), as a JSON object, or nil when there are
// none.
func replyFields(data []byte) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	for _, k := range modelledFields {
		delete(m, k)
	}
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// modelledFields are the top-level fields of a response or chunk that the model carries.
var modelledFields = []string{"id", "object", "created", "model", "choices", "usage", "error"}

// otherChoices returns a native event holding the choices after the first, kept as the upstream
// sent them, or nil when there are none.
func otherChoices(choices []json.RawMessage) *llm.Event {
	if len(choices) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string][]json.RawMessage{"choices": choices})
	if err != nil {
		return nil
	}
	return &llm.Event{Type: llm.EventNative, Raw: raw}
}

type streamChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Created int64             `json:"created"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`
}

type streamChoice struct {
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
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

// StreamDecoder turns the data of an upstream Chat Completions stream into events. The first
// choice (index 0) becomes items; other choices are passed on in native events. Use one decoder
// per stream.
type StreamDecoder struct {
	started   bool
	done      bool
	next      int
	text      int // open message item, or -1
	reasoning int // open reasoning item, or -1
	tools     map[int]int
	extra     map[int]json.RawMessage // extra_content that came after a tool call opened, by item
	finish    *llm.Finish
	usage     *llm.Usage
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{text: -1, reasoning: -1, tools: map[int]int{}, extra: map[int]json.RawMessage{}}
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
		// extra_content sent after the call opened rides on its item.end.
		*ev = append(*ev, llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: d.extra[idx]})
	}
	clear(d.tools)
	clear(d.extra)
}

// Decode takes the data of one SSE event and returns the events it produces. After "[DONE]" it
// returns the finish event and the decoder is done; a "[DONE]" before any finish reason is an
// error, because the reply may have been cut short. An upstream error object becomes an
// *llm.Error. Errors that end the stream carry the usage the upstream reported before them.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openaichat: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
		if d.finish == nil {
			return nil, d.unfinished()
		}
		return d.end(), nil
	}
	var c streamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
	}
	if u := decodeUsage(c.Usage); u != nil {
		d.usage = u
	}
	if hasError(c.Error) {
		return nil, d.withUsage(upstreamErrorObject(c.Error, "upstream reported an error mid-stream"))
	}
	choices := make([]streamChoice, len(c.Choices))
	var others []json.RawMessage
	for i, raw := range c.Choices {
		if err := json.Unmarshal(raw, &choices[i]); err != nil {
			return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
		}
		if choices[i].Index != 0 {
			others = append(others, raw)
		}
	}
	var out []llm.Event
	if !d.started {
		d.started = true
		// Reply-level fields are read from the first chunk only; later chunks are expected to
		// repeat them.
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{
			ID: c.ID, Model: c.Model, Created: c.Created, Format: llm.FormatOpenAIChat, Raw: replyFields(data),
		}})
	}
	for _, ch := range choices {
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
				out[len(out)-1].Raw = extraContent(tc.ExtraContent)
				d.tools[tc.Index] = idx
			} else if raw := extraContent(tc.ExtraContent); raw != nil {
				d.extra[idx] = raw
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
	if ev := otherChoices(others); ev != nil {
		out = append(out, *ev)
	}
	return out, nil
}

// Usage returns the latest usage the upstream reported, or nil. A caller that fails the stream
// for its own reasons (the connection broke, say) puts it on the error.
func (d *StreamDecoder) Usage() *llm.Usage { return d.usage }

// withUsage puts the usage reported so far on an error that ends the stream.
func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.usage
	return e
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
		return nil, d.unfinished()
	}
	return d.end(), nil
}

func (d *StreamDecoder) unfinished() *llm.Error {
	d.done = true
	return d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
}

// hasError reports whether an error field holds an error.
func hasError(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// upstreamErrorObject turns an error object an upstream sent in place of a reply into an
// *llm.Error; fallback is the message when the object has none.
func upstreamErrorObject(raw json.RawMessage, fallback string) *llm.Error {
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &llm.Error{Code: llm.CodeUpstream, Message: e.Message, Raw: append(json.RawMessage(nil), raw...)}
	if out.Message == "" {
		out.Message = fallback
	}
	if e.Code != nil {
		out.ProviderCode = fmt.Sprint(e.Code)
	}
	return out
}

type completion struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Created int64             `json:"created"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`
}

type completionChoice struct {
	Index   int `json:"index"`
	Message *struct {
		Content          *string `json:"content"`
		ReasoningContent *string `json:"reasoning_content"`
		ToolCalls        []struct {
			chatToolCall
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

// extraContent returns a tool call's extra_content as the raw of its item.start event:
// {"extra_content": ...}, or nil when it has none. Gemini's OpenAI-compatible API carries a call's
// thought signature there (extra_content.google.thought_signature), and a Gemini client needs it
// back on the call.
func extraContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"extra_content": raw})
	return b
}

// DecodeResponse turns an upstream non-streamed Chat Completions response into the events a
// stream of it would have produced. The first choice becomes items; other choices are passed on
// in a native event. A body holding an error object, or without choice 0 and its message, is an
// error: it is not an answer, even with status 200.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var c completion
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, malformed("upstream sent a malformed response", body)
	}
	if hasError(c.Error) {
		return nil, upstreamErrorObject(c.Error, "upstream reported an error")
	}
	choices := make([]completionChoice, len(c.Choices))
	var others []json.RawMessage
	first := false
	for i, raw := range c.Choices {
		if err := json.Unmarshal(raw, &choices[i]); err != nil {
			return nil, malformed("upstream sent a malformed response", body)
		}
		if choices[i].Index != 0 {
			others = append(others, raw)
		} else if choices[i].Message != nil {
			first = true
		}
	}
	if !first {
		return nil, malformed("upstream sent a response without a completion", body)
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{
		ID: c.ID, Model: c.Model, Created: c.Created, Format: llm.FormatOpenAIChat, Raw: replyFields(body),
	}}}
	next := 0
	item := func(start llm.ItemStart, delta string, deltaType string, raw json.RawMessage) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start, Raw: raw})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next})
		next++
	}
	finish := llm.Finish{Reason: llm.FinishOther}
	for _, ch := range choices {
		if ch.Index != 0 || ch.Message == nil {
			continue
		}
		m := ch.Message
		if m.ReasoningContent != nil && *m.ReasoningContent != "" {
			item(llm.ItemStart{Kind: llm.ItemReasoning}, *m.ReasoningContent, llm.EventReasoningDelta, nil)
		}
		if m.Content != nil && *m.Content != "" {
			item(llm.ItemStart{Kind: llm.ItemMessage}, *m.Content, llm.EventTextDelta, nil)
		}
		for i, tc := range m.ToolCalls {
			id := tc.ID
			if id == "" {
				id = "call_" + strconv.Itoa(i)
			}
			item(llm.ItemStart{Kind: llm.ItemToolCall, CallID: id, Name: tc.Function.Name}, tc.Function.Arguments, llm.EventToolInputDelta, extraContent(tc.ExtraContent))
		}
		finish = decodeFinish(ch.FinishReason)
	}
	if ev := otherChoices(others); ev != nil {
		out = append(out, *ev)
	}
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &finish, Usage: decodeUsage(c.Usage)}), nil
}
