package geminigenerate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

type obj = map[string]any

func writeData(w io.Writer, data []byte) error {
	var buf bytes.Buffer
	buf.Grow(len(data) + 8)
	buf.WriteString("data: ")
	if err := json.Compact(&buf, data); err != nil {
		return err
	}
	buf.WriteString("\n\n")
	_, err := w.Write(buf.Bytes())
	return err
}

// finishReason returns the Gemini finish reason to write: the upstream's own when it came from
// Gemini, otherwise the Gemini value for the model's reason. A tool-call stop is STOP, as Gemini
// ends a turn of function calls.
func finishReason(f *llm.Finish, upFormat string) string {
	if f == nil {
		return "STOP"
	}
	if upFormat == format && f.RawReason != "" {
		return f.RawReason
	}
	switch f.Reason {
	case llm.FinishStop, llm.FinishToolCalls:
		return "STOP"
	case llm.FinishLength:
		return "MAX_TOKENS"
	case llm.FinishContentFilter:
		return "SAFETY"
	}
	return "OTHER"
}

// encodeUsage writes model usage as usageMetadata, following Gemini's inclusion rules:
// promptTokenCount counts every input token, cached ones included, with the cached part in
// cachedContentTokenCount; candidatesTokenCount leaves out thoughtsTokenCount; totalTokenCount
// is all of them.
func encodeUsage(u *llm.Usage) obj {
	if u == nil {
		return nil
	}
	in, outTok, reasoning := val(u.InputTokens), val(u.OutputTokens), val(u.ReasoningTokens)
	m := obj{
		"promptTokenCount":     in,
		"candidatesTokenCount": max(0, outTok-reasoning),
		"totalTokenCount":      in + outTok,
	}
	if c := val(u.CachedInputTokens); c > 0 {
		m["cachedContentTokenCount"] = c
	}
	if reasoning > 0 {
		m["thoughtsTokenCount"] = reasoning
	}
	return m
}

