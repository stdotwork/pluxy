package anthropicmessages

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

type obj = map[string]any

func writeEvent(w io.Writer, typ string, data []byte) error {
	var buf bytes.Buffer
	buf.Grow(len(data) + len(typ) + 16)
	buf.WriteString("event: ")
	buf.WriteString(typ)
	buf.WriteString("\ndata: ")
	if err := json.Compact(&buf, data); err != nil {
		return err
	}
	buf.WriteString("\n\n")
	_, err := w.Write(buf.Bytes())
	return err
}

// upstreamEvent returns the Messages event type of raw when raw is a Messages stream event.
func upstreamEvent(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	switch {
	case e.Type == "error", e.Type == "ping", strings.HasPrefix(e.Type, "message_"), strings.HasPrefix(e.Type, "content_block_"):
		return e.Type
	}
	return ""
}

// stopReason returns the stop reason to write: the upstream's own when it came from Messages,
// otherwise the Messages value for the model's finish reason, as today's core maps them (a
// content filter stop becomes end_turn). hasToolCalls makes a plain stop with tool calls in the
// reply tool_use.
func stopReason(f *llm.Finish, upFormat string, hasToolCalls bool) string {
	if f == nil {
		return "end_turn"
	}
	if upFormat == format && f.RawReason != "" {
		return f.RawReason
	}
	switch f.Reason {
	case llm.FinishLength:
		return "max_tokens"
	case llm.FinishToolCalls:
		return "tool_use"
	case llm.FinishStop:
		if hasToolCalls {
			return "tool_use"
		}
	}
	return "end_turn"
}

// encodeUsage writes model usage as Messages usage. Messages input_tokens leaves out tokens read
// from or written to a cache, which are counted in their own fields.
func encodeUsage(u *llm.Usage) obj {
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	out := obj{"input_tokens": int64(0), "output_tokens": int64(0)}
	if u == nil {
		return out
	}
	cached, written := v(u.CachedInputTokens), v(u.CacheWriteInputTokens)
	out["input_tokens"] = max(0, v(u.InputTokens)-cached-written)
	out["output_tokens"] = v(u.OutputTokens)
	if cached > 0 {
		out["cache_read_input_tokens"] = cached
	}
	if written > 0 {
		out["cache_creation_input_tokens"] = written
	}
	return out
}

