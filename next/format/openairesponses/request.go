// Package openairesponses converts between OpenAI Responses and pluxy's internal model: core uses
// it to read client requests and write client replies, and provider extensions use it to build
// requests for and read replies from an upstream that speaks Responses.
//
// A request read here keeps its body as the native form, so an upstream that speaks Responses
// gets it unchanged. A reply from such an upstream keeps every upstream event in its raw field,
// so a Responses client gets the upstream's events unchanged; a reply from another format is
// rebuilt into the Responses event sequence.
package openairesponses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatOpenAIResponses

// Headers are the client request headers that belong to a Responses body (lower-case): those the
// Codex CLI sends to name its session, window and turn and its own version and originator, and
// the User-Agent, which tells a Codex client from others. A Codex upstream reads them; an
// upstream that does not ignores them.
var Headers = []string{
	"session-id", "thread-id", "x-codex-window-id", "x-codex-turn-metadata", "x-codex-turn-state",
	"x-codex-beta-features", "x-client-request-id", "version", "originator", "user-agent",
	"x-openai-internal-codex-responses-lite",
}

func invalid(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(msg, args...), Scope: llm.ScopeRequest}
}

type request struct {
	Model             string            `json:"model"`
	Input             json.RawMessage   `json:"input"`
	Instructions      *string           `json:"instructions"`
	Stream            bool              `json:"stream"`
	MaxOutputTokens   *int              `json:"max_output_tokens"`
	Temperature       *float64          `json:"temperature"`
	TopP              *float64          `json:"top_p"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	Tools             []json.RawMessage `json:"tools"`
}

// requestFields are the top-level fields the model carries. tool_choice, text and reasoning are
// carried only in the shapes the parser knows; anything else in them stays in Origin.Raw.
var requestFields = []string{"model", "input", "instructions", "stream", "max_output_tokens", "temperature", "top_p",
	"parallel_tool_calls", "tools", "tool_choice", "text", "reasoning"}

// ParseRequest reads a Responses request body. The result keeps body as its Native form, and
// every item, part and tool keeps the fields the model has no place for in its Origin. Errors
// are *llm.Error with code invalid_request.
func ParseRequest(body []byte) (llm.Request, error) {
	var in request
	if err := json.Unmarshal(body, &in); err != nil {
		return llm.Request{}, invalid("request body is not valid JSON: %v", err)
	}
	if in.Model == "" {
		return llm.Request{}, invalid("model is required")
	}
	top := rawjson.Object(body)
	req := llm.Request{
		Model:  in.Model,
		Stream: in.Stream,
		Native: &llm.Native{Format: format, Body: append(json.RawMessage(nil), body...)},
		Options: llm.Options{
			MaxOutputTokens:   in.MaxOutputTokens,
			Temperature:       in.Temperature,
			TopP:              in.TopP,
			ParallelToolCalls: in.ParallelToolCalls,
		},
	}
	nested := map[string]json.RawMessage{}
	if raw, ok := top["tool_choice"]; ok {
		if tc := parseToolChoice(raw); tc != nil {
			req.Options.ToolChoice = tc
		} else {
			nested["tool_choice"] = raw
		}
	}
	if raw, ok := top["text"]; ok {
		m := rawjson.Object(raw)
		if rf := parseTextFormat(m["format"]); rf != nil {
			req.Options.ResponseFormat = rf
			rest := rawjson.Object(rawjson.Rest(m, "format"))
			if f := rawjson.Rest(rawjson.Object(m["format"]), "type", "name", "description", "schema", "strict"); f != nil {
				if rest == nil {
					rest = map[string]json.RawMessage{}
				}
				rest["format"] = f
			}
			nested["text"] = rawjson.Encode(rest)
		} else {
			nested["text"] = raw
		}
	}
	if raw, ok := top["reasoning"]; ok {
		m := rawjson.Object(raw)
		var effort string
		if json.Unmarshal(m["effort"], &effort) == nil && effort != "" {
			req.Options.ReasoningEffort = effort
			nested["reasoning"] = rawjson.Rest(m, "effort")
		} else if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			nested["reasoning"] = raw
		}
	}
	req.Origin = rawjson.Origin(format, "", top, requestFields, nested)

	for i, raw := range in.Tools {
		req.Tools = append(req.Tools, parseTool(raw, fmt.Sprintf("tools[%d]", i)))
	}
	if in.Instructions != nil && *in.Instructions != "" {
		req.Items = append(req.Items, llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, TextContent: true,
			Content: []llm.Part{{Type: llm.PartText, Text: *in.Instructions}}, Origin: &llm.Origin{Format: format, Path: "instructions"}})
	}
	if len(in.Input) == 0 || bytes.Equal(in.Input, []byte("null")) {
		return req, nil
	}
	var s string
	if json.Unmarshal(in.Input, &s) == nil {
		req.Items = append(req.Items, llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleUser, TextContent: true,
			Content: []llm.Part{{Type: llm.PartText, Text: s}}, Origin: &llm.Origin{Format: format, Path: "input"}})
		return req, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(in.Input, &items); err != nil {
		return llm.Request{}, invalid("input must be a string or a list of items")
	}
	for i, raw := range items {
		it, err := parseItem(raw, fmt.Sprintf("input[%d]", i))
		if err != nil {
			return llm.Request{}, invalid("input[%d]: %v", i, err)
		}
		req.Items = append(req.Items, it)
	}
	return req, nil
}

func parseTool(raw json.RawMessage, path string) llm.Tool {
	var t struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	}
	if json.Unmarshal(raw, &t) != nil || t.Type != "function" || t.Name == "" {
		// Hosted and custom tools are not function tools; they survive whole.
		return llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}
	}
	return llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict,
		Origin: rawjson.Origin(format, path, rawjson.Object(raw), []string{"type", "name", "description", "parameters", "strict"}, nil)}
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
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Type == "function" && o.Name != "" && len(rawjson.Object(raw)) == 2 {
		return &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: o.Name}
	}
	return nil
}

func parseTextFormat(raw json.RawMessage) *llm.ResponseFormat {
	var f struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Strict      *bool           `json:"strict"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil {
		return nil
	}
	switch f.Type {
	case llm.ResponseFormatText, llm.ResponseFormatJSONObject:
		return &llm.ResponseFormat{Type: f.Type}
	case llm.ResponseFormatJSONSchema:
		return &llm.ResponseFormat{Type: f.Type, Name: f.Name, Description: f.Description, Schema: f.Schema, Strict: f.Strict}
	}
	return nil
}

