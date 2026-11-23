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
// is all of them. A count the upstream did not report is left out (UsageMetadata's fields are all
// optional), and the total of a partly unknown usage with it.
func encodeUsage(u *llm.Usage) obj {
	if u == nil {
		return nil
	}
	in, outTok, reasoning := val(u.InputTokens), val(u.OutputTokens), val(u.ReasoningTokens)
	m := obj{}
	known := false
	if u.InputTokens != nil {
		m["promptTokenCount"] = in
		known = true
	}
	if u.OutputTokens != nil {
		m["candidatesTokenCount"] = max(0, outTok-reasoning)
		known = true
	}
	if u.InputTokens != nil && u.OutputTokens != nil {
		m["totalTokenCount"] = in + outTok
	}
	if c := val(u.CachedInputTokens); c > 0 {
		m["cachedContentTokenCount"] = c
		known = true
	}
	if reasoning > 0 {
		m["thoughtsTokenCount"] = reasoning
		known = true
	}
	if !known {
		return nil
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
	id, model        string
	upFormat         string
	losses           []llm.Loss
	lost             map[string]bool
	lossSize         int
	sawCandidates    bool
	sawNativeChoices bool
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
				// redacted_thinking, server tool use and results: no place in a Gemini reply. A
				// Python run is also shown as code parts (codeExecParts), but the blocks themselves
				// are not carried.
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
			// A Chat upstream's native events hold choices after the first; only report them as
			// lost if no canonical candidate events carried them (for example from an extension
			// below protocol minor 34).
			b.sawNativeChoices = true
		}
	case llm.EventCandidate:
		b.sawCandidates = true
	case llm.EventFinish:
		if b.sawNativeChoices && !b.sawCandidates {
			b.lose("choices", "reply data in "+b.upFormat+" with no place in a GenerateContentResponse")
		}
		for _, k := range usageLosses(ev.Usage, b.upFormat) {
			b.lose("usage."+k, "usage field with no place in usageMetadata")
		}
	}
	b.upstreamData(ev)
}

// citedSegment pairs a message item's text and position with its canonical citations so
// buildGroundingMetadata can emit Gemini groundingChunks and groundingSupports with UTF-8 byte
// offsets.
type citedSegment struct {
	text      string
	partIndex int // candidate content part index (non-streaming)
	byteBase  int // byte offset into concatenated streamed text (streaming)
	citations []llm.Citation
}

// buildGroundingMetadata turns canonical citations and sources into Gemini's groundingMetadata
// object, converting Unicode code point (rune) ranges to UTF-8 byte offsets. Returns nil when
// there are no valid http/https sources or citations, and, as its second result, the bytes of
// cited text it wrote. Each support repeats its cited text, so one long message with many
// citations would grow by that text each time; past maxSegmentTextBytes in all the text is left
// out (the offsets stay) and b records the loss.
func buildGroundingMetadata(segments []citedSegment, sources []llm.Source, b *builder) (obj, int) {
	textBytes := 0
	chunkIdx := map[string]int{}
	var chunks []any
	addChunk := func(u, title string) (int, bool) {
		if !llm.ValidCitationURL(u) {
			return -1, false
		}
		if idx, ok := chunkIdx[u]; ok {
			return idx, true
		}
		idx := len(chunks)
		chunkIdx[u] = idx
		web := obj{"uri": u}
		if title != "" {
			web["title"] = title
		}
		chunks = append(chunks, obj{"web": web})
		return idx, true
	}
	var supports []any
	for _, seg := range segments {
		for _, c := range seg.citations {
			ci, ok := addChunk(c.URL, c.Title)
			if !ok {
				continue
			}
			if c.StartIndex == nil || c.EndIndex == nil {
				continue
			}
			sb, ok1 := llm.RuneToUTF8ByteOffset(seg.text, *c.StartIndex)
			eb, ok2 := llm.RuneToUTF8ByteOffset(seg.text, *c.EndIndex)
			if !ok1 || !ok2 || eb < sb {
				continue
			}
			gseg := obj{
				"startIndex": seg.byteBase + sb,
				"endIndex":   seg.byteBase + eb,
			}
			if textBytes+eb-sb <= maxSegmentTextBytes {
				gseg["text"] = seg.text[sb:eb]
				textBytes += eb - sb
			} else {
				b.lose("groundingMetadata.groundingSupports.segment.text", fmt.Sprintf("the cited text repeated in the supports passed %d MiB; the rest is left out and the offsets stay", maxSegmentTextBytes>>20))
			}
			if seg.partIndex > 0 {
				gseg["partIndex"] = seg.partIndex
			}
			supports = append(supports, obj{
				"segment":               gseg,
				"groundingChunkIndices": []int{ci},
			})
		}
	}
	for _, s := range sources {
		addChunk(s.URL, s.Title)
	}
	if len(chunks) == 0 {
		return nil, 0
	}
	gm := obj{"groundingChunks": chunks}
	if len(supports) > 0 {
		gm["groundingSupports"] = supports
	}
	return gm, textBytes
}

