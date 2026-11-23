package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/internal/jsonscan"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
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
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// decodeUsage reads Messages usage. Anthropic's input_tokens counts only the input after the last
// cache breakpoint; cache reads and writes are counted apart from it, so the model's InputTokens
// is their sum.
func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u usage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{OutputTokens: u.OutputTokens, CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteInputTokens: u.CacheCreationInputTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.InputTokens != nil || u.CacheReadInputTokens != nil || u.CacheCreationInputTokens != nil {
		var total int64
		for _, p := range []*int64{u.InputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens} {
			if p != nil {
				total += *p
			}
		}
		out.InputTokens = &total
	}
	// Claude's output_tokens already includes the thinking tokens, so they are the reasoning part
	// of it, never more than it (as LiteLLM reads this field).
	if d := u.OutputTokensDetails; d != nil && d.ThinkingTokens != nil {
		thinking := max(0, *d.ThinkingTokens)
		if u.OutputTokens != nil && thinking > *u.OutputTokens {
			thinking = *u.OutputTokens
		}
		out.ReasoningTokens = &thinking
	}
	return out
}

// mergeUsage overlays the usage of a message_delta on what the stream reported so far: Messages
// stream usage is cumulative, and a field a later event leaves out keeps its earlier value.
func mergeUsage(have, next json.RawMessage) json.RawMessage {
	if len(have) == 0 || string(have) == "null" {
		return next
	}
	m := rawjson.Object(have)
	n := rawjson.Object(next)
	if n == nil {
		return have
	}
	if m == nil {
		return next
	}
	for k, v := range n {
		if string(v) != "null" {
			m[k] = v
		}
	}
	return rawjson.Encode(m)
}

// finishOf maps a stop reason to the model's. The raw value always travels with it.
func finishOf(reason string) llm.Finish {
	f := llm.Finish{Reason: llm.FinishOther, RawReason: reason}
	switch reason {
	case "end_turn", "stop_sequence":
		f.Reason = llm.FinishStop
	case "max_tokens", "model_context_window_exceeded":
		f.Reason = llm.FinishLength
	case "tool_use":
		f.Reason = llm.FinishToolCalls
	case "refusal":
		f.Reason = llm.FinishContentFilter
	}
	return f
}

// kindOf returns the model's item kind for a content block type, or "" when the model has no such
// item.
func kindOf(typ string) string {
	switch typ {
	case "text":
		return llm.ItemMessage
	case "thinking":
		return llm.ItemReasoning
	case "tool_use":
		return llm.ItemToolCall
	}
	return ""
}

