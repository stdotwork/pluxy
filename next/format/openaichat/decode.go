package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	maxNativeAnnotationsBytes = 4 << 20
	maxNativeAudioMetaBytes   = 4 << 20
)

type itemEndMeta struct {
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

type audioEventMeta struct {
	Pending json.RawMessage `json:"pending_audio,omitempty"`
	Audio   json.RawMessage `json:"audio,omitempty"`
}

type finishMeta struct {
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Audio       json.RawMessage `json:"audio,omitempty"`
}

func parseNonEmptyAnnotations(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil
	}
	return items
}

func encodeItemEndAnnotations(items []json.RawMessage) json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	anns, err := json.Marshal(items)
	if err != nil {
		return nil
	}
	b, err := json.Marshal(itemEndMeta{Annotations: anns})
	if err != nil {
		return nil
	}
	return b
}

func encodeAudioEventMeta(pending, audio json.RawMessage) json.RawMessage {
	if len(pending) == 0 && len(audio) == 0 {
		return nil
	}
	b, err := json.Marshal(audioEventMeta{Pending: pending, Audio: audio})
	if err != nil {
		return nil
	}
	return b
}

func encodeFinishMeta(anns []json.RawMessage, audio json.RawMessage) json.RawMessage {
	var rawAnns json.RawMessage
	if len(anns) > 0 {
		rawAnns, _ = json.Marshal(anns)
	}
	if len(rawAnns) == 0 && len(audio) == 0 {
		return nil
	}
	b, err := json.Marshal(finishMeta{Annotations: rawAnns, Audio: audio})
	if err != nil {
		return nil
	}
	return b
}

func audioMetaWithoutData(raw json.RawMessage) (meta json.RawMessage, hasData bool, ok bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, false, false
	}
	if d, exists := obj["data"]; exists {
		var s string
		if json.Unmarshal(d, &s) == nil && s != "" {
			hasData = true
		}
		delete(obj, "data")
	}
	if len(obj) == 0 {
		if hasData {
			return json.RawMessage(`{}`), true, true
		}
		return nil, false, true
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, false, false
	}
	return b, hasData, true
}

type toolCall struct {
	index int
	id    string
	name  string
	item  int
	args  []string
	extra json.RawMessage
}

type chatUsage struct {
	PromptTokens        *int64 `json:"prompt_tokens"`
	CompletionTokens    *int64 `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens     *int64 `json:"cached_tokens"`
		CacheWriteTokens *int64 `json:"cache_write_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails *struct {
		ReasoningTokens *int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func decodeUsage(raw json.RawMessage) *llm.Usage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u chatUsage
	if json.Unmarshal(raw, &u) != nil {
		return &llm.Usage{Raw: raw}
	}
	out := &llm.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, Raw: append(json.RawMessage(nil), raw...)}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
		out.CacheWriteInputTokens = u.PromptTokensDetails.CacheWriteTokens
	}
	if u.CompletionTokensDetails != nil {
		out.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return out
}

func reportsTokens(u *llm.Usage) bool {
	for _, p := range []*int64{u.InputTokens, u.OutputTokens, u.CachedInputTokens, u.CacheWriteInputTokens, u.ReasoningTokens} {
		if p != nil && *p > 0 {
			return true
		}
	}
	return false
}

func decodeFinish(raw string) llm.Finish {
	reason := llm.FinishOther
	switch raw {
	case "stop":
		reason = llm.FinishStop
	case "length":
		reason = llm.FinishLength
	case "tool_calls", "function_call":
		reason = llm.FinishToolCalls
	case "content_filter":
		reason = llm.FinishContentFilter
	}
	return llm.Finish{Reason: reason, RawReason: raw}
}

const maxErrorRaw = 1 << 20

func malformed(msg string, data []byte) *llm.Error {
	e := &llm.Error{Code: llm.CodeUpstream, Message: msg}
	if len(data) <= maxErrorRaw {
		e.Raw = llm.RawJSON(data)
	}
	return e
}

func replyFields(data []byte) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	for _, k := range modelledFields {
		delete(m, k)
	}
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

var modelledFields = []string{"id", "object", "created", "model", "choices", "usage", "error"}

func otherChoices(choices []json.RawMessage) *llm.Event {
	if len(choices) == 0 {
		return nil
	}
	raw, err := json.Marshal(map[string][]json.RawMessage{"choices": choices})
	if err != nil {
		return nil
	}
	return &llm.Event{Type: llm.EventNative, Raw: raw}
}

type streamChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Created int64             `json:"created"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`
}

