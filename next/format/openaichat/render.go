package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
)

type outToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type outDelta struct {
	Role             string        `json:"role,omitempty"`
	Content          *string       `json:"content,omitempty"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type outChoice struct {
	Index        int       `json:"index"`
	Delta        *outDelta `json:"delta,omitempty"`
	Message      *outMsg   `json:"message,omitempty"`
	FinishReason *string   `json:"finish_reason"`
}

type outMsg struct {
	Role             string        `json:"role"`
	Content          *string       `json:"content"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type outUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type outCompletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices any    `json:"choices"`
	Usage   any    `json:"usage,omitempty"`
}

// upstream is what a reply kept from a Chat Completions upstream that the model has no place
// for. It is written back only when the reply came from that format.
type upstream struct {
	native bool
	fields []byte // reply-level fields as `"k":v,...`, without braces
}

func newUpstream(r *llm.ResponseInfo) upstream {
	u := upstream{native: r.Format == llm.FormatOpenAIChat}
	if !u.native || len(r.Raw) == 0 {
		return u
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(r.Raw, &m) != nil {
		return u
	}
	for _, k := range modelledFields {
		delete(m, k)
	}
	if len(m) == 0 {
		return u
	}
	if b, err := json.Marshal(m); err == nil {
		u.fields = b[1 : len(b)-1]
	}
	return u
}

// withFields adds the kept reply-level fields to an encoded response or chunk object.
func (u upstream) withFields(obj []byte) []byte {
	if len(u.fields) == 0 {
		return obj
	}
	out := append(obj[:len(obj)-1:len(obj)-1], ',')
	out = append(out, u.fields...)
	return append(out, '}')
}

// usage returns the usage object to write: the upstream's own when it came from this format, so
// fields the model does not carry (such as a vendor cost) survive.
func (u upstream) usage(us *llm.Usage) any {
	if us == nil {
		return nil
	}
	if u.native && len(us.Raw) > 0 {
		return us.Raw
	}
	return encodeUsage(us)
}

// choices returns the choices kept in a native event.
func (u upstream) choices(ev llm.Event) []json.RawMessage {
	if !u.native {
		return nil
	}
	var n struct {
		Choices []json.RawMessage `json:"choices"`
	}
	_ = json.Unmarshal(ev.Raw, &n)
	return n.Choices
}

func encodeUsage(u *llm.Usage) *outUsage {
	if u == nil {
		return nil
	}
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	out := &outUsage{PromptTokens: v(u.InputTokens), CompletionTokens: v(u.OutputTokens)}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	if u.CachedInputTokens != nil {
		out.PromptTokensDetails = &struct {
			CachedTokens int64 `json:"cached_tokens"`
		}{*u.CachedInputTokens}
	}
	if u.ReasoningTokens != nil {
		out.CompletionTokensDetails = &struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		}{*u.ReasoningTokens}
	}
	return out
}

func finishReason(f *llm.Finish) string {
	if f == nil {
		return "stop"
	}
	switch f.Reason {
	case llm.FinishStop, llm.FinishLength, llm.FinishToolCalls, llm.FinishContentFilter:
		return f.Reason
	}
	if f.RawReason != "" {
		return f.RawReason
	}
	return "stop"
}

// created returns the response's creation time, or now when the upstream gave none; clients
// expect the field to be set.
func created(r *llm.ResponseInfo) int64 {
	if r.Created != 0 {
		return r.Created
	}
	return time.Now().Unix()
}

// WantsStreamUsage reports whether a Chat Completions request asked for a usage chunk at the end
// of its stream (stream_options.include_usage).
func WantsStreamUsage(req llm.Request) bool {
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIChat {
		return false
	}
	var in chatRequest
	return json.Unmarshal(req.Native.Body, &in) == nil && in.StreamOptions != nil && in.StreamOptions.IncludeUsage
}

// StreamWriter writes events to a client as a Chat Completions stream.
type StreamWriter struct {
	w            io.Writer
	includeUsage bool
	head         outCompletion
	up           upstream
	tools        map[int]int // item index -> tool_calls index
}

// NewStreamWriter returns a writer for one client stream. includeUsage adds the final usage
// chunk that stream_options.include_usage asks for.
func NewStreamWriter(w io.Writer, includeUsage bool) *StreamWriter {
	return &StreamWriter{w: w, includeUsage: includeUsage, tools: map[int]int{}, head: outCompletion{Object: "chat.completion.chunk"}}
}