// errorOf reads an Anthropic error object ({"type": ..., "message": ...}).
func errorOf(raw json.RawMessage, fallback string) *llm.Error {
	var e struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &llm.Error{Code: llm.CodeUpstream, Message: e.Message, ProviderCode: e.Type}
	if out.Message == "" {
		out.Message = fallback
	}
	return out
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	Think     string          `json:"thinking"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	Citations json.RawMessage `json:"citations"`
}

// maxCodeExecInput bounds the streamed input_json_delta buffered for a single code execution
// server_tool_use block so a broken stream cannot grow memory without bound. It holds the most
// JSON a run's code of llm.MaxCodeExecutionCodeBytes can take (six bytes per byte, as \u0000), so a
// stream and the same reply not streamed keep the same runs.
const maxCodeExecInput = 6*llm.MaxCodeExecutionCodeBytes + 1<<10

type pendingCodeCall struct {
	id           string
	name         string
	initialInput json.RawMessage
	partial      strings.Builder
	overflow     bool
}

type decodedCodeCall struct {
	name string
	code string
	lang string
}

func isCodeExecToolName(name string) bool {
	switch name {
	case "code_execution", "bash_code_execution", "text_editor_code_execution":
		return true
	}
	return false
}

func isCodeExecResultBlock(typ string) bool {
	switch typ {
	case "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
		return true
	}
	return false
}

func decodeCodeExecInput(name string, raw json.RawMessage) (code, lang string, ok bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return "", "", false
	}
	switch name {
	case "code_execution":
		var in struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Code == "" {
			return "", "", false
		}
		return in.Code, "python", true
	case "bash_code_execution":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Command == "" {
			return "", "", false
		}
		return in.Command, "bash", true
	case "text_editor_code_execution":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &in) != nil || in.Command == "" {
			return "", "", false
		}
		compact, err := compactJSON(raw)
		if err != nil {
			return "", "", false
		}
		return compact, "text_editor", true
	}
	return "", "", false
}

func joinStdoutStderr(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	case strings.HasSuffix(stdout, "\n"):
		return stdout + stderr
	default:
		return stdout + "\n" + stderr
	}
}

func decodeCodeExecResult(blockType string, call decodedCodeCall, raw json.RawMessage) (llm.CodeExecution, bool) {
	wantName := map[string]string{
		"code_execution_tool_result":             "code_execution",
		"bash_code_execution_tool_result":        "bash_code_execution",
		"text_editor_code_execution_tool_result": "text_editor_code_execution",
	}[blockType]
	if wantName == "" || call.name != wantName || len(raw) == 0 || raw[0] != '{' {
		return llm.CodeExecution{}, false
	}
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return llm.CodeExecution{}, false
	}
	switch head.Type {
	case "code_execution_result", "bash_code_execution_result":
		var res struct {
			Stdout     string            `json:"stdout"`
			Stderr     string            `json:"stderr"`
			ReturnCode *int              `json:"return_code"`
			Content    []json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &res) != nil {
			return llm.CodeExecution{}, false
		}
		return llm.CodeExecution{
			Code:         call.code,
			Language:     call.lang,
			Output:       joinStdoutStderr(res.Stdout, res.Stderr),
			Failed:       res.ReturnCode != nil && *res.ReturnCode != 0,
			FilesOmitted: len(res.Content) > 0,
		}, true
	case "code_execution_tool_result_error", "bash_code_execution_tool_result_error", "text_editor_code_execution_tool_result_error":
		var res struct {
			ErrorCode string `json:"error_code"`
		}
		if json.Unmarshal(raw, &res) != nil {
			return llm.CodeExecution{}, false
		}
		return llm.CodeExecution{
			Code:     call.code,
			Language: call.lang,
			Output:   res.ErrorCode,
			Failed:   true,
		}, true
	case "text_editor_code_execution_view_result":
		var res struct {
			FileType string `json:"file_type"`
			Content  string `json:"content"`
		}
		if json.Unmarshal(raw, &res) != nil {
			return llm.CodeExecution{}, false
		}
		return llm.CodeExecution{
			Code:         call.code,
			Language:     call.lang,
			Output:       res.Content,
			FilesOmitted: res.FileType != "" && res.FileType != "text",
		}, true
	case "text_editor_code_execution_create_result":
		return llm.CodeExecution{
			Code:     call.code,
			Language: call.lang,
		}, true
	case "text_editor_code_execution_str_replace_result":
		var res struct {
			Lines []string `json:"lines"`
		}
		if json.Unmarshal(raw, &res) != nil {
			return llm.CodeExecution{}, false
		}
		return llm.CodeExecution{
			Code:     call.code,
			Language: call.lang,
			Output:   strings.Join(res.Lines, "\n"),
		}, true
	}
	return llm.CodeExecution{}, false
}

// extractWebSearchSources reads web_search_result entries from a web_search_tool_result block's
// content array, keeping valid http/https URLs deduplicated in first-seen order.
func extractWebSearchSources(raw json.RawMessage, seen map[string]bool, out []llm.Source, budget *llm.CiteBudget) []llm.Source {
	if len(raw) == 0 || raw[0] != '[' {
		return out
	}
	var entries []struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return out
	}
	for _, e := range entries {
		if e.Type == "web_search_tool_result_error" || !llm.ValidCitationURL(e.URL) || seen[e.URL] {
			continue
		}
		src := llm.Source{URL: e.URL, Title: e.Title}
		if !budget.AddSource(src) {
			break
		}
		seen[e.URL] = true
		out = append(out, src)
	}
	return out
}

// extractCitation reads one Anthropic web_search_result_location citation object.
func extractCitation(raw json.RawMessage) (llm.Citation, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return llm.Citation{}, false
	}
	var c struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Type != "web_search_result_location" || !llm.ValidCitationURL(c.URL) {
		return llm.Citation{}, false
	}
	return llm.Citation{URL: c.URL, Title: c.Title}, true
}

// applyBlockRange sets [0, runeLen) on each citation when runeLen > 0.
func applyBlockRange(cits []llm.Citation, runeLen int) []llm.Citation {
	if len(cits) == 0 || runeLen <= 0 {
		return cits
	}
	for i := range cits {
		s, e := 0, runeLen
		cits[i].StartIndex, cits[i].EndIndex = &s, &e
	}
	return cits
}

// extractCitations reads the web_search_result_location entries from a text block's citations array.
func extractCitations(raw json.RawMessage, runeLen int) []llm.Citation {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []llm.Citation
	for _, it := range items {
		if c, ok := extractCitation(it); ok {
			out = append(out, c)
		}
	}
	return applyBlockRange(out, runeLen)
}

type streamDelta struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	Thinking    string          `json:"thinking"`
	PartialJSON string          `json:"partial_json"`
	Signature   string          `json:"signature"`
	Citation    json.RawMessage `json:"citation"`
	StopReason  string          `json:"stop_reason"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID    string          `json:"id"`
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	} `json:"message"`
	ContentBlock *contentBlock   `json:"content_block"`
	Delta        *streamDelta    `json:"delta"`
	Usage        json.RawMessage `json:"usage"`
	Error        json.RawMessage `json:"error"`
}

