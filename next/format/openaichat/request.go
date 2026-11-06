// Package openaichat converts between OpenAI Chat Completions and pluxy's internal model, in
// both directions: core uses it to read client requests and write client replies, and provider
// extensions use it to build upstream requests and read upstream replies.
package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/stdotwork/pluxy/next/llm"
)

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	Stream              bool              `json:"stream"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	Temperature         *float64          `json:"temperature"`
	TopP                *float64          `json:"top_p"`
	Stop                json.RawMessage   `json:"stop"`
	Tools               []chatTool        `json:"tools"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	} `json:"function"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

func invalid(format string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(format, args...), Scope: llm.ScopeRequest}
}

// ParseRequest reads a Chat Completions request body. The result keeps body as its Native form.
// Errors are *llm.Error with code invalid_request.
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
	req := llm.Request{
		Model:  in.Model,
		Stream: in.Stream,
		Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: append(json.RawMessage(nil), body...)},
		Options: llm.Options{
			MaxOutputTokens: in.MaxCompletionTokens,
			Temperature:     in.Temperature,
			TopP:            in.TopP,
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
	for _, t := range in.Tools {
		if t.Type != "function" {
			return llm.Request{}, invalid("unsupported tool type %q", t.Type)
		}
		req.Tools = append(req.Tools, llm.Tool{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	for i, raw := range in.Messages {
		items, err := parseMessage(raw)
		if err != nil {
			return llm.Request{}, invalid("messages[%d]: %v", i, err)
		}
		req.Items = append(req.Items, items...)
	}
	return req, nil
}

func parseMessage(raw json.RawMessage) ([]llm.Item, error) {
	var m chatMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	switch m.Role {
	case llm.RoleSystem, llm.RoleDeveloper, llm.RoleUser, llm.RoleAssistant:
	case "tool":
		text, err := plainText(m.Content)
		if err != nil {
			return nil, err
		}
		return []llm.Item{{Type: llm.ItemTypeToolResult, CallID: m.ToolCallID, Output: text}}, nil
	default:
		return nil, fmt.Errorf("unsupported role %q", m.Role)
	}
	parts, err := parseContent(m.Content)
	if err != nil {
		return nil, err
	}
	var items []llm.Item
	if len(parts) > 0 || len(m.ToolCalls) == 0 {
		items = append(items, llm.Item{Type: llm.ItemTypeMessage, Role: m.Role, Content: parts})
	}
	for _, tc := range m.ToolCalls {
		items = append(items, llm.Item{Type: llm.ItemTypeToolCall, CallID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
	}
	return items, nil
}

func parseContent(raw json.RawMessage) ([]llm.Part, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return []llm.Part{{Type: llm.PartText, Text: s}}, nil
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil, fmt.Errorf("content must be a string or a list of parts")
	}
	parts := make([]llm.Part, 0, len(rawParts))
	for _, rp := range rawParts {
		var p chatPart
		if err := json.Unmarshal(rp, &p); err != nil {
			return nil, err
		}
		switch {
		case p.Type == "text":
			parts = append(parts, llm.Part{Type: llm.PartText, Text: p.Text})
		case p.Type == "image_url" && p.ImageURL != nil:
			parts = append(parts, llm.Part{Type: llm.PartImage, URL: p.ImageURL.URL, Detail: p.ImageURL.Detail})
		default:
			parts = append(parts, llm.Part{Type: llm.PartNative, Format: llm.FormatOpenAIChat, Data: append(json.RawMessage(nil), rp...)})
		}
	}
	return parts, nil
}

// plainText reads content that must be text: a string or a list of text parts.
func plainText(raw json.RawMessage) (string, error) {
	parts, err := parseContent(raw)
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
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type outPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
}

type outRequest struct {
	Model         string        `json:"model"`
	Messages      []*outMessage `json:"messages"`
	Stream        bool          `json:"stream,omitempty"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	MaxTokens   *int       `json:"max_tokens,omitempty"`
	Temperature *float64   `json:"temperature,omitempty"`
	TopP        *float64   `json:"top_p,omitempty"`
	Stop        []string   `json:"stop,omitempty"`
	Tools       []chatTool `json:"tools,omitempty"`
}

// EncodeRequest builds a Chat Completions request body from req's items and options. Streamed
// requests ask for usage. Parts that Chat Completions cannot carry are an unsupported error.
// It does not look at req.Native; callers that can pass the native body through do so first.
func EncodeRequest(req llm.Request) ([]byte, error) {
	out := outRequest{
		Model:       req.Model,
		Messages:    []*outMessage{},
		Stream:      req.Stream,
		MaxTokens:   req.Options.MaxOutputTokens,
		Temperature: req.Options.Temperature,
		TopP:        req.Options.TopP,
		Stop:        req.Options.Stop,
	}
	if req.Stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	for _, t := range req.Tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name, ct.Function.Description, ct.Function.Parameters = t.Name, t.Description, t.Parameters
		out.Tools = append(out.Tools, ct)
	}
	for i, it := range req.Items {
		switch it.Type {
		case llm.ItemTypeMessage:
			content, err := encodeContent(it.Content)
			if err != nil {
				return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("items[%d]: %v", i, err), Scope: llm.ScopeRequest}
			}
			out.Messages = append(out.Messages, &outMessage{Role: it.Role, Content: content})
		case llm.ItemTypeToolCall:
			var last *outMessage
			if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == llm.RoleAssistant {
				last = out.Messages[n-1]
			} else {
				last = &outMessage{Role: llm.RoleAssistant}
				out.Messages = append(out.Messages, last)
			}
			var tc chatToolCall
			tc.ID, tc.Type = it.CallID, "function"
			tc.Function.Name, tc.Function.Arguments = it.Name, it.Arguments
			last.ToolCalls = append(last.ToolCalls, tc)
		case llm.ItemTypeToolResult:
			out.Messages = append(out.Messages, &outMessage{Role: "tool", Content: it.Output, ToolCallID: it.CallID})
		default:
			return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("items[%d]: unsupported item type %q", i, it.Type), Scope: llm.ScopeRequest}
		}
	}
	return json.Marshal(out)
}

func encodeContent(parts []llm.Part) (any, error) {
	if len(parts) == 0 {
		return "", nil
	}
	if len(parts) == 1 && parts[0].Type == llm.PartText {
		return parts[0].Text, nil
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case llm.PartText:
			out = append(out, outPart{Type: "text", Text: p.Text})
		case llm.PartImage:
			op := outPart{Type: "image_url"}
			op.ImageURL = &struct {
				URL    string `json:"url"`
				Detail string `json:"detail,omitempty"`
			}{URL: p.URL, Detail: p.Detail}
			out = append(out, op)
		case llm.PartNative:
			if p.Format != llm.FormatOpenAIChat {
				return nil, fmt.Errorf("a %s content part cannot be sent as Chat Completions", p.Format)
			}
			out = append(out, p.Data)
		default:
			return nil, fmt.Errorf("unsupported content part %q", p.Type)
		}
	}
	return out, nil
}
