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
	"github.com/stdotwork/pluxy/next/internal/jsonscan"
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

func rawType(raw json.RawMessage) string {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 || raw[0] != '{' {
		return ""
	}
	if t, ok := scanType(raw); ok {
		return t
	}
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.Type
}

func scanType(raw []byte) (string, bool) {
	var space [8]jsonscan.Member
	members, ok := jsonscan.AppendObject(space[:0], raw)
	if !ok {
		return "", false
	}
	var typ string
	found := false
	for _, m := range members {
		if string(m.Key) == `"type"` {
			if found || !jsonscan.IsString(m.Value) {
				return "", false
			}
			t, ok := jsonscan.Unquote(m.Value)
			if !ok {
				return "", false
			}
			typ, found = t, true
			continue
		}
		for _, c := range m.Key {
			if c >= 0x80 || c == '\\' {
				return "", false
			}
		}
		if bytes.EqualFold(m.Key, []byte(`"type"`)) {
			return "", false
		}
	}
	return typ, true
}

func upstreamEvent(raw json.RawMessage) string {
	if t := rawType(raw); t != "message" {
		return t
	}
	return ""
}

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
	case llm.FinishContentFilter:
		return "refusal"
	case llm.FinishStop:
		if hasToolCalls {
			return "tool_use"
		}
	}
	return "end_turn"
}

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
	if r := u.ReasoningTokens; r != nil && *r >= 0 {
		out["output_tokens_details"] = obj{"thinking_tokens": min(*r, v(u.OutputTokens))}
	}
	return out
}

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
					carried := (dk == "cached_tokens" && u.CachedInputTokens != nil || dk == "cache_write_tokens" && u.CacheWriteInputTokens != nil) && (k == "prompt_tokens_details" || k == "input_tokens_details") ||
						dk == "reasoning_tokens" && u.ReasoningTokens != nil && (k == "completion_tokens_details" || k == "output_tokens_details")
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

type builder struct {
	id       string
	model    string
	upFormat string
	tools    bool
	finish   *llm.Finish
	usage    *llm.Usage
	losses   []llm.Loss
	carried  map[string]bool
	lost     map[string]bool
	lossSize int
}

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

func signature(ev llm.Event) string {
	o := ev.Opaque
	if ev.Type != llm.EventItemEnd || o == nil {
		return ""
	}
	switch {
	case o.Format == llm.FormatOpenAIResponses && openairesponses.IsEncryptedReasoning(o.Data):
		return o.Data
	case o.Format == llm.FormatGeminiGenerate && o.Data != "":
		return o.Data
	case o.Format == llm.OpaqueDevin && o.Data != "":
		return o.Data
	}
	return ""
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

type streamBlock struct {
	kind, callID, name string
	index              int
	held               []string
	signature          string
	ended              bool
}

type StreamWriter struct {
	w      io.Writer
	b      builder
	native bool
	ended  bool

	blocks map[int]*streamBlock
	active int
	queue  []int
	next   int
}

func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w, blocks: map[int]*streamBlock{}, active: -1}
}

func (s *StreamWriter) Losses() []llm.Loss { return s.b.losses }

func (s *StreamWriter) emit(typ string, fields obj) error {
	fields["type"] = typ
	data, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return writeEvent(s.w, typ, data)
}

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

func (s *StreamWriter) stop(blk *streamBlock) error {
	if blk.signature != "" && blk.kind == llm.ItemReasoning {
		if err := s.emit("content_block_delta", obj{"index": blk.index, "delta": obj{"type": "signature_delta", "signature": blk.signature}}); err != nil {
			return err
		}
	}
	return s.emit("content_block_stop", obj{"index": blk.index})
}

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
			s.ended = true
			return s.emit("message_stop", obj{})
		}
		return nil
	}
	b := &s.b
	switch ev.Type {
	case llm.EventResponseStart:
		b.start(ev.Response)
		b.upstreamData(ev)
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
			blk.held = append(blk.held, ev.Delta)
			return nil
		}
		return s.emit("content_block_delta", obj{"index": blk.index, "delta": blockDelta(blk.kind, ev.Delta)})
	case llm.EventItemEnd:
		blk := s.blocks[ev.Index]
		blk.signature = b.upstreamData(ev)
		if len(ev.Citations) > 0 {
			b.lose("content.citations", "web search citations have no safe representation in a Message without an Anthropic encrypted_index")
		}
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
	case llm.EventMedia:
		if ev.Media == nil {
			return nil
		}
		b.lose("media."+ev.Media.Kind, "Messages has no output image or audio block")
		return nil
	case llm.EventNative:
		b.native()
		return nil
	case llm.EventFinish:
		b.upstreamData(ev)
		b.finished(ev)
		if len(ev.Sources) > 0 {
			b.lose("sources", "web search sources have no safe representation in a Message without Anthropic encrypted_content")
		}
		if len(ev.CodeExecutions) > 0 {
			b.lose("content.code_execution", "code execution results have no safe representation in a Message without an Anthropic srvtoolu_ ID")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					b.lose("content.code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in a Message")
					break
				}
			}
		}
		if err := s.emit("message_delta", obj{"delta": obj{"stop_reason": b.stopReason(), "stop_sequence": nil}, "usage": encodeUsage(ev.Usage)}); err != nil {
			return err
		}
		return s.emit("message_stop", obj{})
	case llm.EventError:
		return s.emit("error", errorFields(ev.Error))
	}
	return nil
}

