// Package openaichat converts between OpenAI Chat Completions and pluxy's internal model, in
// both directions: core uses it to read client requests and write client replies, and provider
// extensions use it to build upstream requests and read upstream replies.
package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatOpenAIChat

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	Stream              bool              `json:"stream"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	Temperature         *float64          `json:"temperature"`
	TopP                *float64          `json:"top_p"`
	Stop                json.RawMessage   `json:"stop"`
	Tools               []json.RawMessage `json:"tools"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// requestFields are the top-level request fields the model carries. stream_options is written
// by the encoder itself.
var requestFields = []string{"model", "messages", "stream", "stream_options", "max_tokens", "max_completion_tokens",
	"temperature", "top_p", "stop", "tools", "tool_choice", "parallel_tool_calls", "response_format", "reasoning_effort"}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatMessage struct {
	Role             string            `json:"role"`
	Content          json.RawMessage   `json:"content"`
	ToolCalls        []json.RawMessage `json:"tool_calls"`
	ToolCallID       string            `json:"tool_call_id"`
	ReasoningContent *string           `json:"reasoning_content"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// customToolCall is a call to a custom (freeform) tool.
type customToolCall struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Custom struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	} `json:"custom"`
}

type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
	File *struct {
		FileID   string `json:"file_id"`
		FileData string `json:"file_data"`
		Filename string `json:"filename"`
	} `json:"file"`
}

func invalid(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(msg, args...), Scope: llm.ScopeRequest}
}

// ParseRequest reads a Chat Completions request body. The result keeps body as its Native form,
// and every request, message, part and tool keeps the fields the model has no place for in its
// Origin, so EncodeRequest can write them back. Errors are *llm.Error with code invalid_request.
func ParseRequest(body []byte) (llm.Request, error) {
	var in chatRequest
	if err := json.Unmarshal(body, &in); err != nil {
		return llm.Request{}, invalid("request body is not valid JSON: %v", err)
	}
	if in.Model == "" {
		return llm.Request{}, invalid("model is required")
	}
	if len(in.Messages) == 0 {
		return llm.Request{}, invalid("messages must not be empty")
	}
	top := rawjson.Object(body)
	req := llm.Request{
		Model:  in.Model,
		Stream: in.Stream,
		Native: &llm.Native{Format: format, Body: append(json.RawMessage(nil), body...)},
		Options: llm.Options{
			MaxOutputTokens:   in.MaxCompletionTokens,
			Temperature:       in.Temperature,
			TopP:              in.TopP,
			ParallelToolCalls: in.ParallelToolCalls,
			ReasoningEffort:   in.ReasoningEffort,
		},
	}
	if req.Options.MaxOutputTokens == nil {
		req.Options.MaxOutputTokens = in.MaxTokens
	}
	if len(in.Stop) > 0 && !bytes.Equal(in.Stop, []byte("null")) {
		var one string
		if json.Unmarshal(in.Stop, &one) == nil {
			req.Options.Stop = []string{one}
		} else if err := json.Unmarshal(in.Stop, &req.Options.Stop); err != nil {
			return llm.Request{}, invalid("stop must be a string or a list of strings")
		}
	}
	// Request fields the model reads but whose shape it does not know stay in Origin.Raw, and
	// so do the fields it does not read inside the ones it does.
	nested := map[string]json.RawMessage{}
	if raw, ok := top["stream_options"]; ok {
		nested["stream_options"] = rawjson.Rest(rawjson.Object(raw), "include_usage")
	}
	if raw, ok := top["tool_choice"]; ok {
		if tc := parseToolChoice(raw); tc != nil {
			req.Options.ToolChoice = tc
		} else {
			nested["tool_choice"] = raw
		}
	}
	if raw, ok := top["response_format"]; ok {
		if rf := parseResponseFormat(raw); rf != nil {
			req.Options.ResponseFormat = rf
			m := rawjson.Object(raw)
			rest := rawjson.Object(rawjson.Rest(m, "type", "json_schema"))
			if js := rawjson.Rest(rawjson.Object(m["json_schema"]), "name", "description", "schema", "strict"); js != nil {
				if rest == nil {
					rest = map[string]json.RawMessage{}
				}
				rest["json_schema"] = js
			}
			nested["response_format"] = rawjson.Encode(rest)
		} else {
			nested["response_format"] = raw
		}
	}
	req.Origin = rawjson.Origin(format, "", top, requestFields, nested)
	for i, raw := range in.Tools {
		t, err := parseTool(raw, fmt.Sprintf("tools[%d]", i))
		if err != nil {
			return llm.Request{}, invalid("tools[%d]: %v", i, err)
		}
		req.Tools = append(req.Tools, t)
	}
	for i, raw := range in.Messages {
		items, err := parseMessage(raw, fmt.Sprintf("messages[%d]", i))
		if err != nil {
			return llm.Request{}, invalid("messages[%d]: %v", i, err)
		}
		req.Items = append(req.Items, items...)
	}
	return req, nil
}

func parseTool(raw json.RawMessage, path string) (llm.Tool, error) {
	var t chatTool
	if err := json.Unmarshal(raw, &t); err != nil {
		return llm.Tool{}, err
	}
	m := rawjson.Object(raw)
	if t.Type != "function" {
		// A tool the model cannot interpret survives whole, for an upstream that speaks Chat.
		return llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}, nil
	}
	fn := rawjson.Rest(rawjson.Object(m["function"]), "name", "description", "parameters", "strict")
	return llm.Tool{
		Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters, Strict: t.Function.Strict,
		Origin: rawjson.Origin(format, path, m, []string{"type", "function"}, map[string]json.RawMessage{"function": fn}),
	}, nil
}

func parseToolChoice(raw json.RawMessage) *llm.ToolChoice {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case llm.ToolChoiceAuto, llm.ToolChoiceNone, llm.ToolChoiceRequired:
			return &llm.ToolChoice{Mode: s}
		}
		return nil
	}
	var o struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Type == "function" && o.Function.Name != "" && len(rawjson.Object(raw)) == 2 &&
		len(rawjson.Object(rawjson.Object(raw)["function"])) == 1 {
		return &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: o.Function.Name}
	}
	return nil
}

func parseResponseFormat(raw json.RawMessage) *llm.ResponseFormat {
	var o struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	switch o.Type {
	case llm.ResponseFormatText, llm.ResponseFormatJSONObject:
		return &llm.ResponseFormat{Type: o.Type}
	case llm.ResponseFormatJSONSchema:
		if o.JSONSchema == nil {
			return nil
		}
		s := o.JSONSchema
		return &llm.ResponseFormat{Type: o.Type, Name: s.Name, Description: s.Description, Schema: s.Schema, Strict: s.Strict}
	}
	return nil
}

func parseMessage(raw json.RawMessage, path string) ([]llm.Item, error) {
	var m chatMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	obj := rawjson.Object(raw)
	switch m.Role {
	case llm.RoleSystem, llm.RoleDeveloper, llm.RoleUser, llm.RoleAssistant:
	case "tool":
		text, err := plainText(m.Content)
		if err != nil {
			return nil, err
		}
		return []llm.Item{{Type: llm.ItemTypeToolResult, CallID: m.ToolCallID, Output: text,
			Origin: rawjson.Origin(format, path, obj, []string{"role", "content", "tool_call_id"}, nil)}}, nil
	default:
		return nil, fmt.Errorf("unsupported role %q", m.Role)
	}
	parts, textContent, err := parseContent(m.Content, path+".content")
	if err != nil {
		return nil, err
	}
	var items []llm.Item
	if m.Role == llm.RoleAssistant && m.ReasoningContent != nil && *m.ReasoningContent != "" {
		items = append(items, llm.Item{Type: llm.ItemTypeReasoning, Text: *m.ReasoningContent,
			Origin: &llm.Origin{Format: format, Path: path + ".reasoning_content"}})
	}
	origin := rawjson.Origin(format, path, obj, []string{"role", "content", "tool_calls", "tool_call_id", "reasoning_content"}, nil)
	// A message that only carries tool calls still becomes an item when it has fields of its
	// own (such as name), so they survive; its tool calls are written back onto it.
	if len(parts) > 0 || len(m.ToolCalls) == 0 || len(origin.Raw) > 0 {
		items = append(items, llm.Item{Type: llm.ItemTypeMessage, Role: m.Role, Content: parts, TextContent: textContent, Origin: origin})
	}
	for j, rawCall := range m.ToolCalls {
		cp := fmt.Sprintf("%s.tool_calls[%d]", path, j)
		co := rawjson.Object(rawCall)
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawCall, &head); err != nil {
			return nil, err
		}
		switch head.Type {
		case "", "function":
			var tc chatToolCall
			if err := json.Unmarshal(rawCall, &tc); err != nil {
				return nil, err
			}
			fn := rawjson.Rest(rawjson.Object(co["function"]), "name", "arguments")
			items = append(items, llm.Item{Type: llm.ItemTypeToolCall, CallID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				Origin: rawjson.Origin(format, cp, co, []string{"id", "type", "function"}, map[string]json.RawMessage{"function": fn})})
		case "custom":
			var tc customToolCall
			if err := json.Unmarshal(rawCall, &tc); err != nil {
				return nil, err
			}
			cu := rawjson.Rest(rawjson.Object(co["custom"]), "name", "input")
			items = append(items, llm.Item{Type: llm.ItemTypeToolCall, CallID: tc.ID, Name: tc.Custom.Name, Arguments: tc.Custom.Input, Custom: true,
				Origin: rawjson.Origin(format, cp, co, []string{"id", "type", "custom"}, map[string]json.RawMessage{"custom": cu})})
		default:
			return nil, fmt.Errorf("tool_calls[%d]: unsupported tool call type %q", j, head.Type)
		}
	}
	return items, nil
}

// parseContent reads message content: a string (textContent true) or a list of parts.
func parseContent(raw json.RawMessage, path string) (parts []llm.Part, textContent bool, err error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, true, nil
		}
		return []llm.Part{{Type: llm.PartText, Text: s}}, true, nil
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil, false, fmt.Errorf("content must be a string or a list of parts")
	}
	parts = make([]llm.Part, 0, len(rawParts))
	for i, rp := range rawParts {
		var p chatPart
		if err := json.Unmarshal(rp, &p); err != nil {
			return nil, false, err
		}
		pp := fmt.Sprintf("%s[%d]", path, i)
		obj := rawjson.Object(rp)
		switch {
		case p.Type == "text":
			parts = append(parts, llm.Part{Type: llm.PartText, Text: p.Text, Origin: rawjson.Origin(format, pp, obj, []string{"type", "text"}, nil)})
		case p.Type == "image_url" && p.ImageURL != nil:
			img := rawjson.Rest(rawjson.Object(obj["image_url"]), "url", "detail")
			parts = append(parts, llm.Part{Type: llm.PartImage, URL: p.ImageURL.URL, Detail: p.ImageURL.Detail,
				Origin: rawjson.Origin(format, pp, obj, []string{"type", "image_url"}, map[string]json.RawMessage{"image_url": img})})
		case p.Type == "file" && p.File != nil:
			file := rawjson.Rest(rawjson.Object(obj["file"]), "file_id", "file_data", "filename")
			parts = append(parts, llm.Part{Type: llm.PartFile, FileID: p.File.FileID, FileData: p.File.FileData, Filename: p.File.Filename,
				Origin: rawjson.Origin(format, pp, obj, []string{"type", "file"}, map[string]json.RawMessage{"file": file})})
		default:
			parts = append(parts, llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), rp...),
				Origin: &llm.Origin{Format: format, Path: pp}})
		}
	}
	return parts, false, nil
}

// plainText reads content that must be text: a string or a list of text parts.
func plainText(raw json.RawMessage) (string, error) {
	parts, _, err := parseContent(raw, "")
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	for _, p := range parts {
		if p.Type != llm.PartText {
			return "", fmt.Errorf("tool message content must be text")
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

type outMessage struct {
	Role             string            `json:"role"`
	Content          any               `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`

	raw json.RawMessage // kept fields from a Chat source
}

type outPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"` // set for text parts, even when empty
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
	File *struct {
		FileID   string `json:"file_id,omitempty"`
		FileData string `json:"file_data,omitempty"`
		Filename string `json:"filename,omitempty"`
	} `json:"file,omitempty"`
}