// usageLosses lists the usage fields of a reply from another format that usageMetadata has no
// place for, nested detail fields included. Token totals, cached input and reasoning tokens are
// carried.
func usageLosses(u *llm.Usage, upFormat string) []string {
	if u == nil || upFormat == format || len(u.Raw) == 0 {
		return nil
	}
	carried := map[string]bool{"cached_tokens": true, "reasoning_tokens": true}
	var out []string
	for k, v := range rawjson.Object(u.Raw) {
		switch k {
		case "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens",
			"cache_read_input_tokens":
			continue
		case "prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details":
			if d := rawjson.Object(v); d != nil {
				for dk, dv := range d {
					if carried[dk] || string(dv) == "0" || string(dv) == "null" {
						continue
					}
					out = append(out, k+"."+dk)
				}
				continue
			}
		}
		if string(v) == "0" || string(v) == "null" {
			continue
		}
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// builder keeps what a GenerateContent reply needs from model events whose upstream format is
// not Gemini, and what the reply could not carry.
type builder struct {
	id, model string
	upFormat  string
	losses    []llm.Loss
	lost      map[string]bool
	lossSize  int
}

// lose records a loss once per path. lossSize counts what the losses hold, which comes from
// upstream data, so an aggregator can bound it with the reply.
func (b *builder) lose(path, detail string) {
	if b.lost[path] {
		return
	}
	if b.lost == nil {
		b.lost = map[string]bool{}
	}
	b.lost[path] = true
	b.lossSize += len(path) + len(detail)
	b.losses = append(b.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
}

func (b *builder) start(r *llm.ResponseInfo) {
	b.id, b.model, b.upFormat = r.ID, r.Model, r.Format
	if m := rawjson.Object(r.Raw); len(m) > 0 && r.Format != format {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.lose("response."+k, "reply field with no place in a GenerateContentResponse")
		}
	}
}

// upstreamData looks at the upstream data an event from another format carries for what a
// GenerateContentResponse cannot hold: thinking signatures from a Messages upstream, and opaque
// reasoning and annotations from a Responses upstream.
func (b *builder) upstreamData(ev llm.Event) {
	if len(ev.Raw) == 0 {
		return
	}
	switch b.upFormat {
	case llm.FormatOpenAIResponses:
		for _, p := range openairesponses.OpaqueOutput(ev.Raw) {
			b.lose(p, "reply data with no place in a GenerateContentResponse")
		}
	case llm.FormatAnthropicMessages:
		m := rawjson.Object(ev.Raw)
		var t struct {
			Type string `json:"type"`
		}
		if m == nil {
			return
		}
		if json.Unmarshal(m["delta"], &t) == nil && t.Type == "signature_delta" {
			b.lose("content.signature", "a thinking signature only a Messages upstream can check")
		}
		if json.Unmarshal(m["content_block"], &t) == nil {
			switch t.Type {
			case "", "text", "thinking", "tool_use":
			default:
				// redacted_thinking, server tool use and results: no place in a Gemini reply.
				b.lose("content."+t.Type, "a Messages content block with no place in a GenerateContentResponse")
			}
		}
	}
}

// eventLosses records what an event from another format carries that the reply cannot hold.
func (b *builder) eventLosses(ev llm.Event) {
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
	case llm.EventNative:
		if b.upFormat == llm.FormatOpenAIChat {
			// A Chat upstream's native events hold choices after the first.
			b.lose("choices", "reply data in "+b.upFormat+" with no place in a GenerateContentResponse")
		}
	case llm.EventFinish:
		for _, k := range usageLosses(ev.Usage, b.upFormat) {
			b.lose("usage."+k, "usage field with no place in usageMetadata")
		}
	}
	b.upstreamData(ev)
}

// response returns one GenerateContentResponse with the given parts and, when finish is set,
// the finish reason and usage.
func (b *builder) response(parts []any, finish *llm.Finish, usage *llm.Usage) obj {
	if parts == nil {
		parts = []any{}
	}
	cand := obj{"content": obj{"role": "model", "parts": parts}, "index": 0}
	r := obj{"candidates": []any{cand}}
	if finish != nil {
		cand["finishReason"] = finishReason(finish, b.upFormat)
		if u := encodeUsage(usage); u != nil {
			r["usageMetadata"] = u
		}
	}
	if b.model != "" {
		r["modelVersion"] = b.model
	}
	if b.id != "" {
		r["responseId"] = b.id
	}
	return r
}

// toolArgs returns a tool call's arguments as the JSON object Gemini wants: {} when they are
// empty. Arguments that are not a JSON object also become {}, and are reported.
func (b *builder) toolArgs(args, path string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil || m == nil {
		if strings.TrimSpace(args) != "" {
			b.lose(path, "tool arguments that are not a JSON object, sent as {}")
		}
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

func (b *builder) callPart(c *call) obj {
	fc := obj{"name": c.name, "args": b.toolArgs(c.args.String(), "content.tool_call."+c.id)}
	if c.id != "" {
		fc["id"] = c.id
	}
	p := obj{"functionCall": fc}
	if c.signature != nil {
		p["thoughtSignature"] = c.signature
	}
	return p
}

type call struct {
	id, name  string
	args      strings.Builder
	signature json.RawMessage
}

// newCall starts a tool call from its item.start event.
func (b *builder) newCall(ev llm.Event) *call {
	c := &call{id: ev.Item.CallID, name: ev.Item.Name}
	b.signature(c, ev)
	return c
}

// signature takes a call's thought signature from an event of a Chat upstream that is Gemini's
// OpenAI-compatible API: the call's item.start or item.end raw carries it as
// extra_content.google.thought_signature, and it goes back on the call's part.
func (b *builder) signature(c *call, ev llm.Event) {
	if c != nil && b.upFormat == llm.FormatOpenAIChat && len(ev.Raw) > 0 {
		var x struct {
			ExtraContent struct {
				Google struct {
					ThoughtSignature json.RawMessage `json:"thought_signature"`
				} `json:"google"`
			} `json:"extra_content"`
		}
		if json.Unmarshal(ev.Raw, &x) == nil && !isNull(x.ExtraContent.Google.ThoughtSignature) {
			c.signature = x.ExtraContent.Google.ThoughtSignature
		}
	}
}

// StreamWriter writes events to a client as a GenerateContent SSE stream. When the reply came
// from a Gemini upstream it writes the upstream's chunks as they came; otherwise it writes one
// chunk per text or thought delta, and the function calls whole, in the order they started, in
// the last chunk with the finish reason and usage, as Gemini sends calls whole.
type StreamWriter struct {
	w      io.Writer
	b      builder
	native bool

	calls map[int]*call // tool calls by model item index
	order []int         // tool call item indexes in start order
}

// NewStreamWriter returns a writer for one client stream.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w, calls: map[int]*call{}}
}

// Losses returns what the reply had that the client format could not carry.
func (s *StreamWriter) Losses() []llm.Loss { return s.b.losses }

func (s *StreamWriter) emit(v obj) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeData(s.w, data)
}

// errorEvent writes an error after the stream has started: an SSE event named error holding the
// Google error body, as today's core writes it. Gemini clients read the error from the data.
func (s *StreamWriter) errorEvent(e *llm.Error) error {
	_, body := ErrorResponse(e)
	var buf bytes.Buffer
	buf.WriteString("event: error\ndata: ")
	buf.Write(body)
	buf.WriteString("\n\n")
	_, err := s.w.Write(buf.Bytes())
	return err
}

// Write writes the stream chunks for one model event.
func (s *StreamWriter) Write(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart {
		s.native = ev.Response.Format == format
	}
	if s.native {
		if ev.Type == llm.EventError {
			return s.errorEvent(ev.Error)
		}
		if len(ev.Raw) > 0 {
			return writeData(s.w, ev.Raw)
		}
		return nil
	}
	b := &s.b
	b.eventLosses(ev)
	switch ev.Type {
	case llm.EventItemStart:
		if ev.Item.Kind == llm.ItemToolCall {
			s.calls[ev.Index] = b.newCall(ev)
			s.order = append(s.order, ev.Index)
		}
	case llm.EventTextDelta, llm.EventReasoningDelta:
		if ev.Delta == "" {
			return nil
		}
		p := obj{"text": ev.Delta}
		if ev.Type == llm.EventReasoningDelta {
			p["thought"] = true
		}
		return s.emit(b.response([]any{p}, nil, nil))
	case llm.EventToolInputDelta:
		if c := s.calls[ev.Index]; c != nil {
			c.args.WriteString(ev.Delta)
		}
	case llm.EventItemEnd:
		b.signature(s.calls[ev.Index], ev)
	case llm.EventFinish:
		var parts []any
		for _, idx := range s.order {
			parts = append(parts, b.callPart(s.calls[idx]))
		}
		return s.emit(b.response(parts, ev.Finish, ev.Usage))
	case llm.EventError:
		return s.errorEvent(ev.Error)
	}
	return nil
}

// aggPart is one part a non-streamed reply collects.
type aggPart struct {
	kind string
	text strings.Builder
	call *call
}

// Aggregator collects events into a non-streamed GenerateContentResponse. A reply from a Gemini
// upstream is returned as the upstream sent it.
type Aggregator struct {
	b      builder
	native bool
	raws   []json.RawMessage
	parts  map[int]*aggPart
	order  []int
	finish *llm.Finish
	usage  *llm.Usage
	size   int
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{parts: map[int]*aggPart{}} }

// Losses returns what the reply had that the client format could not carry.
func (a *Aggregator) Losses() []llm.Loss { return a.b.losses }

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	if ev.Type == llm.EventResponseStart {
		a.native = ev.Response.Format == format
	}
	if !a.native {
		a.b.eventLosses(ev)
	}
	a.size += len(ev.Delta)
	if a.native && len(ev.Raw) > 0 {
		a.size += len(ev.Raw)
		a.raws = append(a.raws, ev.Raw)
	}
	switch ev.Type {
	case llm.EventResponseStart:
		a.b.id, a.b.model, a.b.upFormat = ev.Response.ID, ev.Response.Model, ev.Response.Format
	case llm.EventItemStart:
		a.size += len(ev.Item.CallID) + len(ev.Item.Name) + partOverhead
		p := &aggPart{kind: ev.Item.Kind}
		if ev.Item.Kind == llm.ItemToolCall {
			p.call = a.b.newCall(ev)
			a.size += len(p.call.signature)
		}
		a.parts[ev.Index] = p
		a.order = append(a.order, ev.Index)
	case llm.EventTextDelta, llm.EventReasoningDelta:
		if p := a.parts[ev.Index]; p != nil {
			p.text.WriteString(ev.Delta)
		}
	case llm.EventToolInputDelta:
		if p := a.parts[ev.Index]; p != nil && p.call != nil {
			p.call.args.WriteString(ev.Delta)
		}
	case llm.EventItemEnd:
		if p := a.parts[ev.Index]; p != nil && p.call != nil {
			// A late signature replaces the call's first one.
			a.size -= len(p.call.signature)
			a.b.signature(p.call, ev)
			a.size += len(p.call.signature)
		}
	case llm.EventFinish:
		a.finish, a.usage = ev.Finish, ev.Usage
	}
}

