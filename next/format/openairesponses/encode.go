package openairesponses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// encoder builds one Responses request and records what it could not carry over.
type encoder struct {
	losses []llm.Loss
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = append(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// kept returns o's kept fields when o came from Responses, so they are written back; for another
// format it reports them as dropped.
func (e *encoder) kept(o *llm.Origin) json.RawMessage {
	if o == nil {
		return nil
	}
	if o.Format == format {
		return o.Raw
	}
	e.losses = append(e.losses, rawjson.Losses(o, format)...)
	return nil
}

func pathOf(o *llm.Origin, fallback string) string {
	if o != nil && o.Path != "" {
		return o.Path
	}
	return fallback
}

func marshal(v any, raw json.RawMessage) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return rawjson.Merge(b, raw)
}

type outRequest struct {
	Model             string            `json:"model"`
	Input             []json.RawMessage `json:"input"`
	Stream            bool              `json:"stream"`
	MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Text              any               `json:"text,omitempty"`
	Reasoning         any               `json:"reasoning,omitempty"`
}

type outFunctionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// EncodeRequest builds a Responses request body from req's items, tools and options, and returns
// what it could not carry over. Every item keeps its place in input; system and developer
// messages stay messages with their role. Fields kept from a Responses source (Origin) are written
// back. Reasoning from another provider cannot be replayed to a Responses upstream, so it is
// dropped and reported, except a Messages thinking block that carries a Responses upstream's
// encrypted reasoning in its signature, which goes back as that reasoning item. A user turn left with nothing to send after its unsupported parts are
// dropped is an invalid_request error, never sent empty. It does not look at req.Native; callers
// that can pass the native body through do so first.
func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	o := req.Options
	out := outRequest{
		Model:             req.Model,
		Input:             []json.RawMessage{},
		Stream:            req.Stream,
		MaxOutputTokens:   o.MaxOutputTokens,
		Temperature:       o.Temperature,
		TopP:              o.TopP,
		ParallelToolCalls: o.ParallelToolCalls,
	}
	if len(o.Stop) > 0 {
		e.lose(llm.LossDropped, "stop", "Responses has no stop sequences")
	}
	if o.ReasoningEffort != "" {
		out.Reasoning = map[string]string{"effort": o.ReasoningEffort}
	}
	if rf := o.ResponseFormat; rf != nil {
		f := map[string]any{"type": rf.Type}
		if rf.Type == llm.ResponseFormatJSONSchema {
			f["name"] = rf.Name
			if rf.Description != "" {
				f["description"] = rf.Description
			}
			if len(rf.Schema) > 0 {
				f["schema"] = rf.Schema
			}
			if rf.Strict != nil {
				f["strict"] = *rf.Strict
			}
		}
		out.Text = map[string]any{"format": f}
	}
	if tc := o.ToolChoice; tc != nil {
		if tc.Mode == llm.ToolChoiceTool {
			out.ToolChoice = map[string]string{"type": "function", "name": tc.Name}
		} else {
			out.ToolChoice = tc.Mode
		}
	}
	for i, t := range req.Tools {
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				out.Tools = append(out.Tools, t.Origin.Raw)
			} else {
				e.lose(llm.LossDropped, pathOf(t.Origin, fmt.Sprintf("tools[%d]", i)), "Responses has no such tool")
			}
			continue
		}
		out.Tools = append(out.Tools, marshal(outFunctionTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters, Strict: t.Strict}, e.kept(t.Origin)))
	}

	for i, it := range req.Items {
		p := pathOf(it.Origin, fmt.Sprintf("items[%d]", i))
		switch it.Type {
		case llm.ItemTypeMessage:
			if len(it.Content) == 0 && !it.TextContent && it.Origin != nil && it.Origin.Format != format && len(it.Origin.Raw) > 0 {
				// An empty message from another format only holds its own fields; they are
				// reported, and nothing is sent for it.
				e.kept(it.Origin)
				break
			}
			content, sendable, dropped, err := e.content(it, p)
			if err != nil {
				return nil, nil, err
			}
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Responses", p)
			}
			m := map[string]any{"type": "message", "role": it.Role, "content": content}
			if it.ID != "" {
				m["id"] = it.ID
			}
			out.Input = append(out.Input, marshal(m, e.kept(it.Origin)))
		case llm.ItemTypeToolCall:
			if it.Custom {
				m := map[string]any{"type": "custom_tool_call", "call_id": it.CallID, "name": it.Name, "input": it.Arguments}
				out.Input = append(out.Input, marshal(m, e.kept(it.Origin)))
				break
			}
			m := map[string]any{"type": "function_call", "call_id": it.CallID, "name": it.Name, "arguments": it.Arguments}
			if it.ID != "" {
				m["id"] = it.ID
			}
			out.Input = append(out.Input, marshal(m, e.kept(it.Origin)))
		case llm.ItemTypeToolResult:
			m := map[string]any{"type": "function_call_output", "call_id": it.CallID, "output": it.Output}
			out.Input = append(out.Input, marshal(m, e.kept(it.Origin)))
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format == format {
				m := map[string]any{"type": "reasoning", "summary": []any{}}
				if it.Text != "" {
					m["summary"] = []any{map[string]string{"type": "summary_text", "text": it.Text}}
				}
				if it.ID != "" {
					m["id"] = it.ID
				}
				out.Input = append(out.Input, marshal(m, it.Origin.Raw))
				break
			}
			if sig, rest := carriedReasoning(it.Origin); sig != "" {
				// A Messages thinking block whose signature is a Responses upstream's encrypted
				// reasoning (see IsEncryptedReasoning) goes back as that reasoning item. Its
				// thinking text was the upstream's summary; the encrypted content is what the
				// upstream reads, so the summary is left empty, as today's core sends it.
				m := map[string]any{"type": "reasoning", "summary": []any{}, "content": nil, "encrypted_content": sig}
				out.Input = append(out.Input, marshal(m, nil))
				e.losses = append(e.losses, rawjson.Losses(&llm.Origin{Format: it.Origin.Format, Path: it.Origin.Path, Raw: rest}, format)...)
				if it.Text != "" {
					e.lose(llm.LossApproximated, p+".thinking", "the reasoning's encrypted content is sent; its summary text is not")
				}
				break
			}
			e.lose(llm.LossDropped, p, "Responses cannot replay reasoning from another provider")
		case llm.ItemTypeNative:
			if it.Origin != nil && it.Origin.Format == format {
				out.Input = append(out.Input, it.Origin.Raw)
				break
			}
			e.lose(llm.LossDropped, p, "Responses has no such item")
		default:
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported item type %q", p, it.Type), Scope: llm.ScopeRequest}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return rawjson.Merge(b, e.kept(req.Origin)), e.losses, nil
}