// scanTextDelta reads a text delta, which is nearly every event of a reply, without decoding it:
// an event of exactly the members type "content_block_delta", index and a delta of exactly type
// "text_delta" and text, each once. It reports false for any other event, which Decode decodes.
func scanTextDelta(data []byte) (index int, text string, ok bool) {
	members, ok := jsonscan.Object(data)
	if !ok || len(members) != 3 {
		return 0, "", false
	}
	var seen uint8
	for _, m := range members {
		var bit uint8
		switch string(m.Key) {
		case `"type"`:
			bit = 1
			if string(m.Value) != `"content_block_delta"` {
				return 0, "", false
			}
		case `"index"`:
			bit = 2
			if len(m.Value) == 0 || len(m.Value) > 9 {
				return 0, "", false
			}
			for _, c := range m.Value {
				if c < '0' || c > '9' {
					return 0, "", false
				}
				index = index*10 + int(c-'0')
			}
		case `"delta"`:
			bit = 4
			dm, ok := jsonscan.Object(m.Value)
			if !ok || len(dm) != 2 {
				return 0, "", false
			}
			var dseen uint8
			for _, f := range dm {
				var dbit uint8
				switch string(f.Key) {
				case `"type"`:
					dbit = 1
					if string(f.Value) != `"text_delta"` {
						return 0, "", false
					}
				case `"text"`:
					dbit = 2
					if !jsonscan.IsString(f.Value) {
						return 0, "", false
					}
					if text, ok = jsonscan.Unquote(f.Value); !ok {
						return 0, "", false
					}
				default:
					return 0, "", false
				}
				if dseen&dbit != 0 {
					return 0, "", false
				}
				dseen |= dbit
			}
		default:
			return 0, "", false
		}
		if seen&bit != 0 {
			return 0, "", false
		}
		seen |= bit
	}
	return index, text, true
}

// MaxSignature bounds the thinking signature the stream decoder collects for one block. Claude's
// signatures run to kilobytes; one this large means a broken stream.
const MaxSignature = 1 << 20

// StreamDecoder turns the events of an upstream Messages stream into model events. Every model
// event carries the upstream event it came from in Raw; upstream events the model has no event
// for (ping, message_delta, signature deltas, redacted thinking, server tool blocks, unknown
// events) become native events, so a Messages client still gets every upstream event once. Use
// one decoder per stream.
type StreamDecoder struct {
	started   bool
	done      bool
	next      int
	items     map[int]int              // upstream block index -> open model item index
	sigs      map[int]*strings.Builder // model item index -> signature of an open thinking block
	textLens  map[int]int              // model item index -> rune count of text seen so far
	citations map[int][]llm.Citation   // model item index -> web citations collected from citations_delta
	sources   []llm.Source
	seenSrc   map[string]bool
	cites     llm.CiteBudget
	finishCut []llm.Loss               // what the finish event left out to fit a frame
	codeBlock map[int]*pendingCodeCall // upstream block index -> open server_tool_use for code execution
	codeCalls map[string]decodedCodeCall
	codeDone  map[string]bool // IDs of calls a result has answered, so a repeated result is ignored
	codeExecs []llm.CodeExecution
	codeLost  bool // a code call or result that gave no run, so the runs end with the left-out record
	stop      string
	usage     json.RawMessage
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		items:     map[int]int{},
		sigs:      map[int]*strings.Builder{},
		textLens:  map[int]int{},
		citations: map[int][]llm.Citation{},
		seenSrc:   map[string]bool{},
		codeBlock: map[int]*pendingCodeCall{},
		codeCalls: map[string]decodedCodeCall{},
		codeDone:  map[string]bool{},
	}
}

