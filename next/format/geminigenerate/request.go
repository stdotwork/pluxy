// Package geminigenerate converts between Gemini's GenerateContent API and pluxy's internal model:
// core uses it to read client requests and write client replies, and provider extensions use it
// to read replies from an upstream that speaks GenerateContent.
//
// A request read here keeps its body as the native form, so an upstream that speaks
// GenerateContent gets it unchanged, thought signatures included. A reply from such an upstream
// keeps every upstream chunk in its raw field, so a Gemini client gets the upstream's chunks
// unchanged; a reply from another format is rebuilt into GenerateContent chunks.
//
// Gemini reads both the camelCase and the snake_case spelling of every field name (proto3 JSON),
// so the parser accepts both. Unlike the other formats, the model and whether the reply streams
// are in the request path.
package geminigenerate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatGeminiGenerate

func invalid(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(msg, args...), Scope: llm.ScopeRequest}
}

// camel returns m with snake_case keys renamed to camelCase, so one spelling can be read. A key
// already present in camelCase wins. Only this level of the object is renamed: nested values,
// such as function arguments and schemas, are user data and keep their keys.
func camel(m map[string]json.RawMessage) map[string]json.RawMessage {
	var out map[string]json.RawMessage
	for k, v := range m {
		if !strings.Contains(k, "_") {
			continue
		}
		if out == nil {
			out = make(map[string]json.RawMessage, len(m))
			for k2, v2 := range m {
				out[k2] = v2
			}
		}
		delete(out, k)
		c := snakeToCamel(k)
		if _, ok := m[c]; !ok {
			out[c] = v
		}
	}
	if out == nil {
		return m
	}
	return out
}

