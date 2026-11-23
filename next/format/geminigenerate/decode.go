package geminigenerate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// maxErrorRaw limits the upstream bytes an error keeps for diagnostics. Longer data is not kept:
// a cut of it could fall inside a credential, whose start core's redaction cannot recognise.
const maxErrorRaw = 1 << 20

// malformed returns the error for upstream data that is not the JSON it should be. The data, when
// it is no longer than maxErrorRaw, is kept in Raw as a JSON string when it is not JSON, so the
// error can still be sent to core.
func malformed(msg string, data []byte) *llm.Error {
	e := &llm.Error{Code: llm.CodeUpstream, Message: msg}
	if len(data) <= maxErrorRaw {
		e.Raw = llm.RawJSON(data)
	}
	return e
}

// chunk is one GenerateContentResponse: a whole non-streamed reply, or one chunk of a stream.
type chunk struct {
	Candidates     []json.RawMessage `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata json.RawMessage `json:"usageMetadata"`
	ModelVersion  string          `json:"modelVersion"`
	ResponseID    string          `json:"responseId"`
	Error         json.RawMessage `json:"error"`
}

type rawCandidate struct {
	Index   *int `json:"index"`
	Content *struct {
		Parts []json.RawMessage `json:"parts"`
	} `json:"content"`
	FinishReason      string          `json:"finishReason"`
	FinishMessage     string          `json:"finishMessage"`
	GroundingMetadata json.RawMessage `json:"groundingMetadata"`
}

type candState struct {
	calls    bool
	nextItem int
}

type usageMetadata struct {
	Prompt     *int64 `json:"promptTokenCount"`
	Cached     *int64 `json:"cachedContentTokenCount"`
	Candidates *int64 `json:"candidatesTokenCount"`
	Thoughts   *int64 `json:"thoughtsTokenCount"`
	ToolPrompt *int64 `json:"toolUsePromptTokenCount"`
}

// decodeUsage reads usageMetadata. promptTokenCount already includes cachedContentTokenCount;
// candidatesTokenCount leaves out thoughtsTokenCount (Google's UsageMetadata reference). In the
// model, input counts every input token (prompt and tool-use prompt) and output counts every
// output token, thoughts included, with reasoning tokens as the part of it they are.
func decodeUsage(raw json.RawMessage) *llm.Usage {
	if isNull(raw) {
		return nil
	}
	var u usageMetadata
	if json.Unmarshal(raw, &u) != nil {
		return nil
	}
	out := &llm.Usage{CachedInputTokens: u.Cached, ReasoningTokens: u.Thoughts, Raw: append(json.RawMessage(nil), raw...)}
	if u.Prompt != nil || u.ToolPrompt != nil {
		out.InputTokens = llm.Int64(val(u.Prompt) + val(u.ToolPrompt))
	}
	if u.Candidates != nil || u.Thoughts != nil {
		out.OutputTokens = llm.Int64(val(u.Candidates) + val(u.Thoughts))
	}
	return out
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// reportsTokens reports whether the usage carries a count the upstream measured, as opposed to an
// empty or zero-filled object a later chunk can send after the usage was already counted.
func reportsTokens(u *llm.Usage) bool {
	for _, p := range []*int64{u.InputTokens, u.OutputTokens, u.CachedInputTokens, u.CacheWriteInputTokens, u.ReasoningTokens} {
		if p != nil && *p > 0 {
			return true
		}
	}
	return false
}

// contentFilterReasons are the finish reasons that mean a safety or policy filter stopped the
// reply.
var contentFilterReasons = map[string]bool{
	"SAFETY": true, "RECITATION": true, "LANGUAGE": true, "BLOCKLIST": true, "PROHIBITED_CONTENT": true, "SPII": true,
	"IMAGE_SAFETY": true, "IMAGE_PROHIBITED_CONTENT": true, "IMAGE_RECITATION": true,
}

// finishOf maps a Gemini finish reason; RawReason keeps Gemini's own value, so SAFETY,
// RECITATION and the rest stay distinct. A STOP with function calls in the reply is a tool-call
// stop. MALFORMED_FUNCTION_CALL never becomes a finish: the decoders end that reply with an error.
func finishOf(reason string, calls bool) llm.Finish {
	f := llm.Finish{Reason: llm.FinishOther, RawReason: reason}
	switch {
	case reason == "STOP" && calls:
		f.Reason = llm.FinishToolCalls
	case reason == "STOP":
		f.Reason = llm.FinishStop
	case reason == "MAX_TOKENS":
		f.Reason = llm.FinishLength
	case contentFilterReasons[reason]:
		f.Reason = llm.FinishContentFilter
	}
	return f
}

// googleStatus maps a Google RPC status to the model's error code and scope.
var googleStatus = map[string]struct{ code, scope string }{
	"INVALID_ARGUMENT":    {llm.CodeInvalidRequest, llm.ScopeRequest},
	"FAILED_PRECONDITION": {llm.CodeInvalidRequest, llm.ScopeRequest},
	"UNAUTHENTICATED":     {llm.CodeAuth, llm.ScopeCredential},
	"PERMISSION_DENIED":   {llm.CodePermission, llm.ScopeCredential},
	"NOT_FOUND":           {llm.CodeNotFound, llm.ScopeModel},
	"RESOURCE_EXHAUSTED":  {llm.CodeRateLimit, llm.ScopeModel},
	"UNAVAILABLE":         {llm.CodeUnavailable, llm.ScopeEndpoint},
	"CANCELLED":           {llm.CodeCancelled, llm.ScopeRequest},
}

// errorOf reads a Google error object, {"code", "message", "status"}, sent inside a 200 reply.
func errorOf(raw json.RawMessage, body []byte) *llm.Error {
	var g struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	_ = json.Unmarshal(raw, &g)
	e := &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: g.Message, Status: g.Code, ProviderCode: g.Status,
		Raw: llm.RawJSON(body)}
	if m, ok := googleStatus[g.Status]; ok {
		e.Code, e.Scope = m.code, m.scope
	}
	if e.Message == "" {
		e.Message = "upstream reported an error"
	}
	if e.Status == 0 || http.StatusText(e.Status) == "" {
		e.Status = 0
	}
	return e
}

// StreamDecoder turns the chunks of an upstream GenerateContent stream into model events. Every
// chunk's data rides, in Raw, on the first event it produces, or on a native event when it
// produces none, so a Gemini client gets every upstream chunk once and unchanged. Each chunk is
// a whole GenerateContentResponse; the stream ends when the upstream closes it, so the finish
// event comes from Close. Use one decoder per stream.
type StreamDecoder struct {
	started bool
	done    bool
	next    int
	text    int // open message item, or -1
	think   int // open reasoning item, or -1
	calls   bool
	finish  *llm.Finish
	usage   *llm.Usage
	// idPrefix is the reply's part of the IDs made for calls that come without one; set at the
	// first such call.
	idPrefix   string
	responseID string
	// malformed is set when the reply ended with MALFORMED_FUNCTION_CALL, with Gemini's message.
	malformed     bool
	malformedText string
	// Grounding state for the reply as a whole. Google's segment offsets count bytes within one
	// Part (partIndex), and a thought signature, a function call or media can close the open text
	// item between parts, so the message text of every item is kept, in order, in allText, and
	// items records which bytes of it each message item holds.
	allText   strings.Builder
	items     []*msgItem
	partSpans map[int][2]int // candidate partIndex -> [byteStart, byteEnd] in allText
	// A part index counts within one chunk, so partSpans holds only while the reply's text came in
	// one chunk. Once it spans chunks (multiChunk), offsets are read against the whole text.
	chunkNo    int
	spanChunk  int
	multiChunk bool
	// citations are the open message item's, put on its item.end when it closes.
	citations []llm.Citation
	citLosses map[string]bool
	sources   []llm.Source
	seenSrc   map[string]bool
	// Pending executableCode parts waiting for their codeExecutionResult, and completed runs.
	pendingCode []pendingExecCode
	codeExecs   []llm.CodeExecution
	mediaBytes  int
	losses      []llm.Loss
	// splitText is set once a media part has closed a text item: Google's offsets then no longer
	// map onto one item's text, so citations carry no range.
	splitText bool
	// cands tracks per-candidate state for candidates with index >= 1.
	cands map[int]*candState
}

// msgItem is one message item of the reply: its bytes in allText and the citations it already
// has, so metadata that repeats on later chunks cites once.
type msgItem struct {
	idx        int
	start, end int
	closed     bool
	seen       map[string]bool
}

type pendingExecCode struct {
	id   string
	code string
	lang string
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{text: -1, think: -1, partSpans: map[int][2]int{}, seenSrc: map[string]bool{}, citLosses: map[string]bool{}}
}

func (d *StreamDecoder) open(kind string, out []llm.Event, start llm.ItemStart) (int, []llm.Event) {
	start.Kind = kind
	idx := d.next
	d.next++
	return idx, append(out, llm.Event{Type: llm.EventItemStart, Index: idx, Item: &start})
}

func (d *StreamDecoder) closeItem(idx *int, out []llm.Event) []llm.Event {
	if *idx < 0 {
		return out
	}
	end := llm.Event{Type: llm.EventItemEnd, Index: *idx}
	if idx == &d.text {
		if len(d.citations) > 0 {
			end.Citations = d.citations
			d.citations = nil
		}
		if n := len(d.items); n > 0 {
			d.items[n-1].closed = true
		}
	}
	out = append(out, end)
	*idx = -1
	return out
}

// decodeGrounding reads Gemini's groundingMetadata (groundingChunks and groundingSupports).
// Malformed metadata, out-of-range chunk indices, and non-http URLs are skipped without failing
// the reply. Segment startIndex/endIndex are UTF-8 byte offsets within the Part that partIndex
// names (Google's GenerateContent API reference, Segment) and are converted to Unicode code point
// (rune) offsets within the message item that holds them. A citation goes to the item its range
// falls in, even when a thought signature or a function call has closed that item since: out
// holds the events of the chunk being decoded, and an item closed within it still takes the
// citation on its item.end. A range that cannot be placed on one item is reported as a loss,
// except that one with no usable range covers the open message item when the reply has only one.
func (d *StreamDecoder) decodeGrounding(raw json.RawMessage, out []llm.Event) {
	if isNull(raw) || raw[0] != '{' {
		return
	}
	var gm struct {
		Chunks   json.RawMessage `json:"groundingChunks"`
		Supports json.RawMessage `json:"groundingSupports"`
	}
	if json.Unmarshal(raw, &gm) != nil {
		return
	}
	var chunks []struct {
		Web *struct {
			URI   string `json:"uri"`
			Title string `json:"title"`
		} `json:"web"`
	}
	if !isNull(gm.Chunks) && gm.Chunks[0] == '[' {
		_ = json.Unmarshal(gm.Chunks, &chunks)
	}
	for _, ch := range chunks {
		if ch.Web == nil || !llm.ValidCitationURL(ch.Web.URI) || d.seenSrc[ch.Web.URI] {
			continue
		}
		d.seenSrc[ch.Web.URI] = true
		d.sources = append(d.sources, llm.Source{URL: ch.Web.URI, Title: ch.Web.Title})
	}
	if isNull(gm.Supports) || gm.Supports[0] != '[' || len(chunks) == 0 {
		return
	}
	var supports []struct {
		Segment *struct {
			PartIndex  *int `json:"partIndex"`
			StartIndex *int `json:"startIndex"`
			EndIndex   *int `json:"endIndex"`
		} `json:"segment"`
		ChunkIndices []int `json:"groundingChunkIndices"`
	}
	if json.Unmarshal(gm.Supports, &supports) != nil {
		return
	}
	for si, sup := range supports {
		if len(sup.ChunkIndices) == 0 {
			continue
		}
		var it *msgItem
		var startRune, endRune int
		ranged := false
		if sup.Segment != nil && sup.Segment.EndIndex != nil {
			startByte := 0
			if sup.Segment.StartIndex != nil {
				startByte = *sup.Segment.StartIndex
			}
			partIdx := 0
			if sup.Segment.PartIndex != nil {
				partIdx = *sup.Segment.PartIndex
			}
			it, startRune, endRune, ranged = d.placeSegment(partIdx, startByte, *sup.Segment.EndIndex)
		}
		if !ranged {
			it = nil
			// No usable range: the citation covers the open message item, if it is the only one.
			if d.text >= 0 && (len(d.items) == 1 || d.splitText) {
				it = d.items[len(d.items)-1]
			}
		}
		for _, ci := range sup.ChunkIndices {
			if ci < 0 || ci >= len(chunks) {
				continue
			}
			ch := chunks[ci]
			if ch.Web == nil || !llm.ValidCitationURL(ch.Web.URI) {
				continue
			}
			c := llm.Citation{URL: ch.Web.URI, Title: ch.Web.Title}
			// Metadata can repeat on later chunks of a stream.
			key := c.URL
			if ranged {
				s, e := startRune, endRune
				c.StartIndex, c.EndIndex = &s, &e
				key = fmt.Sprintf("%s\x00%d\x00%d", c.URL, s, e)
			}
			if it != nil && it.seen[key] {
				continue
			}
			if it != nil && !it.closed {
				it.seen[key] = true
				d.citations = append(d.citations, c)
				continue
			}
			if it != nil {
				// The item closed: its item.end is still ours to change only if this chunk emitted it.
				if k := endEventOf(out, it.idx); k >= 0 {
					it.seen[key] = true
					out[k].Citations = append(out[k].Citations, c)
					continue
				}
			}
			path := fmt.Sprintf("candidates[0].groundingMetadata.groundingSupports[%d]", si)
			detail := "citation not placed: its range does not fall inside one message item"
			if it != nil {
				detail = "citation not placed: the message item it cites ended in an earlier chunk"
			}
			if !d.citLosses[path+detail] {
				d.citLosses[path+detail] = true
				d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
			}
		}
	}
}

// endEventOf returns the position in out of the item.end of item idx, or -1.
func endEventOf(out []llm.Event, idx int) int {
	for k := len(out) - 1; k >= 0; k-- {
		if out[k].Type == llm.EventItemEnd && out[k].Index == idx {
			return k
		}
	}
	return -1
}

// placeSegment finds the message item that holds the bytes [startByte, endByte) of the Part
// partIdx, and returns that range as rune offsets within the item. Text that came in several
// chunks is read as one text, whose part is part 0, across the items a thought signature or a
// function call split it into. In a single chunk with several parts (e.g. a thought part at 0,
// text part at 1), partSpans maps partIndex to the part's bytes. It reports false when the
// offsets do not map onto one item's text.
func (d *StreamDecoder) placeSegment(partIdx, startByte, endByte int) (it *msgItem, startRune, endRune int, ok bool) {
	all := d.allText.String()
	ps, pe := 0, len(all)
	switch {
	case d.splitText:
		return nil, 0, 0, false
	case d.multiChunk:
		if partIdx != 0 {
			return nil, 0, 0, false
		}
	case partIdx > 0:
		span, found := d.partSpans[partIdx]
		if !found {
			return nil, 0, 0, false
		}
		ps, pe = span[0], span[1]
	default:
		if span, found := d.partSpans[0]; found && len(d.partSpans) > 1 {
			ps, pe = span[0], span[1]
		} else if len(d.items) > 1 {
			return nil, 0, 0, false
		}
	}
	if startByte < 0 || endByte < startByte || endByte > pe-ps {
		return nil, 0, 0, false
	}
	part := all[ps:pe]
	if _, ok1 := llm.UTF8ByteToRuneOffset(part, startByte); !ok1 {
		return nil, 0, 0, false
	}
	if _, ok2 := llm.UTF8ByteToRuneOffset(part, endByte); !ok2 {
		return nil, 0, 0, false
	}
	gs, ge := ps+startByte, ps+endByte
	for _, m := range d.items {
		if m.start <= gs && ge <= m.end {
			return m, utf8.RuneCountInString(all[m.start:gs]), utf8.RuneCountInString(all[m.start:ge]), true
		}
	}
	return nil, 0, 0, false
}

// Decode takes the data of one SSE event and returns the events it produces. An error object
// in the stream becomes an *llm.Error that keeps the chunk in Raw.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("geminigenerate: data after the end of the stream")
	}
	t := bytes.TrimSpace(data)
	if string(t) == "[DONE]" {
		// Not part of Gemini's stream, but some relays add it; the stream ends at EOF.
		return nil, nil
	}
	var c chunk
	if err := json.Unmarshal(t, &c); err != nil || len(t) == 0 || t[0] != '{' {
		return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
	}
	if !isNull(c.Error) {
		d.done = true
		return nil, d.withUsage(errorOf(c.Error, data))
	}
	if len(c.Candidates) > 0 {
		var c0 rawCandidate
		if err := json.Unmarshal(c.Candidates[0], &c0); err != nil {
			return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
		}
	}
	out := d.decode(c, nil)
	if len(out) == 0 {
		out = append(out, llm.Event{Type: llm.EventNative})
	}
	out[0].Raw = append(json.RawMessage(nil), data...)
	return out, nil
}

// decode adds the events chunk c produces to out.
func (d *StreamDecoder) decode(c chunk, out []llm.Event) []llm.Event {
	d.chunkNo++
	if d.responseID == "" {
		d.responseID = c.ResponseID
	}
	if !d.started {
		d.started = true
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: c.ResponseID, Model: c.ModelVersion, Format: format}})
	}
	// Gemini usageMetadata is cumulative and the last chunk usually carries it, but a later empty
	// or zero-filled usageMetadata must not overwrite counts the upstream already reported; one
	// with counts does.
	if u := decodeUsage(c.UsageMetadata); u != nil && (d.usage == nil || reportsTokens(u)) {
		d.usage = u
	}
	seenZero := false
	for i, rawCand := range c.Candidates {
		var cand rawCandidate
		if err := json.Unmarshal(rawCand, &cand); err != nil {
			if i > 0 {
				d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("candidates[%d]", i), Detail: "malformed candidate"})
			}
			continue
		}
		idx := i
		if cand.Index != nil {
			idx = *cand.Index
		}
		if idx < 0 {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("candidates[%d]", i), Detail: "candidate with negative index"})
			continue
		}
		if idx > 0 {
			out = d.decodeOtherCandidate(idx, cand, out)
			continue
		}
		if seenZero {
			continue
		}
		seenZero = true
		if cand.Content != nil {
			for pi, pr := range cand.Content.Parts {
				out = d.part(pi, pr, out)
			}
		}
		if !isNull(cand.GroundingMetadata) {
			d.decodeGrounding(cand.GroundingMetadata, out)
		}
		if cand.FinishReason == MalformedFunctionCall {
			d.malformed, d.malformedText = true, cand.FinishMessage
		}
		if cand.FinishReason != "" {
			f := finishOf(cand.FinishReason, d.calls)
			d.finish = &f
			out = d.closeItem(&d.think, out)
			out = d.closeItem(&d.text, out)
		}
	}
	if len(c.Candidates) == 0 && c.PromptFeedback != nil && c.PromptFeedback.BlockReason != "" {
		// The prompt was blocked: no candidates, and the block reason in place of a finish
		// reason.
		d.finish = &llm.Finish{Reason: llm.FinishContentFilter, RawReason: c.PromptFeedback.BlockReason}
	}
	return out
}

func (d *StreamDecoder) cand(idx int) *candState {
	if d.cands == nil {
		d.cands = map[int]*candState{}
	}
	cs := d.cands[idx]
	if cs == nil {
		cs = &candState{}
		d.cands[idx] = cs
	}
	return cs
}

// decodeOtherCandidate emits EventCandidate events for a candidate with index >= 1. Later
// candidates never alter candidate 0's state, finish reason or error status.
func (d *StreamDecoder) decodeOtherCandidate(idx int, cand rawCandidate, out []llm.Event) []llm.Event {
	cs := d.cand(idx)
	if cand.Content != nil {
		for pi, pr := range cand.Content.Parts {
			pm := object(pr)
			if pm == nil {
				d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("candidates[%d].content.parts[%d]", idx, pi), Detail: "malformed candidate part"})
				continue
			}
			idRaw := pm["inlineData"]
			if isNull(idRaw) {
				idRaw = pm["inline_data"]
			}
			if !isNull(idRaw) {
				if isThought(pm) {
					continue
				}
				var blob struct {
					MimeType      string `json:"mimeType"`
					MimeTypeSnake string `json:"mime_type"`
					Data          string `json:"data"`
				}
				path := fmt.Sprintf("candidates[%d].content.parts[%d].inlineData", idx, pi)
				if json.Unmarshal(idRaw, &blob) != nil {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "malformed inlineData part"})
					continue
				}
				mt := strings.TrimSpace(blob.MimeType)
				if mt == "" {
					mt = strings.TrimSpace(blob.MimeTypeSnake)
				}
				kind, problem := llm.SafeOutputMediaKind(mt)
				if problem != "" {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: problem})
					continue
				}
				if !llm.Base64Valid(blob.Data) {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "invalid base64 data"})
					continue
				}
				if len(blob.Data) > llm.MaxOutputMediaPartBytes {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media part exceeds per-part size limit"})
					continue
				}
				if d.mediaBytes+len(blob.Data) > llm.MaxOutputMediaReplyBytes {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media exceeds per-reply size limit"})
					continue
				}
				d.mediaBytes += len(blob.Data)
				out = append(out, llm.Event{
					Type: llm.EventCandidate,
					Candidate: &llm.CandidateEvent{
						Index: idx,
						Kind:  llm.CandidateMedia,
						Media: &llm.OutputMedia{Kind: kind, MediaType: mt, Data: blob.Data},
					},
				})
				continue
			}
			if fc := object(pm["functionCall"]); fc != nil {
				var call struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				b, _ := json.Marshal(fc)
				_ = json.Unmarshal(b, &call)
				if call.Name == "" {
					d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("candidates[%d].content.parts[%d].functionCall", idx, pi), Detail: "function call without name"})
					continue
				}
				cs.calls = true
				args := "{}"
				if a := fc["args"]; !isNull(a) {
					var buf bytes.Buffer
					if json.Compact(&buf, a) == nil {
						args = buf.String()
					}
				}
				id := call.ID
				if id == "" {
					if d.idPrefix == "" {
						d.idPrefix = callIDPrefix(d.responseID)
					}
					id = fmt.Sprintf("call_%s_c%d_%d", d.idPrefix, idx, cs.nextItem)
				}
				item := cs.nextItem
				cs.nextItem++
				out = append(out,
					llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: idx, Kind: llm.CandidateToolCallStart, Item: item, CallID: id, Name: call.Name}},
					llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: idx, Kind: llm.CandidateToolInputDelta, Item: item, Delta: args}},
					llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: idx, Kind: llm.CandidateToolCallEnd, Item: item}},
				)
				continue
			}
			var text string
			if json.Unmarshal(pm["text"], &text) != nil || text == "" {
				continue
			}
			kind := llm.CandidateTextDelta
			if isThought(pm) {
				kind = llm.CandidateReasoningDelta
			}
			out = append(out, llm.Event{
				Type:      llm.EventCandidate,
				Candidate: &llm.CandidateEvent{Index: idx, Kind: kind, Delta: text},
			})
		}
	}
	if cand.FinishReason != "" {
		f := finishOf(cand.FinishReason, cs.calls)
		out = append(out, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: idx, Kind: llm.CandidateFinish, Finish: &f},
		})
	}
	return out
}

// part adds the events of one content part. Thought text is reasoning, other text is message
// text, and a function call is a whole tool call (Gemini sends each call complete). Other parts
// (inline data, code execution) reach a Gemini client in the raw chunk only.
func (d *StreamDecoder) part(partIdx int, raw json.RawMessage, out []llm.Event) []llm.Event {
	pm := object(raw)
	if pm == nil {
		return out
	}
	var sig string
	_ = json.Unmarshal(pm["thoughtSignature"], &sig)
	if sig != "" && !isThought(pm) {
		// The part's thought signature becomes a reasoning item of its own, just before the
		// part, holding the signature as opaque data; a client format with a place for it
		// keeps it and replays it, and EncodeRequest puts it back on the part that follows.
		out = d.closeItem(&d.think, out)
		out = d.closeItem(&d.text, out)
		var idx int
		idx, out = d.open(llm.ItemReasoning, out, llm.ItemStart{})
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: idx, Opaque: &llm.Opaque{Format: format, Data: sig}})
	}
	if ecRaw := pm["executableCode"]; !isNull(ecRaw) {
		var ec struct {
			ID       string `json:"id"`
			Language string `json:"language"`
			Code     string `json:"code"`
		}
		if json.Unmarshal(ecRaw, &ec) == nil && ec.Code != "" {
			lang := strings.ToLower(strings.TrimSpace(ec.Language))
			if lang == "language_unspecified" {
				lang = ""
			}
			d.pendingCode = append(d.pendingCode, pendingExecCode{id: ec.ID, code: ec.Code, lang: lang})
		}
		return out
	}
	if resRaw := pm["codeExecutionResult"]; !isNull(resRaw) {
		var res struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
			Output  string `json:"output"`
		}
		if json.Unmarshal(resRaw, &res) == nil {
			var failed bool
			validOutcome := false
			switch res.Outcome {
			case "OUTCOME_OK":
				validOutcome = true
			case "OUTCOME_FAILED", "OUTCOME_DEADLINE_EXCEEDED":
				validOutcome = true
				failed = true
			}
			if validOutcome {
				matchIdx := -1
				if res.ID != "" {
					for i, pc := range d.pendingCode {
						if pc.id == res.ID {
							matchIdx = i
							break
						}
					}
				} else if len(d.pendingCode) > 0 {
					matchIdx = 0
				}
				if matchIdx >= 0 {
					pc := d.pendingCode[matchIdx]
					d.pendingCode = append(d.pendingCode[:matchIdx], d.pendingCode[matchIdx+1:]...)
					d.codeExecs = llm.AppendCodeExecution(d.codeExecs, llm.CodeExecution{
						Code:     pc.code,
						Language: pc.lang,
						Output:   res.Output,
						Failed:   failed,
					})
				}
			}
		}
		return out
	}
	idRaw := pm["inlineData"]
	if isNull(idRaw) {
		idRaw = pm["inline_data"]
	}
	if !isNull(idRaw) {
		if isThought(pm) {
			return out
		}
		var blob struct {
			MimeType      string `json:"mimeType"`
			MimeTypeSnake string `json:"mime_type"`
			Data          string `json:"data"`
		}
		path := fmt.Sprintf("candidates[0].content.parts[%d].inlineData", partIdx)
		if json.Unmarshal(idRaw, &blob) != nil {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "malformed inlineData part"})
			return out
		}
		mt := strings.TrimSpace(blob.MimeType)
		if mt == "" {
			mt = strings.TrimSpace(blob.MimeTypeSnake)
		}
		kind, problem := llm.SafeOutputMediaKind(mt)
		if problem != "" {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: problem})
			return out
		}
		if !llm.Base64Valid(blob.Data) {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "invalid base64 data"})
			return out
		}
		if len(blob.Data) > llm.MaxOutputMediaPartBytes {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media part exceeds per-part size limit"})
			return out
		}
		if d.mediaBytes+len(blob.Data) > llm.MaxOutputMediaReplyBytes {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media exceeds per-reply size limit"})
			return out
		}
		d.splitText = d.splitText || d.text >= 0
		out = d.closeItem(&d.think, out)
		out = d.closeItem(&d.text, out)
		d.mediaBytes += len(blob.Data)
		return append(out, llm.Event{
			Type:  llm.EventMedia,
			Media: &llm.OutputMedia{Kind: kind, MediaType: mt, Data: blob.Data},
		})
	}
	if fc := object(pm["functionCall"]); fc != nil {
		var call struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		b, _ := json.Marshal(fc)
		_ = json.Unmarshal(b, &call)
		out = d.closeItem(&d.think, out)
		out = d.closeItem(&d.text, out)
		d.calls = true
		args := "{}"
		if a := fc["args"]; !isNull(a) {
			var buf bytes.Buffer
			if json.Compact(&buf, a) == nil {
				args = buf.String()
			}
		}
		var idx int
		id := call.ID
		if id == "" {
			// Gemini's call id is optional; the model needs one, unique across the replies
			// of a conversation. The raw chunk keeps the call as it came.
			if d.idPrefix == "" {
				d.idPrefix = callIDPrefix(d.responseID)
			}
			id = fmt.Sprintf("call_%s_%d", d.idPrefix, d.next)
		}
		idx, out = d.open(llm.ItemToolCall, out, llm.ItemStart{CallID: id, Name: call.Name})
		return append(out,
			llm.Event{Type: llm.EventToolInputDelta, Index: idx, Delta: args},
			llm.Event{Type: llm.EventItemEnd, Index: idx})
	}
	var text string
	if json.Unmarshal(pm["text"], &text) != nil || text == "" {
		return out
	}
	if isThought(pm) {
		out = d.closeItem(&d.text, out)
		if d.think < 0 {
			d.think, out = d.open(llm.ItemReasoning, out, llm.ItemStart{})
		}
		out = append(out, llm.Event{Type: llm.EventReasoningDelta, Index: d.think, Delta: text})
		if sig != "" {
			// A signed thought ends its reasoning item, which carries the signature.
			out = append(out, llm.Event{Type: llm.EventItemEnd, Index: d.think, Opaque: &llm.Opaque{Format: format, Data: sig}})
			d.think = -1
		}
		return out
	}
	out = d.closeItem(&d.think, out)
	if d.text < 0 {
		d.text, out = d.open(llm.ItemMessage, out, llm.ItemStart{})
		d.items = append(d.items, &msgItem{idx: d.text, start: d.allText.Len(), seen: map[string]bool{}})
	}
	if len(d.partSpans) > 0 && d.spanChunk != d.chunkNo {
		d.multiChunk = true
	}
	d.spanChunk = d.chunkNo
	start := d.allText.Len()
	d.allText.WriteString(text)
	d.items[len(d.items)-1].end = d.allText.Len()
	d.partSpans[partIdx] = [2]int{start, d.allText.Len()}
	return append(out, llm.Event{Type: llm.EventTextDelta, Index: d.text, Delta: text})
}

// Losses returns what the reply had that the model could not carry, such as unsafe, malformed,
// or oversized output media parts.
func (d *StreamDecoder) Losses() []llm.Loss { return d.losses }

// Usage returns the latest usage the upstream reported, or nil. A caller that fails the stream
// for its own reasons (the connection broke, say) puts it on the error.
func (d *StreamDecoder) Usage() *llm.Usage { return d.usage }

// withUsage puts the usage reported so far on an error that ends the stream.
func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.usage
	return e
}

// Close is called when the upstream stream ends. A stream that ends before any finish reason is
// an error: the reply may have been cut short. So is one that ended with MALFORMED_FUNCTION_CALL
// (IsMalformedCall), with Gemini's finish message.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	if d.malformed {
		return nil, d.withUsage(malformedCall(d.malformedText))
	}
	if d.finish == nil {
		return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
	}
	var out []llm.Event
	out = d.closeItem(&d.think, out)
	out = d.closeItem(&d.text, out)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: d.finish, Usage: d.usage, Sources: d.sources, CodeExecutions: d.codeExecs}), nil
}

// DecodeResponse turns an upstream non-streamed GenerateContentResponse into the events a stream
// of it would have produced. The first event carries the whole body in Raw, so a Gemini client
// gets it unchanged. A reply holding an error object becomes an *llm.Error; a reply with neither
// a finish reason nor a prompt block counts as finished with reason other, and one that ended with
// MALFORMED_FUNCTION_CALL is the error Close gives it.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	events, _, err := DecodeResponseLosses(body)
	return events, err
}

// DecodeResponseLosses turns an upstream non-streamed GenerateContentResponse into events and
// returns what the reply had that the model could not carry. It is the non-streamed counterpart
// of StreamDecoder.Losses.
func DecodeResponseLosses(body []byte) ([]llm.Event, []llm.Loss, error) {
	var c chunk
	if err := json.Unmarshal(body, &c); err != nil || rawjson.Object(body) == nil {
		return nil, nil, malformed("upstream sent a malformed response", body)
	}
	if !isNull(c.Error) {
		return nil, nil, errorOf(c.Error, body)
	}
	if len(c.Candidates) > 0 {
		var c0 rawCandidate
		if err := json.Unmarshal(c.Candidates[0], &c0); err != nil {
			return nil, nil, malformed("upstream sent a malformed response", body)
		}
	}
	d := NewStreamDecoder()
	out := d.decode(c, nil)
	out[0].Raw = append(json.RawMessage(nil), body...)
	if d.finish == nil {
		d.finish = &llm.Finish{Reason: llm.FinishOther}
	}
	end, err := d.Close()
	if err != nil {
		return nil, d.Losses(), err
	}
	return append(out, end...), d.Losses(), nil
}

// isGoogleError reports whether raw is a Google error body: {"error": {...}}.
func isGoogleError(raw json.RawMessage) bool {
	m := rawjson.Object(raw)
	return m != nil && rawjson.Object(m["error"]) != nil && strings.TrimSpace(string(m["error"])) != ""
}
