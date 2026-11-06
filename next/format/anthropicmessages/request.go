// Package anthropicmessages converts between Anthropic Messages and pluxy's internal model: core
// uses it to read client requests and write client replies, and provider extensions use it to
// read replies from an upstream that speaks Messages.
//
// A request read here keeps its body as the native form, so an upstream that speaks Messages gets
// it unchanged, thinking blocks, signatures and cache_control included. A reply from such an
// upstream keeps every upstream event in its raw field, so a Messages client gets the upstream's
// events unchanged; a reply from another format is rebuilt into the Messages event sequence.
package anthropicmessages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatAnthropicMessages

// Headers are the client request headers that belong to a Messages body: the API version it is
// written against and the beta features it uses. Core passes them on with the native body.
var Headers = []string{"anthropic-version", "anthropic-beta"}

func invalid(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(msg, args...), Scope: llm.ScopeRequest}
}

type request struct {
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	System        json.RawMessage   `json:"system"`
	MaxTokens     *int              `json:"max_tokens"`
	Stream        bool              `json:"stream"`
	Temperature   *float64          `json:"temperature"`
	TopP          *float64          `json:"top_p"`
	StopSequences []string          `json:"stop_sequences"`
	Tools         []json.RawMessage `json:"tools"`
}

// requestFields are the top-level fields the model carries. tool_choice, thinking and
// output_config are carried only in the shapes the parser knows; anything else in them stays in
// Origin.Raw.
var requestFields = []string{"model", "messages", "system", "max_tokens", "stream", "temperature", "top_p",
	"stop_sequences", "tools", "tool_choice", "thinking", "output_config"}

// ParseRequest reads a Messages request body. The result keeps body as its Native form, and every
// item, part and tool keeps the fields the model has no place for (cache_control, signatures,
// citations and the like) in its Origin. Errors are *llm.Error with code invalid_request.
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
			MaxOutputTokens: in.MaxTokens,
			Temperature:     in.Temperature,
			TopP:            in.TopP,
			Stop:            in.StopSequences,
		},
	}
	nested := map[string]json.RawMessage{}
	if raw, ok := top["tool_choice"]; ok {
		tc, parallel, rest := parseToolChoice(raw)
		req.Options.ToolChoice, req.Options.ParallelToolCalls = tc, parallel
		nested["tool_choice"] = rest
	}
	effort, thinkingRest, configRest := reasoningEffort(top["thinking"], top["output_config"])
	req.Options.ReasoningEffort = effort
	nested["thinking"], nested["output_config"] = thinkingRest, configRest
	req.Origin = rawjson.Origin(format, "", top, requestFields, nested)

	for i, raw := range in.Tools {
		req.Tools = append(req.Tools, parseTool(raw, fmt.Sprintf("tools[%d]", i)))
	}
	if sys, err := parseSystem(in.System); err != nil {
		return llm.Request{}, err
	} else if sys != nil {
		req.Items = append(req.Items, *sys)
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

func parseTool(raw json.RawMessage, path string) llm.Tool {
	var t struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
		Strict      *bool           `json:"strict"`
	}
	if json.Unmarshal(raw, &t) != nil || (t.Type != "" && t.Type != "custom") || t.Name == "" || len(t.InputSchema) == 0 {
		// Server tools (web search, code execution, ...) and versioned client tools (bash,
		// text editor, ...) are not function tools; they survive whole.
		return llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}
	}
	return llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.InputSchema, Strict: t.Strict,
		Origin: rawjson.Origin(format, path, rawjson.Object(raw), []string{"type", "name", "description", "input_schema", "strict"}, nil)}
}

// parseToolChoice reads tool_choice: the mode, whether parallel tool use was turned off, and the
// fields left over.
func parseToolChoice(raw json.RawMessage) (*llm.ToolChoice, *bool, json.RawMessage) {
	m := rawjson.Object(raw)
	var tc struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use"`
	}
	if m == nil || json.Unmarshal(raw, &tc) != nil {
		return nil, nil, raw
	}
	var choice *llm.ToolChoice
	switch tc.Type {
	case "auto":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceAuto}
	case "any":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
	case "none":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceNone}
	case "tool":
		if tc.Name != "" {
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: tc.Name}
		}
	}
	if choice == nil {
		return nil, nil, raw
	}
	var parallel *bool
	if d := tc.DisableParallelToolUse; d != nil {
		p := !*d
		parallel = &p
	}
	return choice, parallel, rawjson.Rest(m, "type", "name", "disable_parallel_tool_use")
}

// Thinking budgets at or below these get the named reasoning effort; above the last, xhigh. They
// are today's core's thresholds.
var effortBudgets = []struct {
	max    int
	effort string
}{{0, "none"}, {512, "minimal"}, {1024, "low"}, {8192, "medium"}, {24576, "high"}}