// usageLosses lists usage fields of a reply from another format that Messages usage has no place
// for.
func usageLosses(u *llm.Usage, upFormat string) []string {
	if u == nil || upFormat == format || len(u.Raw) == 0 {
		return nil
	}
	known := map[string]bool{"prompt_tokens": true, "completion_tokens": true, "total_tokens": true, "prompt_tokens_details": true,
		"completion_tokens_details": true, "input_tokens": true, "output_tokens": true, "input_tokens_details": true, "output_tokens_details": true}
	var out []string
	for k := range rawjson.Object(u.Raw) {
		if !known[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// block is one content block being rebuilt from model events.
type outBlock struct {
	kind   string
	index  int // Messages block index
	callID string
	name   string
	text   strings.Builder
}

// builder rebuilds a Messages reply from model events whose upstream format is not Messages.
type builder struct {
	id       string
	model    string
	upFormat string
	blocks   map[int]*outBlock // model item index -> block
	order    []int
	tools    bool
	finish   *llm.Finish
	usage    *llm.Usage
	losses   []llm.Loss
}

func newBuilder() *builder { return &builder{blocks: map[int]*outBlock{}} }

func (b *builder) lose(path, detail string) {
	for _, l := range b.losses {
		if l.Path == path {
			return
		}
	}
	b.losses = append(b.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
}

func (b *builder) start(r *llm.ResponseInfo) {
	b.id, b.model, b.upFormat = r.ID, r.Model, r.Format
	if b.id == "" {
		var x [12]byte
		_, _ = rand.Read(x[:])
		b.id = "msg_" + hex.EncodeToString(x[:])
	}
	if m := rawjson.Object(r.Raw); len(m) > 0 && r.Format != format {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.lose("response."+k, "reply field with no place in a Message")
		}
	}
}

func (b *builder) open(idx int, s *llm.ItemStart) *outBlock {
	blk := &outBlock{kind: s.Kind, index: len(b.order), callID: s.CallID, name: s.Name}
	b.blocks[idx] = blk
	b.order = append(b.order, idx)
	if s.Kind == llm.ItemToolCall {
		b.tools = true
	}
	return blk
}

func (b *builder) native() {
	b.lose("choices", "reply data in "+b.upFormat+" with no place in a Message")
}

func (b *builder) finished(ev llm.Event) {
	b.finish, b.usage = ev.Finish, ev.Usage
	for _, k := range usageLosses(ev.Usage, b.upFormat) {
		b.lose("usage."+k, "usage field with no place in a Message")
	}
}

func (b *builder) stopReason() string { return stopReason(b.finish, b.upFormat, b.tools) }

// toolInput returns a tool call's arguments as the JSON object Messages wants: {} when they are
// empty or not a JSON object.
func toolInput(args string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil || m == nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

// content returns the reply's content blocks.
func (b *builder) content() []any {
	out := []any{}
	for _, idx := range b.order {
		blk := b.blocks[idx]
		switch blk.kind {
		case llm.ItemMessage:
			out = append(out, obj{"type": "text", "text": blk.text.String()})
		case llm.ItemReasoning:
			out = append(out, obj{"type": "thinking", "thinking": blk.text.String()})
		case llm.ItemToolCall:
			out = append(out, obj{"type": "tool_use", "id": blk.callID, "name": blk.name, "input": toolInput(blk.text.String())})
		}
	}
	return out
}

// StreamWriter writes events to a client as a Messages stream. When the reply came from a
// Messages upstream it writes the upstream's events as they came; otherwise it rebuilds the
// Messages event sequence from the model events.
type StreamWriter struct {
	w      io.Writer
	b      *builder
	native bool
	ended  bool // an upstream terminal event was passed through
}

// NewStreamWriter returns a writer for one client stream.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w, b: newBuilder()}
}

// Losses returns what the reply had that the client format could not carry.
func (s *StreamWriter) Losses() []llm.Loss { return s.b.losses }

func (s *StreamWriter) emit(typ string, fields obj) error {
	fields["type"] = typ
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return writeEvent(s.w, typ, data)
}

// Write writes the stream events for one model event.
func (s *StreamWriter) Write(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart {
		s.native = ev.Response.Format == format && upstreamEvent(ev.Raw) != ""
	}
	if s.native {
		raw := ev.Raw
		if ev.Type == llm.EventError {
			// An upstream error event travels as the error's raw.
			raw = ev.Error.Raw
		}
		if typ := upstreamEvent(raw); typ != "" {
			if typ == "message_stop" || typ == "error" {
				s.ended = true
			}
			return writeEvent(s.w, typ, raw)
		}
		if ev.Type == llm.EventError && !s.ended {
			return s.emit("error", errorFields(ev.Error))
		}
		return nil
	}
	b := s.b
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		return s.emit("message_start", obj{"message": obj{"id": b.id, "type": "message", "role": "assistant", "model": b.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": obj{"input_tokens": 0, "output_tokens": 0}}})
	case llm.EventItemStart:
		blk := b.open(ev.Index, ev.Item)
		var cb obj
		switch blk.kind {
		case llm.ItemMessage:
			cb = obj{"type": "text", "text": ""}
		case llm.ItemReasoning:
			cb = obj{"type": "thinking", "thinking": ""}
		default:
			cb = obj{"type": "tool_use", "id": blk.callID, "name": blk.name, "input": obj{}}
		}
		return s.emit("content_block_start", obj{"index": blk.index, "content_block": cb})
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		blk := b.blocks[ev.Index]
		blk.text.WriteString(ev.Delta)
		var d obj
		switch blk.kind {
		case llm.ItemMessage:
			d = obj{"type": "text_delta", "text": ev.Delta}
		case llm.ItemReasoning:
			d = obj{"type": "thinking_delta", "thinking": ev.Delta}
		default:
			d = obj{"type": "input_json_delta", "partial_json": ev.Delta}
		}
		return s.emit("content_block_delta", obj{"index": blk.index, "delta": d})
	case llm.EventItemEnd:
		return s.emit("content_block_stop", obj{"index": b.blocks[ev.Index].index})
	case llm.EventNative:
		b.native()
		return nil
	case llm.EventFinish:
		b.finished(ev)
		if err := s.emit("message_delta", obj{"delta": obj{"stop_reason": b.stopReason(), "stop_sequence": nil}, "usage": encodeUsage(ev.Usage)}); err != nil {
			return err
		}
		return s.emit("message_stop", obj{})
	case llm.EventError:
		return s.emit("error", errorFields(ev.Error))
	}
	return nil
}

// Aggregator collects events into a non-streamed Message. A reply from a Messages upstream is
// returned as the upstream's Message.
type Aggregator struct {
	b     *builder
	final json.RawMessage
	size  int
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{b: newBuilder()} }

// Losses returns what the reply had that the client format could not carry.
func (a *Aggregator) Losses() []llm.Loss { return a.b.losses }

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	b := a.b
	a.size += len(ev.Delta)
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
	case llm.EventItemStart:
		a.size += len(ev.Item.CallID) + len(ev.Item.Name) + blockOverhead
		b.open(ev.Index, ev.Item)
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		b.blocks[ev.Index].text.WriteString(ev.Delta)
	case llm.EventNative:
		if b.upFormat != format {
			b.native()
		}
	case llm.EventFinish:
		b.finished(ev)
		if b.upFormat == format && isMessage(ev.Raw) {
			a.final = ev.Raw
		}
	}
}

// blockOverhead is about the JSON a content block adds around its ID, name and content.
const blockOverhead = len(`{"type":"tool_use","id":"","name":"","input":{}},`)

// Size is about how many bytes of content the response holds so far: text, thinking, tool
// calls and the upstream's final Message, before JSON escaping. Callers use it to bound what
// they collect.
func (a *Aggregator) Size() int { return a.size + len(a.final) }

func isMessage(raw json.RawMessage) bool {
	var m struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &m) == nil && m.Type == "message"
}

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	if len(a.final) > 0 {
		return a.final, nil
	}
	b := a.b
	return json.Marshal(obj{"id": b.id, "type": "message", "role": "assistant", "model": b.model, "content": b.content(),
		"stop_reason": b.stopReason(), "stop_sequence": nil, "usage": encodeUsage(b.usage)})
}

// errorType returns the Anthropic error type for an HTTP status, as today's core names them.
func errorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout_error"
	case 529:
		return "overloaded_error"
	}
	if status >= 500 {
		return "api_error"
	}
	return "invalid_request_error"
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
	case llm.CodeInternal:
		return http.StatusInternalServerError
	}
	return http.StatusBadGateway
}

// errorFields are the fields of an Anthropic error body or error event.
func errorFields(e *llm.Error) obj {
	st := status(e)
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(st), st)
	}
	return obj{"type": "error", "error": obj{"type": errorType(st), "message": msg}}
}

// ErrorResponse returns the HTTP status and Anthropic error body for e.
func ErrorResponse(e *llm.Error) (int, []byte) {
	body, _ := json.Marshal(errorFields(e))
	return status(e), body
}
