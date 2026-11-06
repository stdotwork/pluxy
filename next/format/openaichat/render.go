package openaichat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
	Role             string        `json:"role,omitempty"`
	Content          *string       `json:"content,omitempty"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type outChoice struct {
	Index        int       `json:"index"`
	Delta        *outDelta `json:"delta,omitempty"`
	Message      *outMsg   `json:"message,omitempty"`
	FinishReason *string   `json:"finish_reason"`
}

type outMsg struct {
	Role             string        `json:"role"`
	Content          *string       `json:"content"`
	ReasoningContent *string       `json:"reasoning_content,omitempty"`
	ToolCalls        []outToolCall `json:"tool_calls,omitempty"`
}

type outUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type outCompletion struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	Created int64       `json:"created"`
	Model   string      `json:"model"`
	Choices []outChoice `json:"choices"`
	Usage   *outUsage   `json:"usage,omitempty"`
}

func encodeUsage(u *llm.Usage) *outUsage {
	if u == nil {
		return nil
	}
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	out := &outUsage{PromptTokens: v(u.InputTokens), CompletionTokens: v(u.OutputTokens)}
	out.TotalTokens = out.PromptTokens + out.CompletionTokens
	if u.CachedInputTokens != nil {
		out.PromptTokensDetails = &struct {
			CachedTokens int64 `json:"cached_tokens"`
		}{*u.CachedInputTokens}
	}
	if u.ReasoningTokens != nil {
		out.CompletionTokensDetails = &struct {
			ReasoningTokens int64 `json:"reasoning_tokens"`
		}{*u.ReasoningTokens}
	}
	return out
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
	tools        map[int]int // item index -> tool_calls index
}

// NewStreamWriter returns a writer for one client stream. includeUsage adds the final usage
// chunk that stream_options.include_usage asks for.
func NewStreamWriter(w io.Writer, includeUsage bool) *StreamWriter {
	return &StreamWriter{w: w, includeUsage: includeUsage, tools: map[int]int{}, head: outCompletion{Object: "chat.completion.chunk"}}
}

func (s *StreamWriter) chunk(choices []outChoice, usage *outUsage) error {
	c := s.head
	c.Choices = choices
	c.Usage = usage
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return sse.WriteData(s.w, b)
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
		return s.delta(outDelta{Content: &d})
	case llm.EventReasoningDelta:
		d := ev.Delta
		return s.delta(outDelta{ReasoningContent: &d})
	case llm.EventToolInputDelta:
		n := s.tools[ev.Index]
		tc := outToolCall{Index: &n}
		tc.Function.Arguments = ev.Delta
		return s.delta(outDelta{ToolCalls: []outToolCall{tc}})
	case llm.EventItemEnd:
		return nil
	case llm.EventFinish:
		reason := finishReason(ev.Finish)
		if err := s.chunk([]outChoice{{Delta: &outDelta{}, FinishReason: &reason}}, nil); err != nil {
			return err
		}
		if s.includeUsage && ev.Usage != nil {
			if err := s.chunk([]outChoice{}, encodeUsage(ev.Usage)); err != nil {
				return err
			}
		}
		_, err := io.WriteString(s.w, "data: [DONE]\n\n")
		return err
	case llm.EventError:
		_, body := ErrorResponse(ev.Error)
		return sse.WriteData(s.w, body)
	}
	return nil
}

// Aggregator collects events into a non-streamed Chat Completions response.
type Aggregator struct {
	head      outCompletion
	text      strings.Builder
	reasoning strings.Builder
	tools     []outToolCall
	toolIdx   map[int]int
	finish    *llm.Finish
	usage     *llm.Usage
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator {
	return &Aggregator{head: outCompletion{Object: "chat.completion"}, toolIdx: map[int]int{}}
}

// Add records one event.
func (a *Aggregator) Add(ev llm.Event) {
	switch ev.Type {
	case llm.EventResponseStart:
		a.head.ID, a.head.Model, a.head.Created = ev.Response.ID, ev.Response.Model, created(ev.Response)
	case llm.EventItemStart:
		if ev.Item.Kind == llm.ItemToolCall {
			a.toolIdx[ev.Index] = len(a.tools)
			tc := outToolCall{ID: ev.Item.CallID, Type: "function"}
			tc.Function.Name = ev.Item.Name
			a.tools = append(a.tools, tc)
		}
	case llm.EventTextDelta:
		a.text.WriteString(ev.Delta)
	case llm.EventReasoningDelta:
		a.reasoning.WriteString(ev.Delta)
	case llm.EventToolInputDelta:
		a.tools[a.toolIdx[ev.Index]].Function.Arguments += ev.Delta
	case llm.EventFinish:
		a.finish, a.usage = ev.Finish, ev.Usage
	}
}

// Response returns the response body.
func (a *Aggregator) Response() ([]byte, error) {
	msg := &outMsg{Role: llm.RoleAssistant, ToolCalls: a.tools}
	if a.text.Len() > 0 || len(a.tools) == 0 {
		t := a.text.String()
		msg.Content = &t
	}
	if a.reasoning.Len() > 0 {
		r := a.reasoning.String()
		msg.ReasoningContent = &r
	}
	reason := finishReason(a.finish)
	c := a.head
	c.Choices = []outChoice{{Message: msg, FinishReason: &reason}}
	c.Usage = encodeUsage(a.usage)
	return json.Marshal(c)
}

// ErrorResponse returns the HTTP status and OpenAI-style error body for e.
func ErrorResponse(e *llm.Error) (int, []byte) {
	status := e.Status
	if status < 400 || status > 599 {
		status = map[string]int{
			llm.CodeInvalidRequest:       http.StatusBadRequest,
			llm.CodeUnsupported:          http.StatusBadRequest,
			llm.CodeAuth:                 http.StatusUnauthorized,
			llm.CodePermission:           http.StatusForbidden,
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
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": typ, "code": code}})
	return status, body
}