// closeAll closes items the upstream left open; the events it adds carry no Raw.
func (d *StreamDecoder) closeAll(out []llm.Event) []llm.Event {
	open := make([]int, 0, len(d.items))
	for _, idx := range d.items {
		open = append(open, idx)
	}
	slices.Sort(open)
	for _, idx := range open {
		end := llm.Event{Type: llm.EventItemEnd, Index: idx}
		if cits := d.citations[idx]; len(cits) > 0 {
			end.Citations = applyBlockRange(cits, d.textLens[idx])
		}
		delete(d.citations, idx)
		delete(d.textLens, idx)
		out = append(out, end)
	}
	clear(d.items)
	return out
}

// Decode takes the data of one SSE event and returns the events it produces. message_stop ends
// the stream; an error event becomes an *llm.Error that keeps the upstream event in Raw.
func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("anthropicmessages: data after the end of the stream")
	}
	var ev streamEvent
	if index, text, ok := scanTextDelta(data); ok {
		ev = streamEvent{Type: "content_block_delta", Index: index, Delta: &streamDelta{Type: "text_delta", Text: text}}
	} else if err := json.Unmarshal(data, &ev); err != nil || ev.Type == "" {
		return nil, d.withUsage(malformed("upstream sent a malformed stream event", data))
	}
	raw := append(json.RawMessage(nil), data...)
	if ev.Type == "error" {
		d.done = true
		e := errorOf(ev.Error, "upstream reported an error mid-stream")
		e.Raw = raw
		return nil, d.withUsage(e)
	}
	var out []llm.Event
	if ev.Type == "message_start" && ev.Message != nil {
		// Read even when other events (a ping) came first.
		d.usage = mergeUsage(ev.Message.Usage, d.usage)
	}
	if !d.started {
		d.started = true
		info := llm.ResponseInfo{Format: format}
		if ev.Message != nil {
			info.ID, info.Model = ev.Message.ID, ev.Message.Model
		}
		// The first event's raw goes on response.start, whatever it is, so a Messages client
		// still gets every upstream event once.
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &info, Raw: raw})
		if ev.Type == "message_start" {
			return out, nil
		}
		raw = nil
	}
	native := func() []llm.Event {
		if raw == nil {
			return out
		}
		return append(out, llm.Event{Type: llm.EventNative, Raw: raw})
	}
	switch ev.Type {
	case "content_block_start":
		b := ev.ContentBlock
		if b == nil {
			return native(), nil
		}
		if b.Type == "web_search_tool_result" {
			d.sources = extractWebSearchSources(b.Content, d.seenSrc, d.sources, &d.cites)
		}
		if b.Type == "server_tool_use" && b.ID != "" && isCodeExecToolName(b.Name) {
			d.codeBlock[ev.Index] = &pendingCodeCall{
				id:           b.ID,
				name:         b.Name,
				initialInput: append(json.RawMessage(nil), b.Input...),
			}
		} else if isCodeExecResultBlock(b.Type) && b.ToolUseID != "" {
			call, ok := d.codeCalls[b.ToolUseID]
			delete(d.codeCalls, b.ToolUseID)
			repeated := !ok && d.codeDone[b.ToolUseID]
			if ok {
				d.codeDone[b.ToolUseID] = true
				var ce llm.CodeExecution
				if ce, ok = decodeCodeExecResult(b.Type, call, b.Content); ok {
					d.codeExecs = llm.AppendCodeExecution(d.codeExecs, ce)
				}
			}
			// A result repeated for a call already answered adds nothing, and loses nothing.
			if !ok && !repeated {
				d.codeLost = true
			}
		}
		kind := kindOf(b.Type)
		if kind == "" || (kind == llm.ItemToolCall && (b.ID == "" || b.Name == "")) {
			return native(), nil
		}
		if _, open := d.items[ev.Index]; open {
			return nil, d.withUsage(malformed("upstream opened content block twice", data))
		}
		idx := d.next
		d.next++
		d.items[ev.Index] = idx
		start := llm.ItemStart{Kind: kind}
		if kind == llm.ItemToolCall {
			start.CallID, start.Name = b.ID, b.Name
		}
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: idx, Item: &start, Raw: raw})
		// A block may start with content in it; carry it as a delta that keeps no raw.
		switch {
		case b.Type == "text" && b.Text != "":
			d.textLens[idx] += utf8.RuneCountInString(b.Text)
			out = append(out, llm.Event{Type: llm.EventTextDelta, Index: idx, Delta: b.Text})
		case b.Type == "thinking" && b.Think != "":
			out = append(out, llm.Event{Type: llm.EventReasoningDelta, Index: idx, Delta: b.Think})
		}
		if b.Type == "text" && len(b.Citations) > 0 {
			if cits := d.cites.KeepCitations(extractCitations(b.Citations, 0)); len(cits) > 0 {
				d.citations[idx] = append(d.citations[idx], cits...)
			}
		}
		return out, nil
	case "content_block_delta":
		idx, open := d.items[ev.Index]
		if !open || ev.Delta == nil {
			if ev.Delta != nil && ev.Delta.Type == "input_json_delta" {
				if pc := d.codeBlock[ev.Index]; pc != nil && !pc.overflow {
					if pc.partial.Len()+len(ev.Delta.PartialJSON) > maxCodeExecInput {
						pc.overflow = true
						pc.partial.Reset()
					} else {
						pc.partial.WriteString(ev.Delta.PartialJSON)
					}
				}
			}
			return native(), nil
		}
		switch ev.Delta.Type {
		case "text_delta":
			d.textLens[idx] += utf8.RuneCountInString(ev.Delta.Text)
			return append(out, llm.Event{Type: llm.EventTextDelta, Index: idx, Delta: ev.Delta.Text, Raw: raw}), nil
		case "thinking_delta":
			return append(out, llm.Event{Type: llm.EventReasoningDelta, Index: idx, Delta: ev.Delta.Thinking, Raw: raw}), nil
		case "input_json_delta":
			return append(out, llm.Event{Type: llm.EventToolInputDelta, Index: idx, Delta: ev.Delta.PartialJSON, Raw: raw}), nil
		}
		if ev.Delta.Type == "citations_delta" {
			if c, ok := extractCitation(ev.Delta.Citation); ok && d.cites.AddCitation(c) {
				d.citations[idx] = append(d.citations[idx], c)
			}
		}
		if ev.Delta.Type == "signature_delta" {
			// Carried on the item's end as well, for clients in other formats.
			sig := d.sigs[idx]
			if sig == nil {
				sig = &strings.Builder{}
				d.sigs[idx] = sig
			}
			if sig.Len()+len(ev.Delta.Signature) > MaxSignature {
				d.done = true
				return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint,
					Message: fmt.Sprintf("upstream sent a thinking signature over %d bytes", MaxSignature)})
			}
			sig.WriteString(ev.Delta.Signature)
		}
		// signature_delta, citations_delta and deltas the model has no place for.
		return native(), nil
	case "content_block_stop":
		if pc := d.codeBlock[ev.Index]; pc != nil {
			delete(d.codeBlock, ev.Index)
			if !pc.overflow {
				inRaw := pc.initialInput
				if pc.partial.Len() > 0 {
					inRaw = json.RawMessage(pc.partial.String())
				}
				if code, lang, ok := decodeCodeExecInput(pc.name, inRaw); ok {
					d.codeCalls[pc.id] = decodedCodeCall{name: pc.name, code: code, lang: lang}
				} else {
					d.codeLost = true
				}
			} else {
				d.codeLost = true
			}
		}
		idx, open := d.items[ev.Index]
		if !open {
			return native(), nil
		}
		delete(d.items, ev.Index)
		end := llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: raw}
		if sig := d.sigs[idx]; sig != nil && sig.Len() > 0 {
			end.Opaque = &llm.Opaque{Format: format, Data: sig.String()}
		}
		delete(d.sigs, idx)
		if cits := d.citations[idx]; len(cits) > 0 {
			end.Citations = applyBlockRange(cits, d.textLens[idx])
		}
		delete(d.citations, idx)
		delete(d.textLens, idx)
		return append(out, end), nil
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			d.stop = ev.Delta.StopReason
		}
		if len(ev.Usage) > 0 && string(ev.Usage) != "null" {
			d.usage = mergeUsage(d.usage, ev.Usage)
		}
		return native(), nil
	case "message_stop":
		d.done = true
		out = d.closeAll(out)
		f := finishOf(d.stop)
		if d.codeLost || len(d.codeCalls) > 0 || len(d.codeBlock) > 0 {
			// A call with no result, or a call or result the decoder could not read, is a run left out.
			d.codeExecs = llm.LeaveOutCodeExecutions(d.codeExecs)
		}
		fin := llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(d.usage), Sources: d.sources, CodeExecutions: d.codeExecs, Raw: raw}
		if l, cut := protocol.FitFinish(&fin); cut {
			d.finishCut = append(d.finishCut, l)
		}
		return append(out, fin), nil
	}
	// ping, message_start after the first event, and event types added later.
	return native(), nil
}