// itemBookkeeping are item fields that describe an item rather than say anything to the model:
// its id (kept in Item.ID) and the status a replayed output item had.
var itemBookkeeping = []string{"type", "id", "status"}

func parseItem(raw json.RawMessage, path string) (llm.Item, error) {
	m := rawjson.Object(raw)
	if m == nil {
		return llm.Item{}, fmt.Errorf("item must be an object")
	}
	var head struct {
		Type string `json:"type"`
		Role string `json:"role"`
		ID   string `json:"id"`
	}
	_ = json.Unmarshal(raw, &head)
	if head.Type == "" && head.Role != "" {
		head.Type = "message"
	}
	switch head.Type {
	case "message":
		switch head.Role {
		case llm.RoleSystem, llm.RoleDeveloper, llm.RoleUser, llm.RoleAssistant:
		default:
			return llm.Item{}, fmt.Errorf("unsupported role %q", head.Role)
		}
		parts, textContent, err := parseContent(m["content"], path+".content")
		if err != nil {
			return llm.Item{}, err
		}
		return llm.Item{Type: llm.ItemTypeMessage, ID: head.ID, Role: head.Role, Content: parts, TextContent: textContent,
			Origin: rawjson.Origin(format, path, m, append([]string{"role", "content"}, itemBookkeeping...), nil)}, nil
	case "function_call":
		var c struct {
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return llm.Item{}, err
		}
		return llm.Item{Type: llm.ItemTypeToolCall, ID: head.ID, CallID: c.CallID, Name: c.Name, Arguments: c.Arguments,
			Origin: rawjson.Origin(format, path, m, append([]string{"call_id", "name", "arguments"}, itemBookkeeping...), nil)}, nil
	case "function_call_output":
		var o struct {
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return llm.Item{}, err
		}
		it := llm.Item{Type: llm.ItemTypeToolResult, ID: head.ID, CallID: o.CallID}
		modelled := append([]string{"call_id"}, itemBookkeeping...)
		// shortcut: an output given as a list of content parts stays in Origin.Raw (and is
		// reported as dropped when translated); carry it as parts when a provider needs it.
		if json.Unmarshal(m["output"], &it.Output) == nil {
			modelled = append(modelled, "output")
		}
		it.Origin = rawjson.Origin(format, path, m, modelled, nil)
		return it, nil
	case "reasoning":
		var r struct {
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return llm.Item{}, err
		}
		var text strings.Builder
		for _, s := range r.Summary {
			if s.Type == "summary_text" {
				text.WriteString(s.Text)
			}
		}
		return llm.Item{Type: llm.ItemTypeReasoning, ID: head.ID, Text: text.String(),
			Origin: rawjson.Origin(format, path, m, append([]string{"summary"}, itemBookkeeping...), nil)}, nil
	}
	// Item types the model does not know (custom tool calls, hosted tool calls, item
	// references, ...) survive whole.
	return llm.Item{Type: llm.ItemTypeNative, ID: head.ID, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}, nil
}

