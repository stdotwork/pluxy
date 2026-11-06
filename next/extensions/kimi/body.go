package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// What Kimi needs changed in a request, as today's core sends it (each rule names where Kimi's
// API differs from the format it copies):
//
//   - model names: Kimi Code serves its K2.7 and K2.8 coding models as kimi-for-coding and
//     kimi-for-coding-highspeed, and others without the kimi- prefix (kimi-k3 is k3);
//   - temperature: only 1.0 with thinking on and 0.6 with it off; any other value is removed;
//   - tool schemas: local $ref references are inlined and $defs removed, and a schema without a
//     type gets type object;
//   - Chat Completions: usage is asked for on a stream; an assistant tool call carries
//     reasoning_content (the latest reasoning, else its own text); empty assistant messages are
//     dropped; the reasoning effort becomes thinking {type, effort};
//   - Responses: a batch of function call outputs must follow its calls with nothing between,
//     so items between them move after the outputs;
//   - Messages: Claude Code's billing block is removed from the system prompt, since Kimi reads
//     it as prompt text.

// codingModels are Kimi Code's model aliases.
var codingModels = map[string]string{
	"kimi-k2.8": "kimi-for-coding", "k2.8": "kimi-for-coding",
	"kimi-k2.8-code": "kimi-for-coding", "k2.8-code": "kimi-for-coding",
	"kimi-k2.8-preview": "kimi-for-coding", "k2.8-preview": "kimi-for-coding",
	"kimi-k2.7-code": "kimi-for-coding", "k2.7-code": "kimi-for-coding",
	"kimi-for-coding": "kimi-for-coding", "for-coding": "kimi-for-coding",
	"kimi-k2.7-code-highspeed": "kimi-for-coding-highspeed", "k2.7-code-highspeed": "kimi-for-coding-highspeed",
	"kimi-for-coding-highspeed": "kimi-for-coding-highspeed", "for-coding-highspeed": "kimi-for-coding-highspeed",
}

// upstreamModel returns the name Kimi serves a model under.
func upstreamModel(model string) string {
	m := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(model), "[1m]"))
	if alias, ok := codingModels[strings.ToLower(m)]; ok {
		return alias
	}
	return strings.TrimPrefix(m, "kimi-")
}

// thinks reports whether a Kimi model takes thinking settings. kimi-k2 is the one that does not.
//
// shortcut: decided from the model name, since protocol v0 has no model capabilities.
func thinks(model string) bool { return upstreamModel(model) != "k2" }

// effortOf maps a reasoning effort to Kimi's thinking effort (low, high or max).
func effortOf(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "xhigh", "max":
		return "max"
	}
	return "high"
}

// buildBody returns the request body for up and how it was built.
func buildBody(req llm.Request, up upstream) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: up.format}
	var body []byte
	if n := req.Native; n != nil {
		tr.From = n.Format
		if n.Format == up.format {
			tr.Passthrough = true
			body = n.Body
		}
	}
	if body == nil {
		// Only Chat Completions is rebuilt: Responses and Messages clients get their own API.
		b, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			return nil, nil, err
		}
		body, tr.Losses = b, losses
		if req.DropPolicy == llm.DropReject && tr.Dropped() {
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
				Message: "Chat Completions cannot carry parts of this request"}
		}
	}
	m, err := decodeObject(body)
	if err != nil {
		return nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	m["model"] = upstreamModel(req.Model)
	switch up {
	case upChat:
		patchChat(m, req)
	case upResponses:
		patchResponses(m, req)
	case upMessages:
		stripBillingBlock(m)
	}
	out, err := marshal(m)
	if err != nil {
		return nil, nil, err
	}
	return out, tr, nil
}

func patchChat(m map[string]any, req llm.Request) {
	if req.Stream {
		so, _ := m["stream_options"].(map[string]any)
		if so == nil {
			so = map[string]any{}
		}
		so["include_usage"] = true
		m["stream_options"] = so
	}
	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			if f, ok := t.(map[string]any)["function"].(map[string]any); ok {
				normalizeSchemaField(f, "parameters")
			}
		}
	}
	if effort := req.Options.ReasoningEffort; effort != "" && thinks(req.Model) {
		delete(m, "reasoning_effort")
		if effort == "none" {
			m["thinking"] = map[string]any{"type": "disabled"}
		} else {
			m["thinking"] = map[string]any{"type": "enabled", "effort": effortOf(effort)}
		}
	}
	keepTemperature(m)
	if msgs, ok := m["messages"].([]any); ok {
		m["messages"] = fixToolTurns(msgs)
	}
}

