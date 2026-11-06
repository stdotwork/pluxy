package geminigenerate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// SkipSignature is the thought signature Gemini accepts on a function call it did not issue:
// Google's thought-signature guide names this value for calls carried over from another model
// or made up by the client, which have no signature of their own. Gemini 3 refuses a function
// call in the current turn that has no signature.
const SkipSignature = "skip_thought_signature_validator"

// safetyOff are the safety settings a translated request carries: every filter off, as today's
// core sends with every request it translates to Gemini. Clients in other formats have no way to
// name Gemini's safety settings, and their own providers do not filter this way.
var safetyOff = []map[string]string{
	{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
	{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
	{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
	{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
}

type encoder struct {
	losses []llm.Loss
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = llm.AddLoss(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// kept returns o's kept fields when o came from GenerateContent, so they are written back; for
// another format it reports them as dropped.
func (e *encoder) kept(o *llm.Origin) json.RawMessage {
	if o == nil {
		return nil
	}
	if o.Format == format {
		return o.Raw
	}
	for _, l := range rawjson.Losses(o, format) {
		e.losses = llm.AddLoss(e.losses, l)
	}
	return nil
}

func pathOf(o *llm.Origin, fallback string) string {
	if o != nil && o.Path != "" {
		return o.Path
	}
	return fallback
}

func marshalWith(v any, raw json.RawMessage) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return rawjson.Merge(b, raw)
}

type outContent struct {
	role  string
	parts []json.RawMessage
}

// EncodeRequest builds a GenerateContent request body from req's items, tools and options, and
// returns what it could not carry over. System and developer messages become the system
// instruction; consecutive items of one role become one content (assistant items are the model's);
// tool calls become functionCall parts and their results functionResponse parts named after the
// call. A function call Gemini did not issue carries SkipSignature. Reasoning from another
// provider cannot be replayed (Gemini checks thought signatures), so it is dropped and reported,
// as is the reasoning effort (thinking settings depend on the model; the Gemini provider sets
// them). It does not look at req.Native; callers that can pass the native body through do so
// first.
func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	o := req.Options
	out := map[string]any{"model": req.Model, "safetySettings": safetyOff}

	gen := map[string]any{}
	if o.MaxOutputTokens != nil {
		gen["maxOutputTokens"] = *o.MaxOutputTokens
	}
	if o.Temperature != nil {
		gen["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		gen["topP"] = *o.TopP
	}
	if len(o.Stop) > 0 {
		gen["stopSequences"] = o.Stop
	}
	if rf := o.ResponseFormat; rf != nil {
		switch rf.Type {
		case llm.ResponseFormatJSONObject:
			gen["responseMimeType"] = "application/json"
		case llm.ResponseFormatJSONSchema:
			gen["responseMimeType"] = "application/json"
			if len(rf.Schema) > 0 {
				gen["responseJsonSchema"] = rf.Schema
			}
		}
	}
	if o.ReasoningEffort != "" {
		e.lose(llm.LossDropped, "reasoning_effort", "GenerateContent takes thinking settings, which depend on the model")
	}

	var decls []json.RawMessage
	var tools []json.RawMessage
	for i, t := range req.Tools {
		p := pathOf(t.Origin, fmt.Sprintf("tools[%d]", i))
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
			} else {
				e.lose(llm.LossDropped, p, "GenerateContent has no such tool")
			}
			continue
		}
		fd := map[string]any{"name": t.Name}
		if t.Description != "" {
			fd["description"] = t.Description
		}
		if len(t.Parameters) > 0 {
			fd["parametersJsonSchema"] = t.Parameters
		}
		if t.Strict != nil && *t.Strict {
			e.lose(llm.LossDropped, rawjson.Join(p, "strict"), "GenerateContent has no strict function arguments")
		}
		decls = append(decls, marshalWith(fd, e.kept(t.Origin)))
	}
	if len(decls) > 0 {
		tools = append([]json.RawMessage{marshalWith(map[string]any{"functionDeclarations": decls}, nil)}, tools...)
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if fc := e.functionCalling(o.ToolChoice, o.ParallelToolCalls); fc != nil {
			out["toolConfig"] = map[string]any{"functionCallingConfig": fc}
		}
	}

	var system []json.RawMessage
	var contents []*outContent
	// pending is a thought signature replayed from another format, waiting for the model part it
	// belongs on: the next one in the same turn, or an empty text part of its own at the turn's
	// end.
	var pending string
	put := func(role string, part json.RawMessage) {
		if n := len(contents); n > 0 && contents[n-1].role == role {
			contents[n-1].parts = append(contents[n-1].parts, part)
			return
		}
		contents = append(contents, &outContent{role: role, parts: []json.RawMessage{part}})
	}
	flush := func() {
		if pending != "" {
			put("model", marshalWith(map[string]any{"text": "", "thoughtSignature": pending}, nil))
			pending = ""
		}
	}
	add := func(role string, part json.RawMessage) {
		if role != "model" {
			flush()
		} else if pending != "" {
			part = withSignature(part, pending)
			pending = ""
		}
		put(role, part)
	}
	names := map[string]string{} // call ID to function name
	for i, it := range req.Items {
		p := pathOf(it.Origin, fmt.Sprintf("items[%d]", i))
		switch it.Type {
		case llm.ItemTypeMessage:
			e.kept(it.Origin)
			parts, sendable, dropped := e.parts(it, p)
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to GenerateContent", p)
			}
			switch it.Role {
			case llm.RoleSystem, llm.RoleDeveloper:
				system = append(system, parts...)
			case llm.RoleAssistant:
				for _, part := range parts {
					add("model", part)
				}
			default:
				for _, part := range parts {
					add("user", part)
				}
			}
		case llm.ItemTypeToolCall:
			if it.Custom {
				e.lose(llm.LossDropped, p, "GenerateContent has no custom tool calls")
				break
			}
			names[it.CallID] = it.Name
			args := json.RawMessage(it.Arguments)
			var m map[string]json.RawMessage
			if json.Unmarshal(args, &m) != nil || m == nil {
				if strings.TrimSpace(it.Arguments) != "" {
					e.lose(llm.LossApproximated, p, "tool arguments that are not a JSON object, sent as {}")
				}
				args = json.RawMessage("{}")
			}
			call := map[string]any{"name": it.Name, "args": args}
			if it.CallID != "" {
				call["id"] = it.CallID
			}
			part := map[string]any{"functionCall": call}
			raw := e.kept(it.Origin)
			if (it.Origin == nil || it.Origin.Format != format) && pending == "" {
				part["thoughtSignature"] = SkipSignature
			}
			add("model", marshalWith(part, raw))
		case llm.ItemTypeToolResult:
			name, ok := names[it.CallID]
			if !ok {
				e.lose(llm.LossApproximated, p, "a tool result with no call before it, named after its call ID")
				name = it.CallID
			}
			resp := map[string]any{"name": name, "response": map[string]any{"result": it.Output}}
			if it.CallID != "" {
				resp["id"] = it.CallID
			}
			add("user", marshalWith(map[string]any{"functionResponse": resp}, e.kept(it.Origin)))
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format == format {
				add("model", marshalWith(map[string]any{"text": it.Text, "thought": true}, it.Origin.Raw))
				break
			}
			if sig := replayedSignature(it); sig != "" {
				// Gemini's own signature, carried by the client in its format's opaque field:
				// it goes back on the part that follows. The thought text is not replayed;
				// Gemini needs only the signature.
				flush()
				pending = sig
				break
			}
			e.lose(llm.LossDropped, p, "GenerateContent cannot replay reasoning from another provider")
		case llm.ItemTypeNative:
			if it.Origin != nil && it.Origin.Format == format {
				add("model", it.Origin.Raw)
				break
			}
			e.lose(llm.LossDropped, p, "GenerateContent has no such item")
		default:
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported item type %q", p, it.Type), Scope: llm.ScopeRequest}
		}
	}
	flush()
	if len(system) > 0 {
		out["systemInstruction"] = map[string]any{"parts": system}
	}
	cs := make([]any, 0, len(contents))
	for _, c := range contents {
		cs = append(cs, map[string]any{"role": c.role, "parts": c.parts})
	}
	out["contents"] = cs
	if len(gen) > 0 {
		out["generationConfig"] = gen
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return rawjson.Merge(b, e.kept(req.Origin)), e.losses, nil
}

// functionCalling returns the functionCallingConfig for a tool choice. Gemini cannot limit a reply
// to one call, so parallel_tool_calls false turns function calling off (mode NONE), as today's
// core does rather than risk several calls the client said it cannot take; it is reported.
func (e *encoder) functionCalling(tc *llm.ToolChoice, parallel *bool) map[string]any {
	if parallel != nil && !*parallel {
		e.lose(llm.LossApproximated, "parallel_tool_calls", "GenerateContent cannot limit a reply to one function call; function calling is turned off")
		return map[string]any{"mode": "NONE"}
	}
	if tc == nil {
		return nil
	}
	switch tc.Mode {
	case llm.ToolChoiceAuto:
		return map[string]any{"mode": "AUTO"}
	case llm.ToolChoiceNone:
		return map[string]any{"mode": "NONE"}
	case llm.ToolChoiceRequired:
		return map[string]any{"mode": "ANY"}
	case llm.ToolChoiceTool:
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{tc.Name}}
	}
	return nil
}

// parts encodes a message's content as GenerateContent parts. sendable reports whether any part
// with something in it was kept, dropped whether any part was dropped and reported.
func (e *encoder) parts(it llm.Item, msgPath string) (parts []json.RawMessage, sendable, dropped bool) {
	for i, p := range it.Content {
		pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		var part map[string]any
		switch p.Type {
		case llm.PartText:
			if p.Text == "" {
				e.kept(p.Origin)
				continue
			}
			part = map[string]any{"text": p.Text}
		case llm.PartImage, llm.PartFile:
			part = media(p)
			if part == nil {
				e.lose(llm.LossDropped, pp, "GenerateContent cannot take this "+p.Type+": only inline data (a base64 data URL) is sent")
				dropped = true
				continue
			}
		case llm.PartNative:
			if p.Format == format {
				parts = append(parts, p.Data)
				sendable = true
				continue
			}
			e.lose(llm.LossDropped, pp, "a "+p.Format+" content part GenerateContent cannot carry")
			dropped = true
			continue
		default:
			e.lose(llm.LossDropped, pp, "a "+p.Type+" content part GenerateContent cannot carry")
			dropped = true
			continue
		}
		parts = append(parts, marshalWith(part, e.kept(p.Origin)))
		sendable = true
	}
	return parts, sendable, dropped
}

// media returns the inlineData part for an image or file given as a base64 data URL, or the
// fileData part for one read from GenerateContent with its file URI, or nil.
func media(p llm.Part) map[string]any {
	url := p.URL
	if p.Type == llm.PartFile && p.FileData != "" {
		url = p.FileData
	}
	if strings.HasPrefix(url, "data:") {
		meta, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
		if !ok || !isBase64 || mediaType == "" {
			return nil
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": data}}
	}
	if url != "" && p.Origin != nil && p.Origin.Format == format {
		// The MIME type is in the kept fields.
		return map[string]any{"fileData": map[string]any{"fileUri": url}}
	}
	return nil
}

// withSignature puts sig on part as its thoughtSignature, replacing SkipSignature.
func withSignature(part json.RawMessage, sig string) json.RawMessage {
	m := rawjson.Object(part)
	if m == nil {
		return part
	}
	m["thoughtSignature"], _ = json.Marshal(sig)
	b, err := json.Marshal(m)
	if err != nil {
		return part
	}
	return b
}

// replayedSignature returns the Gemini thought signature a reasoning item from another format
// carries, or "": a Messages thinking block keeps it in signature, a Responses reasoning item in
// encrypted_content, where the client put them when the reply came from Gemini.
func replayedSignature(it llm.Item) string {
	if it.Origin == nil {
		return ""
	}
	var r struct {
		Signature        string `json:"signature"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(it.Origin.Raw, &r) != nil {
		return ""
	}
	switch {
	case it.Origin.Format == llm.FormatAnthropicMessages && IsThoughtSignature(r.Signature):
		return r.Signature
	case it.Origin.Format == llm.FormatOpenAIResponses && IsThoughtSignature(r.EncryptedContent):
		return r.EncryptedContent
	}
	return ""
}

// maxSignature bounds a thought signature this package carries between formats.
const maxSignature = 1 << 20

// IsThoughtSignature reports whether s has the shape of a Gemini thought signature: base64 of a
// protocol buffer message that starts with field 1 (byte 0x0a). It tells one from an Anthropic
// thinking signature (which starts with another field) and OpenAI's encrypted reasoning (a Fernet
// token, version byte 0x80). It checks the shape only, never that Gemini accepts it.
//
// shortcut: told apart by shape, as the Messages format tells Claude signatures apart; an origin
// record travelling with the item would be exact.
func IsThoughtSignature(s string) bool {
	if s == "" || len(s) > maxSignature || s[0] != 'C' {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	}
	return err == nil && len(b) > 0 && b[0] == 0x0a
}