// content encodes a message's parts: input parts for user, system and developer messages,
// output_text for assistant messages. sendable reports whether any part with something in it was
// kept, dropped whether any part was dropped and reported.
func (e *encoder) content(it llm.Item, msgPath string) (content []json.RawMessage, sendable, dropped bool, err error) {
	content = []json.RawMessage{}
	textType := "input_text"
	if it.Role == llm.RoleAssistant {
		textType = "output_text"
	}
	for i, p := range it.Content {
		pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		switch {
		case p.Type == llm.PartText:
			content = append(content, marshal(map[string]string{"type": textType, "text": p.Text}, e.kept(p.Origin)))
			if strings.TrimSpace(p.Text) != "" {
				sendable = true
			}
		case p.Type == llm.PartImage && it.Role != llm.RoleAssistant:
			m := map[string]string{"type": "input_image"}
			if p.URL != "" {
				m["image_url"] = p.URL
			}
			if p.FileID != "" {
				m["file_id"] = p.FileID
			}
			// Responses requires detail on an input image.
			m["detail"] = "auto"
			if p.Detail != "" {
				m["detail"] = p.Detail
			}
			content = append(content, marshal(m, e.kept(p.Origin)))
			sendable = true
		case p.Type == llm.PartFile && it.Role != llm.RoleAssistant:
			m := map[string]string{"type": "input_file"}
			for k, v := range map[string]string{"file_id": p.FileID, "file_data": p.FileData, "file_url": p.URL, "filename": p.Filename} {
				if v != "" {
					m[k] = v
				}
			}
			content = append(content, marshal(m, e.kept(p.Origin)))
			sendable = true
		case p.Type == llm.PartNative && p.Format == format:
			content = append(content, p.Data)
			sendable = true
		default:
			e.lose(llm.LossDropped, pp, "a "+partFormat(p)+" content part Responses cannot carry here")
			dropped = true
		}
	}
	return content, sendable, dropped, nil
}

func partFormat(p llm.Part) string {
	if p.Type == llm.PartNative {
		return p.Format
	}
	return p.Type
}

// carriedReasoning returns the encrypted reasoning a Messages thinking block carries in its
// signature, and the block's other kept fields, when the signature has the shape of a Responses
// upstream's encrypted reasoning; otherwise "".
func carriedReasoning(o *llm.Origin) (string, json.RawMessage) {
	if o == nil || o.Format != llm.FormatAnthropicMessages {
		return "", nil
	}
	m := rawjson.Object(o.Raw)
	var sig string
	if json.Unmarshal(m["signature"], &sig) != nil || !IsEncryptedReasoning(sig) {
		return "", nil
	}
	return sig, rawjson.Rest(m, "signature")
}
