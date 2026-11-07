package main

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// What Meta's Responses API needs changed in a request, as today's core makes the changes
// (internal/runtime/executor/meta_executor_execute.go, prepareResponsesRequest):
//
//   - stream is always true: Muse Code streams every request, and a client that wants one reply
//     gets the stream gathered;
//   - removed fields: generate, prompt_cache_retention, safety_identifier, stream_options and
//     client_metadata;
//   - store false unless the client set store, and reasoning.encrypted_content in include;
//   - a web_search tool loses search_content_types, which Meta refuses;
//   - a request with tools also carries Muse Code's own tool namespace, which Meta's API wants
//     on a subscription key's tool requests.
//
// Not done here: today's core also rewrites apply_patch tools and calls (its Codex Desktop
// support), drops encrypted reasoning content Meta did not issue, and sets the defaults its Codex
// translator adds to every Responses request (empty instructions, a list input, a medium effort,
// tool_choice, parallel_tool_calls and strict false); the extension sends those parts as the
// client wrote them, as the xAI extension does.

// removedFields are the fields today's core deletes from a body it sends to Meta.
var removedFields = []string{"generate", "prompt_cache_retention", "safety_identifier", "stream_options", "client_metadata"}

// museNamespace is Muse Code's tool namespace, as Muse Code 1.4.14 sends it.
const museNamespace = `{"type":"namespace","name":"muse","description":"Muse Code tool set.","tools":[{"type":"function","name":"submit_reminder_decision","description":"Submit this reminder child's explicit decision.","parameters":{"additionalProperties":false,"properties":{"advisory_text":{"description":"Short text to deliver to the main agent when reminding.","type":["null","string"]},"confidence":{"description":"Confidence in the decision.","enum":["low","medium","high"],"type":["null","string"]},"decision":{"enum":["remind","none"],"type":"string"},"priority":{"description":"Requested reminder priority.","enum":["low","normal","high"],"type":["null","string"]},"reason":{"description":"Brief reason for reminding or staying silent.","type":"string"},"skill_id":{"description":"The listed skill id to remind when needs_reminder is true.","type":["null","string"]},"visible_for_steps":{"description":"Requested ephemeral visibility length in main model steps.","maximum":8,"minimum":1,"type":["null","integer"]}},"required":["advisory_text","confidence","decision","priority","reason","skill_id","visible_for_steps"],"type":"object"},"strict":true}]}`

// buildBody returns the request body for Meta's Responses API and how it was built.
func buildBody(req llm.Request) ([]byte, *llm.Translation, error) {
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
			return nil, nil, err
		}
		body, tr.Losses = b, losses
		if req.DropPolicy == llm.DropReject && tr.Dropped() {
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
				Message: "Responses cannot carry parts of this request"}
		}
	}
	m, err := decodeObject(body)
	if err != nil {
		return nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = req.Model
	m["stream"] = true
	for _, k := range removedFields {
		delete(m, k)
	}
	// Meta keeps each response unless told not to, and nothing here sends previous_response_id,
	// so a kept response is never used again: it is not kept unless the client asked. Without
	// stored responses a reasoning model's thinking reaches the next turn only as encrypted
	// reasoning content, so that is asked for, as today's core asks.
	if _, ok := m["store"]; !ok {
		m["store"] = false
	}
	m["include"] = withEncryptedReasoning(m["include"])
	if tools, ok := m["tools"].([]any); ok && len(tools) > 0 {
		m["tools"] = museTools(tools)
	}
	out, err := marshal(m)
	if err != nil {
		return nil, nil, err
	}
	return out, tr, nil
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

// museTools drops search_content_types from web_search tools (in namespaces too) and adds Muse
// Code's namespace when the request does not carry it.
func museTools(tools []any) []any {
	has := false
	for _, t := range tools {
		tool, _ := t.(map[string]any)
		if tool == nil {
			continue
		}
		if tool["type"] == "namespace" {
			if tool["name"] == "muse" {
				has = true
			}
			if sub, ok := tool["tools"].([]any); ok {
				for _, s := range sub {
					if st, _ := s.(map[string]any); st != nil && st["type"] == "web_search" {
						delete(st, "search_content_types")
					}
				}
			}
		}
		if tool["type"] == "web_search" {
			delete(tool, "search_content_types")
		}
	}
	if !has {
		var ns any
		_ = json.Unmarshal([]byte(museNamespace), &ns)
		tools = append(tools, ns)
	}
	return tools
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