func snakeToCamel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// object reads raw as a JSON object with camelCase keys, or nil when it is not an object.
func object(raw json.RawMessage) map[string]json.RawMessage {
	return camel(rawjson.Object(raw))
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// requestFields are the top-level fields the model carries. toolConfig and generationConfig are
// carried only in the shapes the parser knows; anything else in them stays in Origin.Raw.
var requestFields = []string{"contents", "systemInstruction", "tools", "toolConfig", "generationConfig"}

// ParseRequest reads a GenerateContent request body for model, streamed or not (from the request
// path). The result keeps body as its Native form, and every item, part and tool keeps the fields
// the model has no place for (thought signatures and the like) in its Origin. Errors are
// *llm.Error with code invalid_request.
func ParseRequest(model string, stream bool, body []byte) (llm.Request, error) {
	if !json.Valid(body) {
		var v any
		err := json.Unmarshal(body, &v)
		return llm.Request{}, invalid("request body is not valid JSON: %v", err)
	}
	top := object(body)
	if top == nil {
		return llm.Request{}, invalid("request body must be a JSON object")
	}
	if model == "" {
		return llm.Request{}, invalid("model is required")
	}
	req := llm.Request{
		Model:  model,
		Stream: stream,
		Native: &llm.Native{Format: format, Body: append(json.RawMessage(nil), body...)},
	}
	nested := map[string]json.RawMessage{}
	if raw, ok := top["generationConfig"]; ok && !isNull(raw) {
		rest, err := parseGenerationConfig(raw, &req.Options)
		if err != nil {
			return llm.Request{}, err
		}
		nested["generationConfig"] = rest
	}
	if raw, ok := top["toolConfig"]; ok && !isNull(raw) {
		tc, rest := parseToolConfig(raw)
		req.Options.ToolChoice = tc
		nested["toolConfig"] = rest
	}
	req.Origin = rawjson.Origin(format, "", top, requestFields, nested)

	if raw, ok := top["tools"]; ok && !isNull(raw) {
		var tools []json.RawMessage
		if err := json.Unmarshal(raw, &tools); err != nil {
			return llm.Request{}, invalid("tools must be a list")
		}
		for i, t := range tools {
			req.Tools = append(req.Tools, parseTools(t, fmt.Sprintf("tools[%d]", i))...)
		}
	}
	if raw, ok := top["systemInstruction"]; ok && !isNull(raw) {
		it, err := parseSystem(raw)
		if err != nil {
			return llm.Request{}, err
		}
		if it != nil {
			req.Items = append(req.Items, *it)
		}
	}
	var contents []json.RawMessage
	if raw, ok := top["contents"]; ok && !isNull(raw) {
		if err := json.Unmarshal(raw, &contents); err != nil {
			return llm.Request{}, invalid("contents must be a list")
		}
	}
	p := &parser{pending: map[string][]string{}}
	prevRole := ""
	for i, raw := range contents {
		items, role, err := p.content(raw, i, prevRole)
		if err != nil {
			return llm.Request{}, invalid("contents[%d]: %v", i, err)
		}
		prevRole = role
		req.Items = append(req.Items, items...)
	}
	return req, nil
}

// parseGenerationConfig reads the generation options the model carries into o and returns the
// fields it does not carry.
func parseGenerationConfig(raw json.RawMessage, o *llm.Options) (json.RawMessage, error) {
	m := object(raw)
	if m == nil {
		return nil, invalid("generationConfig must be an object")
	}
	var g struct {
		Temperature      *float64        `json:"temperature"`
		TopP             *float64        `json:"topP"`
		MaxOutputTokens  *int            `json:"maxOutputTokens"`
		StopSequences    []string        `json:"stopSequences"`
		ResponseMimeType string          `json:"responseMimeType"`
		ResponseSchema   json.RawMessage `json:"responseSchema"`
		JSONSchema       json.RawMessage `json:"responseJsonSchema"`
	}
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, invalid("generationConfig: %v", err)
	}
	o.Temperature, o.TopP, o.MaxOutputTokens, o.Stop = g.Temperature, g.TopP, g.MaxOutputTokens, g.StopSequences
	modelled := []string{"temperature", "topP", "maxOutputTokens", "stopSequences"}
	switch strings.ToLower(strings.TrimSpace(g.ResponseMimeType)) {
	case "application/json":
		schema := g.JSONSchema
		if isNull(schema) && !isNull(g.ResponseSchema) {
			schema = jsonSchema(g.ResponseSchema)
		}
		if isNull(schema) {
			o.ResponseFormat = &llm.ResponseFormat{Type: llm.ResponseFormatJSONObject}
		} else {
			o.ResponseFormat = &llm.ResponseFormat{Type: llm.ResponseFormatJSONSchema, Name: "response", Schema: schema}
		}
		modelled = append(modelled, "responseMimeType", "responseSchema", "responseJsonSchema")
	case "text/plain":
		o.ResponseFormat = &llm.ResponseFormat{Type: llm.ResponseFormatText}
		modelled = append(modelled, "responseMimeType")
	}
	nested := map[string]json.RawMessage{}
	if tc, ok := m["thinkingConfig"]; ok {
		effort, rest := reasoningEffort(tc)
		o.ReasoningEffort = effort
		if effort != "" {
			modelled = append(modelled, "thinkingConfig")
			nested["thinkingConfig"] = rest
		}
	}
	rest := rawjson.Origin(format, "", m, modelled, nested)
	return rest.Raw, nil
}

// Thinking budgets at or below these get the named reasoning effort; above the last, xhigh. They
// are today's core's thresholds, shared with the Messages format.
var effortBudgets = []struct {
	max    int
	effort string
}{{0, "none"}, {512, "minimal"}, {1024, "low"}, {8192, "medium"}, {24576, "high"}}

// reasoningEffort reads thinkingConfig: thinkingLevel as the effort it names; a thinking budget as
// the effort it falls in, -1 (dynamic) as auto. It returns the fields the effort does not carry.
func reasoningEffort(raw json.RawMessage) (string, json.RawMessage) {
	m := object(raw)
	if m == nil {
		return "", nil
	}
	var t struct {
		Level  string `json:"thinkingLevel"`
		Budget *int   `json:"thinkingBudget"`
	}
	b, _ := json.Marshal(m)
	if json.Unmarshal(b, &t) != nil {
		return "", nil
	}
	if level := strings.ToLower(strings.TrimSpace(t.Level)); level != "" {
		return level, rawjson.Rest(m, "thinkingLevel")
	}
	if t.Budget == nil {
		return "", nil
	}
	if *t.Budget < 0 {
		return "auto", rawjson.Rest(m, "thinkingBudget")
	}
	effort := "xhigh"
	for _, e := range effortBudgets {
		if *t.Budget <= e.max {
			effort = e.effort
			break
		}
	}
	return effort, rawjson.Rest(m, "thinkingBudget")
}