type outRequest struct {
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Stream        bool              `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	MaxTokens         *int              `json:"max_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stop              []string          `json:"stop,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    any               `json:"response_format,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
}

// encoder builds one Chat Completions request and records what it could not carry over.
type encoder struct {
	losses []llm.Loss
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = append(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// kept returns o's kept fields when o came from Chat, so they are written back; for another
// format it reports them as dropped.
func (e *encoder) kept(o *llm.Origin) json.RawMessage {
	if o == nil {
		return nil
	}
	if o.Format == format {
		return o.Raw
	}
	e.losses = append(e.losses, rawjson.Losses(o, format)...)
	return nil
}

func path(o *llm.Origin, fallback string) string {
	if o != nil && o.Path != "" {
		return o.Path
	}
	return fallback
}

func marshal(v any, raw json.RawMessage) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return rawjson.Merge(b, raw)
}

// EncodeRequest builds a Chat Completions request body from req's items, tools and options, and
// returns what it could not carry over. Fields kept from a Chat source (Origin) are written back.
// Streamed requests ask for usage. A user turn left with nothing to send after its unsupported
// parts are dropped is an invalid_request error, never sent empty. It does not look at
// req.Native; callers that can pass the native body through do so first.
func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	out := outRequest{
		Model:           req.Model,
		Messages:        []json.RawMessage{},
		Stream:          req.Stream,
		MaxTokens:       req.Options.MaxOutputTokens,
		Temperature:     req.Options.Temperature,
		TopP:            req.Options.TopP,
		Stop:            req.Options.Stop,
		ReasoningEffort: req.Options.ReasoningEffort,
	}
	if req.Stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	if rf := req.Options.ResponseFormat; rf != nil {
		out.ResponseFormat = encodeResponseFormat(rf)
	}
	for i, t := range req.Tools {
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				out.Tools = append(out.Tools, t.Origin.Raw)
			} else {
				e.lose(llm.LossDropped, path(t.Origin, fmt.Sprintf("tools[%d]", i)), "Chat Completions has no such tool")
			}
			continue
		}
		ct := chatTool{Type: "function", Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict}}
		out.Tools = append(out.Tools, marshal(ct, e.kept(t.Origin)))
	}
	if len(out.Tools) > 0 {
		// Chat Completions accepts these only with tools.
		out.ParallelToolCalls = req.Options.ParallelToolCalls
		if tc := req.Options.ToolChoice; tc != nil {
			out.ToolChoice = encodeToolChoice(tc)
		}
	}

	var msgs []*outMessage
	reasoning := "" // reasoning waiting for the assistant message it belongs to
	flushReasoning := func() {
		if reasoning != "" {
			empty := ""
			msgs = append(msgs, &outMessage{Role: llm.RoleAssistant, Content: &empty, ReasoningContent: reasoning})
			reasoning = ""
		}
	}
	for i, it := range req.Items {
		p := path(it.Origin, fmt.Sprintf("items[%d]", i))
		if it.Type != llm.ItemTypeToolCall && !(it.Type == llm.ItemTypeMessage && it.Role == llm.RoleAssistant) && it.Type != llm.ItemTypeReasoning {
			flushReasoning()
		}
		switch it.Type {
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format != format && it.Text != "" {
				e.lose(llm.LossApproximated, p, "reasoning summary sent as the next assistant message's reasoning_content")
			}
			e.kept(it.Origin)
			reasoning = joinReasoning(reasoning, it.Text)
		case llm.ItemTypeMessage:
			content, sendable, dropped, err := e.content(it, p)
			if err != nil {
				return nil, nil, err
			}
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Chat Completions", p)
			}
			m := &outMessage{Role: it.Role, Content: content, raw: e.kept(it.Origin)}
			if it.Role == llm.RoleAssistant {
				m.ReasoningContent, reasoning = reasoning, ""
			}
			msgs = append(msgs, m)
		case llm.ItemTypeToolCall:
			var last *outMessage
			if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleAssistant && msgs[n-1].ToolCallID == "" && reasoning == "" {
				last = msgs[n-1]
			} else {
				last = &outMessage{Role: llm.RoleAssistant}
				msgs = append(msgs, last)
			}
			if reasoning != "" {
				last.ReasoningContent, reasoning = reasoning, ""
			}
			if it.Custom {
				var tc customToolCall
				tc.ID, tc.Type = it.CallID, "custom"
				tc.Custom.Name, tc.Custom.Input = it.Name, it.Arguments
				last.ToolCalls = append(last.ToolCalls, marshal(tc, e.kept(it.Origin)))
				break
			}
			var tc chatToolCall
			tc.ID, tc.Type = it.CallID, "function"
			tc.Function.Name, tc.Function.Arguments = it.Name, it.Arguments
			last.ToolCalls = append(last.ToolCalls, marshal(tc, e.kept(it.Origin)))
		case llm.ItemTypeToolResult:
			msgs = append(msgs, &outMessage{Role: "tool", Content: it.Output, ToolCallID: it.CallID, raw: e.kept(it.Origin)})
		case llm.ItemTypeNative:
			e.lose(llm.LossDropped, p, "Chat Completions has no such item")
		default:
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported item type %q", p, it.Type), Scope: llm.ScopeRequest}
		}
	}
	flushReasoning()
	for _, m := range msgs {
		out.Messages = append(out.Messages, marshal(m, m.raw))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return rawjson.Merge(b, e.kept(req.Origin)), e.losses, nil
}

// joinReasoning joins the reasoning of consecutive reasoning items, as today's core does.
func joinReasoning(have, next string) string {
	switch {
	case strings.TrimSpace(have) == "":
		return next
	case strings.TrimSpace(next) == "", strings.TrimSpace(have) == strings.TrimSpace(next):
		return have
	}
	return have + "\n\n" + next
}

func encodeToolChoice(tc *llm.ToolChoice) any {
	if tc.Mode == llm.ToolChoiceTool {
		return map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
	}
	return tc.Mode
}

func encodeResponseFormat(rf *llm.ResponseFormat) any {
	if rf.Type != llm.ResponseFormatJSONSchema {
		return map[string]string{"type": rf.Type}
	}
	type schema struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Schema      json.RawMessage `json:"schema,omitempty"`
		Strict      *bool           `json:"strict,omitempty"`
	}
	return struct {
		Type       string `json:"type"`
		JSONSchema schema `json:"json_schema"`
	}{rf.Type, schema{rf.Name, rf.Description, rf.Schema, rf.Strict}}
}

// content encodes a message's parts. sendable reports whether any part with something in it
// was kept, dropped whether any part was dropped and reported because Chat Completions cannot
// carry it.
func (e *encoder) content(it llm.Item, msgPath string) (content any, sendable, dropped bool, err error) {
	parts := it.Content
	if len(parts) == 0 {
		if it.Role == llm.RoleAssistant && !it.TextContent {
			return nil, false, false, nil // an assistant message that only carries tool calls
		}
		return "", false, false, nil
	}
	if len(parts) == 1 && parts[0].Type == llm.PartText && (it.TextContent || it.Origin == nil) && (parts[0].Origin == nil || len(parts[0].Origin.Raw) == 0) {
		return parts[0].Text, parts[0].Text != "", false, nil
	}
	out := make([]json.RawMessage, 0, len(parts))
	for i, p := range parts {
		pp := path(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		switch p.Type {
		case llm.PartText:
			text := p.Text
			out = append(out, marshal(outPart{Type: "text", Text: &text}, e.kept(p.Origin)))
			if p.Text != "" {
				sendable = true
			}
		case llm.PartImage:
			if p.URL == "" && p.FileID != "" {
				e.lose(llm.LossDropped, pp, "Chat Completions cannot take an image by file id")
				dropped = true
				continue
			}
			op := outPart{Type: "image_url"}
			op.ImageURL = &struct {
				URL    string `json:"url"`
				Detail string `json:"detail,omitempty"`
			}{URL: p.URL, Detail: p.Detail}
			out = append(out, marshal(op, e.kept(p.Origin)))
			sendable = true
		case llm.PartFile:
			if p.FileID == "" && p.FileData == "" {
				// Chat Completions takes a file id or inline data, not a URL.
				e.lose(llm.LossDropped, pp, "Chat Completions cannot take a file by URL")
				dropped = true
				continue
			}
			op := outPart{Type: "file"}
			op.File = &struct {
				FileID   string `json:"file_id,omitempty"`
				FileData string `json:"file_data,omitempty"`
				Filename string `json:"filename,omitempty"`
			}{p.FileID, p.FileData, p.Filename}
			out = append(out, marshal(op, e.kept(p.Origin)))
			sendable = true
		case llm.PartNative:
			if p.Format != format {
				e.lose(llm.LossDropped, pp, "a "+p.Format+" content part Chat Completions cannot carry")
				dropped = true
				continue
			}
			out = append(out, p.Data)
			sendable = true
		default:
			return nil, false, false, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported content part %q", pp, p.Type), Scope: llm.ScopeRequest}
		}
	}
	return out, sendable, dropped, nil
}