type streamChoice struct {
	Index int `json:"index"`
	Delta struct {
		reasoningFields
		Content     *string         `json:"content"`
		Refusal     json.RawMessage `json:"refusal"`
		Annotations json.RawMessage `json:"annotations"`
		Audio       json.RawMessage `json:"audio"`
		ToolCalls   []struct {
			Index    *int   `json:"index"`
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	} `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs"`
}

func extractChatAnnotations(raw json.RawMessage, prefixRunes, textRunes int) []llm.Citation {
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var entries []struct {
		Type        string `json:"type"`
		URLCitation *struct {
			URL        string `json:"url"`
			Title      string `json:"title"`
			StartIndex *int   `json:"start_index"`
			EndIndex   *int   `json:"end_index"`
		} `json:"url_citation"`
	}
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	var out []llm.Citation
	for _, e := range entries {
		if e.Type != "url_citation" || e.URLCitation == nil || !llm.ValidCitationURL(e.URLCitation.URL) {
			continue
		}
		uc := e.URLCitation
		c := llm.Citation{URL: uc.URL, Title: uc.Title}
		if uc.StartIndex != nil && uc.EndIndex != nil {
			s, end := *uc.StartIndex-prefixRunes, *uc.EndIndex-prefixRunes
			if s >= 0 && end >= s && end <= textRunes {
				c.StartIndex, c.EndIndex = &s, &end
			}
		}
		out = append(out, c)
	}
	return out
}

type StreamDecoder struct {
	started          bool
	done             bool
	next             int
	text             int
	reasoning        int
	tools            map[int]*toolCall
	last             *toolCall
	extra            map[int]json.RawMessage
	choices          map[int]bool
	finish           *llm.Finish
	usage            *llm.Usage
	calls            int
	opened           int
	used             map[string]bool
	think            thinkFilter
	refused          bool
	textRunes        int
	rawRunes         int
	annRaws          []json.RawMessage
	annRawBytes      int
	cites            llm.CiteBudget
	nativeAnns       []json.RawMessage
	nativeAnnBytes   int
	nativeAnnDropped bool
	annOutsideText   bool
	pendingAudio     map[string]any
	audioMetaBytes   int
	audioMetaDropped bool
	mediaBytes       int
	losses           []llm.Loss
	otherCands       map[int]*otherCand
}

type otherCand struct {
	think    thinkFilter
	tools    map[int]*toolCall
	last     *toolCall
	nextItem int
	calls    int
	opened   int
	used     map[string]bool
	refused  bool
	finish   *llm.Finish
	finished bool
	annLost  bool
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{text: -1, reasoning: -1, tools: map[int]*toolCall{}, extra: map[int]json.RawMessage{}, choices: map[int]bool{}, used: map[string]bool{}}
}

func (d *StreamDecoder) open(kind string, ev *[]llm.Event, item llm.ItemStart) int {
	item.Kind = kind
	i := d.next
	d.next++
	*ev = append(*ev, llm.Event{Type: llm.EventItemStart, Index: i, Item: &item})
	return i
}

func (d *StreamDecoder) closeItem(idx *int, ev *[]llm.Event) {
	if *idx >= 0 {
		end := llm.Event{Type: llm.EventItemEnd, Index: *idx}
		if idx == &d.text {
			if len(d.annRaws) > 0 {
				var cits []llm.Citation
				for _, raw := range d.annRaws {
					cits = append(cits, extractChatAnnotations(raw, max(d.rawRunes-d.textRunes, 0), d.textRunes)...)
				}
				end.Citations = d.cites.KeepCitations(cits)
				d.annRaws, d.annRawBytes = nil, 0
			}
			if len(d.nativeAnns) > 0 {
				end.Raw = encodeItemEndAnnotations(d.nativeAnns)
				d.nativeAnns = nil
			}
			d.textRunes, d.rawRunes = 0, 0
		}
		*ev = append(*ev, end)
		*idx = -1
	}
}

func (d *StreamDecoder) addNativeAnnotations(raw json.RawMessage) {
	items := parseNonEmptyAnnotations(raw)
	if len(items) == 0 {
		return
	}
	if d.nativeAnnBytes+len(raw) > maxNativeAnnotationsBytes {
		if !d.nativeAnnDropped {
			d.nativeAnnDropped = true
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: "choices[0].delta.annotations", Detail: "annotations exceed size limit"})
		}
		return
	}
	d.nativeAnnBytes += len(raw)
	d.nativeAnns = append(d.nativeAnns, items...)
}

func (d *StreamDecoder) dropAudioMeta() {
	if !d.audioMetaDropped {
		d.audioMetaDropped = true
		d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: "choices[0].delta.audio", Detail: "audio metadata exceeds size limit"})
	}
}

func (d *StreamDecoder) mergePendingAudio(raw json.RawMessage) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&m) != nil || len(m) == 0 {
		return
	}
	if d.pendingAudio == nil {
		d.pendingAudio = map[string]any{}
	}
	mergeDelta(d.pendingAudio, m)
}

func (d *StreamDecoder) takePendingAudio() json.RawMessage {
	if len(d.pendingAudio) == 0 {
		return nil
	}
	plain(d.pendingAudio)
	b, err := json.Marshal(d.pendingAudio)
	d.pendingAudio = nil
	if err != nil {
		return nil
	}
	return b
}

func (d *StreamDecoder) closeAll(ev *[]llm.Event) {
	d.closeItem(&d.reasoning, ev)
	d.closeItem(&d.text, ev)
	all := make([]*toolCall, 0, len(d.tools)+1)
	for _, c := range d.tools {
		all = append(all, c)
	}
	seen := false
	for _, c := range all {
		if c == d.last {
			seen = true
		}
	}
	if d.last != nil && !seen {
		all = append(all, d.last)
	}
	open := make([]int, 0, len(all))
	for _, c := range all {
		if c.item >= 0 {
			open = append(open, c.item)
		}
	}
	slices.Sort(open)
	for _, idx := range open {
		*ev = append(*ev, llm.Event{Type: llm.EventItemEnd, Index: idx, Raw: d.extra[idx]})
	}
	for _, c := range all {
		d.dropNameless(c)
	}
	clear(d.tools)
	clear(d.extra)
	d.last = nil
}