// Losses returns what the reply had that the model could not carry: the citations and sources, and
// the runs the finish event left out, that did not fit.
func (d *StreamDecoder) Losses() []llm.Loss { return d.cites.WithLoss(slices.Clip(d.finishCut)) }

// Close is called when the upstream stream ends. A stream that ends before message_stop is an
// error: the reply may have been cut short.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
}

// Usage returns the usage the upstream reported so far, or nil. A caller that fails the stream
// for its own reasons (the connection broke, say) puts it on the error.
func (d *StreamDecoder) Usage() *llm.Usage { return decodeUsage(d.usage) }

// withUsage puts the usage reported so far on an error that ends the stream.
func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.Usage()
	return e
}

type message struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      json.RawMessage   `json:"usage"`
	Error      json.RawMessage   `json:"error"`
}

func eventJSON(fields map[string]any) json.RawMessage {
	b, _ := json.Marshal(fields)
	return b
}

// DecodeResponse turns an upstream non-streamed Message into the events a stream of it would have
// produced, each carrying in Raw the Messages stream event it stands for, so a streaming Messages
// client gets every block whole: signatures as signature_delta, and blocks the model has no place
// for (redacted thinking, server tool results) as they were. The finish event carries the whole
// body in Raw, so a non-streaming Messages client gets it unchanged. A body of type error becomes
// an *llm.Error.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	events, _, err := DecodeResponseLosses(body)
	return events, err
}