// partOverhead is about the JSON a part adds around a call's ID, name and arguments or a text.
const partOverhead = len(`{"functionCall":{"id":"","name":"","args":{}}},`)

// Size is about how many bytes of content the response holds so far: text, thoughts, function
// calls with their thought signatures, the upstream chunks it keeps and what it reports as lost,
// before JSON escaping. Callers use it to bound what they collect.
func (a *Aggregator) Size() int { return a.size + a.b.lossSize }

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	if a.native && len(a.raws) == 1 {
		return a.raws[0], nil
	}
	// shortcut: a Gemini upstream that streamed to a non-streaming client would be rebuilt from
	// the model events, losing its raw fields; the extension asks a Gemini upstream to stream
	// only when the client streams, so this does not happen yet.
	b := &a.b
	var parts []any
	for _, idx := range a.order {
		p := a.parts[idx]
		switch p.kind {
		case llm.ItemMessage:
			parts = append(parts, obj{"text": p.text.String()})
		case llm.ItemReasoning:
			parts = append(parts, obj{"text": p.text.String(), "thought": true})
		case llm.ItemToolCall:
			parts = append(parts, b.callPart(p.call))
		}
	}
	finish := a.finish
	if finish == nil {
		finish = &llm.Finish{Reason: llm.FinishOther}
	}
	return json.Marshal(b.response(parts, finish, a.usage))
}