// parseToolConfig reads toolConfig.functionCallingConfig: AUTO, NONE, and ANY (one allowed
// function names it; otherwise any tool is required). Anything else stays in the fields it
// returns.
func parseToolConfig(raw json.RawMessage) (*llm.ToolChoice, json.RawMessage) {
	m := object(raw)
	fcRaw, ok := m["functionCallingConfig"]
	if m == nil || !ok {
		return nil, raw
	}
	fc := object(fcRaw)
	var c struct {
		Mode    string   `json:"mode"`
		Allowed []string `json:"allowedFunctionNames"`
	}
	b, _ := json.Marshal(fc)
	if fc == nil || json.Unmarshal(b, &c) != nil {
		return nil, raw
	}
	var choice *llm.ToolChoice
	modelled := []string{"mode"}
	switch strings.ToUpper(c.Mode) {
	case "AUTO":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceAuto}
	case "NONE":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceNone}
	case "ANY":
		choice = &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		if len(c.Allowed) == 1 {
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: c.Allowed[0]}
			modelled = append(modelled, "allowedFunctionNames")
		}
	}
	if choice == nil {
		return nil, raw
	}
	rest := rawjson.Rest(fc, modelled...)
	other := rawjson.Rest(m, "functionCallingConfig")
	if rest != nil {
		om := rawjson.Object(other)
		if om == nil {
			om = map[string]json.RawMessage{}
		}
		om["functionCallingConfig"] = rest
		other = rawjson.Encode(om)
	}
	return choice, other
}

// parseTools reads one Tool object: each function declaration becomes a function tool; any other
// tool in it (googleSearch, codeExecution, ...) survives whole as a native tool.
func parseTools(raw json.RawMessage, path string) []llm.Tool {
	m := object(raw)
	if m == nil {
		return []llm.Tool{{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}}
	}
	var out []llm.Tool
	var decls []json.RawMessage
	if d, ok := m["functionDeclarations"]; ok && json.Unmarshal(d, &decls) == nil {
		for i, d := range decls {
			out = append(out, parseDeclaration(d, fmt.Sprintf("%s.functionDeclarations[%d]", path, i)))
		}
	}
	for _, k := range sortedKeys(m) {
		if k == "functionDeclarations" && decls != nil {
			continue
		}
		one, _ := json.Marshal(map[string]json.RawMessage{k: m[k]})
		out = append(out, llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path + "." + k, Raw: one}})
	}
	return out
}

func parseDeclaration(raw json.RawMessage, path string) llm.Tool {
	m := object(raw)
	var d struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	b, _ := json.Marshal(m)
	if m == nil || json.Unmarshal(b, &d) != nil || d.Name == "" {
		return llm.Tool{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: path, Raw: append(json.RawMessage(nil), raw...)}}
	}
	t := llm.Tool{Name: d.Name, Description: d.Description}
	modelled := []string{"name", "description"}
	// parametersJsonSchema is JSON Schema; parameters is Gemini's Schema (an OpenAPI subset),
	// read as JSON Schema.
	if p := m["parametersJsonSchema"]; !isNull(p) {
		t.Parameters = p
		modelled = append(modelled, "parametersJsonSchema")
	} else if p := m["parameters"]; !isNull(p) {
		t.Parameters = jsonSchema(p)
		modelled = append(modelled, "parameters")
	}
	t.Origin = rawjson.Origin(format, path, m, modelled, nil)
	return t
}