func (s *StreamWriter) chunk(choices any, usage any) error {
	c := s.head
	c.Choices = choices
	c.Usage = usage
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return sse.WriteData(s.w, s.up.withFields(b))
}

func (s *StreamWriter) delta(d outDelta) error {
	return s.chunk([]outChoice{{Delta: &d}}, nil)
}

// Write writes the chunks for one event. After a finish event it writes "[DONE]"; after an
// error event it writes the error object and no "[DONE]", so the client can tell the reply is
// incomplete.
func (s *StreamWriter) Write(ev llm.Event) error {
	switch ev.Type {
	case llm.EventResponseStart:
		s.head.ID, s.head.Model, s.head.Created = ev.Response.ID, ev.Response.Model, created(ev.Response)
		s.up = newUpstream(ev.Response)
		empty := ""
		return s.delta(outDelta{Role: llm.RoleAssistant, Content: &empty})
	case llm.EventItemStart:
		if ev.Item.Kind != llm.ItemToolCall {
			return nil
		}
		n := len(s.tools)
		s.tools[ev.Index] = n
		tc := outToolCall{Index: &n, ID: ev.Item.CallID, Type: "function"}
		tc.Function.Name = ev.Item.Name
		return s.delta(outDelta{ToolCalls: []outToolCall{tc}})
	case llm.EventTextDelta:
		d := ev.Delta
		return s.delta(outDelta{Content: &d})
	case llm.EventReasoningDelta:
		d := ev.Delta
		return s.delta(outDelta{ReasoningContent: &d})
	case llm.EventToolInputDelta:
		n := s.tools[ev.Index]
		tc := outToolCall{Index: &n}
		tc.Function.Arguments = ev.Delta
		return s.delta(outDelta{ToolCalls: []outToolCall{tc}})
	case llm.EventItemEnd:
		return nil
	case llm.EventNative:
		if c := s.up.choices(ev); len(c) > 0 {
			return s.chunk(c, nil)
		}
		return nil
	case llm.EventFinish:
		reason := finishReason(ev.Finish)
		if err := s.chunk([]outChoice{{Delta: &outDelta{}, FinishReason: &reason}}, nil); err != nil {
			return err
		}
		if s.includeUsage && ev.Usage != nil {
			if err := s.chunk([]outChoice{}, s.up.usage(ev.Usage)); err != nil {
				return err
			}
		}
		_, err := io.WriteString(s.w, "data: [DONE]\n\n")
		return err
	case llm.EventError:
		_, body := ErrorResponse(ev.Error)
		return sse.WriteData(s.w, body)
	}
	return nil
}

// Aggregator collects events into a non-streamed Chat Completions response.
type Aggregator struct {
	head      outCompletion
	up        upstream
	others    []*otherChoice          // choices after the first, in the order they came
	streamed  map[string]*otherChoice // the ones put together from stream fragments, by index
	text      strings.Builder
	reasoning strings.Builder
	tools     []outToolCall
	toolArgs  []*strings.Builder // the arguments of each tool call, as they streamed, by position in tools
	toolIdx   map[int]int
	finish    *llm.Finish
	usage     *llm.Usage
	size      int
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{head: outCompletion{Object: "chat.completion"}, toolIdx: map[int]int{}, streamed: map[string]*otherChoice{}}
}

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	a.size += len(ev.Delta)
	switch ev.Type {
	case llm.EventResponseStart:
		a.head.ID, a.head.Model, a.head.Created = ev.Response.ID, ev.Response.Model, created(ev.Response)
		a.up = newUpstream(ev.Response)
		a.size += len(a.head.ID) + len(a.head.Model) + len(a.up.fields)
	case llm.EventNative:
		for _, c := range a.up.choices(ev) {
			a.size += len(c)
			a.addOther(c)
		}
	case llm.EventItemStart:
		if ev.Item.Kind == llm.ItemToolCall {
			a.size += len(ev.Item.CallID) + len(ev.Item.Name) + toolCallOverhead
			a.toolIdx[ev.Index] = len(a.tools)
			tc := outToolCall{ID: ev.Item.CallID, Type: "function"}
			tc.Function.Name = ev.Item.Name
			a.tools = append(a.tools, tc)
			a.toolArgs = append(a.toolArgs, new(strings.Builder))
		}
	case llm.EventTextDelta:
		a.text.WriteString(ev.Delta)
	case llm.EventReasoningDelta:
		a.reasoning.WriteString(ev.Delta)
	case llm.EventToolInputDelta:
		// Input for an index no tool call opened has no call to join.
		if i, ok := a.toolIdx[ev.Index]; ok {
			a.toolArgs[i].WriteString(ev.Delta)
		}
	case llm.EventFinish:
		a.finish, a.usage = ev.Finish, ev.Usage
		if ev.Finish != nil {
			a.size += len(ev.Finish.RawReason)
		}
		if ev.Usage != nil {
			a.size += len(ev.Usage.Raw)
		}
	}
}