// parseContent reads message content: a string (textContent true) or a list of parts.
func parseContent(raw json.RawMessage, path string) (parts []llm.Part, textContent bool, err error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []llm.Part{{Type: llm.PartText, Text: s}}, true, nil
	}
	var rawParts []json.RawMessage
	if err := json.Unmarshal(raw, &rawParts); err != nil {
		return nil, false, fmt.Errorf("content must be a string or a list of parts")
	}
	for i, rp := range rawParts {
		pp := fmt.Sprintf("%s[%d]", path, i)
		m := rawjson.Object(rp)
		var p struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
			Detail   string `json:"detail"`
			FileID   string `json:"file_id"`
			FileData string `json:"file_data"`
			FileURL  string `json:"file_url"`
			Filename string `json:"filename"`
		}
		if m == nil || json.Unmarshal(rp, &p) != nil {
			return nil, false, fmt.Errorf("content[%d] must be an object", i)
		}
		switch p.Type {
		case "input_text", "output_text":
			modelled := []string{"type", "text"}
			// Empty annotations and logprobs on a replayed output say nothing.
			for _, k := range []string{"annotations", "logprobs"} {
				if v, ok := m[k]; ok && (bytes.Equal(v, []byte("[]")) || bytes.Equal(v, []byte("null"))) {
					modelled = append(modelled, k)
				}
			}
			parts = append(parts, llm.Part{Type: llm.PartText, Text: p.Text, Origin: rawjson.Origin(format, pp, m, modelled, nil)})
		case "input_image":
			parts = append(parts, llm.Part{Type: llm.PartImage, URL: p.ImageURL, Detail: p.Detail, FileID: p.FileID,
				Origin: rawjson.Origin(format, pp, m, []string{"type", "image_url", "detail", "file_id"}, nil)})
		case "input_file":
			parts = append(parts, llm.Part{Type: llm.PartFile, FileID: p.FileID, FileData: p.FileData, URL: p.FileURL, Filename: p.Filename,
				Origin: rawjson.Origin(format, pp, m, []string{"type", "file_id", "file_data", "file_url", "filename"}, nil)})
		default:
			parts = append(parts, llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), rp...),
				Origin: &llm.Origin{Format: format, Path: pp}})
		}
	}
	return parts, false, nil
}