// parseSystem reads systemInstruction, a Content (or, as Gemini also accepts, a string), into one
// system message.
func parseSystem(raw json.RawMessage) (*llm.Item, error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil, nil
		}
		return &llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, TextContent: true,
			Content: []llm.Part{{Type: llm.PartText, Text: s}}, Origin: &llm.Origin{Format: format, Path: "systemInstruction"}}, nil
	}
	m := object(raw)
	if m == nil {
		return nil, invalid("systemInstruction must be a Content object")
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(m["parts"], &parts); err != nil && !isNull(m["parts"]) {
		return nil, invalid("systemInstruction.parts must be a list")
	}
	it := &llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleSystem,
		Origin: rawjson.Origin(format, "systemInstruction", m, []string{"role", "parts"}, nil)}
	for i, pr := range parts {
		pp := fmt.Sprintf("systemInstruction.parts[%d]", i)
		pm := object(pr)
		if pm == nil {
			return nil, invalid("%s must be an object", pp)
		}
		it.Content = append(it.Content, parsePart(pr, pm, pp))
	}
	if len(it.Content) == 0 && (it.Origin == nil || len(it.Origin.Raw) == 0) {
		return nil, nil
	}
	return it, nil
}

// parser reads contents in order. pending holds, per function name, the call ids waiting for
// their results, so a result without an id pairs with the earliest call of the same name, as
// Gemini's function-calling guide describes.
type parser struct {
	pending map[string][]string
}

// syntheticID returns the id for a call or result Gemini sent without one: deterministic, so the
// same conversation gets the same ids on every turn. kind is "call" or "response"; payload is
// the call's arguments or the result as sent. The form is today's core's.
func syntheticID(kind string, content, part int, name string, payload json.RawMessage) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s|%s", kind, content, part, name, payload)))
	return "call_" + hex.EncodeToString(sum[:12])
}

// content reads contents[ci] into items, in part order: runs of text and media parts become a
// message item, and each function call, function response and thought becomes its own item, so
// their order survives. It returns the role it read the content as. A content without a role is
// read as a model turn when it holds a function call, a user turn when it holds a function
// response, and otherwise as the turn after the previous one.
func (p *parser) content(raw json.RawMessage, ci int, prevRole string) ([]llm.Item, string, error) {
	path := fmt.Sprintf("contents[%d]", ci)
	m := object(raw)
	if m == nil {
		return nil, "", fmt.Errorf("content must be an object")
	}
	var head struct {
		Role  string            `json:"role"`
		Parts []json.RawMessage `json:"parts"`
	}
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, "", fmt.Errorf("content: %v", err)
	}
	partMaps := make([]map[string]json.RawMessage, len(head.Parts))
	for i, pr := range head.Parts {
		partMaps[i] = object(pr)
		if partMaps[i] == nil {
			return nil, "", fmt.Errorf("parts[%d] must be an object", i)
		}
	}
	var role string
	switch strings.ToLower(strings.TrimSpace(head.Role)) {
	case "model":
		role = llm.RoleAssistant
	case "user", "function":
		role = llm.RoleUser
	case "":
		role = llm.RoleUser
		switch {
		case hasPart(partMaps, "functionCall"):
			role = llm.RoleAssistant
		case hasPart(partMaps, "functionResponse"):
		case prevRole == llm.RoleUser:
			role = llm.RoleAssistant
		}
	default:
		return nil, "", fmt.Errorf("unsupported role %q", head.Role)
	}
	origin := rawjson.Origin(format, path, m, []string{"role", "parts"}, nil)
	var items []llm.Item
	msg := -1 // index in items of the message item collecting parts, or -1
	messageOrigin := func() *llm.Origin {
		if origin == nil || len(origin.Raw) == 0 {
			return &llm.Origin{Format: format, Path: path}
		}
		o := origin
		origin = &llm.Origin{Format: format, Path: path}
		return o
	}
	item := func(it llm.Item) {
		items = append(items, it)
		msg = -1
	}
	for pi, pr := range head.Parts {
		pp := fmt.Sprintf("%s.parts[%d]", path, pi)
		pm := partMaps[pi]
		switch {
		case !isNull(pm["functionCall"]):
			it, err := p.call(pm, ci, pi, pp)
			if err != nil {
				return nil, "", err
			}
			item(it)
		case !isNull(pm["functionResponse"]):
			it, err := p.response(pm, ci, pi, pp)
			if err != nil {
				return nil, "", err
			}
			item(it)
		case isThought(pm):
			// A thought is a summary of the model's reasoning, shown to the user; Gemini does
			// not need it back, and like today's core no other format gets it: it stays whole
			// as a native item, reported when a translation drops it.
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: pp, Raw: append(json.RawMessage(nil), pr...)}})
		case pm["text"] != nil || pm["inlineData"] != nil || pm["fileData"] != nil:
			if msg < 0 {
				items = append(items, llm.Item{Type: llm.ItemTypeMessage, Role: role, Origin: messageOrigin()})
				msg = len(items) - 1
			}
			items[msg].Content = append(items[msg].Content, parsePart(pr, pm, pp))
			// Gemini has no plain-string content. A message of one plain text part is marked as
			// text content, so a format that has a string form sends it as one, as today's
			// core does.
			c := items[msg].Content
			items[msg].TextContent = len(c) == 1 && c[0].Type == llm.PartText && (c[0].Origin == nil || len(c[0].Origin.Raw) == 0)
		default:
			// Executable code, code results, server-side tool calls and their responses:
			// opaque to the model, kept whole and in place.
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: pp, Raw: append(json.RawMessage(nil), pr...)}})
		}
	}
	if len(head.Parts) == 0 {
		return []llm.Item{{Type: llm.ItemTypeMessage, Role: role, Origin: messageOrigin()}}, role, nil
	}
	// A content whose parts all became other items keeps its own fields on an empty message
	// item, as the Messages format does: first for a model turn, last for a user turn.
	if origin != nil && len(origin.Raw) > 0 {
		it := llm.Item{Type: llm.ItemTypeMessage, Role: role, Origin: origin}
		if role == llm.RoleAssistant {
			items = append([]llm.Item{it}, items...)
		} else {
			items = append(items, it)
		}
	}
	return items, role, nil
}

