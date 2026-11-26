package openaichat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
)

type outToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type outDelta struct {
	Role             string          `json:"role,omitempty"`
	Content          *string         `json:"content,omitempty"`
	Refusal          *string         `json:"refusal,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall   `json:"tool_calls,omitempty"`
	Annotations      json.RawMessage `json:"annotations,omitempty"`
	Audio            json.RawMessage `json:"audio,omitempty"`
}

type outChoice struct {
	Index        int             `json:"index"`
	Delta        *outDelta       `json:"delta,omitempty"`
	Message      *outMsg         `json:"message,omitempty"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type outAnnotation struct {
	Type        string         `json:"type"`
	URLCitation outURLCitation `json:"url_citation"`
}

type outURLCitation struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

type outMsg struct {
	Role             string        `json:"role"`
	Content          *string       `json:"content"`
	Refusal          *string       `json:"refusal,omitempty"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
	Annotations      any           `json:"annotations,omitempty"`
	Audio            any           `json:"audio,omitempty"`
}

type outUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		// CachedTokens is a pointer, so a cached count nobody reported is left out, as llm.Usage
		// keeps it: nil is not zero.
		CachedTokens     *int64 `json:"cached_tokens,omitempty"`
		CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type outCompletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices any    `json:"choices"`
	Usage   any    `json:"usage,omitempty"`
}

// upstream is what a reply kept from a Chat Completions upstream that the model has no place
// for. It is written back only when the reply came from that format.
type upstream struct {
	native bool
	fields []byte // reply-level fields as `"k":v,...`, without braces
}

func newUpstream(r *llm.ResponseInfo) upstream {
	u := upstream{native: r.Format == llm.FormatOpenAIChat}
	if !u.native || len(r.Raw) == 0 {
		return u
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(r.Raw, &m) != nil {
		return u
	}
	for _, k := range modelledFields {
		delete(m, k)
	}
	if len(m) == 0 {
		return u
	}
	if b, err := json.Marshal(m); err == nil {
		u.fields = b[1 : len(b)-1]
	}
	return u
}

// withFields adds the kept reply-level fields to an encoded response or chunk object.
func (u upstream) withFields(obj []byte) []byte {
	if len(u.fields) == 0 {
		return obj
	}
	out := append(obj[:len(obj)-1:len(obj)-1], ',')
	out = append(out, u.fields...)
	return append(out, '}')
}

// usage returns the usage object to write: the upstream's own when it came from this format, so
// fields the model does not carry (such as a vendor cost) survive.
func (u upstream) usage(us *llm.Usage) any {
	if us == nil {
		return nil
	}
	if u.native && len(us.Raw) > 0 {
		return us.Raw
	}
	if out := encodeUsage(us); out != nil {
		return out
	}
	return nil
}

// choices returns the choices kept in a native event.
func (u upstream) choices(ev llm.Event) []json.RawMessage {
	if !u.native {
		return nil
	}
	var n struct {
		Choices []json.RawMessage `json:"choices"`
	}
	_ = json.Unmarshal(ev.Raw, &n)
	return n.Choices
}

// encodeUsage writes model usage as Chat Completions usage. When any count is known, all three
// counts CompletionUsage requires are written, a missing one as 0; a usage with no counts at all
// is left out, and a count the upstream reported as zero stays zero.
func encodeUsage(u *llm.Usage) *outUsage {
	if u == nil || !reportsAnyCount(u) {
		return nil
	}
	val := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	in, outTok := val(u.InputTokens), val(u.OutputTokens)
	out := &outUsage{PromptTokens: in, CompletionTokens: outTok, TotalTokens: in + outTok}
	if u.CachedInputTokens != nil || val(u.CacheWriteInputTokens) > 0 {
		out.PromptTokensDetails = &struct {
			CachedTokens     *int64 `json:"cached_tokens,omitempty"`
			CacheWriteTokens int64  `json:"cache_write_tokens,omitempty"`
		}{
			CachedTokens:     u.CachedInputTokens,
			CacheWriteTokens: max(0, val(u.CacheWriteInputTokens)),
		}
	}
	if u.ReasoningTokens != nil {
		out.CompletionTokensDetails = &struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		}{*u.ReasoningTokens}
	}
	return out
}

// reportsAnyCount reports whether the usage carries any count at all, the upstream's or 0.
func reportsAnyCount(u *llm.Usage) bool {
	return u.InputTokens != nil || u.OutputTokens != nil || u.CachedInputTokens != nil ||
		u.CacheWriteInputTokens != nil || u.ReasoningTokens != nil
}

func finishReason(f *llm.Finish) string {
	if f == nil {
		return "stop"
	}
	switch f.Reason {
	case llm.FinishStop, llm.FinishLength, llm.FinishToolCalls, llm.FinishContentFilter:
		return f.Reason
	}
	if f.RawReason != "" {
		return f.RawReason
	}
	return "stop"
}

// created returns the response's creation time, or now when the upstream gave none; clients
// expect the field to be set.
func created(r *llm.ResponseInfo) int64 {
	if r.Created != 0 {
		return r.Created
	}
	return time.Now().Unix()
}

// WantsStreamUsage reports whether a Chat Completions request asked for a usage chunk at the end
// of its stream (stream_options.include_usage).
func WantsStreamUsage(req llm.Request) bool {
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIChat {
		return false
	}
	var in chatRequest
	return json.Unmarshal(req.Native.Body, &in) == nil && in.StreamOptions != nil && in.StreamOptions.IncludeUsage
}

// StreamWriter writes events to a client as a Chat Completions stream.
type StreamWriter struct {
	w            io.Writer
	includeUsage bool
	head         outCompletion
	up           upstream
	tools        map[int]int // item index -> tool_calls index
	refused      bool        // a same-format reply carried a refusal
	cands        map[int]*outCandStream
	losses       []llm.Loss
	lost         map[string]bool
}

type outCandStream struct {
	started  bool
	finished bool
	tools    map[int]int
}

// NewStreamWriter returns a writer for one client stream. includeUsage adds the final usage
// chunk that stream_options.include_usage asks for.
func NewStreamWriter(w io.Writer, includeUsage bool) *StreamWriter {
	return &StreamWriter{w: w, includeUsage: includeUsage, tools: map[int]int{}, head: outCompletion{Object: "chat.completion.chunk"}}
}

// Losses returns what the reply had that Chat Completions could not carry.
func (s *StreamWriter) Losses() []llm.Loss { return s.losses }

// hasValidCitation reports whether cs holds a citation a client would be given.
func hasValidCitation(cs []llm.Citation) bool {
	return slices.ContainsFunc(cs, func(c llm.Citation) bool { return llm.ValidCitationURL(c.URL) })
}

// hasValidSource reports whether ss holds a source a client would be given.
func hasValidSource(ss []llm.Source) bool {
	return slices.ContainsFunc(ss, func(s llm.Source) bool { return llm.ValidCitationURL(s.URL) })
}

func (s *StreamWriter) lose(path, detail string) {
	if s.lost[path] || len(s.losses) > llm.MaxLosses {
		return
	}
	if s.lost == nil {
		s.lost = map[string]bool{}
	}
	s.lost[path] = true
	s.losses = llm.AddLoss(s.losses, llm.Loss{Kind: llm.LossDropped, Path: path, Detail: detail})
}

func (s *StreamWriter) loseMedia(m *llm.OutputMedia) {
	if m == nil {
		return
	}
	switch m.Kind {
	case llm.MediaImage:
		s.lose("media.image", "Chat Completions has no official output image field")
	case llm.MediaAudio:
		s.lose("media.audio", "Chat Completions message.audio requires a server-issued id and expires_at")
	default:
		s.lose("media", "Chat Completions has no place for this output media")
	}
}

func (s *StreamWriter) chunk(choices any, usage any) error {
	c := s.head
	c.Choices = choices
	c.Usage = usage
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return sse.WriteData(s.w, s.up.withFields(b))
}

func (s *StreamWriter) delta(d outDelta) error {
	return s.chunk([]outChoice{{Delta: &d}}, nil)
}

// Write writes the chunks for one event. After a finish event it writes "[DONE]"; after an
// error event it writes the error object and no "[DONE]", so the client can tell the reply is
// incomplete.
func (s *StreamWriter) Write(ev llm.Event) error {
	switch ev.Type {
	case llm.EventResponseStart:
		s.head.ID, s.head.Model, s.head.Created = ev.Response.ID, ev.Response.Model, created(ev.Response)
		s.up = newUpstream(ev.Response)
		empty := ""
		return s.delta(outDelta{Role: llm.RoleAssistant, Content: &empty})
	case llm.EventItemStart:
		if ev.Item.Kind != llm.ItemToolCall {
			return nil
		}
		n := len(s.tools)
		s.tools[ev.Index] = n
		tc := outToolCall{Index: &n, ID: ev.Item.CallID, Type: "function"}
		tc.Function.Name = ev.Item.Name
		return s.delta(outDelta{ToolCalls: []outToolCall{tc}})
	case llm.EventTextDelta:
		d := ev.Delta
		meta := textMeta{}
		if s.up.native {
			meta = textMetaOf(ev.Raw)
		}
		delta := outDelta{}
		if meta.Refusal {
			s.refused = true
			delta.Refusal = &d
		} else {
			delta.Content = &d
		}
		c := outChoice{Delta: &delta}
		if s.up.native {
			// The event's raw holds the choice's logprobs when the upstream sent them.
			c.Logprobs = meta.Logprobs
		}
		return s.chunk([]outChoice{c}, nil)
	case llm.EventReasoningDelta:
		d := ev.Delta
		return s.delta(outDelta{ReasoningContent: &d})
	case llm.EventToolInputDelta:
		n := s.tools[ev.Index]
		tc := outToolCall{Index: &n}
		tc.Function.Arguments = ev.Delta
		return s.delta(outDelta{ToolCalls: []outToolCall{tc}})
	case llm.EventItemEnd:
		if s.up.native && len(ev.Raw) > 0 {
			var m itemEndMeta
			if json.Unmarshal(ev.Raw, &m) == nil && len(m.Annotations) > 0 {
				return s.delta(outDelta{Annotations: m.Annotations})
			}
		}
		if !s.up.native && hasValidCitation(ev.Citations) {
			s.lose("content.citations", "Chat Completions streams carry annotations only for a Chat upstream's reply")
		}
		return nil
	case llm.EventMedia:
		if s.up.native && ev.Media != nil && ev.Media.Kind == llm.MediaAudio {
			var m audioEventMeta
			if len(ev.Raw) > 0 {
				_ = json.Unmarshal(ev.Raw, &m)
			}
			if len(m.Pending) > 0 {
				if err := s.delta(outDelta{Audio: m.Pending}); err != nil {
					return err
				}
			}
			if audio := combineAudioDelta(m.Audio, ev.Media.Data); len(audio) > 0 {
				return s.delta(outDelta{Audio: audio})
			}
			return nil
		}
		s.loseMedia(ev.Media)
		return nil
	case llm.EventCandidate:
		if s.up.native || ev.Candidate == nil || ev.Candidate.Index < 1 {
			return nil
		}
		c := ev.Candidate
		if s.cands == nil {
			s.cands = map[int]*outCandStream{}
		}
		cs := s.cands[c.Index]
		if cs == nil {
			cs = &outCandStream{tools: map[int]int{}}
			s.cands[c.Index] = cs
		}
		if !cs.started {
			cs.started = true
			empty := ""
			if err := s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{Role: llm.RoleAssistant, Content: &empty}}}, nil); err != nil {
				return err
			}
		}
		switch c.Kind {
		case llm.CandidateTextDelta:
			d := c.Delta
			return s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{Content: &d}}}, nil)
		case llm.CandidateReasoningDelta:
			d := c.Delta
			return s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{ReasoningContent: &d}}}, nil)
		case llm.CandidateToolCallStart:
			n := len(cs.tools)
			cs.tools[c.Item] = n
			tc := outToolCall{Index: &n, ID: c.CallID, Type: "function"}
			tc.Function.Name = c.Name
			return s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{ToolCalls: []outToolCall{tc}}}}, nil)
		case llm.CandidateToolInputDelta:
			if n, ok := cs.tools[c.Item]; ok {
				tc := outToolCall{Index: &n}
				tc.Function.Arguments = c.Delta
				return s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{ToolCalls: []outToolCall{tc}}}}, nil)
			}
		case llm.CandidateMedia:
			s.loseMedia(c.Media)
		case llm.CandidateFinish:
			reason := finishReason(c.Finish)
			cs.finished = true
			return s.chunk([]outChoice{{Index: c.Index, Delta: &outDelta{}, FinishReason: &reason}}, nil)
		}
		return nil
	case llm.EventNative:
		if c := s.up.choices(ev); len(c) > 0 {
			return s.chunk(c, nil)
		}
		return nil
	case llm.EventFinish:
		if s.up.native && len(ev.Raw) > 0 {
			var fm finishMeta
			if json.Unmarshal(ev.Raw, &fm) == nil {
				if len(fm.Annotations) > 0 {
					if err := s.delta(outDelta{Annotations: fm.Annotations}); err != nil {
						return err
					}
				}
				if len(fm.Audio) > 0 {
					if err := s.delta(outDelta{Audio: fm.Audio}); err != nil {
						return err
					}
				}
			}
		}
		if !s.up.native && hasValidSource(ev.Sources) {
			s.lose("sources", "Chat Completions streams carry no web search sources from another format")
		}
		if !s.up.native && len(ev.CodeExecutions) > 0 {
			s.lose("code_execution", "Chat Completions has no place for code execution results")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					s.lose("code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in Chat Completions")
					break
				}
			}
		}
		reason := finishReason(ev.Finish)
		if s.up.native && s.refused && ev.Finish != nil && ev.Finish.RawReason != "" {
			// A same-format reply keeps the finish reason its upstream sent.
			reason = ev.Finish.RawReason
		}
		if err := s.chunk([]outChoice{{Delta: &outDelta{}, FinishReason: &reason}}, nil); err != nil {
			return err
		}
		if len(s.cands) > 0 {
			idxs := make([]int, 0, len(s.cands))
			for idx, cs := range s.cands {
				if !cs.finished {
					idxs = append(idxs, idx)
				}
			}
			slices.Sort(idxs)
			stop := "stop"
			for _, idx := range idxs {
				if err := s.chunk([]outChoice{{Index: idx, Delta: &outDelta{}, FinishReason: &stop}}, nil); err != nil {
					return err
				}
			}
		}
		if s.includeUsage && ev.Usage != nil {
			if err := s.chunk([]outChoice{}, s.up.usage(ev.Usage)); err != nil {
				return err
			}
		}
		_, err := io.WriteString(s.w, "data: [DONE]\n\n")
		return err
	case llm.EventError:
		_, body := ErrorResponse(ev.Error)
		if err := sse.WriteData(s.w, body); err != nil || ev.Error.UsageLimit == nil {
			return err
		}
		// As in today's core, a stream that ends in the usage-limit reply is closed with [DONE].
		_, err := io.WriteString(s.w, "data: [DONE]\n\n")
		return err
	}
	return nil
}

// Aggregator collects events into a non-streamed Chat Completions response.
type Aggregator struct {
	head              outCompletion
	up                upstream
	others            []*otherChoice          // choices after the first, in the order they came
	streamed          map[string]*otherChoice // the ones put together from stream fragments, by index
	text              strings.Builder
	refusal           strings.Builder
	reasoning         strings.Builder
	tools             []outToolCall
	toolArgs          []*strings.Builder // the arguments of each tool call, as they streamed, by position in tools
	toolIdx           map[int]int
	finish            *llm.Finish
	usage             *llm.Usage
	logprobs          []json.RawMessage // each text delta's logprobs, as the upstream sent them
	refused           bool              // a same-format reply carried a refusal
	msgStart          map[int]int       // message item index -> starting rune offset in text
	msgRunes          map[int]int       // message item index -> rune count written to text
	totalRunes        int               // total runes written to text across message items
	annotations       []outAnnotation
	citedURLs         map[string]bool // the URLs the annotations cite
	nativeAnnotations []json.RawMessage
	audio             *audioAgg
	cands             map[int]*outCandAgg
	losses            []llm.Loss
	lost              map[string]bool
	lossSize          int
	size              int
}

type outCandAgg struct {
	text      strings.Builder
	reasoning strings.Builder
	tools     []outToolCall
	toolArgs  []*strings.Builder
	toolIdx   map[int]int
	finish    *llm.Finish
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{
		head:     outCompletion{Object: "chat.completion"},
		toolIdx:  map[int]int{},
		streamed: map[string]*otherChoice{},
		msgStart: map[int]int{},
		msgRunes: map[int]int{},
	}
}

// Losses returns what the reply had that Chat Completions could not carry.
func (a *Aggregator) Losses() []llm.Loss { return a.losses }

func (a *Aggregator) lose(path, detail string) {
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

func (a *Aggregator) loseMedia(m *llm.OutputMedia) {
	if m == nil {
		return
	}
	switch m.Kind {
	case llm.MediaImage:
		a.lose("media.image", "Chat Completions has no official output image field")
	case llm.MediaAudio:
		a.lose("media.audio", "Chat Completions message.audio requires a server-issued id and expires_at")
	default:
		a.lose("media", "Chat Completions has no place for this output media")
	}
}

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	a.size += len(ev.Delta)
	switch ev.Type {
	case llm.EventResponseStart:
		a.head.ID, a.head.Model, a.head.Created = ev.Response.ID, ev.Response.Model, created(ev.Response)
		a.up = newUpstream(ev.Response)
		a.size += len(a.head.ID) + len(a.head.Model) + len(a.up.fields)
	case llm.EventMedia:
		if a.up.native && ev.Media != nil && ev.Media.Kind == llm.MediaAudio {
			a.size += len(ev.Media.Data) + len(ev.Raw)
			var m audioEventMeta
			if len(ev.Raw) > 0 {
				_ = json.Unmarshal(ev.Raw, &m)
			}
			if a.audio == nil {
				a.audio = &audioAgg{}
			}
			if len(m.Pending) > 0 {
				a.audio.add(m.Pending, "")
			}
			a.audio.add(m.Audio, ev.Media.Data)
			return
		}
		a.loseMedia(ev.Media)
	case llm.EventCandidate:
		if a.up.native || ev.Candidate == nil || ev.Candidate.Index < 1 {
			return
		}
		c := ev.Candidate
		if a.cands == nil {
			a.cands = map[int]*outCandAgg{}
		}
		ca := a.cands[c.Index]
		if ca == nil {
			ca = &outCandAgg{toolIdx: map[int]int{}}
			a.cands[c.Index] = ca
		}
		a.size += len(c.Delta)
		switch c.Kind {
		case llm.CandidateTextDelta:
			ca.text.WriteString(c.Delta)
		case llm.CandidateReasoningDelta:
			ca.reasoning.WriteString(c.Delta)
		case llm.CandidateToolCallStart:
			a.size += len(c.CallID) + len(c.Name) + toolCallOverhead
			ca.toolIdx[c.Item] = len(ca.tools)
			tc := outToolCall{ID: c.CallID, Type: "function"}
			tc.Function.Name = c.Name
			ca.tools = append(ca.tools, tc)
			ca.toolArgs = append(ca.toolArgs, new(strings.Builder))
		case llm.CandidateToolInputDelta:
			if i, ok := ca.toolIdx[c.Item]; ok {
				ca.toolArgs[i].WriteString(c.Delta)
			}
		case llm.CandidateMedia:
			a.loseMedia(c.Media)
		case llm.CandidateFinish:
			ca.finish = c.Finish
			if c.Finish != nil {
				a.size += len(c.Finish.RawReason)
			}
		}
	case llm.EventNative:
		for _, c := range a.up.choices(ev) {
			a.size += len(c)
			a.addOther(c)
		}
	case llm.EventItemStart:
		if ev.Item.Kind == llm.ItemMessage {
			a.msgStart[ev.Index] = a.totalRunes
		}
		if ev.Item.Kind == llm.ItemToolCall {
			a.size += len(ev.Item.CallID) + len(ev.Item.Name) + toolCallOverhead
			a.toolIdx[ev.Index] = len(a.tools)
			tc := outToolCall{ID: ev.Item.CallID, Type: "function"}
			tc.Function.Name = ev.Item.Name
			a.tools = append(a.tools, tc)
			a.toolArgs = append(a.toolArgs, new(strings.Builder))
		}
	case llm.EventTextDelta:
		meta := textMeta{}
		if a.up.native {
			meta = textMetaOf(ev.Raw)
		}
		if meta.Refusal {
			a.refusal.WriteString(ev.Delta)
			a.refused = true
		} else {
			a.text.WriteString(ev.Delta)
			n := utf8.RuneCountInString(ev.Delta)
			a.totalRunes += n
			a.msgRunes[ev.Index] += n
		}
		if len(meta.Logprobs) > 0 {
			a.logprobs = append(a.logprobs, meta.Logprobs)
			a.size += len(meta.Logprobs)
		}
	case llm.EventItemEnd:
		if a.up.native && len(ev.Raw) > 0 {
			var m itemEndMeta
			if json.Unmarshal(ev.Raw, &m) == nil {
				if items := parseNonEmptyAnnotations(m.Annotations); len(items) > 0 {
					a.nativeAnnotations = append(a.nativeAnnotations, items...)
					a.size += len(m.Annotations)
				}
			}
		}
		if len(ev.Citations) > 0 && !a.up.native {
			base := a.msgStart[ev.Index]
			itemRunes := a.msgRunes[ev.Index]
			for _, c := range ev.Citations {
				if !llm.ValidCitationURL(c.URL) {
					continue
				}
				// Chat requires both indices: a citation with no narrower range covers its item.
				uc := outURLCitation{URL: c.URL, Title: c.Title, StartIndex: base, EndIndex: base + itemRunes}
				if c.StartIndex != nil && c.EndIndex != nil && *c.StartIndex >= 0 && *c.EndIndex >= *c.StartIndex && *c.EndIndex <= itemRunes {
					uc.StartIndex, uc.EndIndex = base+*c.StartIndex, base+*c.EndIndex
				}
				a.annotations = append(a.annotations, outAnnotation{Type: "url_citation", URLCitation: uc})
				if a.citedURLs == nil {
					a.citedURLs = map[string]bool{}
				}
				a.citedURLs[c.URL] = true
				a.size += len(c.URL) + len(c.Title) + 48
			}
		}
	case llm.EventReasoningDelta:
		a.reasoning.WriteString(ev.Delta)
	case llm.EventToolInputDelta:
		// Input for an index no tool call opened has no call to join.
		if i, ok := a.toolIdx[ev.Index]; ok {
			a.toolArgs[i].WriteString(ev.Delta)
		}
	case llm.EventFinish:
		a.finish, a.usage = ev.Finish, ev.Usage
		if a.up.native && len(ev.Raw) > 0 {
			var fm finishMeta
			if json.Unmarshal(ev.Raw, &fm) == nil {
				if items := parseNonEmptyAnnotations(fm.Annotations); len(items) > 0 {
					a.nativeAnnotations = append(a.nativeAnnotations, items...)
					a.size += len(fm.Annotations)
				}
				if len(fm.Audio) > 0 {
					if a.audio == nil {
						a.audio = &audioAgg{}
					}
					a.audio.add(fm.Audio, "")
					a.size += len(fm.Audio)
				}
			}
		}
		if !a.up.native {
			for _, src := range ev.Sources {
				if llm.ValidCitationURL(src.URL) && !a.citedURLs[src.URL] {
					a.lose("sources", "uncited web search sources have no place in a Chat Completions message")
					break
				}
			}
		}
		if !a.up.native && len(ev.CodeExecutions) > 0 {
			a.lose("code_execution", "Chat Completions has no place for code execution results")
			for _, ce := range ev.CodeExecutions {
				if ce.FilesOmitted {
					a.lose("code_execution.files", "files, images, and runs left out (past the size limit or with no result to read) have no place in Chat Completions")
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

// otherChoice is a choice after the first: whole as a non-streamed upstream sent it (raw), or
// put together from the fragments a streaming upstream sent (snap).
type otherChoice struct {
	raw  json.RawMessage
	snap map[string]any
}

// addOther records a choice after the first. A stream fragment (a choice with a delta, or without
// a message once its index has streamed) is merged into the choice with its index the way
// OpenAI's Python SDK puts a streamed completion together: the delta into the message, logprobs
// lists appended, other choice fields replaced.
func (a *Aggregator) addOther(c json.RawMessage) {
	var ch map[string]any
	d := json.NewDecoder(bytes.NewReader(c))
	d.UseNumber()
	err := d.Decode(&ch)
	idx := fmt.Sprint(ch["index"])
	o := a.streamed[idx]
	// A fragment may come without a delta, such as one that only finishes its choice.
	_, frag := ch["delta"]
	if err != nil || ch["message"] != nil || !frag && o == nil {
		a.others = append(a.others, &otherChoice{raw: c})
		return
	}
	if o == nil {
		o = &otherChoice{snap: map[string]any{"message": map[string]any{}, "finish_reason": nil}}
		a.streamed[idx] = o
		a.others = append(a.others, o)
	}
	for k, v := range ch {
		switch {
		case v == nil:
		case k == "delta":
			if delta, ok := v.(map[string]any); ok {
				mergeDelta(o.snap["message"].(map[string]any), delta)
			}
		case k == "logprobs":
			if m, ok := v.(map[string]any); ok {
				prev, _ := o.snap[k].(map[string]any)
				if prev == nil {
					prev = map[string]any{}
				}
				o.snap[k] = mergeDelta(prev, m)
			}
		default:
			o.snap[k] = v
		}
	}
}

// value returns the choice as the response holds it. A message put together from fragments
// gets the role and content a whole message has, and its tool calls lose their stream index.
func (o *otherChoice) value() any {
	if o.snap == nil {
		return o.raw
	}
	plain(o.snap)
	msg := o.snap["message"].(map[string]any)
	if msg["role"] == nil {
		msg["role"] = llm.RoleAssistant
	}
	calls, _ := msg["tool_calls"].([]any)
	for _, c := range calls {
		if m, ok := c.(map[string]any); ok {
			delete(m, "index")
		}
	}
	if msg["content"] == nil && len(calls) == 0 {
		msg["content"] = ""
	}
	return o.snap
}

// keepFirst are delta fields that name something rather than stream it: an upstream that repeats
// them in later fragments repeats the same value. extra_content (a Gemini thought signature) is
// replaced whole by a later one, as on the first choice.
var (
	keepFirst = map[string]bool{"index": true, "type": true, "id": true, "role": true, "name": true}
	replaced  = map[string]bool{"extra_content": true}
)

// mergeDelta merges a stream delta into acc and returns acc: strings are joined, objects merged,
// list entries with an index merged into the entry with the same index, other list entries
// appended, and any other value replaced. Unlike the SDK, numbers are replaced rather than
// added, and the keepFirst fields are not joined.
func mergeDelta(acc, delta map[string]any) map[string]any {
	for k, dv := range delta {
		av := acc[k]
		switch {
		case dv == nil:
		case av == nil || replaced[k]:
			if l, ok := dv.([]any); ok {
				dv = new(list).merge(l)
			}
			acc[k] = dv
		case keepFirst[k]:
			if av == "" {
				acc[k] = dv
			}
		default:
			switch a := av.(type) {
			case string:
				if d, ok := dv.(string); ok {
					b := new(strings.Builder)
					b.WriteString(a)
					b.WriteString(d)
					acc[k] = b
				}
			case *strings.Builder:
				if d, ok := dv.(string); ok {
					a.WriteString(d)
				}
			case map[string]any:
				if d, ok := dv.(map[string]any); ok {
					acc[k] = mergeDelta(a, d)
				}
			case []any:
				if d, ok := dv.([]any); ok {
					acc[k] = new(list).merge(a).merge(d)
				}
			case *list:
				if d, ok := dv.([]any); ok {
					a.merge(d)
				}
			default:
				acc[k] = dv
			}
		}
	}
	return acc
}

// list is a list being put together from stream fragments. It finds its entries with an index
// by that index, so merging takes work in proportion to the fragments.
type list struct {
	entries []any
	byIndex map[string]map[string]any
}

// merge merges delta's entries into l and returns l: an entry with an index into the entry with
// the same index, any other entry appended.
func (l *list) merge(delta []any) *list {
	for _, e := range delta {
		m, ok := e.(map[string]any)
		if ok && m["index"] != nil {
			k := indexKey(m["index"])
			if prev := l.byIndex[k]; prev != nil {
				mergeDelta(prev, m)
				continue
			}
			if l.byIndex == nil {
				l.byIndex = map[string]map[string]any{}
			}
			l.byIndex[k] = m
		}
		l.entries = append(l.entries, e)
	}
	return l
}

// indexKey is an index as text, so the number 1 and the string "1" name the same entry.
func indexKey(idx any) string {
	switch v := idx.(type) {
	case json.Number:
		return string(v)
	case string:
		return v
	}
	return fmt.Sprint(idx)
}

// plain turns the lists in v, a value put together from fragments, back into JSON arrays, in
// place, and returns v.
func plain(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = plain(e)
		}
	case *list:
		for i, e := range x.entries {
			x.entries[i] = plain(e)
		}
		return x.entries
	case *strings.Builder:
		return x.String()
	case []any:
		for i, e := range x {
			x[i] = plain(e)
		}
	}
	return v
}

// combineAudioDelta merges a Chat audio delta's metadata object with its base64 data field.
func combineAudioDelta(metaRaw json.RawMessage, data string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(metaRaw) > 0 {
		_ = json.Unmarshal(metaRaw, &m)
	}
	if data != "" {
		if b, err := json.Marshal(data); err == nil {
			m["data"] = b
		}
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

// audioAgg collects same-format Chat audio metadata and base64 audio chunks into a message.audio
// object. Multiple base64 data chunks are decoded and re-encoded together so intermediate padding
// does not corrupt the combined base64 string.
type audioAgg struct {
	obj       map[string]any
	dataParts []string
}

func (a *audioAgg) add(metaRaw json.RawMessage, data string) {
	if len(metaRaw) > 0 {
		var m map[string]any
		dec := json.NewDecoder(bytes.NewReader(metaRaw))
		dec.UseNumber()
		if dec.Decode(&m) == nil && len(m) > 0 {
			if a.obj == nil {
				a.obj = map[string]any{}
			}
			mergeDelta(a.obj, m)
		}
	}
	if data != "" {
		a.dataParts = append(a.dataParts, data)
	}
}

func (a *audioAgg) value() any {
	out := map[string]any{}
	if len(a.obj) > 0 {
		plain(a.obj)
		for k, v := range a.obj {
			out[k] = v
		}
	}
	switch len(a.dataParts) {
	case 0:
	case 1:
		out["data"] = a.dataParts[0]
	default:
		var raw []byte
		ok := true
		for _, p := range a.dataParts {
			b, err := base64.StdEncoding.DecodeString(p)
			if err != nil {
				ok = false
				break
			}
			raw = append(raw, b...)
		}
		if ok {
			out["data"] = base64.StdEncoding.EncodeToString(raw)
		} else {
			out["data"] = strings.Join(a.dataParts, "")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeLogprobs puts the logprobs of the text deltas together as a message's: the entries of each
// delta's content and refusal lists, in the order the tokens came. It returns nil when there are
// none.
func mergeLogprobs(parts []json.RawMessage) json.RawMessage {
	var content, refusal []json.RawMessage
	for _, raw := range parts {
		var p struct {
			Content []json.RawMessage `json:"content"`
			Refusal []json.RawMessage `json:"refusal"`
		}
		if json.Unmarshal(raw, &p) != nil {
			continue
		}
		content = append(content, p.Content...)
		refusal = append(refusal, p.Refusal...)
	}
	if len(content) == 0 && len(refusal) == 0 {
		return nil
	}
	b, err := json.Marshal(struct {
		Content []json.RawMessage `json:"content"`
		Refusal []json.RawMessage `json:"refusal,omitempty"`
	}{content, refusal})
	if err != nil {
		return nil
	}
	return b
}

// toolCallOverhead is the JSON a tool call adds around its ID, name and arguments.
const toolCallOverhead = len(`{"id":"","type":"function","function":{"name":"","arguments":""}},`)

// Size is about how many bytes of content the response holds so far: text, reasoning, tool
// calls, extra choices and what it keeps of the upstream's reply (id, model, reply-level fields,
// finish reason and usage), before JSON escaping. Callers use it to bound what they collect.
func (a *Aggregator) Size() int { return a.size + a.lossSize }

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	for i := range a.tools {
		a.tools[i].Function.Arguments = a.toolArgs[i].String()
	}
	msg := &outMsg{Role: llm.RoleAssistant, ToolCalls: a.tools}
	if a.refused {
		r := a.refusal.String()
		msg.Refusal = &r
	}
	// A refusal, tool call or audio-only message leaves content null when the reply has no text.
	if a.text.Len() > 0 || (len(a.tools) == 0 && !a.refused && a.audio == nil) {
		t := a.text.String()
		msg.Content = &t
	}
	if a.reasoning.Len() > 0 {
		r := a.reasoning.String()
		msg.ReasoningContent = &r
	}
	if len(a.nativeAnnotations) > 0 {
		msg.Annotations = a.nativeAnnotations
	} else if len(a.annotations) > 0 {
		msg.Annotations = a.annotations
	}
	if a.audio != nil {
		msg.Audio = a.audio.value()
	}
	reason := finishReason(a.finish)
	if a.up.native && a.refused && a.finish != nil && a.finish.RawReason != "" {
		// A same-format reply keeps the finish reason its upstream sent.
		reason = a.finish.RawReason
	}
	c := a.head
	choices := []any{outChoice{Message: msg, FinishReason: &reason, Logprobs: mergeLogprobs(a.logprobs)}}
	for _, o := range a.others {
		choices = append(choices, o.value())
	}
	if len(a.cands) > 0 {
		idxs := make([]int, 0, len(a.cands))
		for idx := range a.cands {
			idxs = append(idxs, idx)
		}
		slices.Sort(idxs)
		for _, idx := range idxs {
			ca := a.cands[idx]
			for i := range ca.tools {
				ca.tools[i].Function.Arguments = ca.toolArgs[i].String()
			}
			cm := &outMsg{Role: llm.RoleAssistant, ToolCalls: ca.tools}
			if ca.text.Len() > 0 || len(ca.tools) == 0 {
				t := ca.text.String()
				cm.Content = &t
			}
			if ca.reasoning.Len() > 0 {
				r := ca.reasoning.String()
				cm.ReasoningContent = &r
			}
			creason := finishReason(ca.finish)
			choices = append(choices, outChoice{Index: idx, Message: cm, FinishReason: &creason})
		}
	}
	c.Choices = choices
	c.Usage = a.up.usage(a.usage)
	b, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return a.up.withFields(b), nil
}

// ErrorResponse returns the HTTP status and OpenAI-style error body for e. The usage-limit
// reply has today's core's body.
func ErrorResponse(e *llm.Error) (int, []byte) {
	if u := e.UsageLimit; u != nil {
		o := u.ErrorObject()
		e.Pluxy(o)
		body, _ := json.Marshal(map[string]any{"error": o})
		return http.StatusTooManyRequests, body
	}
	status := e.Status
	if status < 400 || status > 599 {
		status = map[string]int{
			llm.CodeInvalidRequest:       http.StatusBadRequest,
			llm.CodeUnsupported:          http.StatusBadRequest,
			llm.CodeAuth:                 http.StatusUnauthorized,
			llm.CodePermission:           http.StatusForbidden,
			llm.CodeAccountClosed:        http.StatusForbidden,
			llm.CodeNotFound:             http.StatusNotFound,
			llm.CodeRateLimit:            http.StatusTooManyRequests,
			llm.CodeQuota:                http.StatusTooManyRequests,
			llm.CodeUnavailable:          http.StatusServiceUnavailable,
			llm.CodeExtensionUnavailable: http.StatusServiceUnavailable,
		}[e.Code]
		if status == 0 {
			status = http.StatusBadGateway
		}
		if e.Code == llm.CodeInternal {
			status = http.StatusInternalServerError
		}
	}
	typ := "server_error"
	switch {
	case status == http.StatusUnauthorized:
		typ = "authentication_error"
	case status == http.StatusForbidden:
		typ = "permission_error"
	case status == http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case status < 500:
		typ = "invalid_request_error"
	}
	code := e.ProviderCode
	if code == "" {
		code = e.Code
	}
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("%s (%d)", http.StatusText(status), status)
	}
	o := map[string]any{"message": msg, "type": typ, "code": code}
	if p := upstreamParam(e.Raw); p != "" {
		o["param"] = p
	}
	e.Pluxy(o)
	body, _ := json.Marshal(map[string]any{"error": o})
	return status, body
}

// upstreamParam returns the param of an OpenAI-format error, when it names one: in an HTTP error
// body (error.param), a failed Responses reply (response.error.param), or a stream's error event
// or error object (param). Messages and Google errors have no such field.
func upstreamParam(raw json.RawMessage) string {
	var b struct {
		Param any `json:"param"`
		Error *struct {
			Param any `json:"param"`
		} `json:"error"`
		Response *struct {
			Error *struct {
				Param any `json:"param"`
			} `json:"error"`
		} `json:"response"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return ""
	}
	param := b.Param
	if b.Error != nil {
		param = b.Error.Param
	} else if b.Response != nil && b.Response.Error != nil {
		param = b.Response.Error.Param
	}
	s, _ := param.(string)
	return s
}
