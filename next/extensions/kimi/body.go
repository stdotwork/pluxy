package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

var codingModels = map[string]string{
	"kimi-k2.8": "kimi-for-coding", "k2.8": "kimi-for-coding",
	"kimi-k2.8-code": "kimi-for-coding", "k2.8-code": "kimi-for-coding",
	"kimi-k2.8-preview": "kimi-for-coding", "k2.8-preview": "kimi-for-coding",
	"kimi-k2.7-code": "kimi-for-coding", "k2.7-code": "kimi-for-coding",
	"kimi-for-coding": "kimi-for-coding", "for-coding": "kimi-for-coding",
	"kimi-k2.7-code-highspeed": "kimi-for-coding-highspeed", "k2.7-code-highspeed": "kimi-for-coding-highspeed",
	"kimi-for-coding-highspeed": "kimi-for-coding-highspeed", "for-coding-highspeed": "kimi-for-coding-highspeed",
}

func upstreamModel(model string) string {
	m := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(model), "[1m]"))
	if alias, ok := codingModels[strings.ToLower(m)]; ok {
		return alias
	}
	return strings.TrimPrefix(m, "kimi-")
}

func thinks(model string) bool { return upstreamModel(model) != "k2" }

func effortOf(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "xhigh", "max":
		return "max"
	}
	return "high"
}

func passesThrough(req llm.Request, up upstream) bool {
	n := req.Native
	return n != nil && n.Format == up.format && n.Body != nil
}

func encodeChat(req llm.Request) ([]byte, []llm.Loss, error) {
	b, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		return nil, nil, err
	}
	tr := &llm.Translation{Losses: losses}
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: tr.DroppedPaths(),
			Message: "Chat Completions cannot carry parts of this request"}
	}
	return b, losses, nil
}

func buildBody(req llm.Request, up upstream, turn *replayTurn) ([]byte, *llm.Translation, error) {
	tr := &llm.Translation{To: up.format}
	if n := req.Native; n != nil {
		tr.From = n.Format
	}
	var body []byte
	if passesThrough(req, up) {
		tr.Passthrough = true
		body = req.Native.Body
	} else {
		b, losses, err := encodeChat(req)
		if err != nil {
			return nil, nil, err
		}
		body, tr.Losses = b, losses
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
		turn.restore(m)
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
			out = append(out, input[j:k]...)
		} else {
			out = append(out, outputs...)
			out = append(out, between...)
		}
		i = k
	}
	return out
}

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