func hasPart(parts []map[string]json.RawMessage, key string) bool {
	for _, pm := range parts {
		if !isNull(pm[key]) {
			return true
		}
	}
	return false
}

func isThought(pm map[string]json.RawMessage) bool {
	var t bool
	return json.Unmarshal(pm["thought"], &t) == nil && t
}

// call reads a functionCall part. Its id is kept, or made up when Gemini sent none. The part's
// other fields, such as its thought signature, stay in Origin.Raw.
func (p *parser) call(pm map[string]json.RawMessage, ci, pi int, path string) (llm.Item, error) {
	fm := object(pm["functionCall"])
	if fm == nil {
		return llm.Item{}, fmt.Errorf("parts[%d].functionCall must be an object", pi)
	}
	var fc struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	b, _ := json.Marshal(fm)
	if err := json.Unmarshal(b, &fc); err != nil {
		return llm.Item{}, fmt.Errorf("parts[%d].functionCall: %v", pi, err)
	}
	args := fm["args"]
	id := strings.TrimSpace(fc.ID)
	if id == "" {
		id = syntheticID("call", ci, pi, fc.Name, args)
	}
	p.pending[fc.Name] = append(p.pending[fc.Name], id)
	arguments := "{}"
	if !isNull(args) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, args); err != nil {
			return llm.Item{}, fmt.Errorf("parts[%d].functionCall.args: %v", pi, err)
		}
		arguments = buf.String()
	}
	nested := map[string]json.RawMessage{"functionCall": rawjson.Rest(fm, "id", "name", "args")}
	return llm.Item{Type: llm.ItemTypeToolCall, CallID: id, Name: fc.Name, Arguments: arguments,
		Origin: rawjson.Origin(format, path, pm, []string{"functionCall"}, nested)}, nil
}

