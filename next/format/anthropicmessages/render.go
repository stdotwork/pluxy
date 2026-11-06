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
	"github.com/stdotwork/pluxy/next/format/openairesponses"
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

// rawType returns the type field of raw when raw is a JSON object that has one.
func rawType(raw json.RawMessage) string {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.Type
}

// upstreamEvent returns the Messages event type of raw when raw is a Messages stream event: any
// typed object other than a whole Message, so event types Anthropic adds later pass through too.
func upstreamEvent(raw json.RawMessage) string {
	if t := rawType(raw); t != "message" {
		return t
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

// usageLosses lists the usage fields of a reply from another format that Messages usage has no
// place for, nested detail fields included. Token totals and cached input tokens are carried.
func usageLosses(u *llm.Usage, upFormat string) []string {
	if u == nil || upFormat == format || len(u.Raw) == 0 {
		return nil
	}
	var out []string
	for k, v := range rawjson.Object(u.Raw) {
		switch k {
		case "prompt_tokens", "completion_tokens", "total_tokens", "input_tokens", "output_tokens":
			continue
		case "prompt_tokens_details", "input_tokens_details", "completion_tokens_details", "output_tokens_details":
			if d := rawjson.Object(v); d != nil {
				for dk, dv := range d {
					carried := dk == "cached_tokens" && (k == "prompt_tokens_details" || k == "input_tokens_details")
					if carried || string(dv) == "0" || string(dv) == "null" {
						continue
					}
					out = append(out, k+"."+dk)
				}
				continue
			}
		}
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// builder keeps what a Messages reply needs from model events whose upstream format is not
// Messages, and what the reply could not carry.
type builder struct {
	id       string
	model    string
	upFormat string
	tools    bool
	finish   *llm.Finish
	usage    *llm.Usage
	losses   []llm.Loss
	carried  map[string]bool // encrypted reasoning a thinking block's signature holds
	lost     map[string]bool
	lossSize int
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

// upstreamData looks at the upstream data an event from another format carries for what a
// Message cannot hold: annotations from a Responses upstream, and its opaque reasoning except
// what a thinking block's signature holds (signature). It returns the signature for the block
// an item.end event closes.
func (b *builder) upstreamData(ev llm.Event) string {
	sig := signature(ev)
	if sig != "" {
		if b.carried == nil {
			b.carried = map[string]bool{}
		}
		b.carried[sig] = true
	}
	if b.upFormat != llm.FormatOpenAIResponses || len(ev.Raw) == 0 {
		return sig
	}
	sigs := openairesponses.ReasoningSignatures(ev.Raw)
	for _, p := range openairesponses.OpaqueOutput(ev.Raw) {
		if s, ok := sigs[p]; ok && b.carried[s] {
			continue
		}
		b.lose(p, "reply data with no place in a Message")
	}
	return sig
}

// signature returns the signature for the thinking block an item.end event closes: a Responses
// upstream's encrypted reasoning (the event's opaque data), which a Messages client keeps in the
// block's signature and replays with it, so the reasoning carries over to the next turn (see
// openairesponses.IsEncryptedReasoning). It is "" for any other event or opaque data.
func signature(ev llm.Event) string {
	o := ev.Opaque
	if ev.Type != llm.EventItemEnd || o == nil || o.Format != llm.FormatOpenAIResponses || !openairesponses.IsEncryptedReasoning(o.Data) {
		return ""
	}
	return o.Data
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
// empty. Arguments that are not a JSON object also become {}, as in today's core, and are
// reported.
func (b *builder) toolInput(args string, path string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil || m == nil {
		if strings.TrimSpace(args) != "" {
			b.lose(path, "tool arguments that are not a JSON object, sent as {}")
		}
		return json.RawMessage("{}")
	}
	return json.RawMessage(args)
}

func contentBlockStart(kind, callID, name string) obj {
	switch kind {
	case llm.ItemMessage:
		return obj{"type": "text", "text": ""}
	case llm.ItemReasoning:
		return obj{"type": "thinking", "thinking": ""}
	}
	return obj{"type": "tool_use", "id": callID, "name": name, "input": obj{}}
}

func blockDelta(kind, delta string) obj {
	switch kind {
	case llm.ItemMessage:
		return obj{"type": "text_delta", "text": delta}
	case llm.ItemReasoning:
		return obj{"type": "thinking_delta", "thinking": delta}
	}
	return obj{"type": "input_json_delta", "partial_json": delta}
}

// streamBlock is one model item being written as a content block.
type streamBlock struct {
	kind, callID, name string
	index              int      // Messages block index, set when the block starts
	held               []string // deltas waiting while another block is open
	signature          string   // thinking signature, written before the block's stop
	ended              bool
}

// StreamWriter writes events to a client as a Messages stream. When the reply came from a
// Messages upstream it writes the upstream's events as they came; otherwise it rebuilds the
// Messages event sequence from the model events. Messages content blocks do not overlap, so
// while one block is open, items that start later (parallel tool calls) are held and written
// in order after it.
type StreamWriter struct {
	w      io.Writer
	b      builder
	native bool
	ended  bool // an upstream terminal event was passed through

	blocks map[int]*streamBlock // model item index -> block
	active int                  // model item index of the open block, or -1
	queue  []int                // model items waiting to be written, in start order
	next   int                  // next Messages block index
}

// NewStreamWriter returns a writer for one client stream.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w, blocks: map[int]*streamBlock{}, active: -1}
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

// open writes the start of item idx and its held deltas, and its stop when it has ended. It
// reports whether the block is still open.
func (s *StreamWriter) open(idx int) (bool, error) {
	blk := s.blocks[idx]
	blk.index = s.next
	s.next++
	if err := s.emit("content_block_start", obj{"index": blk.index, "content_block": contentBlockStart(blk.kind, blk.callID, blk.name)}); err != nil {
		return false, err
	}
	for _, d := range blk.held {
		if err := s.emit("content_block_delta", obj{"index": blk.index, "delta": blockDelta(blk.kind, d)}); err != nil {
			return false, err
		}
	}
	blk.held = nil
	if blk.ended {
		delete(s.blocks, idx)
		return false, s.stop(blk)
	}
	return true, nil
}

// stop writes the end of a block: its signature, when it has one, then its stop.
func (s *StreamWriter) stop(blk *streamBlock) error {
	if blk.signature != "" && blk.kind == llm.ItemReasoning {
		if err := s.emit("content_block_delta", obj{"index": blk.index, "delta": obj{"type": "signature_delta", "signature": blk.signature}}); err != nil {
			return err
		}
	}
	return s.emit("content_block_stop", obj{"index": blk.index})
}

// drain writes waiting items in order until one stays open.
func (s *StreamWriter) drain() error {
	for len(s.queue) > 0 {
		idx := s.queue[0]
		s.queue = s.queue[1:]
		open, err := s.open(idx)
		if err != nil {
			return err
		}
		if open {
			s.active = idx
			return nil
		}
	}
	return nil
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
		switch {
		case ev.Type == llm.EventError && !s.ended:
			s.ended = true
			return s.emit("error", errorFields(ev.Error))
		case ev.Type == llm.EventFinish && !s.ended:
			// A non-streamed Message rebuilt as events ends with the whole Message.
			s.ended = true
			return s.emit("message_stop", obj{})
		}
		return nil
	}
	b := &s.b
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		b.upstreamData(ev) // a stream that opened with its terminal event
		return s.emit("message_start", obj{"message": obj{"id": b.id, "type": "message", "role": "assistant", "model": b.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": obj{"input_tokens": 0, "output_tokens": 0}}})
	case llm.EventItemStart:
		s.blocks[ev.Index] = &streamBlock{kind: ev.Item.Kind, callID: ev.Item.CallID, name: ev.Item.Name}
		if ev.Item.Kind == llm.ItemToolCall {
			b.tools = true
		}
		if s.active >= 0 {
			s.queue = append(s.queue, ev.Index)
			return nil
		}
		if _, err := s.open(ev.Index); err != nil {
			return err
		}
		s.active = ev.Index
		return nil
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		blk := s.blocks[ev.Index]
		if ev.Index != s.active {
			// shortcut: held deltas are not bounded beyond the reply itself; bound them if
			// upstreams interleave long parallel blocks.
			blk.held = append(blk.held, ev.Delta)
			return nil
		}
		return s.emit("content_block_delta", obj{"index": blk.index, "delta": blockDelta(blk.kind, ev.Delta)})
	case llm.EventItemEnd:
		blk := s.blocks[ev.Index]
		blk.signature = b.upstreamData(ev)
		if ev.Index != s.active {
			blk.ended = true
			return nil
		}
		delete(s.blocks, ev.Index)
		s.active = -1
		if err := s.stop(blk); err != nil {
			return err
		}
		return s.drain()
	case llm.EventNative:
		b.native()
		return nil
	case llm.EventFinish:
		b.upstreamData(ev)
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

// aggBlock is one content block a non-streamed reply collects.
type aggBlock struct {
	kind, callID, name string
	text               strings.Builder
	signature          string
}

// Aggregator collects events into a non-streamed Message. A reply from a Messages upstream is
// returned as the upstream's Message; when that upstream streamed, the Message is put together
// from its events, as Anthropic's SDKs do.
type Aggregator struct {
	b      builder
	blocks map[int]*aggBlock
	order  []int
	final  json.RawMessage
	acc    accumulator
	size   int
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{blocks: map[int]*aggBlock{}} }

// Losses returns what the reply had that the client format could not carry.
func (a *Aggregator) Losses() []llm.Loss { return append(a.b.losses, a.acc.losses...) }

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	b := &a.b
	a.size += len(ev.Delta)
	if ev.Type == llm.EventResponseStart {
		b.start(ev.Response)
		b.upstreamData(ev) // a stream that opened with its terminal event
	}
	if b.upFormat == format {
		if ev.Type == llm.EventFinish && rawType(ev.Raw) == "message" {
			a.final = ev.Raw
		} else if upstreamEvent(ev.Raw) != "" {
			a.size += len(ev.Raw)
			a.acc.add(ev.Raw)
		}
	}
	switch ev.Type {
	case llm.EventItemStart:
		a.size += len(ev.Item.CallID) + len(ev.Item.Name) + blockOverhead
		a.blocks[ev.Index] = &aggBlock{kind: ev.Item.Kind, callID: ev.Item.CallID, name: ev.Item.Name}
		a.order = append(a.order, ev.Index)
		if ev.Item.Kind == llm.ItemToolCall {
			b.tools = true
		}
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		a.blocks[ev.Index].text.WriteString(ev.Delta)
	case llm.EventItemEnd:
		sig := b.upstreamData(ev)
		if blk := a.blocks[ev.Index]; blk != nil {
			blk.signature = sig
			a.size += len(blk.signature)
		}
	case llm.EventNative:
		if b.upFormat != format {
			b.native()
		}
	case llm.EventFinish:
		b.upstreamData(ev)
		b.finished(ev)
	}
}

// blockOverhead is about the JSON a content block adds around its ID, name and content.
const blockOverhead = len(`{"type":"tool_use","id":"","name":"","input":{}},`)

// Size is about how many bytes of content the response holds so far: text, thinking, tool
// calls, the upstream events it puts a Message together from and the upstream's final
// Message and what it reports as lost, before JSON escaping. Callers use it to bound what
// they collect.
func (a *Aggregator) Size() int {
	return a.size + len(a.final) + a.b.lossSize + a.acc.lossSize
}

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	if len(a.final) > 0 {
		return a.final, nil
	}
	if a.acc.message != nil {
		return a.acc.result()
	}
	b := &a.b
	content := []any{}
	for i, idx := range a.order {
		blk := a.blocks[idx]
		switch blk.kind {
		case llm.ItemMessage:
			content = append(content, obj{"type": "text", "text": blk.text.String()})
		case llm.ItemReasoning:
			t := obj{"type": "thinking", "thinking": blk.text.String()}
			if blk.signature != "" {
				t["signature"] = blk.signature
			}
			content = append(content, t)
		case llm.ItemToolCall:
			content = append(content, obj{"type": "tool_use", "id": blk.callID, "name": blk.name,
				"input": b.toolInput(blk.text.String(), fmt.Sprintf("content[%d].input", i))})
		}
	}
	return json.Marshal(obj{"id": b.id, "type": "message", "role": "assistant", "model": b.model, "content": content,
		"stop_reason": b.stopReason(), "stop_sequence": nil, "usage": encodeUsage(b.usage)})
}

// accumulator puts a Message together from the events of a Messages stream, keeping every field
// of the message and its blocks: signatures, citations and blocks the model has no place for.
type accumulator struct {
	message  map[string]json.RawMessage
	blocks   map[int]*accBlock
	usage    json.RawMessage
	losses   []llm.Loss
	lost     map[string]bool
	lossSize int
}

type accBlock struct {
	fields  map[string]json.RawMessage
	texts   map[string]*strings.Builder // text and thinking, appended by deltas
	partial strings.Builder             // tool input JSON, appended by deltas
	inputs  bool
}

// lose records a loss once per path. lossSize counts what the losses hold, which comes from
// upstream data, so an aggregator can bound it with the reply.
func (a *accumulator) lose(path, detail string) {
	if a.lost[path] {
		return
	}
	if a.lost == nil {
		a.lost = map[string]bool{}
	}
	a.lost[path] = true
	a.lossSize += len(path) + len(detail)
	a.losses = append(a.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
}

func (a *accumulator) add(raw json.RawMessage) {
	var ev struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		Message      json.RawMessage `json:"message"`
		ContentBlock json.RawMessage `json:"content_block"`
		Delta        json.RawMessage `json:"delta"`
		Usage        json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		a.message = rawjson.Object(ev.Message)
		if a.message == nil {
			a.message = map[string]json.RawMessage{}
		}
		a.usage = mergeUsage(a.usage, a.message["usage"])
		a.blocks = map[int]*accBlock{}
	case "content_block_start":
		if a.message == nil {
			return
		}
		f := rawjson.Object(ev.ContentBlock)
		if f == nil {
			return
		}
		blk := &accBlock{fields: f, texts: map[string]*strings.Builder{}}
		for _, k := range []string{"text", "thinking"} {
			var s string
			if json.Unmarshal(f[k], &s) == nil {
				blk.texts[k] = &strings.Builder{}
				blk.texts[k].WriteString(s)
			}
		}
		var typ string
		_ = json.Unmarshal(f["type"], &typ)
		blk.inputs = typ == "tool_use" || typ == "server_tool_use" || typ == "mcp_tool_use"
		a.blocks[ev.Index] = blk
	case "content_block_delta":
		blk := a.blocks[ev.Index]
		if blk == nil {
			return
		}
		d := rawjson.Object(ev.Delta)
		var typ, s string
		_ = json.Unmarshal(d["type"], &typ)
		appendText := func(key, from string) {
			if json.Unmarshal(d[from], &s) != nil {
				return
			}
			if blk.texts[key] == nil {
				blk.texts[key] = &strings.Builder{}
			}
			blk.texts[key].WriteString(s)
		}
		switch typ {
		case "text_delta":
			appendText("text", "text")
		case "thinking_delta":
			appendText("thinking", "thinking")
		case "input_json_delta":
			if json.Unmarshal(d["partial_json"], &s) == nil {
				blk.partial.WriteString(s)
			}
		case "signature_delta":
			blk.fields["signature"] = d["signature"]
		case "citations_delta":
			var list []json.RawMessage
			_ = json.Unmarshal(blk.fields["citations"], &list)
			list = append(list, d["citation"])
			blk.fields["citations"], _ = json.Marshal(list)
		default:
			a.lose(fmt.Sprintf("content[%d].%s", ev.Index, typ), "a stream delta the Message could not be put together from")
		}
	case "message_delta":
		if a.message == nil {
			return
		}
		for k, v := range rawjson.Object(ev.Delta) {
			a.message[k] = v
		}
		if len(ev.Usage) > 0 && string(ev.Usage) != "null" {
			a.usage = mergeUsage(a.usage, ev.Usage)
		}
	case "content_block_stop", "message_stop", "ping":
	default:
		a.lose(ev.Type, "a stream event the Message could not be put together from")
	}
}

func (a *accumulator) result() ([]byte, error) {
	indexes := make([]int, 0, len(a.blocks))
	for i := range a.blocks {
		indexes = append(indexes, i)
	}
	slices.Sort(indexes)
	content := make([]json.RawMessage, 0, len(indexes))
	for _, i := range indexes {
		blk := a.blocks[i]
		for k, t := range blk.texts {
			blk.fields[k], _ = json.Marshal(t.String())
		}
		if blk.inputs && blk.partial.Len() > 0 {
			in := json.RawMessage(blk.partial.String())
			if !json.Valid(in) {
				a.lose(fmt.Sprintf("content[%d].input", i), "tool input that is not valid JSON, sent as {}")
				in = json.RawMessage("{}")
			}
			blk.fields["input"] = in
		}
		content = append(content, rawjson.Encode(blk.fields))
	}
	a.message["content"], _ = json.Marshal(content)
	if len(a.usage) > 0 {
		a.message["usage"] = a.usage
	}
	return json.Marshal(a.message)
}

// anthropicStatus maps Anthropic's error types to their HTTP status.
var anthropicStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest,
	"authentication_error":  http.StatusUnauthorized,
	"billing_error":         http.StatusPaymentRequired,
	"permission_error":      http.StatusForbidden,
	"not_found_error":       http.StatusNotFound,
	"request_too_large":     http.StatusRequestEntityTooLarge,
	"rate_limit_error":      http.StatusTooManyRequests,
	"timeout_error":         http.StatusGatewayTimeout,
	"api_error":             http.StatusInternalServerError,
	"overloaded_error":      529,
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

// status returns the HTTP status for e. An error that has no status of its own but names an
// Anthropic error type (an error event at the start of a stream) gets that type's status.
func status(e *llm.Error) int {
	if e.Status >= 400 && e.Status <= 599 {
		return e.Status
	}
	if st, ok := anthropicStatus[e.ProviderCode]; ok && e.Code == llm.CodeUpstream {
		return st
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

// errorFields are the fields of an Anthropic error body or error event. The error keeps the
// upstream's Anthropic error type when it had one.
func errorFields(e *llm.Error) obj {
	st := status(e)
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(st), st)
	}
	typ := errorType(st)
	if _, ok := anthropicStatus[e.ProviderCode]; ok && e.Code == llm.CodeUpstream {
		typ = e.ProviderCode
	}
	return obj{"type": "error", "error": obj{"type": typ, "message": msg}}
}

// ErrorResponse returns the HTTP status and Anthropic error body for e.
func ErrorResponse(e *llm.Error) (int, []byte) {
	body, _ := json.Marshal(errorFields(e))
	return status(e), body
}
