package openairesponses

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
)

// maxErrorRaw limits the upstream bytes an error keeps for diagnostics. Longer data is not kept:
// a cut of it could fall inside a credential, whose start core's redaction cannot recognise.
const maxErrorRaw = 1 << 20

func malformed(msg string, data []byte) *llm.Error {
	e := &llm.Error{Code: llm.CodeUpstream, Message: msg}
	if len(data) <= maxErrorRaw {
		e.Raw = llm.RawJSON(data)
	}
	return e
}

type usage struct {
	InputTokens        *int64 `json:"input_tokens"`
	OutputTokens       *int64 `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens     *int64 `json:"cached_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u usage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.InputTokensDetails != nil {
		out.CachedInputTokens = u.InputTokensDetails.CachedTokens
		out.CacheWriteInputTokens = u.InputTokensDetails.CacheWriteTokens
	}
	if u.OutputTokensDetails != nil {
		out.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
	return out
}

// response is the part of a Response object the decoder reads.
type response struct {
	ID                string            `json:"id"`
	Model             string            `json:"model"`
	CreatedAt         int64             `json:"created_at"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	Usage             json.RawMessage   `json:"usage"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type outputContent struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Refusal     string          `json:"refusal"`
	Annotations json.RawMessage `json:"annotations"`
}

type outputItem struct {
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Content   []outputContent `json:"content"`
	Summary   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	EncryptedContent string `json:"encrypted_content"`
}

// extractURLCitation reads one Responses url_citation annotation. If start_index/end_index are
// valid within [0, maxRunes], they are shifted by runeOffset; otherwise the citation is kept
// without a range so a bad offset never produces a wrong range.
func extractURLCitation(raw json.RawMessage, runeOffset, maxRunes int) (llm.Citation, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return llm.Citation{}, false
	}
	var ann struct {
		Type       string `json:"type"`
		URL        string `json:"url"`
		Title      string `json:"title"`
		StartIndex *int   `json:"start_index"`
		EndIndex   *int   `json:"end_index"`
	}
	if json.Unmarshal(raw, &ann) != nil || ann.Type != "url_citation" || !llm.ValidCitationURL(ann.URL) {
		return llm.Citation{}, false
	}
	c := llm.Citation{URL: ann.URL, Title: ann.Title}
	if ann.StartIndex != nil && ann.EndIndex != nil && *ann.StartIndex >= 0 && *ann.EndIndex >= *ann.StartIndex && *ann.EndIndex <= maxRunes {
		s, e := runeOffset+*ann.StartIndex, runeOffset+*ann.EndIndex
		c.StartIndex, c.EndIndex = &s, &e
	}
	return c, true
}

// extractContentCitations reads url_citation annotations across all output_text parts of a
// Responses message item, shifting each part's rune offsets into the concatenated message text.
func extractContentCitations(content []outputContent) []llm.Citation {
	var out []llm.Citation
	runeOffset := 0
	for _, part := range content {
		if part.Type != "output_text" {
			continue
		}
		partRunes := utf8.RuneCountInString(part.Text)
		if len(part.Annotations) > 0 && part.Annotations[0] == '[' {
			var list []json.RawMessage
			if json.Unmarshal(part.Annotations, &list) == nil {
				for _, raw := range list {
					if c, ok := extractURLCitation(raw, runeOffset, partRunes); ok {
						out = append(out, c)
					}
				}
			}
		}
		runeOffset += partRunes
	}
	return out
}

