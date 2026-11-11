package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// What xAI's Responses API needs changed in a request, as today's core makes the changes on its
// API-key path (internal/runtime/executor/xai_executor_request.go, prepareResponsesRequestToDoc
// and the helpers it calls). Each rule names where xAI's API differs from the body it is given:
//
//   - stream: the upstream stream flag follows the client's stream flag;
//   - removed fields: stop, previous_response_id, prompt_cache_retention, safety_identifier and
//     stream_options are taken out, as today's core takes them out;
//   - store false unless the client set store, and reasoning.encrypted_content in include;
//   - tools: a custom tool is sent as a function tool; a function's local $ref references are
//     inlined and its $defs and definitions removed; a function without parameters gets an empty
//     object schema; more than 200 tools are cut to the first 200 (xaiMaxTools);
//   - input: a custom tool call and its output are sent as a function call and its output
//     (normalizeXAIInputCustomToolCallsDoc);
//   - apply_patch: Codex's patch tool is sent as a function with an input argument, and xAI's call
//     to it is turned back into a custom tool call (patch.go).
//
// Not done here, and why it is left out: namespace tools are not folded into functions with
// qualified names, and their responses are not restored (the Codex app's tools; the extension
// sends them as the client wrote them); image references and encrypted reasoning content are not rewritten
// (sanitizeXAIInputEncryptedContent and normalizeXAIImageRefsDoc); the reasoning effort is not
// checked against each model's levels, which needs today's model catalog. Today's core also
// sets instructions and a list input, and the Codex translator's tool defaults (tool_choice,
// parallel_tool_calls, strict); those come from the Codex translator that core runs for every
// Responses request, not from a rule about xAI, so the extension leaves them out.

// maxTools is how many tools xAI takes in one request (xaiMaxTools in today's core).
const maxTools = 200

// removedFields are the fields today's core deletes from a Responses body before it sends it.
// The reason is not written down in core. xAI's Responses API has no stop (core says so);
// the others are left out by the core on the same path, unverified here.
var removedFields = []string{"stop", "previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"}

// buildBody returns the request body for xAI's Responses API and how it was built.
func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	body, tr, _, err := buildRequest(req, false)
	return body, tr, err
}

// buildSearchBody is buildBody with the x_search tool injected when injectXSearch is set.
func buildSearchBody(req llm.Request, injectXSearch bool) ([]byte, *llm.Translation, error) {
	body, tr, _, err := buildRequest(req, injectXSearch)
	return body, tr, err
}

// buildRequest is buildSearchBody that also returns the bridge that turns xAI's calls to the custom
// apply_patch tool back into custom tool calls (patch.go), or nil when the request has no such
// tool.
func buildRequest(req llm.Request, injectXSearch bool) ([]byte, *llm.Translation, *patchBridge, error) {
	tr := &llm.Translation{To: llm.FormatOpenAIResponses}
	var body []byte
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == llm.FormatOpenAIResponses {
			tr.Passthrough = true
			body = n.Body
		}
	}
	if body == nil {
		b, losses, err := openairesponses.EncodeRequest(req)
		if err != nil {
			return nil, nil, nil, err
		}
		body, tr.Losses = b, losses
		if req.DropPolicy == llm.DropReject && tr.Dropped() {
			return nil, nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
				Message: "Responses cannot carry parts of this request"}
		}
	}
	m, err := decodeObject(body)
	if err != nil {
		return nil, nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = req.Model
	m["stream"] = req.Stream
	for _, k := range removedFields {
		delete(m, k)
	}
	// xAI keeps each response unless told not to. With previous_response_id removed a kept
	// response is never used again, so the conversation is not left on xAI unless the client
	// asked for that. Without stored responses, a reasoning model's thinking reaches the next turn
	// only as encrypted reasoning content, so that is asked for (as the Codex extension does).
	if _, ok := m["store"]; !ok {
		m["store"] = false
	}
	m["include"] = withEncryptedReasoning(m["include"])
	var patch *patchBridge
	if tools, ok := m["tools"].([]any); ok {
		tools, active := rewriteApplyPatchTools(tools)
		if active {
			patch = newPatchBridge()
			rewriteApplyPatchChoice(m["tool_choice"])
		}
		m["tools"] = rewriteTools(tools)
	}
	if injectXSearch {
		injectSearch(m)
	}
	if tools, ok := m["tools"].([]any); ok && len(tools) > maxTools {
		m["tools"] = tools[:maxTools]
	}
	if input, ok := m["input"].([]any); ok {
		items, err := rewriteCustomToolItems(input)
		if err != nil {
			return nil, nil, nil, err
		}
		m["input"] = items
	}
	out, err := marshal(m)
	if err != nil {
		return nil, nil, nil, err
	}
	return out, tr, patch, nil
}

