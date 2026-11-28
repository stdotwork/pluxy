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

var echoFields = []string{"instructions", "max_output_tokens", "max_tool_calls", "parallel_tool_calls", "previous_response_id",
	"prompt_cache_key", "reasoning", "safety_identifier", "service_tier", "store", "temperature", "text", "tool_choice", "tools",
	"top_logprobs", "top_p", "truncation", "user", "metadata"}

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

type item struct {
	kind       string
	id         string
	callID     string
	name       string
	text       strings.Builder
	done       bool
	opaque     string
	citations  []llm.Citation
	out        int
	query      string
	input      strings.Builder
	streamed   bool
	status     string
	results    json.RawMessage
	hasResults bool
	ref        *clienttools.Ref
	decoder    clienttools.InputDecoder
	finalInput string
	inputWhole bool
	inputRead  bool
}

type builder struct {
	req      llm.Request
	id       string
	created  int64
	items    map[int]*item
	order    []*item
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

func (b *builder) lose(path, detail string) { b.note(llm.LossDropped, path, detail) }

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

func usageKnown(u *llm.Usage) bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.CachedInputTokens != nil ||
		u.CacheWriteInputTokens != nil || u.ReasoningTokens != nil
}

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

func errorFields(e *llm.Error) obj {
	_, body := openaichat.ErrorResponse(e)
	var b struct {
		Error obj `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return obj{"code": b.Error["code"], "message": b.Error["message"], "param": b.Error["param"], "error": b.Error}
}

type StreamWriter struct {
	w          io.Writer
	send       func(typ string, data []byte) error
	b          *builder
	native     bool
	ended      bool
	seq        int
	pending    []int
	done       map[int]json.RawMessage
	unindexed  []json.RawMessage
	KeepOutput bool
}

func NewStreamWriter(w io.Writer, req llm.Request) *StreamWriter {
	return &StreamWriter{w: w, b: newBuilder(req)}
}

func NewMessageWriter(send func(typ string, data []byte) error, req llm.Request) *StreamWriter {
	return &StreamWriter{send: send, b: newBuilder(req)}
}

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

func (s *StreamWriter) passthrough(typ string, raw json.RawMessage) error {
	var n struct {
		Seq *int `json:"sequence_number"`
	}
	if json.Unmarshal(raw, &n) == nil && n.Seq != nil {
		s.seq = *n.Seq
	}
	return s.out(typ, raw)
}

func (s *StreamWriter) Write(ev llm.Event) error {
	if ev.Type == llm.EventResponseStart {
		s.native = ev.Response.Format == format && upstreamEvent(ev.Raw) != ""
	}
	if s.native {
		raw := ev.Raw
		if ev.Type == llm.EventError {
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
				return nil
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

func (s *StreamWriter) searchesDone(ended []*item) error {
	for _, it := range ended {
		if err := s.emit("response.output_item.done", obj{"output_index": it.out, "item": searchItem(it, false)}); err != nil {
			return err
		}
	}
	return nil
}

type CustomCallCheck struct {
	refs map[string]clienttools.Ref
	open map[int]*customCall
	list []*customCall
}

type customCall struct {
	name    string
	decoder clienttools.InputDecoder
}

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
			return nil
		}
		for _, call := range c.list {
			if !call.decoder.Whole() {
				return clienttools.NotWholeError(call.name, ev.Usage)
			}
		}
	}
	return nil
}

type Aggregator struct {
	b     *builder
	final json.RawMessage
	size  int
}

func NewAggregator(req llm.Request) *Aggregator {
	return &Aggregator{b: newBuilder(req)}
}

func (a *Aggregator) Losses() []llm.Loss { return a.b.losses }

func (a *Aggregator) Add(ev llm.Event) {
	b := a.b
	a.size += len(ev.Delta)
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		a.size += len(b.id)
		if b.upFormat == format {
			a.final = finalResponse(ev.Raw)
		}
	case llm.EventItemStart:
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

const itemOverhead = len(`{"id":"","type":"function_call","status":"completed","call_id":"","name":"","arguments":""},`)

func (a *Aggregator) Size() int { return a.size + len(a.final) + a.b.lossSize }

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

func (a *Aggregator) Response() ([]byte, error) {
	if len(a.final) > 0 {
		return a.final, nil
	}
	if e := a.b.undelivered(); e != nil {
		return nil, e
	}
	return json.Marshal(a.b.response(false))
}
