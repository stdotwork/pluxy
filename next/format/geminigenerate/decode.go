package geminigenerate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

func malformed(msg string, data []byte) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Message: msg, Raw: llm.RawJSON(data)}
}

// chunk is one GenerateContentResponse: a whole non-streamed reply, or one chunk of a stream.
type chunk struct {
	Candidates []struct {
		Index   *int `json:"index"`
		Content *struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata json.RawMessage `json:"usageMetadata"`
	ModelVersion  string          `json:"modelVersion"`
	ResponseID    string          `json:"responseId"`
	Error         json.RawMessage `json:"error"`
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

// contentFilterReasons are the finish reasons that mean a safety or policy filter stopped the
// reply.
var contentFilterReasons = map[string]bool{
	"SAFETY": true, "RECITATION": true, "LANGUAGE": true, "BLOCKLIST": true, "PROHIBITED_CONTENT": true, "SPII": true,
	"IMAGE_SAFETY": true, "IMAGE_PROHIBITED_CONTENT": true, "IMAGE_RECITATION": true,
}

// finishOf maps a Gemini finish reason; RawReason keeps Gemini's own value, so SAFETY,
// RECITATION and the rest stay distinct. A STOP with function calls in the reply is a tool-call
// stop.
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
	"RESOURCE_EXHAUSTED":  {llm.CodeRateLimit, llm.ScopeCredential},
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
}

// NewStreamDecoder returns a decoder for one stream.
func NewStreamDecoder() *StreamDecoder { return &StreamDecoder{text: -1, think: -1} }

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
	out = append(out, llm.Event{Type: llm.EventItemEnd, Index: *idx})
	*idx = -1
	return out
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
	out := d.decode(c, nil)
	if len(out) == 0 {
		out = append(out, llm.Event{Type: llm.EventNative})
	}
	out[0].Raw = append(json.RawMessage(nil), data...)
	return out, nil
}

// decode adds the events chunk c produces to out.
func (d *StreamDecoder) decode(c chunk, out []llm.Event) []llm.Event {
	if !d.started {
		d.started = true
		out = append(out, llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: c.ResponseID, Model: c.ModelVersion, Format: format}})
	}
	if u := decodeUsage(c.UsageMetadata); u != nil {
		d.usage = u
	}
	for _, cand := range c.Candidates {
		// shortcut: only the first candidate becomes model events; the others reach a Gemini
		// client in the raw chunks and are lost to other formats. Report them when a client
		// format other than Gemini reads Gemini upstreams (the Gemini provider extension).
		if cand.Index != nil && *cand.Index != 0 {
			continue
		}
		if cand.Content != nil {
			for _, pr := range cand.Content.Parts {
				out = d.part(pr, out)
			}
		}
		if cand.FinishReason != "" {
			f := finishOf(cand.FinishReason, d.calls)
			d.finish = &f
			out = d.closeItem(&d.think, out)
			out = d.closeItem(&d.text, out)
		}
		break
	}
	if len(c.Candidates) == 0 && c.PromptFeedback != nil && c.PromptFeedback.BlockReason != "" {
		// The prompt was blocked: no candidates, and the block reason in place of a finish
		// reason.
		d.finish = &llm.Finish{Reason: llm.FinishContentFilter, RawReason: c.PromptFeedback.BlockReason}
	}
	return out
}

// part adds the events of one content part. Thought text is reasoning, other text is message
// text, and a function call is a whole tool call (Gemini sends each call complete). Other parts
// (inline data, code execution) reach a Gemini client in the raw chunk only.
func (d *StreamDecoder) part(raw json.RawMessage, out []llm.Event) []llm.Event {
	pm := object(raw)
	if pm == nil {
		return out
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
		idx, out = d.open(llm.ItemToolCall, out, llm.ItemStart{CallID: call.ID, Name: call.Name})
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
		return append(out, llm.Event{Type: llm.EventReasoningDelta, Index: d.think, Delta: text})
	}
	out = d.closeItem(&d.think, out)
	if d.text < 0 {
		d.text, out = d.open(llm.ItemMessage, out, llm.ItemStart{})
	}
	return append(out, llm.Event{Type: llm.EventTextDelta, Index: d.text, Delta: text})
}

// Usage returns the latest usage the upstream reported, or nil. A caller that fails the stream
// for its own reasons (the connection broke, say) puts it on the error.
func (d *StreamDecoder) Usage() *llm.Usage { return d.usage }

// withUsage puts the usage reported so far on an error that ends the stream.
func (d *StreamDecoder) withUsage(e *llm.Error) *llm.Error {
	e.Usage = d.usage
	return e
}

// Close is called when the upstream stream ends. A stream that ends before any finish reason is
// an error: the reply may have been cut short.
func (d *StreamDecoder) Close() ([]llm.Event, error) {
	if d.done {
		return nil, nil
	}
	d.done = true
	if d.finish == nil {
		return nil, d.withUsage(&llm.Error{Code: llm.CodeUpstream, Message: "upstream stream ended before the reply finished"})
	}
	var out []llm.Event
	out = d.closeItem(&d.think, out)
	out = d.closeItem(&d.text, out)
	return append(out, llm.Event{Type: llm.EventFinish, Finish: d.finish, Usage: d.usage}), nil
}

// DecodeResponse turns an upstream non-streamed GenerateContentResponse into the events a stream
// of it would have produced. The first event carries the whole body in Raw, so a Gemini client
// gets it unchanged. A reply holding an error object becomes an *llm.Error; a reply with neither
// a finish reason nor a prompt block counts as finished with reason other.
func DecodeResponse(body []byte) ([]llm.Event, error) {
	var c chunk
	if err := json.Unmarshal(body, &c); err != nil || rawjson.Object(body) == nil {
		return nil, malformed("upstream sent a malformed response", body)
	}
	if !isNull(c.Error) {
		return nil, errorOf(c.Error, body)
	}
	d := NewStreamDecoder()
	out := d.decode(c, nil)
	out[0].Raw = append(json.RawMessage(nil), body...)
	if d.finish == nil {
		d.finish = &llm.Finish{Reason: llm.FinishOther}
	}
	end, _ := d.Close()
	return append(out, end...), nil
}

// isGoogleError reports whether raw is a Google error body: {"error": {...}}.
func isGoogleError(raw json.RawMessage) bool {
	m := rawjson.Object(raw)
	return m != nil && rawjson.Object(m["error"]) != nil && strings.TrimSpace(string(m["error"])) != ""
}