// reasoningEffort reads the reasoning effort a request asks for from thinking and
// output_config.effort, as today's core does: an explicit effort as given; a thinking budget as
// the effort it falls in; adaptive thinking without an effort as xhigh; disabled thinking as none.
// It returns the fields of thinking and output_config the effort does not carry, so they are
// reported when a translation drops them.
func reasoningEffort(thinkingRaw, configRaw json.RawMessage) (effort string, thinkingRest, configRest json.RawMessage) {
	thinkingRest, configRest = thinkingRaw, configRaw
	th := rawjson.Object(thinkingRaw)
	cfg := rawjson.Object(configRaw)
	var t struct {
		Type   string `json:"type"`
		Budget *int   `json:"budget_tokens"`
	}
	if th == nil || json.Unmarshal(thinkingRaw, &t) != nil {
		return "", thinkingRest, configRest
	}
	var explicit string
	_ = json.Unmarshal(cfg["effort"], &explicit)
	explicit = strings.ToLower(strings.TrimSpace(explicit))
	useExplicit := func() {
		effort = explicit
		configRest = rawjson.Rest(cfg, "effort")
	}
	switch t.Type {
	case "enabled":
		switch {
		case t.Budget != nil && *t.Budget >= 0:
			effort = "xhigh"
			for _, b := range effortBudgets {
				if *t.Budget <= b.max {
					effort = b.effort
					break
				}
			}
		case explicit != "":
			useExplicit()
		default:
			effort = "auto"
		}
	case "adaptive", "auto":
		if explicit != "" {
			useExplicit()
		} else {
			effort = "xhigh"
		}
	case "disabled":
		effort = "none"
	default:
		return "", thinkingRest, configRest
	}
	// The type is carried by the effort; the budget and anything else are not.
	return effort, rawjson.Rest(th, "type"), configRest
}

// parseSystem reads the system prompt, a string or a list of text blocks, into one system
// message.
func parseSystem(raw json.RawMessage) (*llm.Item, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return &llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, TextContent: true,
			Content: []llm.Part{{Type: llm.PartText, Text: s}}, Origin: &llm.Origin{Format: format, Path: "system"}}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, invalid("system must be a string or a list of text blocks")
	}
	it := &llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, Origin: &llm.Origin{Format: format, Path: "system"}}
	for i, b := range blocks {
		p, err := parsePart(b, fmt.Sprintf("system[%d]", i))
		if err != nil {
			return nil, invalid("system[%d]: %v", i, err)
		}
		it.Content = append(it.Content, p)
	}
	return it, nil
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	Source    *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
		FileID    string `json:"file_id"`
	} `json:"source"`
	Title string `json:"title"`
}