// otherChoice is a choice after the first: whole as a non-streamed upstream sent it (raw), or
// put together from the fragments a streaming upstream sent (snap).
type otherChoice struct {
	raw  json.RawMessage
	snap map[string]any
}

// addOther records a choice after the first. A stream fragment (a choice with a delta, or without
// a message once its index has streamed) is merged into the choice with its index the way
// OpenAI's Python SDK puts a streamed completion together: the delta into the message, logprobs
// lists appended, other choice fields replaced.
func (a *Aggregator) addOther(c json.RawMessage) {
	var ch map[string]any
	d := json.NewDecoder(bytes.NewReader(c))
	d.UseNumber()
	err := d.Decode(&ch)
	idx := fmt.Sprint(ch["index"])
	o := a.streamed[idx]
	// A fragment may come without a delta, such as one that only finishes its choice.
	_, frag := ch["delta"]
	if err != nil || ch["message"] != nil || !frag && o == nil {
		a.others = append(a.others, &otherChoice{raw: c})
		return
	}
	if o == nil {
		o = &otherChoice{snap: map[string]any{"message": map[string]any{}, "finish_reason": nil}}
		a.streamed[idx] = o
		a.others = append(a.others, o)
	}
	for k, v := range ch {
		switch {
		case v == nil:
		case k == "delta":
			if delta, ok := v.(map[string]any); ok {
				mergeDelta(o.snap["message"].(map[string]any), delta)
			}
		case k == "logprobs":
			if m, ok := v.(map[string]any); ok {
				prev, _ := o.snap[k].(map[string]any)
				if prev == nil {
					prev = map[string]any{}
				}
				o.snap[k] = mergeDelta(prev, m)
			}
		default:
			o.snap[k] = v
		}
	}
}

// value returns the choice as the response holds it. A message put together from fragments
// gets the role and content a whole message has, and its tool calls lose their stream index.
func (o *otherChoice) value() any {
	if o.snap == nil {
		return o.raw
	}
	plain(o.snap)
	msg := o.snap["message"].(map[string]any)
	if msg["role"] == nil {
		msg["role"] = llm.RoleAssistant
	}
	calls, _ := msg["tool_calls"].([]any)
	for _, c := range calls {
		if m, ok := c.(map[string]any); ok {
			delete(m, "index")
		}
	}
	if msg["content"] == nil && len(calls) == 0 {
		msg["content"] = ""
	}
	return o.snap
}

// keepFirst are delta fields that name something rather than stream it: an upstream that repeats
// them in later fragments repeats the same value. extra_content (a Gemini thought signature) is
// replaced whole by a later one, as on the first choice.
var (
	keepFirst = map[string]bool{"index": true, "type": true, "id": true, "role": true, "name": true}
	replaced  = map[string]bool{"extra_content": true}
)

// mergeDelta merges a stream delta into acc and returns acc: strings are joined, objects merged,
// list entries with an index merged into the entry with the same index, other list entries
// appended, and any other value replaced. Unlike the SDK, numbers are replaced rather than
// added, and the keepFirst fields are not joined.
func mergeDelta(acc, delta map[string]any) map[string]any {
	for k, dv := range delta {
		av := acc[k]
		switch {
		case dv == nil:
		case av == nil || replaced[k]:
			if l, ok := dv.([]any); ok {
				dv = new(list).merge(l)
			}
			acc[k] = dv
		case keepFirst[k]:
			if av == "" {
				acc[k] = dv
			}
		default:
			switch a := av.(type) {
			case string:
				if d, ok := dv.(string); ok {
					b := new(strings.Builder)
					b.WriteString(a)
					b.WriteString(d)
					acc[k] = b
				}
			case *strings.Builder:
				if d, ok := dv.(string); ok {
					a.WriteString(d)
				}
			case map[string]any:
				if d, ok := dv.(map[string]any); ok {
					acc[k] = mergeDelta(a, d)
				}
			case []any:
				if d, ok := dv.([]any); ok {
					acc[k] = new(list).merge(a).merge(d)
				}
			case *list:
				if d, ok := dv.([]any); ok {
					a.merge(d)
				}
			default:
				acc[k] = dv
			}
		}
	}
	return acc
}

