package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const reqIDTokenLen = 8 // 8 hex digits inside "bm-req:<8-hex>;"

var placeholderToken = []byte("bm-req:" + strings.Repeat("0", reqIDTokenLen) + ";")

// RequestTemplate holds a pre-serialized JSON body with the exact byte offset of the
// 8-character request ID inside "bm-req:<8-hex>;".
type RequestTemplate struct {
	raw      []byte
	idOffset int
}

// Materialize returns a copy of the template with reqID (exactly 8 hex bytes) stamped in place.
func (t *RequestTemplate) Materialize(reqID string) []byte {
	out := make([]byte, len(t.raw))
	copy(out, t.raw)
	copy(out[t.idOffset:t.idOffset+reqIDTokenLen], reqID)
	return out
}

type benchTool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

func benchTools() []benchTool {
	names := []string{
		"read_file", "write_file", "edit_file", "delete_file", "list_dir",
		"search_files", "grep_text", "run_shell", "git_status", "git_diff",
		"git_log", "git_commit", "fetch_url", "web_search", "list_symbols",
		"find_references", "go_to_definition", "run_tests", "format_code", "check_lint",
	}
	tools := make([]benchTool, 0, len(names))
	for i, name := range names {
		tools = append(tools, benchTool{
			Name:        name,
			Description: fmt.Sprintf("Coding agent tool #%d (%s) for workspace inspection and edits.", i+1, name),
			Properties: map[string]any{
				"path":    map[string]any{"type": "string", "description": "Workspace-relative file or directory path."},
				"pattern": map[string]any{"type": "string", "description": "Optional filter or search expression."},
				"limit":   map[string]any{"type": "integer", "description": "Maximum number of items to return."},
				"verbose": map[string]any{"type": "boolean", "description": "Include detailed metadata in the output."},
			},
			Required: []string{"path"},
		})
	}
	return tools
}

// BuildMessagesTemplate builds a deterministic Anthropic Messages JSON request body sized to
// approximately targetKB kilobytes, with "bm-req:<8-hex>;" in the final user message.
func BuildMessagesTemplate(model string, stream bool, targetKB int) (*RequestTemplate, error) {
	tools := make([]map[string]any, 0, 20)
	for _, tl := range benchTools() {
		tools = append(tools, map[string]any{
			"name":        tl.Name,
			"description": tl.Description,
			"input_schema": map[string]any{
				"type":       "object",
				"properties": tl.Properties,
				"required":   tl.Required,
			},
		})
	}

	systemPrompt := strings.Repeat("You are an engineering assistant operating inside a software repository. Follow workspace conventions, inspect files before editing, and verify every change with tests. ", 6)

	messages := []map[string]any{
		{"role": "user", "content": "Inspect the proxy server and summarize how requests flow through the extension."},
		{"role": "assistant", "content": "I will inspect the router, server handler, and extension host to trace request lifecycle, streaming flush behavior, and error mapping."},
		{"role": "user", "content": "Check connection pooling and memory allocation patterns under high concurrency as well."},
		{"role": "assistant", "content": "Let me review the transport configuration and buffering across the server and openai-compatible extension."},
	}

	// Build base without padding first to measure how much deterministic turn padding is needed.
	makeDoc := func(pad string) ([]byte, error) {
		msgs := make([]map[string]any, 0, len(messages)+2)
		msgs = append(msgs, messages...)
		if pad != "" {
			msgs = append(msgs,
				map[string]any{"role": "user", "content": "Context excerpt:\n" + pad},
				map[string]any{"role": "assistant", "content": "Understood, continuing with the context excerpt."},
			)
		}
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": "Please provide the next delta response for request " + string(placeholderToken),
		})
		doc := struct {
			Model     string           `json:"model"`
			Stream    bool             `json:"stream"`
			MaxTokens int              `json:"max_tokens"`
			System    string           `json:"system"`
			Tools     []map[string]any `json:"tools"`
			Messages  []map[string]any `json:"messages"`
		}{
			Model:     model,
			Stream:    stream,
			MaxTokens: 1024,
			System:    systemPrompt,
			Tools:     tools,
			Messages:  msgs,
		}
		return json.Marshal(doc)
	}

	base, err := makeDoc("")
	if err != nil {
		return nil, err
	}
	targetBytes := targetKB * 1024
	if need := targetBytes - len(base); need > 128 {
		unit := "func handleRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) { /* deterministic padding line for benchmark payload */ }\n"
		pad := strings.Repeat(unit, (need/len(unit))+1)
		if len(pad) > need {
			pad = pad[:need]
		}
		base, err = makeDoc(pad)
		if err != nil {
			return nil, err
		}
	}

	idx := bytes.Index(base, placeholderToken)
	if idx < 0 {
		return nil, fmt.Errorf("placeholder token missing in serialized messages payload")
	}
	return &RequestTemplate{
		raw:      base,
		idOffset: idx + len("bm-req:"),
	}, nil
}

