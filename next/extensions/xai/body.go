package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

const maxTools = 200

var removedFields = []string{"stop", "previous_response_id", "prompt_cache_retention", "safety_identifier", "stream_options"}

func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
	body, tr, _, err := buildRequest(req, false)
	return body, tr, err
}

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
			return nil, nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: tr.DroppedPaths(),
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
		injected := 0
		if injectXSearch && !requestHasSearch(m) {
			injected = 1
		}
		tools, namespaces = rewriteNamespaces(tools, injected)
		if namespaces != nil {
			reply.ns = newNSRestorer(namespaces.refs)
			namespaces.rewriteChoice(m["tool_choice"])
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

func withEncryptedReasoning(v any) []any {
	list, _ := v.([]any)
	for _, x := range list {
		if x == "reasoning.encrypted_content" {
			return list
		}
	}
	return append(list, "reasoning.encrypted_content")
}

func rewriteTools(tools []any) []any {
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if tm["type"] == "custom" {
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