func (d *StreamDecoder) content(s string, ev *[]llm.Event) {
	for _, p := range d.think.feed(s) {
		d.piece(p, ev)
	}
}

func (d *StreamDecoder) piece(p thinkPiece, ev *[]llm.Event) {
	if p.reasoning {
		d.closeItem(&d.text, ev)
		if d.reasoning < 0 {
			d.reasoning = d.open(llm.ItemReasoning, ev, llm.ItemStart{})
		}
		*ev = append(*ev, llm.Event{Type: llm.EventReasoningDelta, Index: d.reasoning, Delta: p.text})
		return
	}
	d.closeItem(&d.reasoning, ev)
	if d.text < 0 {
		d.text = d.open(llm.ItemMessage, ev, llm.ItemStart{})
	}
	d.textRunes += utf8.RuneCountInString(p.text)
	*ev = append(*ev, llm.Event{Type: llm.EventTextDelta, Index: d.text, Delta: p.text})
}

func refusalText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func logprobs(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func (d *StreamDecoder) attachTextMeta(meta textMeta, from int, ev *[]llm.Event) {
	raw := meta.raw()
	if raw == nil {
		return
	}
	for i := len(*ev) - 1; i >= from; i-- {
		if (*ev)[i].Type == llm.EventTextDelta {
			(*ev)[i].Raw = raw
			return
		}
	}
}

func (d *StreamDecoder) flushThink(ev *[]llm.Event) {
	for _, p := range d.think.flush() {
		d.piece(p, ev)
	}
}

func (d *StreamDecoder) openCall(c *toolCall, ev *[]llm.Event) {
	c.item = d.open(llm.ItemToolCall, ev, llm.ItemStart{CallID: c.id, Name: c.name})
	d.opened++
	if c.extra != nil {
		(*ev)[len(*ev)-1].Raw = c.extra
		c.extra = nil
	}
	for _, a := range c.args {
		*ev = append(*ev, llm.Event{Type: llm.EventToolInputDelta, Index: c.item, Delta: a})
	}
	c.args = nil
}

func (d *StreamDecoder) closeCall(c *toolCall, ev *[]llm.Event) {
	if c.item >= 0 {
		*ev = append(*ev, llm.Event{Type: llm.EventItemEnd, Index: c.item, Raw: d.extra[c.item]})
		delete(d.extra, c.item)
	}
	d.dropNameless(c)
	if c.index >= 0 && d.tools[c.index] == c {
		delete(d.tools, c.index)
	}
	if d.last == c {
		d.last = nil
	}
}

func (d *StreamDecoder) dropNameless(c *toolCall) {
	if c.item >= 0 || c.name != "" {
		return
	}
	path := "choices[0].delta.tool_calls"
	if c.index >= 0 {
		path = fmt.Sprintf("%s[%d]", path, c.index)
	}
	d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: path,
		Detail: "a streamed tool call that never named its function"})
}

func (d *StreamDecoder) callID(index int) string {
	if index >= 0 {
		if id := "call_" + strconv.Itoa(index); !d.used[id] {
			return id
		}
	}
	for {
		id := "call_" + strconv.Itoa(d.calls)
		d.calls++
		if !d.used[id] {
			return id
		}
	}
}

func (d *StreamDecoder) Losses() []llm.Loss { return d.cites.WithLoss(d.losses) }