// DecodeResponseLosses turns an upstream non-streamed message into events and returns what the
// reply had that the model could not carry. It is the non-streamed counterpart of
// StreamDecoder.Losses.
func DecodeResponseLosses(body []byte) ([]llm.Event, []llm.Loss, error) {
	var m message
	top := rawjson.Object(body)
	if err := json.Unmarshal(body, &m); err != nil || top == nil {
		return nil, nil, malformed("upstream sent a malformed response", body)
	}
	if m.Type == "error" {
		e := errorOf(m.Error, "upstream reported an error")
		e.Raw = append(json.RawMessage(nil), body...)
		return nil, nil, e
	}
	if m.Type != "message" {
		return nil, nil, malformed("upstream sent a response that is not a message", body)
	}
	head := map[string]json.RawMessage{}
	for k, v := range top {
		head[k] = v
	}
	// The stop fields come at the end of a stream, in message_delta.
	stopFields := []string{"stop_reason", "stop_sequence", "stop_details", "container"}
	head["content"] = json.RawMessage("[]")
	for _, k := range stopFields {
		if _, ok := head[k]; ok || k == "stop_reason" || k == "stop_sequence" {
			head[k] = json.RawMessage("null")
		}
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: m.ID, Model: m.Model, Format: format},
		Raw: eventJSON(map[string]any{"type": "message_start", "message": head})}}
	next := 0
	var sources []llm.Source
	var cites llm.CiteBudget
	seenSrc := map[string]bool{}
	codeCalls := map[string]decodedCodeCall{}
	codeDone := map[string]bool{}
	var codeExecs []llm.CodeExecution
	codeLost := false
	for i, raw := range m.Content {
		b := rawjson.Object(raw)
		var cb contentBlock
		if b == nil || json.Unmarshal(raw, &cb) != nil {
			return nil, nil, malformed("upstream sent a malformed response", body)
		}
		if cb.Type == "web_search_tool_result" {
			sources = extractWebSearchSources(cb.Content, seenSrc, sources, &cites)
		}
		if cb.Type == "server_tool_use" && cb.ID != "" && isCodeExecToolName(cb.Name) {
			if code, lang, ok := decodeCodeExecInput(cb.Name, cb.Input); ok {
				codeCalls[cb.ID] = decodedCodeCall{name: cb.Name, code: code, lang: lang}
			} else {
				codeLost = true
			}
		} else if isCodeExecResultBlock(cb.Type) && cb.ToolUseID != "" {
			call, ok := codeCalls[cb.ToolUseID]
			delete(codeCalls, cb.ToolUseID)
			repeated := !ok && codeDone[cb.ToolUseID]
			if ok {
				codeDone[cb.ToolUseID] = true
				var ce llm.CodeExecution
				if ce, ok = decodeCodeExecResult(cb.Type, call, cb.Content); ok {
					codeExecs = llm.AppendCodeExecution(codeExecs, ce)
				}
			}
			if !ok && !repeated {
				codeLost = true
			}
		}
		kind := kindOf(cb.Type)
		if kind == llm.ItemToolCall && (cb.ID == "" || cb.Name == "") {
			kind = ""
		}
		stop := eventJSON(map[string]any{"type": "content_block_stop", "index": i})
		if kind == "" {
			out = append(out,
				llm.Event{Type: llm.EventNative, Raw: eventJSON(map[string]any{"type": "content_block_start", "index": i, "content_block": json.RawMessage(raw)})},
				llm.Event{Type: llm.EventNative, Raw: stop})
			continue
		}
		start := llm.ItemStart{Kind: kind}
		startBlock := map[string]json.RawMessage{}
		for k, v := range b {
			startBlock[k] = v
		}
		var deltaType, delta string
		var deltaRaw json.RawMessage
		switch kind {
		case llm.ItemMessage:
			startBlock["text"] = json.RawMessage(`""`)
			deltaType, delta = llm.EventTextDelta, cb.Text
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "text_delta", "text": cb.Text}})
		case llm.ItemReasoning:
			startBlock["thinking"], startBlock["signature"] = json.RawMessage(`""`), json.RawMessage(`""`)
			deltaType, delta = llm.EventReasoningDelta, cb.Think
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "thinking_delta", "thinking": cb.Think}})
		case llm.ItemToolCall:
			start.CallID, start.Name = cb.ID, cb.Name
			args, err := compactJSON(cb.Input)
			if err != nil {
				return nil, nil, malformed("upstream sent a malformed response", body)
			}
			startBlock["input"] = json.RawMessage("{}")
			deltaType, delta = llm.EventToolInputDelta, args
			deltaRaw = eventJSON(map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]string{"type": "input_json_delta", "partial_json": args}})
		}
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start,
			Raw: eventJSON(map[string]any{"type": "content_block_start", "index": i, "content_block": startBlock})})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta, Raw: deltaRaw})
		}
		if sig, ok := b["signature"]; ok && kind == llm.ItemReasoning && string(sig) != `""` {
			out = append(out, llm.Event{Type: llm.EventNative, Raw: eventJSON(map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]json.RawMessage{"type": json.RawMessage(`"signature_delta"`), "signature": sig}})})
		}
		end := llm.Event{Type: llm.EventItemEnd, Index: next, Raw: stop}
		if kind == llm.ItemMessage && len(cb.Citations) > 0 {
			end.Citations = cites.KeepCitations(extractCitations(cb.Citations, utf8.RuneCountInString(cb.Text)))
		}
		var sig string
		if kind == llm.ItemReasoning && json.Unmarshal(b["signature"], &sig) == nil && sig != "" {
			end.Opaque = &llm.Opaque{Format: format, Data: sig}
		}
		out = append(out, end)
		next++
	}
	md := map[string]json.RawMessage{"stop_reason": json.RawMessage("null"), "stop_sequence": json.RawMessage("null")}
	for _, k := range stopFields {
		if v, ok := top[k]; ok {
			md[k] = v
		}
	}
	delta := map[string]any{"type": "message_delta", "delta": md}
	if len(m.Usage) > 0 {
		delta["usage"] = m.Usage
	}
	out = append(out, llm.Event{Type: llm.EventNative, Raw: eventJSON(delta)})
	f := finishOf(m.StopReason)
	if codeLost || len(codeCalls) > 0 {
		codeExecs = llm.LeaveOutCodeExecutions(codeExecs)
	}
	fin := llm.Event{Type: llm.EventFinish, Finish: &f, Usage: decodeUsage(m.Usage), Sources: sources, CodeExecutions: codeExecs, Raw: append(json.RawMessage(nil), body...)}
	var losses []llm.Loss
	if l, cut := protocol.FitFinish(&fin); cut {
		losses = llm.AddLoss(losses, l)
	}
	return append(out, fin), cites.WithLoss(losses), nil
}