func patchResponses(m map[string]any, req llm.Request) {
	m["stream"] = req.Stream
	if tools, ok := m["tools"].([]any); ok {
		for _, t := range tools {
			if tm, ok := t.(map[string]any); ok {
				normalizeSchemaField(tm, "parameters")
			}
		}
	}
	keepTemperature(m)
	if input, ok := m["input"].([]any); ok {
		m["input"] = contiguousOutputs(input)
	}
}

// keepTemperature removes a temperature Kimi does not take: it takes 0.6 with thinking off and
// 1.0 otherwise.
func keepTemperature(m map[string]any) {
	t, ok := m["temperature"].(json.Number)
	if !ok {
		return
	}
	v, _ := t.Float64()
	off := false
	if th, ok := m["thinking"].(map[string]any); ok {
		off = th["type"] == "disabled"
	}
	if (off && v != 0.6) || (!off && v != 1.0) {
		delete(m, "temperature")
	}
}

// fixToolTurns drops empty assistant messages and gives each assistant tool call the
// reasoning_content Kimi requires with it: the latest reasoning before it, or else its own text.
func fixToolTurns(msgs []any) []any {
	out := msgs[:0]
	latest := ""
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || msg["role"] != "assistant" {
			out = append(out, raw)
			continue
		}
		reasoning, _ := msg["reasoning_content"].(string)
		calls, _ := msg["tool_calls"].([]any)
		text := contentText(msg["content"])
		if reasoning == "" && len(calls) == 0 && text == "" && msg["function_call"] == nil {
			continue
		}
		if reasoning != "" {
			latest = reasoning
		} else if len(calls) > 0 {
			switch {
			case latest != "":
				msg["reasoning_content"] = latest
			case text != "":
				msg["reasoning_content"] = text
			default:
				msg["reasoning_content"] = "[reasoning unavailable]"
			}
		}
		out = append(out, msg)
	}
	return out
}

// contentText returns a Chat message's text content.
func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, p := range v {
			if pm, ok := p.(map[string]any); ok && pm["type"] == "text" {
				s, _ := pm["text"].(string)
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// contiguousOutputs moves items that sit between a batch of function calls' outputs to after the
// last of them, so the outputs follow their calls with nothing between. A batch whose outputs are
// not all there is left as it is.
func contiguousOutputs(input []any) []any {
	typeOf := func(it any) string {
		m, _ := it.(map[string]any)
		s, _ := m["type"].(string)
		return s
	}
	callID := func(it any) string {
		m, _ := it.(map[string]any)
		s, _ := m["call_id"].(string)
		return s
	}
	var out []any
	for i := 0; i < len(input); {
		if typeOf(input[i]) != "function_call" {
			out = append(out, input[i])
			i++
			continue
		}
		pending := map[string]bool{}
		j := i
		for ; j < len(input) && typeOf(input[j]) == "function_call"; j++ {
			out = append(out, input[j])
			pending[callID(input[j])] = true
		}
		// Collect the outputs of this batch until all have been seen.
		var outputs, between []any
		k := j
		for ; k < len(input) && len(pending) > 0; k++ {
			if typeOf(input[k]) == "function_call_output" && pending[callID(input[k])] {
				delete(pending, callID(input[k]))
				outputs = append(outputs, input[k])
				continue
			}
			if typeOf(input[k]) == "function_call" {
				break
			}
			between = append(between, input[k])
		}
		if len(pending) > 0 {
			// Not every output is there: keep the order the client gave.
			out = append(out, input[j:k]...)
		} else {
			out = append(out, outputs...)
			out = append(out, between...)
		}
		i = k
	}
	return out
}

// stripBillingBlock removes Claude Code's billing block (a system text block starting with
// x-anthropic-billing-header:) from a Messages body.
func stripBillingBlock(m map[string]any) {
	sys, ok := m["system"].([]any)
	if !ok {
		return
	}
	kept := sys[:0]
	for _, b := range sys {
		bm, _ := b.(map[string]any)
		if text, _ := bm["text"].(string); bm["type"] == "text" && strings.HasPrefix(strings.TrimSpace(text), "x-anthropic-billing-header:") {
			continue
		}
		kept = append(kept, b)
	}
	if len(kept) == 0 {
		delete(m, "system")
		return
	}
	m["system"] = kept
}

// normalizeSchemaField makes the JSON schema at holder[key] one Kimi takes.
func normalizeSchemaField(holder map[string]any, key string) {
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
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
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