func (d *StreamDecoder) Decode(data []byte) ([]llm.Event, error) {
	if d.done {
		return nil, fmt.Errorf("openaichat: data after the end of the stream")
	}
	if string(data) == "[DONE]" {
		if !d.finished() {
			return nil, d.unfinished()
		}
		return d.end(), nil
	}
	var c streamChunk
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
	}
	if u := decodeUsage(c.Usage); u != nil && (d.usage == nil || reportsTokens(u)) {
		d.usage = u
	}
	if hasError(c.Error) {
		return nil, d.withUsage(upstreamErrorObject(c.Error, "upstream reported an error mid-stream"))
	}
	choices := make([]streamChoice, 0, len(c.Choices))
	var others []json.RawMessage
	for i, raw := range c.Choices {
		var ch streamChoice
		if err := json.Unmarshal(raw, &ch); err != nil {
			if i == 0 {
				return nil, d.withUsage(malformed("upstream sent a malformed stream chunk", data))
			}
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d]", i), Detail: "malformed candidate"})
			continue
		}
		if ch.Index < 0 {
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d]", i), Detail: "candidate with negative index"})
			others = append(others, raw)
			continue
		}
		choices = append(choices, ch)
		if ch.Index != 0 {
			others = append(others, raw)
		}
		if f := ch.FinishReason; f != nil && *f != "" {
			d.choices[ch.Index] = true
		} else if _, ok := d.choices[ch.Index]; !ok {
			d.choices[ch.Index] = false
		}
	}
	var out []llm.Event
	if !d.started {
		d.started = true
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{
			ID: c.ID, Model: c.Model, Created: c.Created, Format: llm.FormatOpenAIChat, Raw: replyFields(data),
		}})
	}
	for _, ch := range choices {
		if ch.Index != 0 {
			d.decodeOtherChoice(ch, &out)
			continue
		}
		if r := ch.Delta.text(); r != "" {
			d.closeItem(&d.text, &out)
			if d.reasoning < 0 {
				d.reasoning = d.open(llm.ItemReasoning, &out, llm.ItemStart{})
			}
			out = append(out, llm.Event{Type: llm.EventReasoningDelta, Index: d.reasoning, Delta: r})
		}
		if t := ch.Delta.Content; t != nil && *t != "" {
			before := len(out)
			d.rawRunes += utf8.RuneCountInString(*t)
			d.content(*t, &out)
			d.attachTextMeta(textMeta{Logprobs: logprobs(ch.Logprobs)}, before, &out)
		}
		if d.text >= 0 && len(ch.Delta.Annotations) > 0 && ch.Delta.Annotations[0] == '[' {
			if d.annRawBytes+len(ch.Delta.Annotations) > llm.MaxCitationBytes {
				d.cites.LeftOut()
			} else {
				d.annRawBytes += len(ch.Delta.Annotations)
				d.annRaws = append(d.annRaws, append(json.RawMessage(nil), ch.Delta.Annotations...))
			}
		} else if d.text < 0 && !d.annOutsideText && len(extractChatAnnotations(ch.Delta.Annotations, 0, 0)) > 0 {
			d.annOutsideText = true
			d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: llm.LossPathCitations,
				Detail: "annotations that arrived with no text open have no message text to cite"})
		}
		if len(ch.Delta.Annotations) > 0 {
			d.addNativeAnnotations(ch.Delta.Annotations)
		}
		if r := refusalText(ch.Delta.Refusal); r != "" {
			d.refused = true
			d.flushThink(&out)
			before := len(out)
			d.piece(thinkPiece{text: r}, &out)
			d.attachTextMeta(textMeta{Logprobs: logprobs(ch.Logprobs), Refusal: true}, before, &out)
		}
		if len(ch.Delta.Audio) > 0 && string(ch.Delta.Audio) != "null" {
			// OpenAI streams audio only as pcm16 at 24 kHz, in pieces with no header to read.
			media := decodeChatAudio(ch.Delta.Audio, "audio/pcm;rate=24000", false, "choices[0].delta.audio", &d.mediaBytes, &d.losses)
			meta, hasData, ok := audioMetaWithoutData(ch.Delta.Audio)
			if media != nil {
				var curMeta json.RawMessage
				if ok && len(meta) > 0 {
					if d.audioMetaBytes+len(meta) > maxNativeAudioMetaBytes {
						d.dropAudioMeta()
					} else {
						d.audioMetaBytes += len(meta)
						curMeta = meta
					}
				}
				pending := d.takePendingAudio()
				out = append(out, llm.Event{Type: llm.EventMedia, Media: media, Raw: encodeAudioEventMeta(pending, curMeta)})
			} else if ok && !hasData && len(meta) > 0 {
				if d.audioMetaBytes+len(meta) > maxNativeAudioMetaBytes {
					d.dropAudioMeta()
				} else {
					d.audioMetaBytes += len(meta)
					d.mergePendingAudio(meta)
				}
			}
		}
		if len(ch.Delta.ToolCalls) > 0 {
			d.flushThink(&out)
		}
		for _, tc := range ch.Delta.ToolCalls {
			index := -1
			if tc.Index != nil {
				index = *tc.Index
			}
			var call *toolCall
			if index >= 0 {
				call = d.tools[index]
			} else {
				call = d.last
			}
			if call == nil || (tc.ID != "" && tc.ID != call.id) {
				if call != nil {
					d.closeCall(call, &out)
				}
				d.closeItem(&d.reasoning, &out)
				d.closeItem(&d.text, &out)
				call = &toolCall{index: index, id: tc.ID, item: -1}
				if call.id == "" {
					call.id = d.callID(index)
				}
				d.used[call.id] = true
				if index >= 0 {
					d.tools[index] = call
				}
				d.last = call
			}
			if call.item < 0 && call.name == "" && tc.Function.Name != "" {
				call.name = tc.Function.Name
				d.closeItem(&d.reasoning, &out)
				d.closeItem(&d.text, &out)
				d.openCall(call, &out)
			}
			if raw := extraContent(tc.ExtraContent); raw != nil {
				if call.item >= 0 {
					d.extra[call.item] = raw
				} else {
					call.extra = raw
				}
			}
			if tc.Function.Arguments != "" {
				if call.item >= 0 {
					out = append(out, llm.Event{Type: llm.EventToolInputDelta, Index: call.item, Delta: tc.Function.Arguments})
				} else {
					call.args = append(call.args, tc.Function.Arguments)
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			f := decodeFinish(*ch.FinishReason)
			d.finish = &f
			d.flushThink(&out)
			d.closeAll(&out)
		}
	}
	if ev := otherChoices(others); ev != nil {
		out = append(out, *ev)
	}
	return out, nil
}

func (d *StreamDecoder) Usage() *llm.Usage { return d.usage }

func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.usage
	return e
}