// extractWebSearchCallSources reads URL sources from a Responses web_search_call item's
// action.sources and results arrays, deduplicated in first-seen order.
func extractWebSearchCallSources(raw json.RawMessage, seen map[string]bool, out []llm.Source) []llm.Source {
	var ws struct {
		Type   string `json:"type"`
		Action *struct {
			Sources []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"sources"`
		} `json:"action"`
		Results json.RawMessage `json:"results"`
	}
	if json.Unmarshal(raw, &ws) != nil || ws.Type != "web_search_call" {
		return out
	}
	add := func(u, title string) {
		if !llm.ValidCitationURL(u) || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, llm.Source{URL: u, Title: title})
	}
	if ws.Action != nil {
		for _, s := range ws.Action.Sources {
			add(s.URL, s.Title)
		}
	}
	if len(ws.Results) > 0 && ws.Results[0] == '[' {
		var res []struct {
			Type  string `json:"type"`
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		if json.Unmarshal(ws.Results, &res) == nil {
			for _, r := range res {
				if r.Type != "web_search_tool_result_error" {
					add(r.URL, r.Title)
				}
			}
		}
	}
	return out
}

// opaque returns a reasoning item's encrypted content as the opaque data its item.end carries, or
// nil when it has none.
func opaque(it outputItem) *llm.Opaque {
	if it.Type != "reasoning" || it.EncryptedContent == "" {
		return nil
	}
	return &llm.Opaque{Format: format, Data: it.EncryptedContent}
}

// kindOf returns the model's item kind for a Responses output item, or "" when the model has no
// such item.
func kindOf(it outputItem) string {
	switch it.Type {
	case "message":
		return llm.ItemMessage
	case "reasoning":
		return llm.ItemReasoning
	case "function_call":
		return llm.ItemToolCall
	}
	return ""
}

// finishOf says why a terminal Response ended.
func finishOf(r response) llm.Finish {
	switch r.Status {
	case "incomplete":
		reason := ""
		if r.IncompleteDetails != nil {
			reason = r.IncompleteDetails.Reason
		}
		switch reason {
		case "max_output_tokens":
			return llm.Finish{Reason: llm.FinishLength, RawReason: reason}
		case "content_filter":
			return llm.Finish{Reason: llm.FinishContentFilter, RawReason: reason}
		}
		return llm.Finish{Reason: llm.FinishOther, RawReason: reason}
	case "completed":
		for _, raw := range r.Output {
			var it outputItem
			if json.Unmarshal(raw, &it) == nil && it.Type == "function_call" {
				return llm.Finish{Reason: llm.FinishToolCalls, RawReason: r.Status}
			}
		}
		return llm.Finish{Reason: llm.FinishStop, RawReason: r.Status}
	}
	return llm.Finish{Reason: llm.FinishOther, RawReason: r.Status}
}

func failure(r response, raw []byte) *llm.Error {
	e := &llm.Error{Code: llm.CodeUpstream, Message: "upstream reported the response failed", Raw: llm.RawJSON(raw), Usage: decodeUsage(r.Usage)}
	if r.Error != nil {
		if r.Error.Message != "" {
			e.Message = r.Error.Message
		}
		e.ProviderCode = r.Error.Code
	}
	return e
}

type streamEvent struct {
	Type        string          `json:"type"`
	OutputIndex int             `json:"output_index"`
	Item        json.RawMessage `json:"item"`
	Delta       string          `json:"delta"`
	Annotation  json.RawMessage `json:"annotation"`
	Response    json.RawMessage `json:"response"`
	Code        any             `json:"code"`
	Message     string          `json:"message"`
}

// StreamDecoder turns the events of an upstream Responses stream into model events. Every model
// event carries the upstream event it came from in Raw; upstream events the model has no place
// for (response.in_progress, content parts, done events, hosted tool items) become native
// events. Use one decoder per stream.
type StreamDecoder struct {
	started  bool
	done     bool
	next     int
	items    map[int]int               // upstream output_index -> open model item index
	textLens map[int]int               // model item index -> rune count of text deltas seen
	annRaws  map[int][]json.RawMessage // model item index -> raw annotations from annotation.added
	sources  []llm.Source
	seenSrc  map[string]bool
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		items:    map[int]int{},
		textLens: map[int]int{},
		annRaws:  map[int][]json.RawMessage{},
		seenSrc:  map[string]bool{},
	}
}

func (d *StreamDecoder) start(out []llm.Event, info llm.ResponseInfo, raw json.RawMessage) []llm.Event {
	d.started = true
	info.Format = format
	return append(out, llm.Event{Type: llm.EventResponseStart, Response: &info, Raw: raw})
}

// closeAll closes items the upstream left open; the events it adds carry no Raw.
func (d *StreamDecoder) closeAll(out []llm.Event) []llm.Event {
	open := make([]int, 0, len(d.items))
	for _, idx := range d.items {
		open = append(open, idx)
	}
	slices.Sort(open)
	for _, idx := range open {
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: idx})
	}
	clear(d.items)
	return out
}

// Decode takes the data of one SSE event and returns the events it produces. A terminal
// upstream event (response.completed, response.incomplete, response.failed or error) ends the
// stream; response.failed and error become an *llm.Error.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openairesponses: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
		// Not part of Responses; some compatible servers send it after the terminal event.
		return nil, nil
	}
	var ev streamEvent
	if err := json.Unmarshal(data, &ev); err != nil || ev.Type == "" {
		return nil, malformed("upstream sent a malformed stream event", data)
	}
	raw := append(json.RawMessage(nil), data...)
	var out []llm.Event
	if ev.Type == "error" {
		d.done = true
		e := &llm.Error{Code: llm.CodeUpstream, Message: ev.Message, Raw: raw}
		if e.Message == "" {
			e.Message = "upstream reported an error mid-stream"
		}
		if ev.Code != nil {
			e.ProviderCode = fmt.Sprint(ev.Code)
		}
		return nil, e
	}
	var r response
	if len(ev.Response) > 0 {
		_ = json.Unmarshal(ev.Response, &r)
	}
	if !d.started {
		info := llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt}
		if ev.Type == "response.created" {
			return d.start(out, info, raw), nil
		}
		// The stream did not open with response.created. The first event's raw goes on
		// response.start, so a Responses client still gets every upstream event once.
		out = d.start(out, info, raw)
		raw = nil
	}
	// native passes on an upstream event the model has no place for, unless its raw already went
	// on response.start.
	native := func() []llm.Event {
		if raw == nil {
			return out
		}
		return append(out, llm.Event{Type: llm.EventNative, Raw: raw})
	}
	switch ev.Type {
	case "response.output_item.added":
		var it outputItem
		_ = json.Unmarshal(ev.Item, &it)
		kind := kindOf(it)
		if kind == "" || (kind == llm.ItemToolCall && (it.CallID == "" || it.Name == "")) {
			return native(), nil
		}
		if _, open := d.items[ev.OutputIndex]; open {
			return nil, malformed("upstream opened output item twice", data)
		}
		idx := d.next
		d.next++
		d.items[ev.OutputIndex] = idx
		start := llm.ItemStart{Kind: kind}
		if kind == llm.ItemToolCall {
			start.CallID, start.Name = it.CallID, it.Name
		}
		return append(out, llm.Event{Type: llm.EventItemStart, Index: idx, Item: &start, Raw: raw}), nil
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.function_call_arguments.delta":
		idx, open := d.items[ev.OutputIndex]
		if !open {
			return native(), nil
		}
		if ev.Type == "response.output_text.delta" {
			d.textLens[idx] += utf8.RuneCountInString(ev.Delta)
		}
		t := map[string]string{
			"response.output_text.delta":             llm.EventTextDelta,
			"response.reasoning_summary_text.delta":  llm.EventReasoningDelta,
			"response.reasoning_text.delta":          llm.EventReasoningDelta,
			"response.function_call_arguments.delta": llm.EventToolInputDelta,
		}[ev.Type]
		return append(out, llm.Event{Type: t, Index: idx, Delta: ev.Delta, Raw: raw}), nil
	case "response.output_text.annotation.added":
		if idx, open := d.items[ev.OutputIndex]; open && len(ev.Annotation) > 0 {
			d.annRaws[idx] = append(d.annRaws[idx], append(json.RawMessage(nil), ev.Annotation...))
		}
		return native(), nil
	case "response.output_item.done":
		idx, open := d.items[ev.OutputIndex]
		if !open {
			d.sources = extractWebSearchCallSources(ev.Item, d.seenSrc, d.sources)
			return native(), nil
		}
		delete(d.items, ev.OutputIndex)
		var it outputItem
		_ = json.Unmarshal(ev.Item, &it)
		end := llm.Event{Type: llm.EventItemEnd, Index: idx, Opaque: opaque(it), Raw: raw}
		if it.Type == "message" {
			cits := extractContentCitations(it.Content)
			if len(cits) == 0 && len(d.annRaws[idx]) > 0 {
				maxRunes := d.textLens[idx]
				for _, ar := range d.annRaws[idx] {
					if c, ok := extractURLCitation(ar, 0, maxRunes); ok {
						cits = append(cits, c)
					}
				}
			}
			end.Citations = cits
		}
		delete(d.annRaws, idx)
		delete(d.textLens, idx)
		return append(out, end), nil
	case "response.completed", "response.incomplete":
		d.done = true
		out = d.closeAll(out)
		for _, itemRaw := range r.Output {
			d.sources = extractWebSearchCallSources(itemRaw, d.seenSrc, d.sources)
		}
		f := finishOf(r)
		return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Sources: d.sources, Raw: raw}), nil
	case "response.failed":
		d.done = true
		e := failure(r, data)
		if raw == nil {
			e.Raw = nil // already on response.start
		}
		return out, e
	}
	return native(), nil
}

// Usage returns the usage the upstream reported before the stream failed. A Responses stream
// reports usage only in its terminal event, which ends the stream, so there is none to return;
// a response.failed event's usage is on its error.
func (d *StreamDecoder) Usage() *llm.Usage { return nil }

// Close is called when the upstream stream ends. A stream that ends before its terminal event
// is an error: the reply may have been cut short.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"}
}

// DecodeResponse turns an upstream non-streamed Response object into the events a stream of it
// would have produced. The finish event carries the whole body in Raw, so a Responses client
// gets it unchanged.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, malformed("upstream sent a malformed response", body)
	}
	if r.Status == "failed" {
		return nil, failure(r, body)
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt, Format: format}}}
	next := 0
	var sources []llm.Source
	seenSrc := map[string]bool{}
	add := func(start llm.ItemStart, deltaType, delta string, o *llm.Opaque, cits []llm.Citation) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next, Opaque: o, Citations: cits})
		next++
	}
	for _, raw := range r.Output {
		var it outputItem
		if json.Unmarshal(raw, &it) != nil {
			return nil, malformed("upstream sent a malformed response", body)
		}
		switch kindOf(it) {
		case llm.ItemMessage:
			var text strings.Builder
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
			add(llm.ItemStart{Kind: llm.ItemMessage}, llm.EventTextDelta, text.String(), nil, extractContentCitations(it.Content))
		case llm.ItemReasoning:
			var text strings.Builder
			for _, s := range it.Summary {
				text.WriteString(s.Text)
			}
			add(llm.ItemStart{Kind: llm.ItemReasoning}, llm.EventReasoningDelta, text.String(), opaque(it), nil)
		case llm.ItemToolCall:
			if it.CallID == "" || it.Name == "" {
				out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
				continue
			}
			add(llm.ItemStart{Kind: llm.ItemToolCall, CallID: it.CallID, Name: it.Name}, llm.EventToolInputDelta, it.Arguments, nil, nil)
		default:
			sources = extractWebSearchCallSources(raw, seenSrc, sources)
			out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
		}
	}
	f := finishOf(r)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Sources: sources, Raw: append(json.RawMessage(nil), body...)}), nil
}

// OpaqueOutput lists, by path in the Response, the output data a client in another format has no
// place for: a reasoning item's encrypted content, an assistant message's phase (commentary or
// final_answer, which Codex asks clients to send back) and the annotations on output text. raw is
// an output_item.done event, a terminal event or a Response object.
func OpaqueOutput(raw json.RawMessage) []string {
	var out []string
	outputItems(raw, func(i int, item json.RawMessage) {
		var it struct {
			EncryptedContent string `json:"encrypted_content"`
			Phase            string `json:"phase"`
			Content          []struct {
				Annotations []json.RawMessage `json:"annotations"`
			} `json:"content"`
		}
		if json.Unmarshal(item, &it) != nil {
			return
		}
		if it.EncryptedContent != "" {
			out = append(out, fmt.Sprintf("output[%d].encrypted_content", i))
		}
		if it.Phase != "" {
			out = append(out, fmt.Sprintf("output[%d].phase", i))
		}
		for j, c := range it.Content {
			if len(c.Annotations) > 0 {
				out = append(out, fmt.Sprintf("output[%d].content[%d].annotations", i, j))
			}
		}
	})
	return out
}

// outputItems calls fn with each output item in raw, by its index in the Response: the item of a
// response.output_item.done event, or every item of a Response object or of an event that holds
// one.
func outputItems(raw json.RawMessage, fn func(i int, item json.RawMessage)) {
	var ev struct {
		Type        string            `json:"type"`
		Object      string            `json:"object"`
		OutputIndex int               `json:"output_index"`
		Item        json.RawMessage   `json:"item"`
		Response    *response         `json:"response"`
		Output      []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch {
	case ev.Type == "response.output_item.done":
		fn(ev.OutputIndex, ev.Item)
	case ev.Response != nil:
		for i, item := range ev.Response.Output {
			fn(i, item)
		}
	case ev.Object == "response":
		for i, item := range ev.Output {
			fn(i, item)
		}
	}
}
