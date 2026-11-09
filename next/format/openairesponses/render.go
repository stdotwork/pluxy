package openairesponses

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// echoFields are the request fields a Response object repeats, in the order today's core writes
// them.
var echoFields = []string{"instructions", "max_output_tokens", "max_tool_calls", "parallel_tool_calls", "previous_response_id",
	"prompt_cache_key", "reasoning", "safety_identifier", "service_tier", "store", "temperature", "text", "tool_choice", "tools",
	"top_logprobs", "top_p", "truncation", "user", "metadata"}

// echo returns the fields of the client's Responses request that a Response object repeats.
func echo(req llm.Request) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if req.Native == nil || req.Native.Format != format {
		return out
	}
	m := rawjson.Object(req.Native.Body)
	for _, k := range echoFields {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

// upstreamEvent returns the Responses event type of raw when raw is a Responses stream event.
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
	if e.Type == "error" || strings.HasPrefix(e.Type, "response.") {
		return e.Type
	}
	return ""
}

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

// item is one output item being rebuilt from model events.
type item struct {
	kind   string
	id     string
	callID string
	name   string
	text   strings.Builder
	done   bool
	// opaque is a reasoning item's opaque record from another provider (an Anthropic thinking
	// signature), written as its encrypted_content so the client can replay it.
	opaque string
}

// builder rebuilds Responses output from model events whose upstream format is not Responses.
// It keeps what the stream and the final Response object both need.
type builder struct {
	req      llm.Request
	id       string
	created  int64
	items    map[int]*item
	order    []int
	messages int
	reasons  int
	finish   *llm.Finish
	usage    *llm.Usage
	losses   []llm.Loss
	lost     map[string]bool
	lossSize int
	upFormat string
}

func newBuilder(req llm.Request) *builder {
	return &builder{req: req, items: map[int]*item{}}
}

func (b *builder) start(r *llm.ResponseInfo) {
	b.id = r.ID
	if b.id == "" {
		var x [12]byte
		_, _ = rand.Read(x[:])
		b.id = "resp_" + hex.EncodeToString(x[:])
	}
	b.created = r.Created
	if b.created == 0 {
		b.created = time.Now().Unix()
	}
	b.upFormat = r.Format
	if m := rawjson.Object(r.Raw); len(m) > 0 {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.lose("response."+k, "reply field with no place in a Response")
		}
	}
}