func (d *StreamDecoder) end() []llm.Event {
	d.done = true
	var out []llm.Event
	if !d.started {
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}})
	}
	d.flushThink(&out)
	d.closeAll(&out)
	d.closeOtherCands(&out)
	f := llm.Finish{Reason: llm.FinishOther}
	if d.finish != nil {
		f = *d.finish
	}
	if f.Reason == llm.FinishToolCalls && d.opened == 0 {
		f.Reason = llm.FinishStop
	}
	if d.refused {
		f.Reason = llm.FinishContentFilter
	}
	fRaw := encodeFinishMeta(d.nativeAnns, d.takePendingAudio())
	d.nativeAnns = nil
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &f, Usage: d.usage, Raw: fRaw})
}

func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	if !d.finished() {
		return nil, d.unfinished()
	}
	return d.end(), nil
}

func (d *StreamDecoder) finished() bool {
	if d.finish == nil {
		return false
	}
	for _, done := range d.choices {
		if !done {
			return false
		}
	}
	return true
}

func (d *StreamDecoder) unfinished() *llm.Error {
	d.done = true
	return d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
}

func (d *StreamDecoder) otherCand(idx int) *otherCand {
	if d.otherCands == nil {
		d.otherCands = map[int]*otherCand{}
	}
	oc := d.otherCands[idx]
	if oc == nil {
		oc = &otherCand{tools: map[int]*toolCall{}, used: map[string]bool{}}
		d.otherCands[idx] = oc
	}
	return oc
}

func (oc *otherCand) callID(candIdx, index int) string {
	if index >= 0 {
		if id := fmt.Sprintf("call_c%d_%d", candIdx, index); !oc.used[id] {
			return id
		}
	}
	for {
		id := fmt.Sprintf("call_c%d_%d", candIdx, oc.calls)
		oc.calls++
		if !oc.used[id] {
			return id
		}
	}
}

func (oc *otherCand) openCall(candIdx int, c *toolCall, ev *[]llm.Event) {
	c.item = oc.nextItem
	oc.nextItem++
	oc.opened++
	*ev = append(*ev, llm.Event{
		Type:      llm.EventCandidate,
		Candidate: &llm.CandidateEvent{Index: candIdx, Kind: llm.CandidateToolCallStart, Item: c.item, CallID: c.id, Name: c.name},
	})
	for _, a := range c.args {
		*ev = append(*ev, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: candIdx, Kind: llm.CandidateToolInputDelta, Item: c.item, Delta: a},
		})
	}
	c.args = nil
}

func (oc *otherCand) closeCall(candIdx int, c *toolCall, ev *[]llm.Event, losses *[]llm.Loss) {
	if c.item >= 0 {
		*ev = append(*ev, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: candIdx, Kind: llm.CandidateToolCallEnd, Item: c.item},
		})
	} else if c.name == "" {
		path := fmt.Sprintf("choices[%d].delta.tool_calls", candIdx)
		if c.index >= 0 {
			path = fmt.Sprintf("%s[%d]", path, c.index)
		}
		*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "a streamed tool call that never named its function"})
	}
	if c.index >= 0 && oc.tools[c.index] == c {
		delete(oc.tools, c.index)
	}
	if oc.last == c {
		oc.last = nil
	}
}

func (oc *otherCand) flushAndClose(candIdx int, ev *[]llm.Event, losses *[]llm.Loss) {
	for _, p := range oc.think.flush() {
		kind := llm.CandidateTextDelta
		if p.reasoning {
			kind = llm.CandidateReasoningDelta
		}
		*ev = append(*ev, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: candIdx, Kind: kind, Delta: p.text},
		})
	}
	all := make([]*toolCall, 0, len(oc.tools)+1)
	for _, c := range oc.tools {
		all = append(all, c)
	}
	if oc.last != nil && !slices.Contains(all, oc.last) {
		all = append(all, oc.last)
	}
	open := make([]int, 0, len(all))
	for _, c := range all {
		if c.item >= 0 {
			open = append(open, c.item)
		}
	}
	slices.Sort(open)
	for _, item := range open {
		*ev = append(*ev, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: candIdx, Kind: llm.CandidateToolCallEnd, Item: item},
		})
	}
	for _, c := range all {
		if c.item < 0 && c.name == "" {
			path := fmt.Sprintf("choices[%d].delta.tool_calls", candIdx)
			if c.index >= 0 {
				path = fmt.Sprintf("%s[%d]", path, c.index)
			}
			*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "a streamed tool call that never named its function"})
		}
	}
	clear(oc.tools)
	oc.last = nil
}