// maxSegmentTextBytes bounds the cited text a reply's groundingSupports repeat, in all.
const maxSegmentTextBytes = 1 << 20

// codeExecParts turns canonical CodeExecution records into Gemini executableCode and
// codeExecutionResult parts. Gemini's ExecutableCode.language enum has only PYTHON, so a run in
// another language, such as a shell command, is left out and reported rather than shown as Python.
// So is a result with no code to pair it with. Omitted files and images are reported as dropped.
func (b *builder) codeExecParts(runs []llm.CodeExecution) []any {
	if len(runs) == 0 {
		return nil
	}
	var out []any
	for _, run := range runs {
		if run.FilesOmitted {
			b.lose("code_execution.files", "files, images or code runs past the size limit have no place in a GenerateContentResponse")
		}
		if run == (llm.CodeExecution{FilesOmitted: true}) {
			continue
		}
		if run.Code == "" || (run.Language != "" && !strings.EqualFold(run.Language, "python")) {
			b.lose("code_execution", "GenerateContent executableCode holds only Python code")
			continue
		}
		out = append(out, obj{"executableCode": obj{"language": "PYTHON", "code": run.Code}})
		outcome := "OUTCOME_OK"
		if run.Failed {
			outcome = "OUTCOME_FAILED"
		}
		res := obj{"outcome": outcome}
		if run.Output != "" {
			res["output"] = run.Output
		}
		out = append(out, obj{"codeExecutionResult": res})
	}
	return out
}

// response returns one GenerateContentResponse with the given parts and, when finish is set,
// the finish reason and usage.
func (b *builder) response(parts []any, finish *llm.Finish, usage *llm.Usage) obj {
	return b.candidateResponse(0, parts, finish, usage)
}

func (b *builder) candidateResponse(index int, parts []any, finish *llm.Finish, usage *llm.Usage) obj {
	if parts == nil {
		parts = []any{}
	}
	cand := obj{"content": obj{"role": "model", "parts": parts}, "index": index}
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

	calls        map[int]*call // tool calls by model item index
	order        []int         // tool call item indexes in start order
	msgText      map[int]*strings.Builder
	msgBase      map[int]int
	totalBytes   int
	cited        []citedSegment
	candCalls    map[int]map[int]*call
	candOrder    map[int][]int
	candFinished map[int]bool
}