// parseMessage reads one message into items, in block order: runs of content blocks become a
// message item, and each thinking block, tool_use and tool_result becomes its own item, so the
// order of thinking, calls and results survives.
func parseMessage(raw json.RawMessage, path string) ([]llm.Item, error) {
	m := rawjson.Object(raw)
	if m == nil {
		return nil, fmt.Errorf("message must be an object")
	}
	var head struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	if head.Role != llm.RoleUser && head.Role != llm.RoleAssistant {
		return nil, fmt.Errorf("unsupported role %q", head.Role)
	}
	origin := rawjson.Origin(format, path, m, []string{"role", "content"}, nil)
	var s string
	if json.Unmarshal(head.Content, &s) == nil {
		it := llm.Item{Type: llm.ItemTypeMessage, Role: head.Role, TextContent: true, Origin: origin}
		if s != "" {
			it.Content = []llm.Part{{Type: llm.PartText, Text: s}}
		}
		return []llm.Item{it}, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(head.Content, &blocks); err != nil {
		return nil, fmt.Errorf("content must be a string or a list of content blocks")
	}
	var items []llm.Item
	msg := -1 // index in items of the message item collecting content parts, or -1
	// The message's own fields go on its first message item.
	messageOrigin := func() *llm.Origin {
		if origin == nil || len(origin.Raw) == 0 {
			return &llm.Origin{Format: format, Path: path}
		}
		o := origin
		origin = &llm.Origin{Format: format, Path: path}
		return o
	}
	if len(blocks) == 0 {
		return []llm.Item{{Type: llm.ItemTypeMessage, Role: head.Role, Origin: messageOrigin()}}, nil
	}
	for i, rb := range blocks {
		bp := fmt.Sprintf("%s.content[%d]", path, i)
		bm := rawjson.Object(rb)
		var b block
		if bm == nil || json.Unmarshal(rb, &b) != nil {
			return nil, fmt.Errorf("content[%d] must be an object", i)
		}
		item := func(it llm.Item) {
			items = append(items, it)
			msg = -1
		}
		switch {
		case b.Type == "thinking" && head.Role == llm.RoleAssistant:
			// The signature stays in Origin.Raw: it is opaque, and only a Messages upstream can
			// check it.
			item(llm.Item{Type: llm.ItemTypeReasoning, Text: b.Thinking, Origin: rawjson.Origin(format, bp, bm, []string{"type", "thinking"}, nil)})
		case b.Type == "tool_use" && head.Role == llm.RoleAssistant && b.ID != "" && b.Name != "":
			args, err := compactJSON(b.Input)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: tool_use input: %v", i, err)
			}
			item(llm.Item{Type: llm.ItemTypeToolCall, CallID: b.ID, Name: b.Name, Arguments: args,
				Origin: rawjson.Origin(format, bp, bm, []string{"type", "id", "name", "input"}, nil)})
		case b.Type == "tool_result" && head.Role == llm.RoleUser:
			it := llm.Item{Type: llm.ItemTypeToolResult, CallID: b.ToolUseID}
			modelled := []string{"type", "tool_use_id"}
			// shortcut: only text results are carried (a string, or text blocks with nothing
			// else in them, joined); a result holding images, documents or cache_control on
			// its inner blocks stays in Origin.Raw and is reported as dropped when translated.
			// Carry it as parts when a provider needs it.
			if out, ok := textResult(b.Content); ok {
				it.Output = out
				modelled = append(modelled, "content")
			}
			it.Origin = rawjson.Origin(format, bp, bm, modelled, nil)
			item(it)
		case b.Type == "text" || b.Type == "image" || b.Type == "document":
			p, err := parsePart(rb, bp)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %v", i, err)
			}
			if msg < 0 {
				items = append(items, llm.Item{Type: llm.ItemTypeMessage, Role: head.Role, Origin: messageOrigin()})
				msg = len(items) - 1
			}
			items[msg].Content = append(items[msg].Content, p)
		default:
			// redacted_thinking, server tool use and results, container uploads, ...: opaque
			// to the model, kept whole and in place.
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: bp, Raw: append(json.RawMessage(nil), rb...)}})
		}
	}
	return items, nil
}

// parsePart reads a text, image or document block.
func parsePart(raw json.RawMessage, path string) (llm.Part, error) {
	m := rawjson.Object(raw)
	var b block
	if m == nil || json.Unmarshal(raw, &b) != nil {
		return llm.Part{}, fmt.Errorf("content block must be an object")
	}
	native := llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), raw...), Origin: &llm.Origin{Format: format, Path: path}}
	switch b.Type {
	case "text":
		return llm.Part{Type: llm.PartText, Text: b.Text, Origin: rawjson.Origin(format, path, m, []string{"type", "text"}, nil)}, nil
	case "image", "document":
		if b.Source == nil {
			return native, nil
		}
		src := rawjson.Rest(rawjson.Object(m["source"]), "type", "media_type", "data", "url", "file_id")
		nested := map[string]json.RawMessage{"source": src}
		modelled := []string{"type", "source"}
		var p llm.Part
		switch b.Source.Type {
		case "base64":
			url := "data:" + b.Source.MediaType + ";base64," + b.Source.Data
			if b.Type == "image" {
				p = llm.Part{Type: llm.PartImage, URL: url}
			} else {
				p = llm.Part{Type: llm.PartFile, FileData: url}
			}
		case "url":
			p = llm.Part{Type: llm.PartImage, URL: b.Source.URL}
			if b.Type == "document" {
				p = llm.Part{Type: llm.PartFile, URL: b.Source.URL}
			}
		case "file":
			p = llm.Part{Type: llm.PartImage, FileID: b.Source.FileID}
			if b.Type == "document" {
				p = llm.Part{Type: llm.PartFile, FileID: b.Source.FileID}
			}
		default:
			// Plain-text and content-block documents have no place in the model.
			return native, nil
		}
		if b.Type == "document" && b.Title != "" {
			p.Filename = b.Title
			modelled = append(modelled, "title")
		}
		p.Origin = rawjson.Origin(format, path, m, modelled, nested)
		return p, nil
	}
	return native, nil
}

// textResult returns a tool result's content as text when it is a string or a list of text
// blocks with no other fields.
func textResult(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	texts := make([]string, 0, len(blocks))
	for _, rb := range blocks {
		m := rawjson.Object(rb)
		var b block
		if len(m) != 2 || json.Unmarshal(rb, &b) != nil || b.Type != "text" || m["text"] == nil {
			return "", false
		}
		texts = append(texts, b.Text)
	}
	return strings.Join(texts, "\n"), true
}

// compactJSON returns a tool_use input as compact JSON text; a missing input is {}.
func compactJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", err
	}
	return buf.String(), nil
}
