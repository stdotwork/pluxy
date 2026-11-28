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

var Headers = []string{"anthropic-version", "anthropic-beta"}

var ClientHeaders = []string{
	"user-agent",
	"x-app",
	"anthropic-dangerous-direct-browser-access",
	"x-claude-code-session-id",
	"x-client-request-id",
	"x-stainless-lang",
	"x-stainless-package-version",
	"x-stainless-os",
	"x-stainless-arch",
	"x-stainless-runtime",
	"x-stainless-runtime-version",
	"x-stainless-retry-count",
	"x-stainless-timeout",
	"x-stainless-helper-method",
	"x-stainless-async",
	"x-client-app",
	"x-anthropic-additional-protection",
}

var ClaudeCodeHeaders = []string{
	"x-claude-code-agent-id",
	"x-claude-code-parent-agent-id",
	"x-claude-code-agent-type",
	"x-claude-code-prompt-id",
	"x-claude-code-request-class",
	"x-claude-code-prev-tool-durations",
	"x-claude-code-compaction",
	"x-claude-code-context-compacted",
	"x-claude-remote-container-id",
	"x-claude-remote-session-id",
}

func IsClaudeCode(headers map[string]string) bool {
	if (headers["x-app"] != "cli" && headers["x-app"] != "cli-bg") || !strings.HasPrefix(headers["user-agent"], "claude-cli/") {
		return false
	}
	for _, b := range strings.Split(headers["anthropic-beta"], ",") {
		if strings.TrimSpace(b) == "claude-code-20250219" {
			return true
		}
	}
	return false
}

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

var requestFields = []string{"model", "messages", "system", "max_tokens", "stream", "temperature", "top_p",
	"stop_sequences", "tools", "tool_choice", "thinking", "output_config"}

func parseServiceTier(top map[string]json.RawMessage) (string, []string) {
	if speed, ok := rawjson.Text(top["speed"]); ok && speed == "fast" {
		return llm.ServiceTierPriority, rawjson.With(requestFields, "speed")
	}
	if tier, ok := rawjson.Text(top["service_tier"]); ok {
		switch tier {
		case "auto":
			return llm.ServiceTierAuto, rawjson.With(requestFields, "service_tier")
		case "standard_only":
			return llm.ServiceTierDefault, rawjson.With(requestFields, "service_tier")
		}
	}
	return "", requestFields
}