// lose records a loss once per path. Past llm.MaxLosses the list is closed: llm.AddLoss has written
// its last entry, and nothing more is recorded. lossSize counts what the losses hold, which comes
// from upstream data, so an aggregator can bound it with the reply.
func (b *builder) lose(path, detail string) {
	if b.lost[path] || len(b.losses) > llm.MaxLosses {
		return
	}
	if b.lost == nil {
		b.lost = map[string]bool{}
	}
	b.lost[path] = true
	b.lossSize += len(path) + len(detail)
	b.losses = llm.AddLoss(b.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
}

func (b *builder) open(idx int, s *llm.ItemStart) *item {
	it := &item{kind: s.Kind, callID: s.CallID, name: s.Name}
	switch s.Kind {
	case llm.ItemMessage:
		it.id = "msg_" + b.id + "_" + strconv.Itoa(b.messages)
		b.messages++
	case llm.ItemReasoning:
		it.id = "rs_" + b.id + "_" + strconv.Itoa(b.reasons)
		b.reasons++
	case llm.ItemToolCall:
		it.id = "fc_" + s.CallID
	}
	b.items[idx] = it
	b.order = append(b.order, idx)
	return it
}

func (b *builder) incomplete() bool {
	return b.finish != nil && (b.finish.Reason == llm.FinishLength || b.finish.Reason == llm.FinishContentFilter)
}

func (b *builder) itemStatus() string {
	if b.incomplete() {
		return "incomplete"
	}
	return "completed"
}

type obj = map[string]any

func textPart(text string) obj {
	return obj{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": text}
}

// doneItem is an item as response.output_item.done and the final output list show it. final
// leaves out fields today's core writes only in the done event.
func (b *builder) doneItem(it *item, final bool) obj {
	switch it.kind {
	case llm.ItemMessage:
		return obj{"id": it.id, "type": "message", "status": b.itemStatus(), "role": "assistant", "content": []any{textPart(it.text.String())}}
	case llm.ItemReasoning:
		o := obj{"id": it.id, "type": "reasoning", "summary": []any{obj{"type": "summary_text", "text": it.text.String()}}}
		switch {
		case it.opaque != "":
			o["encrypted_content"] = it.opaque
		case !final:
			o["encrypted_content"] = ""
		}
		return o
	}
	return obj{"id": it.id, "type": "function_call", "status": b.itemStatus(), "arguments": it.text.String(), "call_id": it.callID, "name": it.name}
}

// response returns the Response object at the end of the reply.
func (b *builder) response(stream bool) obj {
	r := obj{"id": b.id, "object": "response", "created_at": b.created, "status": "completed", "background": false, "error": nil}
	for k, v := range echo(b.req) {
		r[k] = v
	}
	r["model"] = b.req.Model
	if !stream {
		r["incomplete_details"] = nil
	}
	if b.incomplete() {
		r["status"] = "incomplete"
		reason := "max_output_tokens"
		if b.finish.Reason == llm.FinishContentFilter {
			reason = "content_filter"
		}
		r["incomplete_details"] = obj{"reason": reason}
	}
	output := []any{}
	for _, idx := range b.order {
		if it := b.items[idx]; it.done {
			output = append(output, b.doneItem(it, true))
		}
	}
	r["output"] = output
	if u := b.usage; u != nil {
		v := func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		}
		total := v(u.InputTokens) + v(u.OutputTokens)
		r["usage"] = obj{
			"input_tokens":          v(u.InputTokens),
			"input_tokens_details":  obj{"cached_tokens": v(u.CachedInputTokens)},
			"output_tokens":         v(u.OutputTokens),
			"output_tokens_details": obj{"reasoning_tokens": v(u.ReasoningTokens)},
			"total_tokens":          total,
		}
		if b.upFormat != format && len(u.Raw) > 0 {
			for _, k := range unmodelledUsage(u.Raw) {
				b.lose("usage."+k, "usage field with no place in a Response")
			}
		}
	}
	return r
}

// unmodelledUsage lists Chat usage fields a Response's usage has no place for.
func unmodelledUsage(raw json.RawMessage) []string {
	m := rawjson.Object(raw)
	var out []string
	for k := range m {
		switch k {
		case "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details":
		default:
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// errorFields are the fields of the error event a Responses client gets when the reply fails
// after it started: code, message and param, as OpenAI's error event has them, and the same
// error nested under error, as today's core writes it.
func errorFields(e *llm.Error) obj {
	_, body := openaichat.ErrorResponse(e)
	var b struct {
		Error obj `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return obj{"code": b.Error["code"], "message": b.Error["message"], "param": nil, "error": b.Error}
}

// StreamWriter writes events to a client as a Responses stream. When the reply came from a
// Responses upstream it writes the upstream's events as they came; otherwise it rebuilds the
// Responses event sequence from the model events.
type StreamWriter struct {
	w io.Writer
	// send, when set, takes each event instead of w: its type and its JSON (a WebSocket
	// client gets each event as one message).
	send    func(typ string, data []byte) error
	b       *builder
	native  bool
	ended   bool  // an upstream terminal event was passed through
	seq     int   // last sequence number written
	pending []int // items whose end waits for the next event, which may say they are incomplete
	// done holds the items an upstream's response.output_item.done events finished, by
	// output_index, and those without one in order (see repairOutput).
	done      map[int]json.RawMessage
	unindexed []json.RawMessage
}

// NewStreamWriter returns a writer for one client stream of req.
func NewStreamWriter(w io.Writer, req llm.Request) *StreamWriter {
	return &StreamWriter{w: w, b: newBuilder(req)}
}

// NewMessageWriter returns a writer for one turn of a WebSocket client of req: send gets each
// event's type and JSON, one event per call.
func NewMessageWriter(send func(typ string, data []byte) error, req llm.Request) *StreamWriter {
	return &StreamWriter{send: send, b: newBuilder(req)}
}

// Losses returns what the reply had that the client format could not carry.
func (s *StreamWriter) Losses() []llm.Loss { return s.b.losses }

func (s *StreamWriter) out(typ string, data []byte) error {
	if s.send != nil {
		var buf bytes.Buffer
		if err := json.Compact(&buf, data); err != nil {
			return err
		}
		return s.send(typ, buf.Bytes())
	}
	return writeEvent(s.w, typ, data)
}

func (s *StreamWriter) emit(typ string, fields obj) error {
	s.seq++
	fields["type"] = typ
	fields["sequence_number"] = s.seq
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return s.out(typ, data)
}

// repairOutput keeps the items the upstream's response.output_item.done events finish, and gives
// a response.completed or response.failed whose response has no output (absent, null or empty)
// those items as its output, as today's core does: some upstreams send the final response with
// an empty output. Items are ordered by output_index, then those without one in the order they
// came. Every other event, and a response with output, is returned unchanged.
func (s *StreamWriter) repairOutput(typ string, raw json.RawMessage) json.RawMessage {
	switch typ {
	case "response.output_item.done":
		var ev struct {
			OutputIndex *int            `json:"output_index"`
			Item        json.RawMessage `json:"item"`
		}
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &ev) != nil || json.Unmarshal(ev.Item, &item) != nil || item.Type == "" {
			return raw
		}
		if ev.OutputIndex == nil {
			s.unindexed = append(s.unindexed, ev.Item)
			return raw
		}
		if s.done == nil {
			s.done = map[int]json.RawMessage{}
		}
		s.done[*ev.OutputIndex] = ev.Item
		return raw
	case "response.completed", "response.failed":
	default:
		return raw
	}
	if len(s.done) == 0 && len(s.unindexed) == 0 {
		return raw
	}
	var ev map[string]json.RawMessage
	var resp map[string]json.RawMessage
	if json.Unmarshal(raw, &ev) != nil || json.Unmarshal(ev["response"], &resp) != nil || resp == nil {
		return raw
	}
	if out := bytes.TrimSpace(resp["output"]); len(out) > 0 && !bytes.Equal(out, []byte("null")) {
		var items []json.RawMessage
		if json.Unmarshal(out, &items) != nil || len(items) > 0 {
			return raw
		}
	}
	output := make([]json.RawMessage, 0, len(s.done)+len(s.unindexed))
	for _, i := range slices.Sorted(maps.Keys(s.done)) {
		output = append(output, s.done[i])
	}
	output = append(output, s.unindexed...)
	var err error
	if resp["output"], err = json.Marshal(output); err != nil {
		return raw
	}
	if ev["response"], err = json.Marshal(resp); err != nil {
		return raw
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return raw
	}
	return b
}

// passthrough writes an upstream event unchanged.
func (s *StreamWriter) passthrough(typ string, raw json.RawMessage) error {
	var n struct {
		Seq *int `json:"sequence_number"`
	}
	if json.Unmarshal(raw, &n) == nil && n.Seq != nil {
		s.seq = *n.Seq
	}
	return s.out(typ, raw)
}

// Write writes the stream events for one model event.
func (s *StreamWriter) Write(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart {
		s.native = ev.Response.Format == format && upstreamEvent(ev.Raw) != ""
	}
	if s.native {
		raw := ev.Raw
		if ev.Type == llm.EventError {
			// An upstream error or response.failed event travels as the error's raw.
			raw = ev.Error.Raw
		}
		if typ := upstreamEvent(raw); typ != "" {
			switch typ {
			case "response.completed", "response.incomplete", "response.failed", "error":
				s.ended = true
			}
			return s.passthrough(typ, s.repairOutput(typ, raw))
		}
		if ev.Type == llm.EventError && !s.ended {
			return s.emit("error", errorFields(ev.Error))
		}
		return nil
	}
	if ev.Type != llm.EventItemEnd {
		if err := s.flushEnds(ev); err != nil {
			return err
		}
	}
	b := s.b
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		if err := s.emit("response.created", obj{"response": obj{"id": b.id, "object": "response", "created_at": b.created,
			"status": "in_progress", "background": false, "error": nil, "model": b.req.Model, "output": []any{}}}); err != nil {
			return err
		}
		return s.emit("response.in_progress", obj{"response": obj{"id": b.id, "object": "response", "created_at": b.created,
			"status": "in_progress", "model": b.req.Model, "output": []any{}}})
	case llm.EventItemStart:
		it := b.open(ev.Index, ev.Item)
		switch it.kind {
		case llm.ItemMessage:
			if err := s.emit("response.output_item.added", obj{"output_index": ev.Index,
				"item": obj{"id": it.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}); err != nil {
				return err
			}
			return s.emit("response.content_part.added", obj{"output_index": ev.Index, "item_id": it.id, "content_index": 0, "part": textPart("")})
		case llm.ItemReasoning:
			if err := s.emit("response.output_item.added", obj{"output_index": ev.Index,
				"item": obj{"id": it.id, "type": "reasoning", "status": "in_progress", "summary": []any{}}}); err != nil {
				return err
			}
			return s.emit("response.reasoning_summary_part.added", obj{"output_index": ev.Index, "item_id": it.id, "summary_index": 0,
				"part": obj{"type": "summary_text", "text": ""}})
		}
		return s.emit("response.output_item.added", obj{"output_index": ev.Index,
			"item": obj{"id": it.id, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": it.callID, "name": it.name}})
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		it := b.items[ev.Index]
		it.text.WriteString(ev.Delta)
		switch it.kind {
		case llm.ItemMessage:
			return s.emit("response.output_text.delta", obj{"output_index": ev.Index, "item_id": it.id, "content_index": 0, "delta": ev.Delta, "logprobs": []any{}})
		case llm.ItemReasoning:
			return s.emit("response.reasoning_summary_text.delta", obj{"output_index": ev.Index, "item_id": it.id, "summary_index": 0, "delta": ev.Delta})
		}
		return s.emit("response.function_call_arguments.delta", obj{"output_index": ev.Index, "item_id": it.id, "delta": ev.Delta})
	case llm.EventItemEnd:
		if it := b.items[ev.Index]; it != nil && ev.Opaque != nil {
			it.opaque = ev.Opaque.Data
		}
		s.pending = append(s.pending, ev.Index)
		return nil
	case llm.EventNative:
		b.lose("choices", "reply data in "+b.upFormat+" with no place in a Response")
		return nil
	case llm.EventFinish:
		b.finish, b.usage = ev.Finish, ev.Usage
		typ := "response.completed"
		if b.incomplete() {
			typ = "response.incomplete"
		}
		return s.emit(typ, obj{"response": b.response(true)})
	case llm.EventError:
		return s.emit("error", errorFields(ev.Error))
	}
	return nil
}

// flushEnds writes the done events of items that ended before ev. A finish event decides
// whether they completed.
func (s *StreamWriter) flushEnds(ev llm.Event) error {
	if len(s.pending) == 0 {
		return nil
	}
	if ev.Type == llm.EventFinish {
		s.b.finish, s.b.usage = ev.Finish, ev.Usage
	}
	ends := s.pending
	s.pending = nil
	for _, idx := range ends {
		it := s.b.items[idx]
		it.done = true
		text := it.text.String()
		switch it.kind {
		case llm.ItemMessage:
			if err := s.emit("response.output_text.done", obj{"output_index": idx, "item_id": it.id, "content_index": 0, "text": text, "logprobs": []any{}}); err != nil {
				return err
			}
			if err := s.emit("response.content_part.done", obj{"output_index": idx, "item_id": it.id, "content_index": 0, "part": textPart(text)}); err != nil {
				return err
			}
		case llm.ItemReasoning:
			if err := s.emit("response.reasoning_summary_text.done", obj{"output_index": idx, "item_id": it.id, "summary_index": 0, "text": text}); err != nil {
				return err
			}
			if err := s.emit("response.reasoning_summary_part.done", obj{"output_index": idx, "item_id": it.id, "summary_index": 0,
				"part": obj{"type": "summary_text", "text": text}}); err != nil {
				return err
			}
		case llm.ItemToolCall:
			if err := s.emit("response.function_call_arguments.done", obj{"output_index": idx, "item_id": it.id, "arguments": text}); err != nil {
				return err
			}
		}
		if err := s.emit("response.output_item.done", obj{"output_index": idx, "item": s.b.doneItem(it, false)}); err != nil {
			return err
		}
	}
	return nil
}

// Aggregator collects events into a non-streamed Response object. A reply from a Responses
// upstream is returned as the upstream's final Response object.
type Aggregator struct {
	b     *builder
	final json.RawMessage
	size  int
}

// NewAggregator returns an aggregator for a reply to req.
func NewAggregator(req llm.Request) *Aggregator {
	return &Aggregator{b: newBuilder(req)}
}

// Losses returns what the reply had that the client format could not carry.
func (a *Aggregator) Losses() []llm.Loss { return a.b.losses }

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	b := a.b
	a.size += len(ev.Delta)
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		a.size += len(b.id)
		if b.upFormat == format {
			a.final = finalResponse(ev.Raw) // a stream that opened with its terminal event
		}
	case llm.EventItemStart:
		// An item's id holds the response id or the call id.
		it := b.open(ev.Index, ev.Item)
		a.size += len(it.id) + len(ev.Item.CallID) + len(ev.Item.Name) + itemOverhead
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		b.items[ev.Index].text.WriteString(ev.Delta)
	case llm.EventItemEnd:
		b.items[ev.Index].done = true
		if ev.Opaque != nil {
			b.items[ev.Index].opaque = ev.Opaque.Data
			a.size += len(ev.Opaque.Data)
		}
	case llm.EventNative:
		if b.upFormat != format {
			b.lose("choices", "reply data in "+b.upFormat+" with no place in a Response")
		}
	case llm.EventFinish:
		b.finish, b.usage = ev.Finish, ev.Usage
		if f := finalResponse(ev.Raw); b.upFormat == format && f != nil {
			a.final = f
		}
		if ev.Finish != nil {
			a.size += len(ev.Finish.RawReason)
		}
		if ev.Usage != nil {
			a.size += len(ev.Usage.Raw)
		}
	}
}

// itemOverhead is about the JSON an output item adds around its ID, call ID, name and text.
const itemOverhead = len(`{"id":"","type":"function_call","status":"completed","call_id":"","name":"","arguments":""},`)

// Size is about how many bytes of content the response holds so far: its id, the text,
// reasoning and tool calls of its items with their ids, the upstream's final Response object, its
// finish reason and usage as the upstream sent them, and what it reports as lost, before JSON
// escaping. Callers use it to bound what they collect.
func (a *Aggregator) Size() int { return a.size + len(a.final) + a.b.lossSize }

// finalResponse returns the Response object in a terminal event's raw: the response of a
// response.completed or response.incomplete event, or a whole Response object.
func finalResponse(raw json.RawMessage) json.RawMessage {
	m := rawjson.Object(raw)
	if m == nil {
		return nil
	}
	var typ, object string
	_ = json.Unmarshal(m["type"], &typ)
	_ = json.Unmarshal(m["object"], &object)
	switch {
	case typ == "response.completed" || typ == "response.incomplete":
		return m["response"]
	case object == "response":
		return raw
	}
	return nil
}

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	if len(a.final) > 0 {
		return a.final, nil
	}
	return json.Marshal(a.b.response(false))
}