// NewStreamWriter returns a writer for one client stream.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{
		w:       w,
		calls:   map[int]*call{},
		msgText: map[int]*strings.Builder{},
		msgBase: map[int]int{},
	}
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
		if ev.Item.Kind == llm.ItemMessage {
			s.msgText[ev.Index] = &strings.Builder{}
			s.msgBase[ev.Index] = s.totalBytes
		}
		if ev.Item.Kind == llm.ItemToolCall {
			s.calls[ev.Index] = b.newCall(ev)
			s.order = append(s.order, ev.Index)
		}
	case llm.EventTextDelta, llm.EventReasoningDelta:
		if ev.Delta == "" {
			return nil
		}
		if ev.Type == llm.EventTextDelta {
			if buf := s.msgText[ev.Index]; buf != nil {
				buf.WriteString(ev.Delta)
			}
			s.totalBytes += len(ev.Delta)
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
		if len(ev.Citations) > 0 {
			text := ""
			if buf := s.msgText[ev.Index]; buf != nil {
				text = buf.String()
			}
			s.cited = append(s.cited, citedSegment{
				text:      text,
				byteBase:  s.msgBase[ev.Index],
				citations: ev.Citations,
			})
		}
		delete(s.msgText, ev.Index)
		delete(s.msgBase, ev.Index)
		if o := ev.Opaque; o != nil && o.Format == llm.OpaqueDevin && o.Data != "" {
			// A Devin reasoning signature goes in a thought part of its own, as today's core
			// writes it.
			return s.emit(b.response([]any{obj{"text": "", "thought": true, "thoughtSignature": o.Data}}, nil, nil))
		}
	case llm.EventMedia:
		if ev.Media == nil {
			return nil
		}
		if err := llm.ValidOutputMedia(ev.Media); err != nil {
			b.lose("media."+ev.Media.Kind, "invalid or unsafe output media")
			return nil
		}
		return s.emit(b.response([]any{obj{"inlineData": obj{"mimeType": ev.Media.MediaType, "data": ev.Media.Data}}}, nil, nil))
	case llm.EventCandidate:
		c := ev.Candidate
		if c == nil || c.Index < 1 {
			return nil
		}
		switch c.Kind {
		case llm.CandidateTextDelta:
			if c.Delta == "" {
				return nil
			}
			return s.emit(b.candidateResponse(c.Index, []any{obj{"text": c.Delta}}, nil, nil))
		case llm.CandidateReasoningDelta:
			if c.Delta == "" {
				return nil
			}
			return s.emit(b.candidateResponse(c.Index, []any{obj{"text": c.Delta, "thought": true}}, nil, nil))
		case llm.CandidateMedia:
			if c.Media == nil {
				return nil
			}
			if err := llm.ValidOutputMedia(c.Media); err != nil {
				b.lose("media."+c.Media.Kind, "invalid or unsafe output media")
				return nil
			}
			return s.emit(b.candidateResponse(c.Index, []any{obj{"inlineData": obj{"mimeType": c.Media.MediaType, "data": c.Media.Data}}}, nil, nil))
		case llm.CandidateToolCallStart:
			if s.candCalls == nil {
				s.candCalls = map[int]map[int]*call{}
				s.candOrder = map[int][]int{}
			}
			if s.candCalls[c.Index] == nil {
				s.candCalls[c.Index] = map[int]*call{}
			}
			s.candCalls[c.Index][c.Item] = &call{id: c.CallID, name: c.Name}
			s.candOrder[c.Index] = append(s.candOrder[c.Index], c.Item)
		case llm.CandidateToolInputDelta:
			if m := s.candCalls[c.Index]; m != nil {
				if fc := m[c.Item]; fc != nil {
					fc.args.WriteString(c.Delta)
				}
			}
		case llm.CandidateFinish:
			var parts []any
			for _, item := range s.candOrder[c.Index] {
				if fc := s.candCalls[c.Index][item]; fc != nil {
					parts = append(parts, b.callPart(fc))
				}
			}
			delete(s.candOrder, c.Index)
			delete(s.candCalls, c.Index)
			if s.candFinished == nil {
				s.candFinished = map[int]bool{}
			}
			s.candFinished[c.Index] = true
			return s.emit(b.candidateResponse(c.Index, parts, c.Finish, nil))
		}
	case llm.EventFinish:
		if len(s.candOrder) > 0 {
			idxs := make([]int, 0, len(s.candOrder))
			for idx := range s.candOrder {
				if !s.candFinished[idx] {
					idxs = append(idxs, idx)
				}
			}
			slices.Sort(idxs)
			for _, idx := range idxs {
				var parts []any
				for _, item := range s.candOrder[idx] {
					if fc := s.candCalls[idx][item]; fc != nil {
						parts = append(parts, b.callPart(fc))
					}
				}
				if err := s.emit(b.candidateResponse(idx, parts, &llm.Finish{Reason: llm.FinishStop}, nil)); err != nil {
					return err
				}
			}
			clear(s.candOrder)
			clear(s.candCalls)
		}
		parts := b.codeExecParts(ev.CodeExecutions)
		for _, idx := range s.order {
			parts = append(parts, b.callPart(s.calls[idx]))
		}
		resp := b.response(parts, ev.Finish, ev.Usage)
		if gm, _ := buildGroundingMetadata(s.cited, ev.Sources, b); gm != nil {
			resp["candidates"].([]any)[0].(obj)["groundingMetadata"] = gm
		}
		return s.emit(resp)
	case llm.EventError:
		return s.errorEvent(ev.Error)
	}
	return nil
}