func ParseRequest(body []byte) (llm.Request, error) {
	var in request
	if err := rawjson.Unmarshal(body, &in); err != nil {
		return llm.Request{}, invalid("request body is not valid JSON: %v", err)
	}
	if in.Model == "" {
		return llm.Request{}, invalid("model is required")
	}
	top := rawjson.Object(body)
	req := llm.Request{
		Model:  in.Model,
		Stream: in.Stream,
		Native: &llm.Native{Format: format, Body: body},
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
	rf, configRest := parseOutputConfigFormat(configRest)
	req.Options.ResponseFormat = rf
	nested["thinking"], nested["output_config"] = thinkingRest, configRest
	var modelled []string
	req.Options.ServiceTier, modelled = parseServiceTier(top)
	req.Origin = rawjson.Origin(format, "", top, modelled, nested)

	for i, raw := range in.Tools {
		req.Tools = append(req.Tools, parseTool(raw, fmt.Sprintf("tools[%d]", i)))
	}
	if tc := req.Options.ToolChoice; tc != nil && tc.Mode == llm.ToolChoiceTool {
		hasCodeExec := false
		hasFunc := false
		for _, t := range req.Tools {
			if t.Type == llm.ToolCodeExecution && t.Name == tc.Name {
				hasCodeExec = true
			} else if t.IsFunction() && t.Name == tc.Name {
				hasFunc = true
			}
		}
		if hasCodeExec && !hasFunc {
			tc.Mode = llm.ToolChoiceCodeExecution
		}
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
	err := rawjson.Unmarshal(raw, &t)
	if err == nil && strings.HasPrefix(t.Type, "web_search_") {
		var w struct {
			MaxUses        *int              `json:"max_uses"`
			AllowedDomains []string          `json:"allowed_domains"`
			BlockedDomains []string          `json:"blocked_domains"`
			UserLocation   *llm.UserLocation `json:"user_location"`
		}
		_ = rawjson.Unmarshal(raw, &w)
		var allowed, blocked []string
		if len(w.AllowedDomains) > 0 {
			allowed = w.AllowedDomains
		}
		if len(w.BlockedDomains) > 0 {
			blocked = w.BlockedDomains
		}
		return llm.Tool{
			Type: llm.ToolWebSearch,
			Name: t.Name,
			WebSearch: &llm.WebSearchTool{
				MaxUses:        w.MaxUses,
				AllowedDomains: allowed,
				BlockedDomains: blocked,
				UserLocation:   w.UserLocation,
			},
			Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)},
		}
	}
	if err == nil && strings.HasPrefix(t.Type, "code_execution_") {
		return llm.Tool{
			Type:   llm.ToolCodeExecution,
			Name:   t.Name,
			Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)},
		}
	}
	if err != nil || (t.Type != "" && t.Type != "custom") || t.Name == "" || len(t.InputSchema) == 0 {
		return llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}
	}
	return llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.InputSchema, Strict: t.Strict,
		Origin: rawjson.Origin(format, path, rawjson.Object(raw), []string{"type", "name", "description", "input_schema", "strict"}, nil)}
}