func (d *StreamDecoder) decodeOtherChoice(ch streamChoice, out *[]llm.Event) {
	oc := d.otherCand(ch.Index)
	if !oc.annLost && parseNonEmptyAnnotations(ch.Delta.Annotations) != nil {
		oc.annLost = true
		d.losses = llm.AddLoss(d.losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d].delta.annotations", ch.Index),
			Detail: "citations of a candidate after the first have no place in the reply"})
	}
	if r := ch.Delta.text(); r != "" {
		*out = append(*out, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateReasoningDelta, Delta: r},
		})
	}
	if t := ch.Delta.Content; t != nil && *t != "" {
		for _, p := range oc.think.feed(*t) {
			kind := llm.CandidateTextDelta
			if p.reasoning {
				kind = llm.CandidateReasoningDelta
			}
			*out = append(*out, llm.Event{
				Type:      llm.EventCandidate,
				Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: kind, Delta: p.text},
			})
		}
	}
	if r := refusalText(ch.Delta.Refusal); r != "" {
		oc.refused = true
		for _, p := range oc.think.flush() {
			kind := llm.CandidateTextDelta
			if p.reasoning {
				kind = llm.CandidateReasoningDelta
			}
			*out = append(*out, llm.Event{
				Type:      llm.EventCandidate,
				Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: kind, Delta: p.text},
			})
		}
		*out = append(*out, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateTextDelta, Delta: r},
		})
	}
	if len(ch.Delta.Audio) > 0 && string(ch.Delta.Audio) != "null" {
		path := fmt.Sprintf("choices[%d].delta.audio", ch.Index)
		if media := decodeChatAudio(ch.Delta.Audio, "audio/pcm;rate=24000", false, path, &d.mediaBytes, &d.losses); media != nil {
			*out = append(*out, llm.Event{
				Type:      llm.EventCandidate,
				Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateMedia, Media: media},
			})
		}
	}
	if len(ch.Delta.ToolCalls) > 0 {
		for _, p := range oc.think.flush() {
			kind := llm.CandidateTextDelta
			if p.reasoning {
				kind = llm.CandidateReasoningDelta
			}
			*out = append(*out, llm.Event{
				Type:      llm.EventCandidate,
				Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: kind, Delta: p.text},
			})
		}
	}
	for _, tc := range ch.Delta.ToolCalls {
		index := -1
		if tc.Index != nil {
			index = *tc.Index
		}
		var call *toolCall
		if index >= 0 {
			call = oc.tools[index]
		} else {
			call = oc.last
		}
		if call == nil || (tc.ID != "" && tc.ID != call.id) {
			if call != nil {
				oc.closeCall(ch.Index, call, out, &d.losses)
			}
			call = &toolCall{index: index, id: tc.ID, item: -1}
			if call.id == "" {
				call.id = oc.callID(ch.Index, index)
			}
			oc.used[call.id] = true
			if index >= 0 {
				oc.tools[index] = call
			}
			oc.last = call
		}
		if call.item < 0 && call.name == "" && tc.Function.Name != "" {
			call.name = tc.Function.Name
			oc.openCall(ch.Index, call, out)
		}
		if tc.Function.Arguments != "" {
			if call.item >= 0 {
				*out = append(*out, llm.Event{
					Type:      llm.EventCandidate,
					Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateToolInputDelta, Item: call.item, Delta: tc.Function.Arguments},
				})
			} else {
				call.args = append(call.args, tc.Function.Arguments)
			}
		}
	}
	if ch.FinishReason != nil && *ch.FinishReason != "" && !oc.finished {
		oc.flushAndClose(ch.Index, out, &d.losses)
		f := decodeFinish(*ch.FinishReason)
		if f.Reason == llm.FinishToolCalls && oc.opened == 0 {
			f.Reason = llm.FinishStop
		}
		if oc.refused {
			f.Reason = llm.FinishContentFilter
		}
		oc.finish = &f
		oc.finished = true
		*out = append(*out, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateFinish, Finish: &f},
		})
	}
}

func (d *StreamDecoder) closeOtherCands(out *[]llm.Event) {
	if len(d.otherCands) == 0 {
		return
	}
	idxs := make([]int, 0, len(d.otherCands))
	for idx := range d.otherCands {
		idxs = append(idxs, idx)
	}
	slices.Sort(idxs)
	for _, idx := range idxs {
		oc := d.otherCands[idx]
		if oc.finished {
			continue
		}
		oc.flushAndClose(idx, out, &d.losses)
		f := llm.Finish{Reason: llm.FinishOther}
		if oc.finish != nil {
			f = *oc.finish
		}
		if f.Reason == llm.FinishToolCalls && oc.opened == 0 {
			f.Reason = llm.FinishStop
		}
		if oc.refused {
			f.Reason = llm.FinishContentFilter
		}
		oc.finished = true
		*out = append(*out, llm.Event{
			Type:      llm.EventCandidate,
			Candidate: &llm.CandidateEvent{Index: idx, Kind: llm.CandidateFinish, Finish: &f},
		})
	}
}

func hasError(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

func upstreamErrorObject(raw json.RawMessage, fallback string) *llm.Error {
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	out := &llm.Error{Code: llm.CodeUpstream, Message: e.Message, Raw: append(json.RawMessage(nil), raw...)}
	if out.Message == "" {
		out.Message = fallback
	}
	if e.Code != nil {
		out.ProviderCode = fmt.Sprint(e.Code)
	}
	return out
}

type completion struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Created int64             `json:"created"`
	Choices []json.RawMessage `json:"choices"`
	Usage   json.RawMessage   `json:"usage"`
	Error   json.RawMessage   `json:"error"`
}

type completionChoice struct {
	Index   int `json:"index"`
	Message *struct {
		reasoningFields
		Content     *string         `json:"content"`
		Refusal     json.RawMessage `json:"refusal"`
		Annotations json.RawMessage `json:"annotations"`
		Audio       json.RawMessage `json:"audio"`
		ToolCalls   []struct {
			chatToolCall
			ExtraContent json.RawMessage `json:"extra_content"`
		} `json:"tool_calls"`
	} `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs"`
}