// BuildChatTemplate builds a deterministic OpenAI Chat Completions JSON request body matching
// the Messages template content, for the Chat-direct baseline.
func BuildChatTemplate(model string, stream bool, targetKB int) (*RequestTemplate, error) {
	tools := make([]map[string]any, 0, 20)
	for _, tl := range benchTools() {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tl.Name,
				"description": tl.Description,
				"parameters": map[string]any{
					"type":       "object",
					"properties": tl.Properties,
					"required":   tl.Required,
				},
			},
		})
	}

	systemPrompt := strings.Repeat("You are an engineering assistant operating inside a software repository. Follow workspace conventions, inspect files before editing, and verify every change with tests. ", 6)

	messages := []map[string]any{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": "Inspect the proxy server and summarize how requests flow through the extension."},
		{"role": "assistant", "content": "I will inspect the router, server handler, and extension host to trace request lifecycle, streaming flush behavior, and error mapping."},
		{"role": "user", "content": "Check connection pooling and memory allocation patterns under high concurrency as well."},
		{"role": "assistant", "content": "Let me review the transport configuration and buffering across the server and openai-compatible extension."},
	}

	type streamOpts struct {
		IncludeUsage bool `json:"include_usage"`
	}

	makeDoc := func(pad string) ([]byte, error) {
		msgs := make([]map[string]any, 0, len(messages)+2)
		msgs = append(msgs, messages...)
		if pad != "" {
			msgs = append(msgs,
				map[string]any{"role": "user", "content": "Context excerpt:\n" + pad},
				map[string]any{"role": "assistant", "content": "Understood, continuing with the context excerpt."},
			)
		}
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": "Please provide the next delta response for request " + string(placeholderToken),
		})
		doc := struct {
			Model         string           `json:"model"`
			Stream        bool             `json:"stream"`
			StreamOptions *streamOpts      `json:"stream_options,omitempty"`
			MaxTokens     int              `json:"max_tokens"`
			Tools         []map[string]any `json:"tools"`
			Messages      []map[string]any `json:"messages"`
		}{
			Model:     model,
			Stream:    stream,
			MaxTokens: 1024,
			Tools:     tools,
			Messages:  msgs,
		}
		if stream {
			doc.StreamOptions = &streamOpts{IncludeUsage: true}
		}
		return json.Marshal(doc)
	}

	base, err := makeDoc("")
	if err != nil {
		return nil, err
	}
	targetBytes := targetKB * 1024
	if need := targetBytes - len(base); need > 128 {
		unit := "func handleRequest(ctx context.Context, w http.ResponseWriter, r *http.Request) { /* deterministic padding line for benchmark payload */ }\n"
		pad := strings.Repeat(unit, (need/len(unit))+1)
		if len(pad) > need {
			pad = pad[:need]
		}
		base, err = makeDoc(pad)
		if err != nil {
			return nil, err
		}
	}

	idx := bytes.Index(base, placeholderToken)
	if idx < 0 {
		return nil, fmt.Errorf("placeholder token missing in serialized chat payload")
	}
	return &RequestTemplate{
		raw:      base,
		idOffset: idx + len("bm-req:"),
	}, nil
}