// aggPart is one part a non-streamed reply collects.
type aggPart struct {
	kind      string
	text      strings.Builder
	call      *call
	media     *llm.OutputMedia
	citations []llm.Citation
}

type candAgg struct {
	order  []*aggPart
	calls  map[int]*call
	finish *llm.Finish
}

// Aggregator collects events into a non-streamed GenerateContentResponse. A reply from a Gemini
// upstream is returned as the upstream sent it.
type Aggregator struct {
	b              builder
	native         bool
	raws           []json.RawMessage
	parts          map[int]*aggPart
	order          []*aggPart
	cands          map[int]*candAgg
	finish         *llm.Finish
	usage          *llm.Usage
	sources        []llm.Source
	codeExecutions []llm.CodeExecution
	size           int
	media          []*aggPart // the media parts the reply still holds, in the order they came
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{parts: map[int]*aggPart{}} }

// Losses returns what the reply had that the client format could not carry.
func (a *Aggregator) Losses() []llm.Loss { return a.b.losses }

// Add records one event. Once the reply passes llm.MaxNonStreamedReplyBytes it leaves out media
// parts, last first, until it fits or none is left, so a reply whose images do not all fit keeps
// its text and the images that do, with a loss for each kind it left out.
func (a *Aggregator) Add(ev llm.Event) {
	a.add(ev)
	for a.Size() > llm.MaxNonStreamedReplyBytes && len(a.media) > 0 {
		p := a.media[len(a.media)-1]
		a.media = a.media[:len(a.media)-1]
		a.size -= len(p.media.MediaType) + len(p.media.Data) + partOverhead
		a.b.lose("media."+p.media.Kind, fmt.Sprintf("left out: the reply would pass %d MiB, the limit for a non-streamed reply", llm.MaxNonStreamedReplyBytes>>20))
		p.kind, p.media = "", nil
	}
}