func decodeChatAudio(raw json.RawMessage, defaultMT string, sniff bool, path string, mediaBytes *int, losses *[]llm.Loss) *llm.OutputMedia {
	var a struct {
		Data string `json:"data"`
	}
	if json.Unmarshal(raw, &a) != nil {
		*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: "malformed audio object"})
		return nil
	}
	if a.Data == "" {
		return nil
	}
	if !llm.Base64Valid(a.Data) {
		*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path + ".data", Detail: "invalid base64 data"})
		return nil
	}
	if len(a.Data) > llm.MaxOutputMediaPartBytes {
		*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path + ".data", Detail: "output media part exceeds per-part size limit"})
		return nil
	}
	if *mediaBytes+len(a.Data) > llm.MaxOutputMediaReplyBytes {
		*losses = llm.AddLoss(*losses, llm.Loss{Kind: llm.LossDropped, Path: path + ".data", Detail: "output media exceeds per-reply size limit"})
		return nil
	}
	*mediaBytes += len(a.Data)
	mt := defaultMT
	if sniff {
		mt = llm.SniffAudioMediaType(a.Data, defaultMT)
	}
	return &llm.OutputMedia{Kind: llm.MediaAudio, MediaType: mt, Data: a.Data}
}

func extraContent(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"extra_content": raw})
	return b
}

func DecodeResponse(body []byte) ([]llm.Event, error) {
	events, _, err := DecodeResponseLosses(body)
	return events, err
}