// list is a list being put together from stream fragments. It finds its entries with an index
// by that index, so merging takes work in proportion to the fragments.
type list struct {
	entries []any
	byIndex map[string]map[string]any
}

// merge merges delta's entries into l and returns l: an entry with an index into the entry with
// the same index, any other entry appended.
func (l *list) merge(delta []any) *list {
	for _, e := range delta {
		m, ok := e.(map[string]any)
		if ok && m["index"] != nil {
			k := indexKey(m["index"])
			if prev := l.byIndex[k]; prev != nil {
				mergeDelta(prev, m)
				continue
			}
			if l.byIndex == nil {
				l.byIndex = map[string]map[string]any{}
			}
			l.byIndex[k] = m
		}
		l.entries = append(l.entries, e)
	}
	return l
}

// indexKey is an index as text, so the number 1 and the string "1" name the same entry.
func indexKey(idx any) string {
	switch v := idx.(type) {
	case json.Number:
		return string(v)
	case string:
		return v
	}
	return fmt.Sprint(idx)
}

// plain turns the lists in v, a value put together from fragments, back into JSON arrays, in
// place, and returns v.
func plain(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = plain(e)
		}
	case *list:
		for i, e := range x.entries {
			x.entries[i] = plain(e)
		}
		return x.entries
	case *strings.Builder:
		return x.String()
	case []any:
		for i, e := range x {
			x[i] = plain(e)
		}
	}
	return v
}

// toolCallOverhead is the JSON a tool call adds around its ID, name and arguments.
const toolCallOverhead = len(`{"id":"","type":"function","function":{"name":"","arguments":""}},`)

// Size is about how many bytes of content the response holds so far: text, reasoning, tool
// calls, extra choices and what it keeps of the upstream's reply (id, model, reply-level fields,
// finish reason and usage), before JSON escaping. Callers use it to bound what they collect.
func (a *Aggregator) Size() int { return a.size }

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	for i := range a.tools {
		a.tools[i].Function.Arguments = a.toolArgs[i].String()
	}
	msg := &outMsg{Role: llm.RoleAssistant, ToolCalls: a.tools}
	if a.text.Len() > 0 || len(a.tools) == 0 {
		t := a.text.String()
		msg.Content = &t
	}
	if a.reasoning.Len() > 0 {
		r := a.reasoning.String()
		msg.ReasoningContent = &r
	}
	reason := finishReason(a.finish)
	c := a.head
	choices := []any{outChoice{Message: msg, FinishReason: &reason}}
	for _, o := range a.others {
		choices = append(choices, o.value())
	}
	c.Choices = choices
	c.Usage = a.up.usage(a.usage)
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return a.up.withFields(b), nil
}

// ErrorResponse returns the HTTP status and OpenAI-style error body for e.
func ErrorResponse(e *llm.Error) (int, []byte) {
	status := e.Status
	if status < 400 || status > 599 {
		status = map[string]int{
			llm.CodeInvalidRequest:       http.StatusBadRequest,
			llm.CodeUnsupported:          http.StatusBadRequest,
			llm.CodeAuth:                 http.StatusUnauthorized,
			llm.CodePermission:           http.StatusForbidden,
			llm.CodeNotFound:             http.StatusNotFound,
			llm.CodeRateLimit:            http.StatusTooManyRequests,
			llm.CodeQuota:                http.StatusTooManyRequests,
			llm.CodeUnavailable:          http.StatusServiceUnavailable,
			llm.CodeExtensionUnavailable: http.StatusServiceUnavailable,
		}[e.Code]
		if status == 0 {
			status = http.StatusBadGateway
		}
		if e.Code == llm.CodeInternal {
			status = http.StatusInternalServerError
		}
	}
	typ := "server_error"
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status < 500:
		typ = "invalid_request_error"
	}
	code := e.ProviderCode
	if code == "" {
		code = e.Code
	}
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(status), status)
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": typ, "code": code}})
	return status, body
}