// response reads a functionResponse part. Without an id it pairs with the earliest waiting call
// of the same name, or gets a made-up id when there is none. The output is response.content when
// the response has one, otherwise the whole response, as JSON text (today's core does the same).
func (p *parser) response(pm map[string]json.RawMessage, ci, pi int, path string) (llm.Item, error) {
	fm := object(pm["functionResponse"])
	if fm == nil {
		return llm.Item{}, fmt.Errorf("parts[%d].functionResponse must be an object", pi)
	}
	var fr struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	b, _ := json.Marshal(fm)
	if err := json.Unmarshal(b, &fr); err != nil {
		return llm.Item{}, fmt.Errorf("parts[%d].functionResponse: %v", pi, err)
	}
	resp := fm["response"]
	out := resp
	if rm := rawjson.Object(resp); rm != nil {
		if c, ok := rm["content"]; ok {
			out = c
		}
	}
	id := strings.TrimSpace(fr.ID)
	queue := p.pending[fr.Name]
	switch {
	case id != "":
		for i, q := range queue {
			if q == id {
				p.pending[fr.Name] = append(queue[:i:i], queue[i+1:]...)
				break
			}
		}
	case len(queue) > 0:
		id, p.pending[fr.Name] = queue[0], queue[1:]
	default:
		id = syntheticID("response", ci, pi, fr.Name, out)
	}
	output := ""
	if !isNull(out) {
		output = string(out)
	}
	// The name is carried by the call the result pairs with.
	nested := map[string]json.RawMessage{"functionResponse": rawjson.Rest(fm, "id", "name", "response")}
	return llm.Item{Type: llm.ItemTypeToolResult, CallID: id, Output: output,
		Origin: rawjson.Origin(format, path, pm, []string{"functionResponse"}, nested)}, nil
}

// parsePart reads a text, inlineData or fileData part. Inline data and file URIs are images when
// their MIME type is an image type, files otherwise.
func parsePart(raw json.RawMessage, pm map[string]json.RawMessage, path string) llm.Part {
	native := llm.Part{Type: llm.PartNative, Format: format, Data: append(json.RawMessage(nil), raw...), Origin: &llm.Origin{Format: format, Path: path}}
	var text *string
	if t := pm["text"]; t != nil {
		var s string
		if json.Unmarshal(t, &s) != nil {
			return native
		}
		text = &s
	}
	switch {
	case text != nil && pm["inlineData"] == nil && pm["fileData"] == nil:
		return llm.Part{Type: llm.PartText, Text: *text, Origin: rawjson.Origin(format, path, pm, []string{"text"}, nil)}
	case text == nil && pm["inlineData"] != nil && pm["fileData"] == nil:
		dm := object(pm["inlineData"])
		var d struct {
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
		}
		b, _ := json.Marshal(dm)
		if dm == nil || json.Unmarshal(b, &d) != nil || d.Data == "" {
			return native
		}
		url := "data:" + d.MimeType + ";base64," + d.Data
		p := llm.Part{Type: llm.PartFile, FileData: url}
		if strings.HasPrefix(strings.ToLower(d.MimeType), "image/") {
			p = llm.Part{Type: llm.PartImage, URL: url}
		}
		p.Origin = rawjson.Origin(format, path, pm, []string{"inlineData"}, map[string]json.RawMessage{"inlineData": rawjson.Rest(dm, "mimeType", "data")})
		return p
	case text == nil && pm["fileData"] != nil && pm["inlineData"] == nil:
		fm := object(pm["fileData"])
		var f struct {
			MimeType string `json:"mimeType"`
			URI      string `json:"fileUri"`
		}
		b, _ := json.Marshal(fm)
		if fm == nil || json.Unmarshal(b, &f) != nil || f.URI == "" {
			return native
		}
		p := llm.Part{Type: llm.PartFile, URL: f.URI}
		if strings.HasPrefix(strings.ToLower(f.MimeType), "image/") {
			p = llm.Part{Type: llm.PartImage, URL: f.URI}
		}
		// The MIME type is kept: a file URL has no other place for it.
		p.Origin = rawjson.Origin(format, path, pm, []string{"fileData"}, map[string]json.RawMessage{"fileData": rawjson.Rest(fm, "fileUri")})
		return p
	}
	return native
}

// geminiTypes are the type names of Gemini's Schema, which JSON Schema spells in lower case.
var geminiTypes = map[string]bool{"STRING": true, "NUMBER": true, "INTEGER": true, "BOOLEAN": true, "ARRAY": true, "OBJECT": true, "NULL": true}

// jsonSchema returns a Gemini Schema (responseSchema, a declaration's parameters) as JSON
// Schema: Gemini's upper-case type names in lower case, everything else as it is.
//
// shortcut: other OpenAPI forms (nullable, TYPE_UNSPECIFIED) are left as they are; convert them
// when an upstream rejects them.
func jsonSchema(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	changed := false
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if s, ok := val.(string); ok && k == "type" && geminiTypes[s] {
					x[k] = strings.ToLower(s)
					changed = true
					continue
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(v)
	if !changed {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