// googleStatusOf names the Google RPC status for an HTTP status, as Google's API errors do.
func googleStatusOf(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "ABORTED"
	case http.StatusRequestEntityTooLarge:
		return "INVALID_ARGUMENT"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	}
	if status >= 500 {
		return "INTERNAL"
	}
	return "FAILED_PRECONDITION"
}

// status returns the HTTP status for e.
func status(e *llm.Error) int {
	if e.Status >= 400 && e.Status <= 599 {
		return e.Status
	}
	switch e.Code {
	case llm.CodeInvalidRequest, llm.CodeUnsupported:
		return http.StatusBadRequest
	case llm.CodeAuth:
		return http.StatusUnauthorized
	case llm.CodePermission:
		return http.StatusForbidden
	case llm.CodeNotFound:
		return http.StatusNotFound
	case llm.CodeRateLimit, llm.CodeQuota:
		return http.StatusTooManyRequests
	case llm.CodeUnavailable, llm.CodeExtensionUnavailable:
		return http.StatusServiceUnavailable
	case llm.CodeCancelled:
		return 499
	case llm.CodeInternal:
		return http.StatusInternalServerError
	}
	return http.StatusBadGateway
}

// ErrorResponse returns the HTTP status and Google error body for e:
// {"error": {"code", "message", "status"}}. An upstream Google error keeps its status name and
// its details (retry info and the like).
func ErrorResponse(e *llm.Error) (int, []byte) {
	st := status(e)
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(st), st)
	}
	name := googleStatusOf(st)
	if _, ok := googleStatus[e.ProviderCode]; ok || (isGoogleError(e.Raw) && strings.ToUpper(e.ProviderCode) == e.ProviderCode && e.ProviderCode != "") {
		name = e.ProviderCode
	}
	body := obj{"code": st, "message": msg, "status": name}
	if isGoogleError(e.Raw) {
		var g struct {
			Error struct {
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(e.Raw, &g) == nil && !isNull(g.Error.Details) {
			body["details"] = g.Error.Details
		}
	}
	b, _ := json.Marshal(obj{"error": body})
	return st, b
}