func DecodeResponseLosses(body []byte) ([]llm.Event, []llm.Loss, error) {
	var c completion
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, nil, malformed("upstream sent a malformed response", body)
	}
	if hasError(c.Error) {
		return nil, nil, upstreamErrorObject(c.Error, "upstream reported an error")
	}
	var losses []llm.Loss
	choices := make([]completionChoice, 0, len(c.Choices))
	var others []json.RawMessage
	first := false
	for i, raw := range c.Choices {
		var ch completionChoice
		if err := json.Unmarshal(raw, &ch); err != nil {
			if i == 0 {
				return nil, nil, malformed("upstream sent a malformed response", body)
			}
			losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d]", i), Detail: "malformed candidate"})
			continue
		}
		if ch.Index < 0 {
			losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d]", i), Detail: "candidate with negative index"})
			others = append(others, raw)
			continue
		}
		choices = append(choices, ch)
		if ch.Index != 0 {
			others = append(others, raw)
		} else if ch.Message != nil {
			first = true
		}
	}
	if !first {
		return nil, nil, malformed("upstream sent a response without a completion", body)
	}
	out := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{
		ID: c.ID, Model: c.Model, Created: c.Created, Format: llm.FormatOpenAIChat, Raw: replyFields(body),
	}}}
	next := 0
	item := func(start llm.ItemStart, delta string, deltaType string, raw, deltaRaw json.RawMessage) {
		out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start, Raw: raw})
		if delta != "" {
			out = append(out, llm.Event{Type: deltaType, Index: next, Delta: delta, Raw: deltaRaw})
		}
		out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next})
		next++
	}
	mediaBytes := 0
	var cites llm.CiteBudget
	var finishAnns []json.RawMessage
	var finishAudio json.RawMessage
	refused := false
	finish := llm.Finish{Reason: llm.FinishOther}
	used := map[string]bool{}
	made := 0
	calls := 0
	for _, ch := range choices {
		if ch.Index != 0 || ch.Message == nil {
			continue
		}
		m := ch.Message
		content := ""
		if m.Content != nil {
			content = *m.Content
		}
		reasoning := m.text()
		tagged, text := parseThink(content)
		if reasoning != "" {
			item(llm.ItemStart{Kind: llm.ItemReasoning}, reasoning, llm.EventReasoningDelta, nil, nil)
		}
		if tagged != "" && tagged != reasoning {
			item(llm.ItemStart{Kind: llm.ItemReasoning}, tagged, llm.EventReasoningDelta, nil, nil)
		}
		refusal := refusalText(m.Refusal)
		if refusal != "" {
			refused = true
		}
		var nativeAnns []json.RawMessage
		if items := parseNonEmptyAnnotations(m.Annotations); len(items) > 0 {
			if len(m.Annotations) > maxNativeAnnotationsBytes {
				losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: "choices[0].message.annotations", Detail: "annotations exceed size limit"})
			} else {
				nativeAnns = items
			}
		}
		if text != "" || refusal != "" {
			start := llm.ItemStart{Kind: llm.ItemMessage}
			out = append(out, llm.Event{Type: llm.EventItemStart, Index: next, Item: &start})
			if text != "" {
				meta := textMeta{}
				if refusal == "" {
					meta.Logprobs = logprobs(ch.Logprobs)
				}
				out = append(out, llm.Event{Type: llm.EventTextDelta, Index: next, Delta: text, Raw: meta.raw()})
			}
			if refusal != "" {
				meta := textMeta{Logprobs: logprobs(ch.Logprobs), Refusal: true}
				out = append(out, llm.Event{Type: llm.EventTextDelta, Index: next, Delta: refusal, Raw: meta.raw()})
			}
			var cits []llm.Citation
			if len(m.Annotations) > 0 && text != "" {
				prefixRunes := utf8.RuneCountInString(content) - utf8.RuneCountInString(text)
				cits = cites.KeepCitations(extractChatAnnotations(m.Annotations, prefixRunes, utf8.RuneCountInString(text)))
			}
			out = append(out, llm.Event{Type: llm.EventItemEnd, Index: next, Citations: cits, Raw: encodeItemEndAnnotations(nativeAnns)})
			next++
		} else if len(nativeAnns) > 0 {
			finishAnns = append(finishAnns, nativeAnns...)
		}
		if len(m.Audio) > 0 && string(m.Audio) != "null" {
			media := decodeChatAudio(m.Audio, "audio/wav", true, "choices[0].message.audio", &mediaBytes, &losses)
			meta, hasData, ok := audioMetaWithoutData(m.Audio)
			if media != nil {
				var evRaw json.RawMessage
				if ok && len(meta) > 0 {
					if len(meta) > maxNativeAudioMetaBytes {
						losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: "choices[0].message.audio", Detail: "audio metadata exceeds size limit"})
					} else {
						evRaw = encodeAudioEventMeta(nil, meta)
					}
				}
				out = append(out, llm.Event{Type: llm.EventMedia, Media: media, Raw: evRaw})
			} else if ok && !hasData && len(meta) > 0 {
				if len(meta) > maxNativeAudioMetaBytes {
					losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: "choices[0].message.audio", Detail: "audio metadata exceeds size limit"})
				} else {
					finishAudio = meta
				}
			}
		}
		for i, tc := range m.ToolCalls {
			if tc.Function.Name == "" {
				losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped,
					Path:   fmt.Sprintf("choices[0].message.tool_calls[%d]", i),
					Detail: "a tool call that never named its function"})
				continue
			}
			id := tc.ID
			if id == "" {
				id = "call_" + strconv.Itoa(i)
				for used[id] {
					id = "call_" + strconv.Itoa(made)
					made++
				}
			}
			used[id] = true
			calls++
			item(llm.ItemStart{Kind: llm.ItemToolCall, CallID: id, Name: tc.Function.Name}, tc.Function.Arguments, llm.EventToolInputDelta, extraContent(tc.ExtraContent), nil)
		}
		finish = decodeFinish(ch.FinishReason)
	}
	if finish.Reason == llm.FinishToolCalls && calls == 0 {
		finish.Reason = llm.FinishStop
	}
	if refused {
		finish.Reason = llm.FinishContentFilter
	}
	if ev := otherChoices(others); ev != nil {
		out = append(out, *ev)
	}
	for _, ch := range choices {
		if ch.Index <= 0 {
			continue
		}
		candRefused := false
		candCalls := 0
		candUsed := map[string]bool{}
		candMade := 0
		candItem := 0
		if m := ch.Message; m != nil {
			content := ""
			if m.Content != nil {
				content = *m.Content
			}
			reasoning := m.text()
			tagged, text := parseThink(content)
			if parseNonEmptyAnnotations(m.Annotations) != nil {
				losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped, Path: fmt.Sprintf("choices[%d].message.annotations", ch.Index),
					Detail: "citations of a candidate after the first have no place in the reply"})
			}
			if reasoning != "" {
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateReasoningDelta, Delta: reasoning}})
			}
			if tagged != "" && tagged != reasoning {
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateReasoningDelta, Delta: tagged}})
			}
			if text != "" {
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateTextDelta, Delta: text}})
			}
			if r := refusalText(m.Refusal); r != "" {
				candRefused = true
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateTextDelta, Delta: r}})
			}
			if len(m.Audio) > 0 && string(m.Audio) != "null" {
				path := fmt.Sprintf("choices[%d].message.audio", ch.Index)
				if media := decodeChatAudio(m.Audio, "audio/wav", true, path, &mediaBytes, &losses); media != nil {
					out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateMedia, Media: media}})
				}
			}
			for i, tc := range m.ToolCalls {
				if tc.Function.Name == "" {
					losses = llm.AddLoss(losses, llm.Loss{Kind: llm.LossDropped,
						Path:   fmt.Sprintf("choices[%d].message.tool_calls[%d]", ch.Index, i),
						Detail: "a tool call that never named its function"})
					continue
				}
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_c%d_%d", ch.Index, i)
					for candUsed[id] {
						id = fmt.Sprintf("call_c%d_%d", ch.Index, candMade)
						candMade++
					}
				}
				candUsed[id] = true
				candCalls++
				idx := candItem
				candItem++
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateToolCallStart, Item: idx, CallID: id, Name: tc.Function.Name}})
				if tc.Function.Arguments != "" {
					out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateToolInputDelta, Item: idx, Delta: tc.Function.Arguments}})
				}
				out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateToolCallEnd, Item: idx}})
			}
		}
		cf := decodeFinish(ch.FinishReason)
		if cf.Reason == llm.FinishToolCalls && candCalls == 0 {
			cf.Reason = llm.FinishStop
		}
		if candRefused {
			cf.Reason = llm.FinishContentFilter
		}
		out = append(out, llm.Event{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: ch.Index, Kind: llm.CandidateFinish, Finish: &cf}})
	}
	return append(out, llm.Event{Type: llm.EventFinish, Finish: &finish, Usage: decodeUsage(c.Usage), Raw: encodeFinishMeta(finishAnns, finishAudio)}), cites.WithLoss(losses), nil
}