type aggBlock struct {
	kind, callID, name string
	text               strings.Builder
	signature          string
}

type Aggregator struct {
	b      builder
	blocks map[int]*aggBlock
	order  []int
	final  json.RawMessage
	acc    accumulator
	size   int
}

func NewAggregator() *Aggregator { return &Aggregator{blocks: map[int]*aggBlock{}} }

func (a *Aggregator) Losses() []llm.Loss { return append(a.b.losses, a.acc.losses...) }

func (a *Aggregator) Add(ev llm.Event) {
	b := &a.b
	a.size += len(ev.Delta)
	if ev.Type == llm.EventResponseStart {
		b.start(ev.Response)
		a.size += len(b.id) + len(b.model)
		b.upstreamData(ev)
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
		if b.upFormat != format && len(ev.Citations) > 0 {
			b.lose("content.citations", "web search citations have no safe representation in a Message without an Anthropic encrypted_index")
		}
		if blk := a.blocks[ev.Index]; blk != nil {
			blk.signature = sig
			a.size += len(blk.signature)
		}
	case llm.EventMedia:
		if b.upFormat != format && ev.Media != nil {
			b.lose("media."+ev.Media.Kind, "Messages has no output image or audio block")
		}
	case llm.EventNative:
		if b.upFormat != format {
			b.native()
		}
	case llm.EventFinish:
		b.upstreamData(ev)
		b.finished(ev)
		if b.upFormat != format && len(ev.Sources) > 0 {
			b.lose("sources", "web search sources have no safe representation in a Message without Anthropic encrypted_content")
		}
		if b.upFormat != format && len(ev.CodeExecutions) > 0 {
			b.lose("content.code_execution", "code execution results have no safe representation in a Message without an Anthropic srvtoolu_ ID")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					b.lose("content.code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in a Message")
					break
				}
			}
		}
		if ev.Finish != nil {
			a.size += len(ev.Finish.RawReason)
		}
		if ev.Usage != nil {
			a.size += len(ev.Usage.Raw)
		}
	}
}

const blockOverhead = len(`{"type":"tool_use","id":"","name":"","input":{}},`)

func (a *Aggregator) Size() int {
	return a.size + len(a.final) + a.b.lossSize + a.acc.lossSize
}

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
	texts   map[string]*strings.Builder
	partial strings.Builder
	inputs  bool
}

func (a *accumulator) lose(path, detail string) {
	if a.lost[path] || len(a.losses) > llm.MaxLosses {
		return
	}
	if a.lost == nil {
		a.lost = map[string]bool{}
	}
	a.lost[path] = true
	a.lossSize += len(path) + len(detail)
	a.losses = llm.AddLoss(a.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
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
	case llm.CodePermission, llm.CodeAccountClosed:
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

func errorFields(e *llm.Error) obj {
	if u := e.UsageLimit; u != nil {
		o := obj{"type": "rate_limit_error",
			"message":   fmt.Sprintf("%s (reset after %s)", u.Message(), u.ResetAfter()),
			"resets_at": u.ResetsAt.Unix(), "retryable": false}
		e.Pluxy(o)
		return obj{"type": "error", "error": o}
	}
	st := status(e)
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(st), st)
	}
	typ := errorType(st)
	if _, ok := anthropicStatus[e.ProviderCode]; ok && e.Code == llm.CodeUpstream {
		typ = e.ProviderCode
	}
	// Anthropic's own words for a prompt over the context window, which a Messages client looks for,
	// ahead of another upstream's.
	if e.ProviderCode == llm.ProviderContextLengthExceeded && !strings.Contains(strings.ToLower(msg), "prompt is too long") {
		msg = "prompt is too long: " + msg
	}
	o := obj{"type": typ, "message": msg}
	out := obj{"type": "error", "error": o}
	requestID, details := messagesErrorFields(e.Raw)
	if requestID != nil {
		out["request_id"] = requestID
	}
	if details != nil {
		o["details"] = details
	}
	e.Pluxy(o)
	return out
}

// messagesErrorFields returns the request_id and the details to forward when raw is a Messages
// error body or event: type "error" with an error object. A Codex WebSocket error message looks the
// same but has a status, and its fields are not these. Of details only a text error_code is kept,
// the one member Anthropic's SDK reads for every error type: another can identify the upstream's
// organization to whoever holds a client key.
func messagesErrorFields(raw json.RawMessage) (requestID json.RawMessage, details obj) {
	m := rawjson.Object(raw)
	if m == nil || string(m["type"]) != `"error"` || m["status"] != nil {
		return nil, nil
	}
	inner := rawjson.Object(m["error"])
	if inner == nil {
		return nil, nil
	}
	var id string
	if json.Unmarshal(m["request_id"], &id) == nil && id != "" {
		requestID = m["request_id"]
	}
	var code *string
	if d := rawjson.Object(inner["details"]); d != nil && json.Unmarshal(d["error_code"], &code) == nil && code != nil {
		details = obj{"error_code": *code}
	}
	return requestID, details
}

func ErrorResponse(e *llm.Error) (int, []byte) {
	body, _ := json.Marshal(errorFields(e))
	return status(e), body
}
