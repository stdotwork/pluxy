package geminigenerate

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	mediainput "github.com/stdotwork/pluxy/next/format/internal/media"
	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/format/internal/textdoc"
	"github.com/stdotwork/pluxy/next/llm"
)

// SkipSignature is the thought signature Gemini accepts on a function call it did not issue:
// Google's thought-signature guide names this value for calls carried over from another model
// or made up by the client, which have no signature of their own. Gemini 3 refuses a function
// call in the current turn that has no signature.
const SkipSignature = "skip_thought_signature_validator"

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

func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	return EncodeRequestWith(req, EncodeOptions{})
}

type EncodeOptions struct {
	ServiceTier    bool
	WebSearch      bool
	CodeExecution  bool
	Vertex         bool
	CandidateCount bool
}

func EncodeRequestWith(req llm.Request, opts EncodeOptions) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	req, lowered := clienttools.Lower(req)
	e.losses = append(e.losses, lowered...)
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
	if o.CandidateCount >= 1 {
		own := req.SourceFormat() == format
		switch {
		case own:
			gen["candidateCount"] = o.CandidateCount
		case o.CandidateCount == 1:
		case !opts.CandidateCount:
			e.lose(llm.LossDropped, llm.CandidateCountPath(req), llm.CandidateCountUnsent)
		case !supportsCandidateCount(req.Model):
			e.lose(llm.LossDropped, llm.CandidateCountPath(req), fmt.Sprintf("GenerateContent model %q does not support candidateCount > 1", req.Model))
		case o.CandidateCount > maxGeminiCandidates:
			e.lose(llm.LossDropped, llm.CandidateCountPath(req), fmt.Sprintf("GenerateContent candidateCount must be at most %d", maxGeminiCandidates))
		default:
			gen["candidateCount"] = o.CandidateCount
		}
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
	e.serviceTier(req, opts, out)
	if o.Verbosity != "" {
		e.lose(llm.LossDropped, llm.VerbosityPath(req), "GenerateContent has no verbosity setting")
	}

	toolNameMap := ToolNames(req)
	hasFunctionTools := false
	for _, t := range req.Tools {
		if t.IsFunction() {
			hasFunctionTools = true
			break
		}
	}
	var decls []json.RawMessage
	var tools []json.RawMessage
	hasOwnTools := false
	droppedBuiltIn := false
	type translatedCode struct {
		tool llm.Tool
		at   int
		path string
	}
	var codeTools []translatedCode
	searchSent := false
	for i, t := range req.Tools {
		p := pathOf(t.Origin, fmt.Sprintf("tools[%d]", i))
		if t.Type == llm.ToolCodeExecution {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
				hasOwnTools = true
				continue
			}
			if !opts.CodeExecution {
				e.lose(llm.LossDropped, p, llm.ToolCodeExecutionUnsent)
				droppedBuiltIn = true
				continue
			}
			if !supportsCodeExecution(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("GenerateContent model %q does not support codeExecution", req.Model))
				droppedBuiltIn = true
				continue
			}
			if hasFunctionTools {
				e.lose(llm.LossDropped, p, "GenerateContent does not combine codeExecution with functionDeclarations")
				droppedBuiltIn = true
				continue
			}
			if rf := o.ResponseFormat; rf != nil && (rf.Type == llm.ResponseFormatJSONObject || rf.Type == llm.ResponseFormatJSONSchema) && !combinesStructuredOutput(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("GenerateContent model %q does not combine codeExecution with structured output", req.Model))
				droppedBuiltIn = true
				continue
			}
			codeTools = append(codeTools, translatedCode{t, len(tools), p})
			tools = append(tools, json.RawMessage(`{"codeExecution":{}}`))
			continue
		}
		if t.Type == llm.ToolWebSearch {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
				hasOwnTools = true
				continue
			}
			if !opts.WebSearch {
				e.lose(llm.LossDropped, p, llm.WebSearchUnsent)
				continue
			}
			if t.WebSearch != nil && t.WebSearch.Disabled {
				e.lose(llm.LossDropped, p, "GenerateContent has no external_web_access setting for googleSearch")
				continue
			}
			if !supportsGoogleSearch(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("GenerateContent model %q does not support googleSearch", req.Model))
				continue
			}
			if hasFunctionTools {
				e.lose(llm.LossDropped, p, "GenerateContent does not combine googleSearch with functionDeclarations")
				continue
			}
			if rf := o.ResponseFormat; rf != nil && (rf.Type == llm.ResponseFormatJSONObject || rf.Type == llm.ResponseFormatJSONSchema) && !combinesStructuredOutput(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("GenerateContent model %q does not combine googleSearch with structured output", req.Model))
				droppedBuiltIn = true
				continue
			}
			searchSent = true
			tools = append(tools, json.RawMessage(`{"googleSearch":{}}`))
			for _, l := range rawjson.WebSearchLosses(t, p, format, rawjson.WebSearchCarried{}) {
				e.losses = llm.AddLoss(e.losses, l)
			}
			continue
		}
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
				hasOwnTools = true
			} else {
				e.lose(llm.LossDropped, p, "GenerateContent has no such tool")
			}
			continue
		}
		fd := map[string]any{"name": ToolName(toolNameMap, t.Name)}
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
	if len(codeTools) > 0 {
		var why string
		switch {
		case searchSent && opts.Vertex:
			why = "Vertex AI allows several tools in one request only when all are search tools, so codeExecution is not sent beside googleSearch"
		case searchSent && !allowsSearchWithCode(req.Model):
			why = fmt.Sprintf("GenerateContent model %q does not take codeExecution beside googleSearch", req.Model)
		}
		if why != "" {
			for k := len(codeTools) - 1; k >= 0; k-- {
				tools = slices.Delete(tools, codeTools[k].at, codeTools[k].at+1)
			}
		}
		for _, c := range codeTools {
			if why != "" {
				e.lose(llm.LossDropped, c.path, why)
				droppedBuiltIn = true
				continue
			}
			for _, l := range rawjson.CodeExecutionLosses(c.tool, c.path, format) {
				e.losses = llm.AddLoss(e.losses, l)
			}
		}
	}
	if len(decls) > 0 {
		tools = append([]json.RawMessage{marshalWith(map[string]any{"functionDeclarations": decls}, nil)}, tools...)
	}
	if o.ToolChoice != nil && o.ToolChoice.Mode == llm.ToolChoiceCodeExecution {
		e.lose(llm.LossDropped, "tool_choice", "GenerateContent toolConfig cannot force codeExecution")
		if len(tools) > 0 {
			out["tools"] = tools
			if (len(decls) > 0 || hasOwnTools) && o.ParallelToolCalls != nil && !*o.ParallelToolCalls {
				if fc := e.functionCalling(nil, o.ParallelToolCalls, toolNameMap); fc != nil {
					out["toolConfig"] = map[string]any{"functionCallingConfig": fc}
				}
			}
		}
	} else if len(tools) > 0 {
		out["tools"] = tools
		if len(decls) > 0 || hasOwnTools {
			if fc := e.functionCalling(o.ToolChoice, o.ParallelToolCalls, toolNameMap); fc != nil {
				out["toolConfig"] = map[string]any{"functionCallingConfig": fc}
			}
		} else if o.ToolChoice != nil && (o.ToolChoice.Mode == llm.ToolChoiceRequired || o.ToolChoice.Mode == llm.ToolChoiceTool) {
			e.lose(llm.LossDropped, "tool_choice", "GenerateContent functionCallingConfig cannot force built-in tools without functionDeclarations")
		}
	} else if droppedBuiltIn && o.ToolChoice != nil && (o.ToolChoice.Mode == llm.ToolChoiceRequired || o.ToolChoice.Mode == llm.ToolChoiceTool) {
		e.lose(llm.LossDropped, "tool_choice", "GenerateContent has no tools left to force")
	}

	var system []json.RawMessage
	var contents []*outContent
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
	callName := map[string]string{}
	for i, it := range req.Items {
		p := pathOf(it.Origin, fmt.Sprintf("items[%d]", i))
		switch it.Type {
		case llm.ItemTypeMessage:
			e.kept(it.Origin)
			parts, sendable, dropped := e.parts(it, p, true)
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
			callName[it.CallID] = ToolName(toolNameMap, it.Name)
			args := json.RawMessage(it.Arguments)
			var m map[string]json.RawMessage
			if json.Unmarshal(args, &m) != nil || m == nil {
				if strings.TrimSpace(it.Arguments) != "" {
					e.lose(llm.LossApproximated, p, "tool arguments that are not a JSON object, sent as {}")
				}
				args = json.RawMessage("{}")
			}
			call := map[string]any{"name": callName[it.CallID], "args": args}
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
			name, ok := callName[it.CallID]
			if !ok {
				e.lose(llm.LossApproximated, p, "a tool result with no call before it, named after its call ID")
				name = ToolName(toolNameMap, it.CallID)
			}
			key := "result"
			if it.IsError {
				key = "error"
			}
			resp := map[string]any{"name": name, "response": map[string]any{key: it.Output}}
			if it.CallID != "" {
				resp["id"] = it.CallID
			}
			if media := e.responseParts(it, p); len(media) > 0 {
				resp["parts"] = media
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
	for i := 1; i < len(contents); i++ {
		if contents[i-1].role == "model" && contents[i].role != "model" {
			contents[i].parts, _ = orderResponses(callsOf(contents[i-1].parts), contents[i].parts)
		}
	}
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

func (e *encoder) functionCalling(tc *llm.ToolChoice, parallel *bool, names map[string]string) map[string]any {
	if parallel != nil && !*parallel {
		e.lose(llm.LossApproximated, "parallel_tool_calls", "GenerateContent cannot limit a reply to one function call; function calling is turned off")
		return map[string]any{"mode": "NONE"}
	}
	if tc == nil {
		return nil
	}
	switch tc.Mode {
	case llm.ToolChoiceAuto:
		if len(tc.Allowed) > 0 {
			return map[string]any{"mode": "VALIDATED", "allowedFunctionNames": e.allowedNames(tc.Allowed, names)}
		}
		return map[string]any{"mode": "AUTO"}
	case llm.ToolChoiceNone:
		return map[string]any{"mode": "NONE"}
	case llm.ToolChoiceRequired:
		if len(tc.Allowed) > 0 {
			return map[string]any{"mode": "ANY", "allowedFunctionNames": e.allowedNames(tc.Allowed, names)}
		}
		return map[string]any{"mode": "ANY"}
	case llm.ToolChoiceTool:
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{ToolName(names, tc.Name)}}
	}
	return nil
}

func (e *encoder) allowedNames(allowed []string, names map[string]string) []string {
	out := make([]string, len(allowed))
	for i, name := range allowed {
		out[i] = ToolName(names, name)
	}
	return out
}

func (e *encoder) parts(it llm.Item, msgPath string, textDocs bool) (parts []json.RawMessage, sendable, dropped bool) {
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
			var problem string
			part, problem = media(p)
			if problem != "" {
				e.lose(llm.LossDropped, pp, "GenerateContent cannot take this "+p.Type+": "+problem)
				dropped = true
				continue
			}
		case llm.PartNative:
			if p.Format == format {
				parts = append(parts, p.Data)
				sendable = true
				continue
			}
			if p.Format == llm.FormatAnthropicMessages && textDocs {
				if text, title, ok := textdoc.FromAnthropic(p.Data); ok {
					part = map[string]any{"text": textdoc.Wrap(title, text)}
					e.lose(llm.LossApproximated, pp, "a Messages plain-text document, carried as text")
					break
				}
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

func (e *encoder) responseParts(it llm.Item, itemPath string) []json.RawMessage {
	var media []llm.Part
	for i, p := range it.Content {
		if p.Type == llm.PartText {
			pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", itemPath, i))
			if !strings.Contains(it.Output, p.Text) {
				e.lose(llm.LossDropped, pp, "GenerateContent cannot take a text part beside a tool result's text; this one is not in its output")
				continue
			}
			e.kept(p.Origin)
			continue
		}
		if p.Origin == nil || p.Origin.Path == "" {
			o := llm.Origin{}
			if p.Origin != nil {
				o = *p.Origin
			}
			o.Path = fmt.Sprintf("%s.content[%d]", itemPath, i)
			p.Origin = &o
		}
		media = append(media, p)
	}
	parts, _, _ := e.parts(llm.Item{Content: media}, itemPath, false)
	return parts
}

func media(p llm.Part) (map[string]any, string) {
	url := p.URL
	if p.Type == llm.PartFile && p.FileData != "" {
		url = p.FileData
	}
	if mediainput.IsDataURL(url) {
		mediaType, data, ok := mediainput.DataURL(url)
		if !ok {
			return nil, "it is not a base64 data URL"
		}
		if p.Origin == nil || p.Origin.Format != format {
			mediaType = mediainput.Type(mediaType)
			if !mediainput.Base64(data) {
				return nil, "its data is not valid base64"
			}
			if p.Type == llm.PartImage || strings.HasPrefix(mediaType, "image/") {
				if _, problem := mediainput.InlineImage(url, mediainput.GeminiImages); problem != "" {
					return nil, problem
				}
			}
		}
		return map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": data}}, ""
	}
	if url != "" && p.Origin != nil && p.Origin.Format == format {
		return map[string]any{"fileData": map[string]any{"fileUri": url}}, ""
	}
	if !geminiFileURI(url) {
		return nil, "GenerateContent takes a URL only as a Files API URI (/v1/files/ or /v1beta/files/)"
	}
	return map[string]any{"fileData": map[string]any{"fileUri": url}}, ""
}

// A Cloud Storage (gs://) URI is refused because Vertex AI would read that object with the
// account's own permissions on behalf of whoever holds the client key.
func geminiFileURI(raw string) bool {
	if strings.ContainsAny(raw, "?#") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" {
		return false
	}
	if u.User != nil || u.Port() != "" || !strings.EqualFold(u.Hostname(), "generativelanguage.googleapis.com") {
		return false
	}
	if u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return false
	}
	for _, prefix := range []string{"/v1/files/", "/v1beta/files/"} {
		if id, ok := strings.CutPrefix(u.Path, prefix); ok && fileID(id) {
			return u.EscapedPath() == prefix+id
		}
	}
	return false
}

func fileID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

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

const maxSignature = 1 << 20

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

func (e *encoder) serviceTier(req llm.Request, opts EncodeOptions, out map[string]any) {
	tier := req.Options.ServiceTier
	if tier == "" {
		return
	}
	if !opts.ServiceTier && req.SourceFormat() != format {
		e.lose(llm.LossDropped, llm.ServiceTierPath(req), llm.ServiceTierUnsent)
		return
	}
	switch t := llm.CanonicalServiceTier(tier); t {
	case llm.ServiceTierDefault:
		out["serviceTier"] = "standard"
	case llm.ServiceTierFlex, llm.ServiceTierPriority:
		out["serviceTier"] = t
	default:
		e.lose(llm.LossDropped, llm.ServiceTierPath(req), fmt.Sprintf("GenerateContent has no %q service tier", tier))
	}
}

var googleSearchModels = []string{
	"gemini-4-carbon",
	"gemini-4-argon",
	"gemini-4-neon",
	"gemini-4-hydrogen",
	"gemini-3.8-live",
	"gemini-3.8-flash",
	"gemini-3.7-flash",
	"gemini-3.6-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.5-flash",
	"gemini-3.1-pro",
	"gemini-3.1-flash-lite",
	"gemini-3.1-flash-image",
	"gemini-3-pro-image",
	"gemini-3-flash",
	"gemini-2.5-pro",
	"gemini-2.5-flash-lite",
	"gemini-2.5-flash",
	"gemini-live-2.5-flash-native-audio",
	"gemini-2.0-flash",
	"gemini-robotics-er-2",
	"gemini-nano-banana-2.1",
}

func supportsGoogleSearch(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if strings.Contains(m, "embedding") || strings.Contains(m, "-tts") {
		return false
	}
	if strings.HasPrefix(m, "gemini-2.0-flash-lite") {
		return false
	}
	if strings.Contains(m, "-image") {
		if !strings.HasPrefix(m, "gemini-3.1-flash-image") && !strings.HasPrefix(m, "gemini-3-pro-image") {
			return false
		}
	}
	for _, family := range googleSearchModels {
		if m == family || strings.HasPrefix(m, family+"-") {
			return true
		}
	}
	return false
}

func allowsSearchWithCode(model string) bool {
	m := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	return !strings.HasPrefix(m, "gemini-2.0-")
}

func combinesStructuredOutput(model string) bool {
	m := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	for _, series := range []string{"gemini-3-", "gemini-3.", "gemini-4-", "gemini-4."} {
		if strings.HasPrefix(m, series) {
			return true
		}
	}
	return false
}

var codeExecutionModels = []string{
	"gemini-4-carbon",
	"gemini-4-argon",
	"gemini-4-neon",
	"gemini-4-hydrogen",
	"gemini-3.8-flash",
	"gemini-3.6-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.1-pro",
	"gemini-3.1-flash-lite",
	"gemini-3-pro",
	"gemini-3-flash",
	"gemini-2.5-pro",
	"gemini-2.5-flash-lite",
	"gemini-2.5-flash",
	"gemini-2.0-flash",
	"gemini-robotics-er-1.6",
}

func supportsCodeExecution(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if strings.Contains(m, "embedding") || strings.Contains(m, "-tts") || strings.Contains(m, "-image") ||
		strings.Contains(m, "-live") || strings.Contains(m, "-native-audio") || strings.Contains(m, "-transcribe") {
		return false
	}
	if strings.HasPrefix(m, "gemini-2.0-flash-lite") {
		return false
	}
	for _, family := range codeExecutionModels {
		if m == family || strings.HasPrefix(m, family+"-") {
			return true
		}
	}
	return false
}

const maxGeminiCandidates = 8

var candidateCountModels = []string{
	"gemini-2.5-pro",
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.0-flash",
	"gemini-2.0-flash-lite",
	"gemini-1.5-pro",
	"gemini-1.5-flash",
}

func supportsCandidateCount(model string) bool {
	m := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(model)), "models/")
	if strings.Contains(m, "embedding") || strings.Contains(m, "-tts") || strings.Contains(m, "-image") ||
		strings.Contains(m, "-live") || strings.Contains(m, "-native-audio") || strings.Contains(m, "-transcribe") ||
		strings.Contains(m, "-computer-use") {
		return false
	}
	for _, family := range candidateCountModels {
		if m == family || strings.HasPrefix(m, family+"-") {
			return true
		}
	}
	return false
}
