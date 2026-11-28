package openairesponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

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

func extractContentCitations(content []outputContent, budget *llm.CiteBudget) []llm.Citation {
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
						if !budget.AddCitation(c) {
							return out
						}
						out = append(out, c)
					}
				}
			}
		}
		runeOffset += partRunes
	}
	return out
}

func extractWebSearchCallSources(raw json.RawMessage, seen map[string]bool, out []llm.Source, budget *llm.CiteBudget) []llm.Source {
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
		src := llm.Source{URL: u, Title: title}
		if !budget.AddSource(src) {
			return
		}
		seen[u] = true
		out = append(out, src)
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

const maxCodeExecInput = 1 << 20

func extractCodeInterpreterCall(raw json.RawMessage, fallbackCode string) (id string, ce llm.CodeExecution, ok bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return "", llm.CodeExecution{}, false
	}
	var item struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Status  string          `json:"status"`
		Code    *string         `json:"code"`
		Outputs json.RawMessage `json:"outputs"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Type != "code_interpreter_call" {
		return "", llm.CodeExecution{}, false
	}
	var outputs []json.RawMessage
	if len(item.Outputs) > 0 && string(item.Outputs) != "null" {
		if json.Unmarshal(item.Outputs, &outputs) != nil {
			return "", llm.CodeExecution{}, false
		}
	} else if item.Status == "completed" {
		return "", llm.CodeExecution{}, false
	}
	var failed bool
	switch item.Status {
	case "completed":
	case "failed", "incomplete":
		failed = true
	default:
		return "", llm.CodeExecution{}, false
	}
	code := fallbackCode
	if item.Code != nil && *item.Code != "" {
		code = *item.Code
	}
	var out strings.Builder
	filesOmitted := false
	for _, oRaw := range outputs {
		if len(oRaw) == 0 || oRaw[0] != '{' {
			continue
		}
		var o struct {
			Type string `json:"type"`
			Logs string `json:"logs"`
		}
		if json.Unmarshal(oRaw, &o) != nil {
			continue
		}
		switch o.Type {
		case "logs":
			if o.Logs == "" {
				continue
			}
			if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
				out.WriteByte('\n')
			}
			out.WriteString(o.Logs)
		case "image":
			filesOmitted = true
		}
	}
	if code == "" && out.Len() == 0 && !failed && !filesOmitted {
		return "", llm.CodeExecution{}, false
	}
	return item.ID, llm.CodeExecution{
		Code:         code,
		Language:     "python",
		Output:       out.String(),
		Failed:       failed,
		FilesOmitted: filesOmitted,
	}, true
}

func codeInterpreterCallID(raw json.RawMessage) (id string, ok bool) {
	var item struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if len(raw) == 0 || raw[0] != '{' {
		return "", false
	}
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal(raw, &item); err != nil && !errors.As(err, &typeErr) {
		return "", false
	}
	return item.ID, item.Type == "code_interpreter_call"
}

func (d *StreamDecoder) noteCodeCall(raw json.RawMessage, index int, fallback string, over bool) {
	id, isCall := codeInterpreterCallID(raw)
	if !isCall {
		return
	}
	key := id
	if key == "" {
		key = fmt.Sprintf("#%d", index)
	}
	if d.seenCode[key] {
		return
	}
	if over {
		d.codeOverKeys[key] = true
	}
	over = d.codeOverKeys[key]
	_, ce, ok := extractCodeInterpreterCall(raw, fallback)
	if ok && over && ce.Code == "" {
		ok = false
	}
	if !ok {
		d.codeUnread[key] = true
		return
	}
	d.seenCode[key] = true
	delete(d.codeUnread, key)
	d.codeExecs.Append(ce)
}

func extractImageGenerationCall(raw json.RawMessage) (id string, media *llm.OutputMedia, problem string, isImg bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return "", nil, "", false
	}
	var item struct {
		ID           string  `json:"id"`
		Type         string  `json:"type"`
		Status       string  `json:"status"`
		Result       *string `json:"result"`
		OutputFormat string  `json:"output_format"`
	}
	if json.Unmarshal(raw, &item) != nil || item.Type != "image_generation_call" {
		return "", nil, "", false
	}
	if item.Status == "failed" {
		return item.ID, nil, "image_generation_call failed", true
	}
	if item.Result == nil || *item.Result == "" {
		return item.ID, nil, "empty image_generation_call result", true
	}
	data := *item.Result
	if !llm.Base64Valid(data) {
		return item.ID, nil, "invalid base64 data", true
	}
	var mt string
	switch strings.ToLower(strings.TrimSpace(item.OutputFormat)) {
	case "":
		mt = llm.SniffImageMediaType(data)
	case "png":
		mt = "image/png"
	case "jpeg", "jpg":
		mt = "image/jpeg"
	case "webp":
		mt = "image/webp"
	case "gif":
		mt = "image/gif"
	case "svg":
		mt = "image/svg+xml"
	default:
		mt = "image/" + strings.ToLower(strings.TrimSpace(item.OutputFormat))
	}
	kind, prob := llm.SafeOutputMediaKind(mt)
	if prob != "" {
		return item.ID, nil, prob, true
	}
	return item.ID, &llm.OutputMedia{Kind: kind, MediaType: mt, Data: data}, "", true
}

func opaque(it outputItem) *llm.Opaque {
	if it.Type != "reasoning" || it.EncryptedContent == "" {
		return nil
	}
	return &llm.Opaque{Format: format, Data: it.EncryptedContent}
}

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
	Type         string          `json:"type"`
	OutputIndex  int             `json:"output_index"`
	ContentIndex int             `json:"content_index"`
	Item         json.RawMessage `json:"item"`
	Delta        string          `json:"delta"`
	Annotation   json.RawMessage `json:"annotation"`
	Response     json.RawMessage `json:"response"`
	Code         any             `json:"code"`
	Message      string          `json:"message"`
}

type StreamDecoder struct {
	started      bool
	done         bool
	next         int
	items        map[int]int
	textParts    map[int][]int
	annRaws      map[int][]partAnnotation
	sources      []llm.Source
	seenSrc      map[string]bool
	cites        llm.CiteBudget
	annRawSize   int
	finishCut    []llm.Loss
	codeBuf      map[int]*strings.Builder
	codeOver     map[int]bool
	codeExecs    llm.CodeRuns
	seenCode     map[string]bool
	codeUnread   map[string]bool
	codeOverKeys map[string]bool
	mediaBytes   int
	seenMedia    map[string]bool
	losses       []llm.Loss
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		items:        map[int]int{},
		textParts:    map[int][]int{},
		annRaws:      map[int][]partAnnotation{},
		seenSrc:      map[string]bool{},
		codeBuf:      map[int]*strings.Builder{},
		codeOver:     map[int]bool{},
		seenCode:     map[string]bool{},
		codeUnread:   map[string]bool{},
		codeOverKeys: map[string]bool{},
		seenMedia:    map[string]bool{},
	}
}

func (d *StreamDecoder) Losses() []llm.Loss {
	return d.cites.WithLoss(append(slices.Clip(d.losses), d.finishCut...))
}

func (d *StreamDecoder) start(out []llm.Event, info llm.ResponseInfo, raw json.RawMessage) []llm.Event {
	d.started = true
	info.Format = format
	return append(out, llm.Event{Type: llm.EventResponseStart, Response: &info, Raw: raw})
}

const maxContentParts = 1024

type partAnnotation struct {
	raw  json.RawMessage
	part int
}

func (d *StreamDecoder) messageCitations(idx int, it *outputItem) []llm.Citation {
	var cits []llm.Citation
	if it != nil {
		cits = extractContentCitations(it.Content, &d.cites)
	}
	if len(cits) == 0 && len(d.annRaws[idx]) > 0 {
		parts := d.textParts[idx]
		for _, ar := range d.annRaws[idx] {
			offset, runes := 0, -1
			for i, n := range parts {
				if i < ar.part {
					offset += n
				} else if i == ar.part {
					runes = n
				}
			}
			if c, ok := extractURLCitation(ar.raw, offset, runes); ok {
				cits = append(cits, c)
			}
		}
		cits = d.cites.KeepCitations(cits)
	}
	d.releaseItem(idx)
	return cits
}

func (d *StreamDecoder) releaseItem(idx int) {
	for _, ar := range d.annRaws[idx] {
		d.annRawSize -= len(ar.raw)
	}
	delete(d.annRaws, idx)
	delete(d.textParts, idx)
}

func (d *StreamDecoder) closeAll(out []llm.Event, final []json.RawMessage) []llm.Event {
	type openItem struct{ output, idx int }
	open := make([]openItem, 0, len(d.items))
	for o, idx := range d.items {
		open = append(open, openItem{o, idx})
	}
	slices.SortFunc(open, func(a, b openItem) int { return a.idx - b.idx })
	for _, o := range open {
		end := llm.Event{Type: llm.EventItemEnd, Index: o.idx}
		var it outputItem
		if o.output < len(final) {
			_ = json.Unmarshal(final[o.output], &it)
		}
		if it.Type == "message" {
			end.Citations = d.messageCitations(o.idx, &it)
		} else if it.Type == "" {
			end.Citations = d.messageCitations(o.idx, nil)
		} else {
			d.releaseItem(o.idx)
		}
		out = append(out, end)
	}
	clear(d.items)
	return out
}

func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openairesponses: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
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
		out = d.start(out, info, raw)
		raw = nil
	}
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
		if ev.Type == "response.output_text.delta" && ev.ContentIndex >= 0 && ev.ContentIndex < maxContentParts {
			parts := d.textParts[idx]
			for len(parts) <= ev.ContentIndex {
				parts = append(parts, 0)
			}
			parts[ev.ContentIndex] += utf8.RuneCountInString(ev.Delta)
			d.textParts[idx] = parts
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
			if d.annRawSize+len(ev.Annotation) > llm.MaxCitationBytes {
				d.cites.LeftOut()
			} else {
				d.annRawSize += len(ev.Annotation)
				d.annRaws[idx] = append(d.annRaws[idx], partAnnotation{append(json.RawMessage(nil), ev.Annotation...), ev.ContentIndex})
			}
		}
		return native(), nil
	case "response.code_interpreter_call_code.delta":
		if ev.Delta != "" && !d.codeOver[ev.OutputIndex] {
			buf := d.codeBuf[ev.OutputIndex]
			if buf == nil {
				buf = &strings.Builder{}
				d.codeBuf[ev.OutputIndex] = buf
			}
			if buf.Len()+len(ev.Delta) > maxCodeExecInput {
				d.codeOver[ev.OutputIndex] = true
				delete(d.codeBuf, ev.OutputIndex)
			} else {
				buf.WriteString(ev.Delta)
			}
		}
		return native(), nil
	case "response.code_interpreter_call_code.done":
		if code, ok := ev.Code.(string); ok && code != "" {
			if len(code) > maxCodeExecInput {
				d.codeOver[ev.OutputIndex] = true
				delete(d.codeBuf, ev.OutputIndex)
			} else {
				buf := &strings.Builder{}
				buf.WriteString(code)
				d.codeBuf[ev.OutputIndex] = buf
				delete(d.codeOver, ev.OutputIndex)
			}
		}
		return native(), nil
	case "response.output_item.done":
		idx, open := d.items[ev.OutputIndex]
		if !open {
			d.sources = extractWebSearchCallSources(ev.Item, d.seenSrc, d.sources, &d.cites)
			fallback := ""
			if buf := d.codeBuf[ev.OutputIndex]; buf != nil {
				fallback = buf.String()
				delete(d.codeBuf, ev.OutputIndex)
			}
			over := d.codeOver[ev.OutputIndex]
			delete(d.codeOver, ev.OutputIndex)
			d.noteCodeCall(ev.Item, ev.OutputIndex, fallback, over)
			if id, media, problem, isImg := extractImageGenerationCall(ev.Item); isImg {
				key := id
				if key == "" {
					key = fmt.Sprintf("#%d", ev.OutputIndex)
				}
				if !d.seenMedia[key] {
					d.seenMedia[key] = true
					path := fmt.Sprintf("output[%d]", ev.OutputIndex)
					switch {
					case problem != "":
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: problem})
					case len(media.Data) > llm.MaxOutputMediaPartBytes:
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media part exceeds per-part size limit"})
					case d.mediaBytes+len(media.Data) > llm.MaxOutputMediaReplyBytes:
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media exceeds per-reply size limit"})
					case len(media.Data)+len(raw) > protocol.MaxFrame-protocol.FrameHeadroom:
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media with its upstream event would not fit one protocol frame"})
					default:
						d.mediaBytes += len(media.Data)
						return append(out, llm.Event{Type: llm.EventMedia, Media: media, Raw: raw}), nil
					}
				}
			}
			return native(), nil
		}
		delete(d.items, ev.OutputIndex)
		var it outputItem
		_ = json.Unmarshal(ev.Item, &it)
		end := llm.Event{Type: llm.EventItemEnd, Index: idx, Opaque: opaque(it), Raw: raw}
		if it.Type == "message" {
			end.Citations = d.messageCitations(idx, &it)
		} else {
			d.releaseItem(idx)
		}
		return append(out, end), nil
	case "response.completed", "response.incomplete":
		d.done = true
		out = d.closeAll(out, r.Output)
		for i, itemRaw := range r.Output {
			d.sources = extractWebSearchCallSources(itemRaw, d.seenSrc, d.sources, &d.cites)
			d.noteCodeCall(itemRaw, i, "", false)
			if id, media, problem, isImg := extractImageGenerationCall(itemRaw); isImg {
				key := id
				if key == "" {
					key = fmt.Sprintf("#%d", i)
				}
				if !d.seenMedia[key] {
					d.seenMedia[key] = true
					path := fmt.Sprintf("output[%d]", i)
					switch {
					case problem != "":
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: problem})
					case len(media.Data) > llm.MaxOutputMediaPartBytes:
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media part exceeds per-part size limit"})
					case d.mediaBytes+len(media.Data) > llm.MaxOutputMediaReplyBytes:
						d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media exceeds per-reply size limit"})
					default:
						d.mediaBytes += len(media.Data)
						out = append(out, llm.Event{Type: llm.EventMedia, Media: media})
					}
				}
			}
		}
		f := finishOf(r)
		if len(d.codeUnread) > 0 {
			d.codeExecs.LeaveOut()
		}
		fin := llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Sources: d.sources, CodeExecutions: d.codeExecs.List(), Raw: raw}
		if l, cut := protocol.FitFinish(&fin); cut {
			d.finishCut = append(d.finishCut, l)
		}
		return append(out, fin), nil
	case "response.failed":
		d.done = true
		e := failure(r, data)
		if raw == nil {
			e.Raw = nil
		}
		return out, e
	}
	return native(), nil
}

func (d *StreamDecoder) Usage() *llm.Usage { return nil }

func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	return nil, &llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"}
}

func DecodeResponse(body []byte) ([]llm.Event, error) {
	events, _, err := DecodeResponseLosses(body)
	return events, err
}

func DecodeResponseLosses(body []byte) ([]llm.Event, []llm.Loss, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, nil, malformed("upstream sent a malformed response", body)
	}
	if r.Status == "failed" {
		return nil, nil, failure(r, body)
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: r.ID, Model: r.Model, Created: r.CreatedAt, Format: format}}}
	next := 0
	var sources []llm.Source
	seenSrc := map[string]bool{}
	var cites llm.CiteBudget
	var codeExecs llm.CodeRuns
	var losses []llm.Loss
	mediaBytes := 0
	add := func(start llm.ItemStart, deltaType, delta string, o *llm.Opaque, cits []llm.Citation) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next, Opaque: o, Citations: cits})
		next++
	}
	for i, raw := range r.Output {
		var it outputItem
		if json.Unmarshal(raw, &it) != nil {
			return nil, nil, malformed("upstream sent a malformed response", body)
		}
		switch kindOf(it) {
		case llm.ItemMessage:
			var text strings.Builder
			for _, c := range it.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				}
			}
			add(llm.ItemStart{Kind: llm.ItemMessage}, llm.EventTextDelta, text.String(), nil, extractContentCitations(it.Content, &cites))
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
			sources = extractWebSearchCallSources(raw, seenSrc, sources, &cites)
			if _, isCall := codeInterpreterCallID(raw); isCall {
				if _, ce, ok := extractCodeInterpreterCall(raw, ""); ok {
					codeExecs.Append(ce)
				} else {
					codeExecs.LeaveOut()
				}
			}
			if _, media, problem, isImg := extractImageGenerationCall(raw); isImg {
				path := fmt.Sprintf("output[%d]", i)
				switch {
				case problem != "":
					losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: problem})
				case len(media.Data) > llm.MaxOutputMediaPartBytes:
					losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media part exceeds per-part size limit"})
				case mediaBytes+len(media.Data) > llm.MaxOutputMediaReplyBytes:
					losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "output media exceeds per-reply size limit"})
				default:
					mediaBytes += len(media.Data)
					out = append(out, llm.Event{Type: llm.EventMedia, Media: media})
					continue
				}
			}
			out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
		}
	}
	f := finishOf(r)
	fin := llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(r.Usage), Sources: sources, CodeExecutions: codeExecs.List(), Raw: append(json.RawMessage(nil), body...)}
	if l, cut := protocol.FitFinish(&fin); cut {
		losses = llm.AddLoss(losses, l)
	}
	return append(out, fin), cites.WithLoss(losses), nil
}

func OpaqueOutput(raw json.RawMessage) []string { return opaqueOutput(raw, false) }

func OpaqueOutputExceptCitations(raw json.RawMessage) []string { return opaqueOutput(raw, true) }

func opaqueOutput(raw json.RawMessage, citationsCarried bool) []string {
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
			if len(c.Annotations) > 0 && !(citationsCarried && allCitations(c.Annotations)) {
				out = append(out, fmt.Sprintf("output[%d].content[%d].annotations", i, j))
			}
		}
	})
	return out
}

func allCitations(anns []json.RawMessage) bool {
	for _, raw := range anns {
		if _, ok := extractURLCitation(raw, 0, 0); !ok {
			return false
		}
	}
	return true
}

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
