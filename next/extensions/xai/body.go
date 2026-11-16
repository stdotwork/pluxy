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
//   - additional_tools: the tools of each such input item (Responses Lite) join the tools list and
//     the item is removed, before the rules below;
//   - tools: a function's local $ref references are inlined and its $defs and definitions removed;
//     a function without parameters gets an empty object schema; more than 200 tools are cut to the
//     first 200 (xaiMaxTools);
//   - custom tools: each custom tool the client declares, Codex's apply_patch included, is sent as a
//     function with one input argument, and xAI's call to it is turned back into a custom tool
//     call (patch.go). Today's core does this for apply_patch only and sends another custom tool
//     as a function without an input field; that differs on purpose;
//   - input: a custom tool call and its output are sent as a function call and its output
//     (normalizeXAIInputCustomToolCallsDoc); a call to a custom tool of the request carries its
//     text in the input argument;
//   - namespace tools: the Codex app's namespaces are sent as functions, flattened or folded, and
//     xAI's calls to them are turned back into calls in their namespaces (namespace.go); the
//     Codex app's automation_update tool gets parameters that accept any object.
//
// Not done here, and why it is left out: image references and encrypted reasoning content are not rewritten
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

// buildRequest is buildSearchBody that also returns what turns xAI's reply back for the client:
// calls to the custom tools of the request become custom tool calls (patch.go) and calls to the
// functions that stand for namespace tools become calls in their namespaces (namespace.go). It is
// nil when the request has neither.
func buildRequest(req llm.Request, injectXSearch bool) ([]byte, *llm.Translation, *replyBridge, error) {
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
	topLevel := 0
	if tools, ok := m["tools"].([]any); ok {
		topLevel = len(tools)
	}
	promoteAdditionalTools(m)
	reply := &replyBridge{}
	var customs customTools
	var namespaces *namespaceTools
	if tools, ok := m["tools"].([]any); ok {
		tools, patch := rewriteCustomTools(tools, topLevel)
		customs = patch
		if patch.active() {
			reply.patch = newPatchBridge(patch)
			patch.rewriteChoice(m["tool_choice"])
		}
		// Today's core counts the x_search tool it will inject when it decides to fold. Its other
		// condition, a forced hosted tool that is the only tool, cannot hold with a namespace there.
		injected := 0
		if injectXSearch && !requestHasSearch(m) {
			injected = 1
		}
		tools, namespaces = rewriteNamespaces(tools, injected)
		if namespaces != nil {
			reply.ns = newNSRestorer(namespaces.refs)
			namespaces.rewriteChoice(m["tool_choice"])
			// A folded namespace with a custom tool may be called for it.
			dispatchers := map[string]bool{}
			for ns := range patch.namespaces {
				if ref := namespaces.refs[ns]; ref != nil && ref.dispatcher {
					dispatchers[ns] = true
				}
			}
			if len(dispatchers) > 0 {
				reply.hold = newDispatchHold(dispatchers, patch, namespaces.refs, reply.ns)
			}
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
		items, err := rewriteCustomToolItems(input, namespaces != nil, customs)
		if err != nil {
			return nil, nil, nil, err
		}
		if namespaces != nil {
			namespaces.rewriteHistory(items)
		}
		m["input"] = items
	}
	out, err := marshal(m)
	if err != nil {
		return nil, nil, nil, err
	}
	if reply.patch == nil && reply.ns == nil && reply.hold == nil {
		reply = nil
	}
	return out, tr, reply, nil
}

// promoteAdditionalTools moves the tools of each additional_tools input item (how a Responses Lite
// client, the Codex app, declares tools) to the end of the tools list and removes the item, because
// xAI does not take that item (promoteXAIAdditionalToolsDoc in today's core). It runs before the
// rules for tools, so a promoted tool is flattened, folded, counted and cut like a top-level one.
// A function or custom tool whose name an earlier tool has is dropped: the top-level list wins
// over the items, and the first item over the next. Namespaces are all kept; rewriteNamespaces
// merges those of one name and keeps the first of each child. Hosted tools have no name and all
// stay, as in today's core.
func promoteAdditionalTools(m map[string]any) {
	input, ok := m["input"].([]any)
	if !ok {
		return
	}
	var kept, promoted []any
	for _, v := range input {
		if item, ok := v.(map[string]any); ok && item["type"] == "additional_tools" {
			list, _ := item["tools"].([]any)
			promoted = append(promoted, list...)
			continue
		}
		kept = append(kept, v)
	}
	if len(kept) == len(input) {
		return
	}
	if kept == nil {
		kept = []any{}
	}
	m["input"] = kept
	tools, _ := m["tools"].([]any)
	seen := map[string]bool{}
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok && isTool(tm) {
			seen[strings.TrimSpace(str(tm, "name"))] = true
		}
	}
	for _, t := range promoted {
		if tm, ok := t.(map[string]any); ok && isTool(tm) {
			if name := strings.TrimSpace(str(tm, "name")); name != "" {
				if seen[name] {
					continue
				}
				seen[name] = true
			}
		}
		tools = append(tools, t)
	}
	if len(tools) > 0 {
		m["tools"] = tools
	}
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

// rewriteTools sends the custom tools that are left as function tools, gives each function a
// parameters schema xAI accepts. The caller caps the list after optional search injection. The
// custom tools of the client have been made functions with an input argument by then
// (rewriteCustomTools); one that is left is a custom tool without a name.
func rewriteTools(tools []any) []any {
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if tm["type"] == "custom" {
			// Today's core sends a custom tool as a function and keeps its other fields (normalizeXAITool).
			// A custom tool without parameters takes the empty object schema below, like any
			// function without one.
			tm["type"] = "function"
		}
		if tm["type"] != "function" {
			continue
		}
		simplifyAutomation(tm, "", str(tm, "name"))
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
// is dropped, as today's core drops it. A call to apply_patch, or to a custom tool the request
// declares (customs), carries its text in the input argument (patch.go), whatever the text looks
// like, and must have text for its input; one in a namespace keeps its namespace when the request
// has namespace tools (namespaceTools is true), which the rules for namespaces then write the way
// the tools were sent. A call to any other tool keeps the rule it had.
func rewriteCustomToolItems(input []any, namespaceTools bool, customs customTools) ([]any, error) {
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
			arguments, namespace := "", ""
			if name == applyPatchName || customs.names(str(item, "namespace"), name) {
				if namespaceTools {
					namespace = strings.TrimSpace(str(item, "namespace"))
				}
				text, ok := item["input"].(string)
				if !ok {
					return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: name + " history input must be a string", Scope: llm.ScopeRequest}
				}
				arguments = wrapPatchInput(text)
			} else {
				arguments = customCallArguments(item["input"])
			}
			call := map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": arguments}
			if namespace != "" {
				call["namespace"] = namespace
			}
			out = append(out, call)
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