func (a *Aggregator) add(ev llm.Event) {
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
		a.size += len(a.b.id) + len(a.b.model)
	case llm.EventItemStart:
		a.size += len(ev.Item.CallID) + len(ev.Item.Name) + partOverhead
		p := &aggPart{kind: ev.Item.Kind}
		if ev.Item.Kind == llm.ItemToolCall {
			p.call = a.b.newCall(ev)
			a.size += len(p.call.signature)
		}
		a.parts[ev.Index] = p
		a.order = append(a.order, p)
	case llm.EventMedia:
		if a.native || ev.Media == nil {
			return
		}
		if err := llm.ValidOutputMedia(ev.Media); err != nil {
			a.b.lose("media."+ev.Media.Kind, "invalid or unsafe output media")
			return
		}
		m := *ev.Media
		a.size += len(m.MediaType) + len(m.Data) + partOverhead
		p := &aggPart{kind: llm.EventMedia, media: &m}
		a.order = append(a.order, p)
		a.media = append(a.media, p)
	case llm.EventCandidate:
		if a.native || ev.Candidate == nil || ev.Candidate.Index < 1 {
			return
		}
		c := ev.Candidate
		if a.cands == nil {
			a.cands = map[int]*candAgg{}
		}
		ca := a.cands[c.Index]
		if ca == nil {
			ca = &candAgg{calls: map[int]*call{}}
			a.cands[c.Index] = ca
		}
		a.size += len(c.Delta)
		switch c.Kind {
		case llm.CandidateTextDelta:
			if c.Delta == "" {
				return
			}
			if n := len(ca.order); n > 0 && ca.order[n-1].kind == llm.ItemMessage {
				ca.order[n-1].text.WriteString(c.Delta)
			} else {
				p := &aggPart{kind: llm.ItemMessage}
				p.text.WriteString(c.Delta)
				ca.order = append(ca.order, p)
				a.size += partOverhead
			}
		case llm.CandidateReasoningDelta:
			if c.Delta == "" {
				return
			}
			if n := len(ca.order); n > 0 && ca.order[n-1].kind == llm.ItemReasoning {
				ca.order[n-1].text.WriteString(c.Delta)
			} else {
				p := &aggPart{kind: llm.ItemReasoning}
				p.text.WriteString(c.Delta)
				ca.order = append(ca.order, p)
				a.size += partOverhead
			}
		case llm.CandidateToolCallStart:
			a.size += len(c.CallID) + len(c.Name) + partOverhead
			fc := &call{id: c.CallID, name: c.Name}
			ca.calls[c.Item] = fc
			ca.order = append(ca.order, &aggPart{kind: llm.ItemToolCall, call: fc})
		case llm.CandidateToolInputDelta:
			if fc := ca.calls[c.Item]; fc != nil {
				fc.args.WriteString(c.Delta)
			}
		case llm.CandidateMedia:
			if c.Media == nil {
				return
			}
			if err := llm.ValidOutputMedia(c.Media); err != nil {
				a.b.lose("media."+c.Media.Kind, "invalid or unsafe output media")
				return
			}
			m := *c.Media
			a.size += len(m.MediaType) + len(m.Data) + partOverhead
			p := &aggPart{kind: llm.EventMedia, media: &m}
			ca.order = append(ca.order, p)
			a.media = append(a.media, p)
		case llm.CandidateFinish:
			ca.finish = c.Finish
			if c.Finish != nil {
				a.size += len(c.Finish.RawReason)
			}
		}
	case llm.EventTextDelta, llm.EventReasoningDelta:
		if p := a.parts[ev.Index]; p != nil {
			p.text.WriteString(ev.Delta)
		}
	case llm.EventToolInputDelta:
		if p := a.parts[ev.Index]; p != nil && p.call != nil {
			p.call.args.WriteString(ev.Delta)
		}
	case llm.EventItemEnd:
		if o := ev.Opaque; o != nil && o.Format == llm.OpaqueDevin && o.Data != "" && !a.native {
			// Today's core leaves a Devin reasoning signature out of an unstreamed reply.
			a.b.lose("content.thoughtSignature", "a Devin reasoning signature, which an unstreamed GenerateContentResponse does not carry")
		}
		if p := a.parts[ev.Index]; p != nil {
			// A reply of this format keeps its citations in the chunks it holds already.
			if len(ev.Citations) > 0 && !a.native {
				p.citations = ev.Citations
				for _, c := range ev.Citations {
					a.size += len(c.URL) + len(c.Title) + 48
				}
			}
			if p.call != nil {
				// A late signature replaces the call's first one.
				a.size -= len(p.call.signature)
				a.b.signature(p.call, ev)
				a.size += len(p.call.signature)
			}
		}
	case llm.EventFinish:
		a.finish, a.usage, a.sources, a.codeExecutions = ev.Finish, ev.Usage, ev.Sources, ev.CodeExecutions
		// A reply of this format keeps its sources and runs in the chunks it holds already.
		if !a.native {
			for _, s := range ev.Sources {
				a.size += len(s.URL) + len(s.Title) + 32
			}
			for _, ce := range ev.CodeExecutions {
				a.size += len(ce.Code) + len(ce.Language) + len(ce.Output) + 64
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

// partOverhead is about the JSON a part adds around a call's ID, name and arguments or a text.
const partOverhead = len(`{"functionCall":{"id":"","name":"","args":{}}},`)

// Size is about how many bytes of content the response holds so far: its id and model, text,
// thoughts, function calls with their thought signatures, the upstream chunks it keeps, its
// finish reason and usage as the upstream sent them, and what it reports as lost, before JSON
// escaping. Callers use it to bound what they collect.
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
	parts := b.codeExecParts(a.codeExecutions)
	var cited []citedSegment
	for _, p := range a.order {
		switch p.kind {
		case llm.ItemMessage:
			text := p.text.String()
			if len(p.citations) > 0 {
				cited = append(cited, citedSegment{
					text:      text,
					partIndex: len(parts),
					citations: p.citations,
				})
			}
			parts = append(parts, obj{"text": text})
		case llm.ItemReasoning:
			parts = append(parts, obj{"text": p.text.String(), "thought": true})
		case llm.ItemToolCall:
			parts = append(parts, b.callPart(p.call))
		case llm.EventMedia:
			parts = append(parts, obj{"inlineData": obj{"mimeType": p.media.MediaType, "data": p.media.Data}})
		}
	}
	finish := a.finish
	if finish == nil {
		finish = &llm.Finish{Reason: llm.FinishOther}
	}
	resp := b.response(parts, finish, a.usage)
	if gm, textBytes := buildGroundingMetadata(cited, a.sources, b); gm != nil {
		resp["candidates"].([]any)[0].(obj)["groundingMetadata"] = gm
		a.size += textBytes
	}
	if len(a.cands) > 0 {
		idxs := make([]int, 0, len(a.cands))
		for idx := range a.cands {
			idxs = append(idxs, idx)
		}
		slices.Sort(idxs)
		cands := resp["candidates"].([]any)
		for _, idx := range idxs {
			ca := a.cands[idx]
			cparts := []any{}
			for _, p := range ca.order {
				switch p.kind {
				case llm.ItemMessage:
					cparts = append(cparts, obj{"text": p.text.String()})
				case llm.ItemReasoning:
					cparts = append(cparts, obj{"text": p.text.String(), "thought": true})
				case llm.ItemToolCall:
					cparts = append(cparts, b.callPart(p.call))
				case llm.EventMedia:
					cparts = append(cparts, obj{"inlineData": obj{"mimeType": p.media.MediaType, "data": p.media.Data}})
				}
			}
			cf := ca.finish
			if cf == nil {
				cf = &llm.Finish{Reason: llm.FinishOther}
			}
			cands = append(cands, obj{
				"content":      obj{"role": "model", "parts": cparts},
				"index":        idx,
				"finishReason": finishReason(cf, b.upFormat),
			})
		}
		resp["candidates"] = cands
	}
	return json.Marshal(resp)
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
	case llm.CodePermission, llm.CodeAccountClosed:
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
//
// The usage-limit reply has today's core's body, which is the same for every client but
// Anthropic's.
func ErrorResponse(e *llm.Error) (int, []byte) {
	if u := e.UsageLimit; u != nil {
		o := u.ErrorObject()
		e.Pluxy(o)
		b, _ := json.Marshal(obj{"error": o})
		return http.StatusTooManyRequests, b
	}
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
		// Google's code is a number and its details a list; another format's error can have a
		// details member that means something else.
		var g struct {
			Error struct {
				Code    json.RawMessage `json:"code"`
				Status  string          `json:"status"`
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		var code int
		if json.Unmarshal(e.Raw, &g) == nil && json.Unmarshal(g.Error.Code, &code) == nil {
			var details []json.RawMessage
			if !isNull(g.Error.Details) && json.Unmarshal(g.Error.Details, &details) == nil {
				body["details"] = g.Error.Details
			}
			// An overflow's provider code is not Google's: it keeps the status Google gave it, unless
			// core changed the HTTP status, as for a 500 that is an overflow.
			if _, ok := googleStatus[g.Error.Status]; ok && code == st && e.ProviderCode == llm.ProviderContextLengthExceeded {
				body["status"] = g.Error.Status
			}
		}
	}
	e.Pluxy(body)
	b, _ := json.Marshal(obj{"error": body})
	return st, b
}