func parseToolChoice(raw json.RawMessage) (*llm.ToolChoice, *bool, json.RawMessage) {
	m := rawjson.Object(raw)
	var tc struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse *bool  `json:"disable_parallel_tool_use"`
	}
	if m == nil || rawjson.Unmarshal(raw, &tc) != nil {
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

var effortBudgets = []struct {
	max    int
	effort string
}{{0, "none"}, {512, "minimal"}, {1024, "low"}, {8192, "medium"}, {24576, "high"}}

func reasoningEffort(thinkingRaw, configRaw json.RawMessage) (effort string, thinkingRest, configRest json.RawMessage) {
	thinkingRest, configRest = thinkingRaw, configRaw
	th := rawjson.Object(thinkingRaw)
	cfg := rawjson.Object(configRaw)
	var t struct {
		Type   string `json:"type"`
		Budget *int   `json:"budget_tokens"`
	}
	if th == nil || rawjson.Unmarshal(thinkingRaw, &t) != nil {
		return "", thinkingRest, configRest
	}
	var explicit string
	_ = rawjson.Unmarshal(cfg["effort"], &explicit)
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
	return effort, rawjson.Rest(th, "type"), configRest
}

func parseOutputConfigFormat(configRaw json.RawMessage) (*llm.ResponseFormat, json.RawMessage) {
	cfg := rawjson.Object(configRaw)
	if cfg == nil {
		return nil, configRaw
	}
	formatRaw, ok := cfg["format"]
	if !ok {
		return nil, configRaw
	}
	fm := rawjson.Object(formatRaw)
	var f struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if fm == nil || json.Unmarshal(formatRaw, &f) != nil || f.Type != llm.ResponseFormatJSONSchema || rawjson.Object(f.Schema) == nil {
		return nil, configRaw
	}
	rest := rawjson.Object(rawjson.Rest(cfg, "format"))
	if fr := rawjson.Rest(fm, "type", "schema"); fr != nil {
		if rest == nil {
			rest = map[string]json.RawMessage{}
		}
		rest["format"] = fr
	}
	return &llm.ResponseFormat{Type: llm.ResponseFormatJSONSchema, Name: "response", Schema: f.Schema}, rawjson.Encode(rest)
}

func parseSystem(raw json.RawMessage) (*llm.Item, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var s string
	if rawjson.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return &llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, TextContent: true,
			Content: []llm.Part{{Type: llm.PartText, Text: s}}, Origin: &llm.Origin{Format: format, Path: "system"}}, nil
	}
	var blocks []json.RawMessage
	if err := rawjson.Unmarshal(raw, &blocks); err != nil {
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

func parseMessage(raw json.RawMessage, path string) ([]llm.Item, error) {
	m := rawjson.Object(raw)
	if m == nil {
		return nil, fmt.Errorf("message must be an object")
	}
	var head struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := rawjson.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(head.Role), llm.RoleSystem) {
		head.Role = llm.RoleSystem
	}
	if head.Role != llm.RoleUser && head.Role != llm.RoleAssistant && head.Role != llm.RoleSystem {
		return nil, fmt.Errorf("unsupported role %q", head.Role)
	}
	origin := rawjson.Origin(format, path, m, []string{"role", "content"}, nil)
	var s string
	if rawjson.Unmarshal(head.Content, &s) == nil {
		it := llm.Item{Type: llm.ItemTypeMessage, Role: head.Role, TextContent: true, Origin: origin}
		if s != "" {
			it.Content = []llm.Part{{Type: llm.PartText, Text: s}}
		}
		return []llm.Item{it}, nil
	}
	var blocks []json.RawMessage
	if err := rawjson.Unmarshal(head.Content, &blocks); err != nil {
		return nil, fmt.Errorf("content must be a string or a list of content blocks")
	}
	var items []llm.Item
	msg := -1
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
		if bm == nil || rawjson.Unmarshal(rb, &b) != nil {
			return nil, fmt.Errorf("content[%d] must be an object", i)
		}
		item := func(it llm.Item) {
			items = append(items, it)
			msg = -1
		}
		switch {
		case b.Type == "thinking" && head.Role == llm.RoleAssistant:
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
			if out, ok := textResult(b.Content); ok {
				it.Output = out
				modelled = append(modelled, "content")
			} else if parts, out, ok := partsResult(b.Content, bp+".content"); ok {
				it.Content, it.Output = parts, out
				modelled = append(modelled, "content")
			}
			if json.Unmarshal(bm["is_error"], &it.IsError) == nil {
				modelled = append(modelled, "is_error")
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
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: bp, Raw: append(json.RawMessage(nil), rb...)}})
		}
	}
	if origin != nil && len(origin.Raw) > 0 {
		it := llm.Item{Type: llm.ItemTypeMessage, Role: head.Role, Origin: origin}
		if head.Role == llm.RoleAssistant {
			items = append([]llm.Item{it}, items...)
		} else {
			items = append(items, it)
		}
	}
	return items, nil
}

func parsePart(raw json.RawMessage, path string) (llm.Part, error) {
	m := rawjson.Object(raw)
	var b block
	if m == nil || rawjson.Unmarshal(raw, &b) != nil {
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

func textResult(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	var s string
	if rawjson.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var blocks []json.RawMessage
	if rawjson.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	texts := make([]string, 0, len(blocks))
	for _, rb := range blocks {
		m := rawjson.Object(rb)
		var b block
		if len(m) != 2 || rawjson.Unmarshal(rb, &b) != nil || b.Type != "text" || m["text"] == nil {
			return "", false
		}
		texts = append(texts, b.Text)
	}
	return strings.Join(texts, "\n"), true
}

func partsResult(raw json.RawMessage, path string) ([]llm.Part, string, bool) {
	var blocks []json.RawMessage
	if rawjson.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return nil, "", false
	}
	parts := make([]llm.Part, 0, len(blocks))
	var texts []string
	for i, rb := range blocks {
		p, err := parsePart(rb, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, "", false
		}
		if p.Type == llm.PartText {
			texts = append(texts, p.Text)
		}
		parts = append(parts, p)
	}
	return parts, strings.Join(texts, "\n"), true
}

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