// withEncryptedReasoning returns the client's include list with reasoning.encrypted_content added.
func withEncryptedReasoning(v any) []any {
	list, _ := v.([]any)
	for _, x := range list {
		if x == "reasoning.encrypted_content" {
			return list
		}
	}
	return append(list, "reasoning.encrypted_content")
}

// rewriteTools sends custom tools as function tools, gives each function a parameters schema
// xAI accepts. The caller caps the list after optional search injection.
func rewriteTools(tools []any) []any {
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if tm["type"] == "custom" {
			// Today's core sends a custom tool as a function and keeps its other fields, its format
			// grammar included (normalizeXAITool). A custom tool without parameters takes the empty
			// object schema below, like any function without one.
			tm["type"] = "function"
		}
		if tm["type"] != "function" {
			continue
		}
		if _, ok := tm["parameters"].(map[string]any); ok {
			normalizeSchema(tm, "parameters")
		} else {
			tm["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
	}
	return tools
}

// rewriteCustomToolItems turns each custom tool call into a function call and each custom tool
// output into a function call output, the form xAI takes. An item without its call ID or name
// is dropped, as today's core drops it. A call to apply_patch carries its patch text in the
// input argument (patch.go), whatever the text looks like, and must have text for its input.
func rewriteCustomToolItems(input []any) ([]any, error) {
	out := make([]any, 0, len(input))
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok {
			out = append(out, raw)
			continue
		}
		callID, _ := item["call_id"].(string)
		callID = strings.TrimSpace(callID)
		switch item["type"] {
		case "custom_tool_call":
			name, _ := item["name"].(string)
			name = strings.TrimSpace(name)
			if callID == "" || name == "" {
				continue
			}
			arguments := ""
			if name == applyPatchName {
				text, ok := item["input"].(string)
				if !ok {
					return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "apply_patch history input must be a string", Scope: llm.ScopeRequest}
				}
				arguments = wrapPatchInput(text)
			} else {
				arguments = customCallArguments(item["input"])
			}
			out = append(out, map[string]any{"type": "function_call", "call_id": callID, "name": name,
				"arguments": arguments})
		case "custom_tool_call_output":
			if callID == "" {
				continue
			}
			out = append(out, map[string]any{"type": "function_call_output", "call_id": callID,
				"output": customCallOutput(item["output"])})
		default:
			out = append(out, raw)
		}
	}
	return out, nil
}

// customCallArguments turns a custom tool's input into the arguments of a function call. An
// object is kept; a string that is an object is parsed; any other text is wrapped as "input".
func customCallArguments(in any) string {
	switch v := in.(type) {
	case nil:
		return "{}"
	case string:
		trimmed := strings.TrimSpace(v)
		var obj map[string]any
		if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
			return trimmed
		}
		b, _ := json.Marshal(map[string]any{"input": v})
		return string(b)
	case map[string]any:
		b, _ := json.Marshal(v)
		return string(b)
	default:
		b, _ := json.Marshal(map[string]any{"input": v})
		return string(b)
	}
}

// customCallOutput returns a custom tool's output as the text a function call output carries.
func customCallOutput(out any) string {
	switch v := out.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// normalizeSchema inlines the local $ref references of the JSON schema at holder[key] and removes
// its $defs and definitions, which xAI does not resolve.
func normalizeSchema(holder map[string]any, key string) {
	s, ok := holder[key].(map[string]any)
	if !ok {
		return
	}
	defs := map[string]any{}
	for _, k := range []string{"$defs", "definitions"} {
		if d, ok := s[k].(map[string]any); ok {
			for name, v := range d {
				defs["#/"+k+"/"+name] = v
			}
		}
	}
	out := inline(s, defs, 0).(map[string]any)
	delete(out, "$defs")
	delete(out, "definitions")
	holder[key] = out
}

// inline replaces local $ref references in v with what they point at, up to a depth that stops
// recursive schemas.
func inline(v any, defs map[string]any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok && depth < 16 {
			if target, ok := defs[ref]; ok {
				return inline(target, defs, depth+1)
			}
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = inline(e, defs, depth)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = inline(e, defs, depth)
		}
		return out
	}
	return v
}

func decodeObject(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, err
	}
	return m, nil
}

func marshal(m map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
