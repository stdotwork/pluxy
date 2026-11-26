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
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
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
	// citations are the web search citations attached to a message item's text.
	citations []llm.Citation
	// out is the item's output_index: model items and web searches share one count.
	out int
	// A web search (websearch.go): its query, the input it is read from, its status once done
	// and its results.
	query      string
	input      strings.Builder
	streamed   bool
	status     string
	results    json.RawMessage
	hasResults bool
	// ref is the client's tool when the call is to a function that stands for a custom, namespaced
	// or tool_search tool (clienttools), and decoder reads a custom tool's text as it arrives.
	ref     *clienttools.Ref
	decoder clienttools.InputDecoder
	// finalInput is a finished custom tool call's text, read once from its arguments, and
	// inputWhole whether they were one whole input string.
	finalInput string
	inputWhole bool
	inputRead  bool
}

// builder rebuilds Responses output from model events whose upstream format is not Responses.
// It keeps what the stream and the final Response object both need.
type builder struct {
	req      llm.Request
	id       string
	created  int64
	items    map[int]*item
	order    []*item // every output item, in output order
	nextOut  int
	search   searches
	messages int
	reasons  int
	finish   *llm.Finish
	usage    *llm.Usage
	losses   []llm.Loss
	lost     map[string]bool
	lossSize int
	upFormat string
	// refs are the request's tools that went upstream as functions standing for something else.
	refs     map[string]clienttools.Ref
	refsRead bool
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
func (b *builder) lose(path, detail string) { b.note(llm.LossDropped, path, detail) }

// note records a loss of the given kind, once per path, as lose says.
func (b *builder) note(kind, path, detail string) {
	if b.lost[path] || len(b.losses) > llm.MaxLosses {
		return
	}
	if b.lost == nil {
		b.lost = map[string]bool{}
	}
	b.lost[path] = true
	b.lossSize += len(path) + len(detail)
	b.losses = llm.AddLoss(b.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

func (b *builder) open(idx int, s *llm.ItemStart) *item {
	it := &item{kind: s.Kind, callID: s.CallID, name: s.Name, out: b.nextOut}
	b.nextOut++
	switch s.Kind {
	case llm.ItemMessage:
		it.id = "msg_" + b.id + "_" + strconv.Itoa(b.messages)
		b.messages++
	case llm.ItemReasoning:
		it.id = "rs_" + b.id + "_" + strconv.Itoa(b.reasons)
		b.reasons++
	case llm.ItemToolCall:
		it.id = "fc_" + s.CallID
		if it.ref = b.clientTool(s.Name); it.ref != nil {
			switch it.ref.Kind {
			case clienttools.KindCustom:
				it.id = "ctc_" + s.CallID
			case clienttools.KindSearch:
				it.id = "tsc_" + s.CallID
			}
		}
	}
	b.items[idx] = it
	b.order = append(b.order, it)
	return it
}

// loseMedia reports generated media a Response cannot hold: its image_generation_call item needs
// an ID only OpenAI issues, which the client would send back on its next turn, and it has no output
// audio item.
func (b *builder) loseMedia(m *llm.OutputMedia) {
	if m != nil {
		b.lose("media."+m.Kind, "a Response has no safe place for this generated media")
	}
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

func textPart(text string, annotations []any) obj {
	if annotations == nil {
		annotations = []any{}
	}
	return obj{"type": "output_text", "annotations": annotations, "logprobs": []any{}, "text": text}
}

// urlAnnotations builds Responses url_citation annotation objects from canonical citations.
// Valid rune ranges are preserved. Responses requires both indices, so a citation with nil or
// out-of-range offsets covers the whole text, which is what a citation with no narrower range means.
func urlAnnotations(citations []llm.Citation, text string) []any {
	if len(citations) == 0 {
		return []any{}
	}
	maxRunes := utf8.RuneCountInString(text)
	out := make([]any, 0, len(citations))
	for _, c := range citations {
		if !llm.ValidCitationURL(c.URL) {
			continue
		}
		ann := obj{"type": "url_citation", "url": c.URL, "title": c.Title, "start_index": 0, "end_index": maxRunes}
		if c.StartIndex != nil && c.EndIndex != nil && *c.StartIndex >= 0 && *c.EndIndex >= *c.StartIndex && *c.EndIndex <= maxRunes {
			ann["start_index"] = *c.StartIndex
			ann["end_index"] = *c.EndIndex
		}
		out = append(out, ann)
	}
	return out
}

// reportUncitedSources records a loss when the upstream returned web search sources that were
// neither cited in reply text nor carried in a Claude web_search_call item.
func (b *builder) reportUncitedSources(sources []llm.Source) {
	if len(sources) == 0 || b.upFormat == format || b.upFormat == llm.FormatAnthropicMessages {
		return
	}
	cited := map[string]bool{}
	for _, it := range b.order {
		for _, c := range it.citations {
			cited[c.URL] = true
		}
	}
	for _, s := range sources {
		if llm.ValidCitationURL(s.URL) && !cited[s.URL] {
			b.lose("sources", "uncited web search sources have no place in a Response")
			return
		}
	}
}

// doneItem is an item as response.output_item.done and the final output list show it. final
// leaves out fields today's core writes only in the done event.
func (b *builder) doneItem(it *item, final bool) obj {
	switch it.kind {
	case itemWebSearch:
		return searchItem(it, false)
	case llm.ItemMessage:
		text := it.text.String()
		return obj{"id": it.id, "type": "message", "status": b.itemStatus(), "role": "assistant", "content": []any{textPart(text, urlAnnotations(it.citations, text))}}
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
	return b.callItem(it, b.itemStatus(), true)
}

// clientTool returns the tool a call's function stands for, or nil for a plain function. A
// Responses upstream was sent the client's own tools, so its calls are left as they are.
func (b *builder) clientTool(name string) *clienttools.Ref {
	if b.upFormat == format {
		return nil
	}
	if !b.refsRead {
		b.refs, b.refsRead = clienttools.Refs(b.req), true
	}
	if ref, ok := b.refs[name]; ok {
		return &ref
	}
	return nil
}

// callItem is a tool call as an output item, in the shape the client declared the tool in. done
// is false for the added event, which has none of the call's input yet.
func (b *builder) callItem(it *item, status string, done bool) obj {
	if it.ref != nil {
		switch it.ref.Kind {
		case clienttools.KindCustom:
			input := ""
			if done {
				input, _ = b.customInput(it)
			}
			o := obj{"id": it.id, "type": "custom_tool_call", "status": status, "call_id": it.callID, "name": it.ref.Name, "input": input}
			if it.ref.Namespace != "" {
				o["namespace"] = it.ref.Namespace
			}
			return o
		case clienttools.KindSearch:
			arguments := json.RawMessage("{}")
			if done {
				var ok bool
				if arguments, ok = clienttools.SearchCallArguments(it.text.String()); !ok {
					status = "incomplete"
					b.note(llm.LossApproximated, clienttools.OutputPath(it.out, "arguments"), "tool_search arguments that are not a JSON object, sent as {}")
				}
			}
			return obj{"id": it.id, "type": "tool_search_call", "status": status, "call_id": it.callID, "execution": "client", "arguments": arguments}
		}
	}
	arguments := ""
	if done {
		arguments = it.text.String()
	}
	o := obj{"id": it.id, "type": "function_call", "status": status, "arguments": arguments, "call_id": it.callID, "name": it.name}
	if it.ref != nil {
		o["name"], o["namespace"] = it.ref.Name, it.ref.Namespace
	}
	return o
}

// customInput is a custom tool call's text, from the arguments of the function it was sent as, and
// whether they were one whole input string.
func (b *builder) customInput(it *item) (string, bool) {
	if !it.inputRead {
		it.finalInput, it.inputWhole = clienttools.CustomInput(it.text.String())
		it.inputRead = true
	}
	return it.finalInput, it.inputWhole
}

// deliverable reports whether an ended item may reach the client. A custom tool call whose
// arguments are not whole may not: Codex runs a done item whatever its status says, so a command
// the token limit cut off would run.
func (b *builder) deliverable(it *item) bool {
	if it.kind != llm.ItemToolCall || it.ref == nil || it.ref.Kind != clienttools.KindCustom {
		return true
	}
	_, whole := b.customInput(it)
	return whole
}

// undelivered settles the custom tool calls that were held back, once the reply has finished. A
// reply cut off by the token limit or a filter explains them: it stays incomplete, and each is
// reported as dropped. On any other finish the upstream sent a call it should not have, which is
// an error. The extensions report that error first (CustomCallCheck), so that core counts the
// reply as failed; this keeps an extension built without the check from showing a success.
func (b *builder) undelivered() *llm.Error {
	for _, it := range b.order {
		if it.kind != llm.ItemToolCall || it.done || it.ref == nil || it.ref.Kind != clienttools.KindCustom {
			continue
		}
		if !b.incomplete() {
			return clienttools.NotWholeError(it.ref.Name, b.usage)
		}
		b.lose(clienttools.OutputPath(it.out, ""), "a custom tool call whose arguments are not one whole input string")
	}
	return nil
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
	if b.finish != nil && b.finish.RawReason == "pause_turn" {
		// Claude paused a turn of server tool use (web search): the response is complete but
		// the turn is not, and the client continues it, as today's core marks it.
		r["end_turn"] = false
	}
	output := []any{}
	for _, it := range b.order {
		if it.done {
			output = append(output, b.doneItem(it, true))
		}
	}
	r["output"] = output
	if u := b.usage; u != nil && usageKnown(u) {
		v := func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		}
		// When any count is known, every field OpenAI's ResponseUsage requires is written, a
		// missing input or output count as 0; a usage with no counts at all is left out.
		in, outTok := v(u.InputTokens), v(u.OutputTokens)
		inDetails := obj{"cached_tokens": v(u.CachedInputTokens)}
		if w := v(u.CacheWriteInputTokens); w > 0 {
			inDetails["cache_write_tokens"] = w
		}
		r["usage"] = obj{
			"input_tokens":          in,
			"input_tokens_details":  inDetails,
			"output_tokens":         outTok,
			"output_tokens_details": obj{"reasoning_tokens": v(u.ReasoningTokens)},
			"total_tokens":          in + outTok,
		}
		if b.upFormat != format && len(u.Raw) > 0 {
			for _, k := range unmodelledUsage(u.Raw) {
				b.lose("usage."+k, "usage field with no place in a Response")
			}
		}
	}
	return r
}

// usageKnown reports whether the upstream reported any count at all: a usage with none is left
// out of a Response rather than written as an object of zeros.
func usageKnown(u *llm.Usage) bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.CachedInputTokens != nil ||
		u.CacheWriteInputTokens != nil || u.ReasoningTokens != nil
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
	return obj{"code": b.Error["code"], "message": b.Error["message"], "param": b.Error["param"], "error": b.Error}
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
	// KeepOutput, set by the caller before the first Write, passes a terminal event's output on
	// as the upstream sent it: an empty output stays empty. Today's core does so for a
	// Responses-lite WebSocket turn on a Codex account.
	KeepOutput bool
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
// a response.completed, response.incomplete or response.failed whose response has no output
// (absent, null or empty) those items as its output, as today's core does: some upstreams send
// the final response with an empty output. Items are ordered by output_index, then those without
// one in the order they came. Every other event, and a response with output, is returned
// unchanged.
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
	case "response.completed", "response.incomplete", "response.failed":
	default:
		return raw
	}
	if s.KeepOutput || len(s.done) == 0 && len(s.unindexed) == 0 {
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
		if err := s.searchesDone(b.readySearches()); err != nil {
			return err
		}
		it := b.open(ev.Index, ev.Item)
		switch it.kind {
		case llm.ItemMessage:
			if err := s.emit("response.output_item.added", obj{"output_index": it.out,
				"item": obj{"id": it.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}); err != nil {
				return err
			}
			return s.emit("response.content_part.added", obj{"output_index": it.out, "item_id": it.id, "content_index": 0, "part": textPart("", nil)})
		case llm.ItemReasoning:
			if err := s.emit("response.output_item.added", obj{"output_index": it.out,
				"item": obj{"id": it.id, "type": "reasoning", "status": "in_progress", "summary": []any{}}}); err != nil {
				return err
			}
			return s.emit("response.reasoning_summary_part.added", obj{"output_index": it.out, "item_id": it.id, "summary_index": 0,
				"part": obj{"type": "summary_text", "text": ""}})
		}
		return s.emit("response.output_item.added", obj{"output_index": it.out, "item": b.callItem(it, "in_progress", false)})
	case llm.EventTextDelta, llm.EventReasoningDelta, llm.EventToolInputDelta:
		it := b.items[ev.Index]
		it.text.WriteString(ev.Delta)
		switch it.kind {
		case llm.ItemMessage:
			return s.emit("response.output_text.delta", obj{"output_index": it.out, "item_id": it.id, "content_index": 0, "delta": ev.Delta, "logprobs": []any{}})
		case llm.ItemReasoning:
			return s.emit("response.reasoning_summary_text.delta", obj{"output_index": it.out, "item_id": it.id, "summary_index": 0, "delta": ev.Delta})
		}
		if it.ref != nil {
			switch it.ref.Kind {
			case clienttools.KindCustom:
				text := it.decoder.Push(ev.Delta)
				if text == "" {
					return nil
				}
				return s.emit("response.custom_tool_call_input.delta", obj{"output_index": it.out, "item_id": it.id, "call_id": it.callID, "delta": text})
			case clienttools.KindSearch:
				return nil // the client reads a search's arguments from its done item
			}
		}
		return s.emit("response.function_call_arguments.delta", obj{"output_index": it.out, "item_id": it.id, "delta": ev.Delta})
	case llm.EventItemEnd:
		if it := b.items[ev.Index]; it != nil {
			if ev.Opaque != nil {
				it.opaque = ev.Opaque.Data
			}
			if len(ev.Citations) > 0 {
				it.citations = ev.Citations
			}
		}
		s.pending = append(s.pending, ev.Index)
		return nil
	case llm.EventMedia:
		b.loseMedia(ev.Media)
		return nil
	case llm.EventCandidate:
		b.lose("candidates", "Responses has no place for candidates after the first")
		return nil
	case llm.EventNative:
		if b.upFormat == llm.FormatAnthropicMessages && isBlockStart(ev.Raw) {
			// A search with its results ends when the next block starts.
			if err := s.searchesDone(b.readySearches()); err != nil {
				return err
			}
		}
		if it, added, consumed := b.searchEvent(ev.Raw); consumed {
			if added {
				return s.emit("response.output_item.added", obj{"output_index": it.out, "item": searchItem(it, true)})
			}
			return nil
		}
		b.lose("choices", "reply data in "+b.upFormat+" with no place in a Response")
		return nil
	case llm.EventFinish:
		b.finish, b.usage = ev.Finish, ev.Usage
		b.reportUncitedSources(ev.Sources)
		if b.upFormat != format && len(ev.CodeExecutions) > 0 {
			b.lose("code_execution", "code execution results have no safe representation in a Response without an OpenAI container_id")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					b.lose("code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in a Response")
					break
				}
			}
		}
		if err := s.searchesDone(b.endSearches()); err != nil {
			return err
		}
		if e := b.undelivered(); e != nil {
			return s.emit("error", errorFields(e))
		}
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
		if !s.b.deliverable(it) {
			continue
		}
		it.done = true
		text := it.text.String()
		switch it.kind {
		case llm.ItemMessage:
			anns := urlAnnotations(it.citations, text)
			for i, ann := range anns {
				if err := s.emit("response.output_text.annotation.added", obj{
					"output_index":     it.out,
					"item_id":          it.id,
					"content_index":    0,
					"annotation_index": i,
					"annotation":       ann,
				}); err != nil {
					return err
				}
			}
			if err := s.emit("response.output_text.done", obj{"output_index": it.out, "item_id": it.id, "content_index": 0, "text": text, "logprobs": []any{}}); err != nil {
				return err
			}
			if err := s.emit("response.content_part.done", obj{"output_index": it.out, "item_id": it.id, "content_index": 0, "part": textPart(text, anns)}); err != nil {
				return err
			}
		case llm.ItemReasoning:
			if err := s.emit("response.reasoning_summary_text.done", obj{"output_index": it.out, "item_id": it.id, "summary_index": 0, "text": text}); err != nil {
				return err
			}
			if err := s.emit("response.reasoning_summary_part.done", obj{"output_index": it.out, "item_id": it.id, "summary_index": 0,
				"part": obj{"type": "summary_text", "text": text}}); err != nil {
				return err
			}
		case llm.ItemToolCall:
			if err := s.callInputDone(it, text); err != nil {
				return err
			}
		}
		if err := s.emit("response.output_item.done", obj{"output_index": it.out, "item": s.b.doneItem(it, false)}); err != nil {
			return err
		}
	}
	return nil
}

// callInputDone writes the event that ends a tool call's input, for the kind of tool it is.
func (s *StreamWriter) callInputDone(it *item, text string) error {
	if it.ref != nil {
		switch it.ref.Kind {
		case clienttools.KindCustom:
			input, _ := s.b.customInput(it)
			return s.emit("response.custom_tool_call_input.done", obj{"output_index": it.out, "item_id": it.id, "call_id": it.callID, "input": input})
		case clienttools.KindSearch:
			return nil
		}
	}
	return s.emit("response.function_call_arguments.done", obj{"output_index": it.out, "item_id": it.id, "arguments": text})
}

// searchesDone writes the done events of web searches that ended.
func (s *StreamWriter) searchesDone(ended []*item) error {
	for _, it := range ended {
		if err := s.emit("response.output_item.done", obj{"output_index": it.out, "item": searchItem(it, false)}); err != nil {
			return err
		}
	}
	return nil
}

// CustomCallCheck is what a provider extension runs on a translated reply: it ends the reply with
// an upstream error when a call to one of the client's custom tools is not one whole input string
// and the finish does not explain it (a token limit or a filter), so core handles the reply as it
// does any failed one. Use one per reply. A nil check passes everything.
//
// It reads candidate 0 only. A Responses client is shown no other candidate (the writers drop
// them and report the loss), so a call in one never reaches it, and failing the reply for such a
// call would fail a reply whose client got a whole one.
type CustomCallCheck struct {
	refs map[string]clienttools.Ref
	open map[int]*customCall
	list []*customCall
}

type customCall struct {
	name    string
	decoder clienttools.InputDecoder
}

// NewCustomCallCheck returns the check for a reply to req translated as tr, or nil when no call in
// it can be to a custom tool: a body passed through, a Responses upstream, no custom tool declared.
func NewCustomCallCheck(req llm.Request, tr *llm.Translation) *CustomCallCheck {
	if tr == nil || tr.Passthrough || tr.To == format {
		return nil
	}
	refs := map[string]clienttools.Ref{}
	for sent, ref := range clienttools.Refs(req) {
		if ref.Kind == clienttools.KindCustom {
			refs[sent] = ref
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return &CustomCallCheck{refs: refs, open: map[int]*customCall{}}
}

// Check reads one event of the reply, with its tool names already the client's, and returns the
// error to end the reply with in place of a finish event.
func (c *CustomCallCheck) Check(ev llm.Event) error {
	if c == nil {
		return nil
	}
	switch ev.Type {
	case llm.EventItemStart:
		if ev.Item == nil || ev.Item.Kind != llm.ItemToolCall {
			delete(c.open, ev.Index)
			return nil
		}
		ref, ok := c.refs[ev.Item.Name]
		if !ok {
			delete(c.open, ev.Index)
			return nil
		}
		call := &customCall{name: ref.Name}
		c.open[ev.Index] = call
		c.list = append(c.list, call)
	case llm.EventToolInputDelta:
		if call := c.open[ev.Index]; call != nil {
			call.decoder.Check(ev.Delta)
		}
	case llm.EventFinish:
		if ev.Finish != nil && (ev.Finish.Reason == llm.FinishLength || ev.Finish.Reason == llm.FinishContentFilter) {
			return nil // the writer leaves the calls out of the incomplete reply
		}
		for _, call := range c.list {
			if !call.decoder.Whole() {
				return clienttools.NotWholeError(call.name, ev.Usage)
			}
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
		b.items[ev.Index].done = b.deliverable(b.items[ev.Index])
		if ev.Opaque != nil {
			b.items[ev.Index].opaque = ev.Opaque.Data
			a.size += len(ev.Opaque.Data)
		}
		if len(ev.Citations) > 0 {
			b.items[ev.Index].citations = ev.Citations
			for _, c := range ev.Citations {
				a.size += len(c.URL) + len(c.Title) + 48
			}
		}
	case llm.EventMedia:
		if b.upFormat != format {
			b.loseMedia(ev.Media)
		}
	case llm.EventCandidate:
		if b.upFormat != format {
			b.lose("candidates", "Responses has no place for candidates after the first")
		}
	case llm.EventNative:
		if it, added, consumed := b.searchEvent(ev.Raw); consumed {
			if added {
				a.size += len(it.id) + itemOverhead
			}
			// The search's query input and results come in these events.
			a.size += len(ev.Raw)
		} else if b.upFormat != format {
			b.lose("choices", "reply data in "+b.upFormat+" with no place in a Response")
		}
	case llm.EventFinish:
		b.finish, b.usage = ev.Finish, ev.Usage
		b.reportUncitedSources(ev.Sources)
		if b.upFormat != format && len(ev.CodeExecutions) > 0 {
			b.lose("code_execution", "code execution results have no safe representation in a Response without an OpenAI container_id")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					b.lose("code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in a Response")
					break
				}
			}
		}
		b.endSearches()
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
// reasoning and tool calls of its items with their ids, its web searches as the upstream sent
// them, the upstream's final Response object, its
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
	if e := a.b.undelivered(); e != nil {
		return nil, e
	}
	return json.Marshal(a.b.response(false))
}
