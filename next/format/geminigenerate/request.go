package geminigenerate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const format = llm.FormatGeminiGenerate

func invalid(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf(msg, args...), Scope: llm.ScopeRequest}
}

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

func object(raw json.RawMessage) map[string]json.RawMessage {
	return camel(rawjson.Object(raw))
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

var requestFields = []string{"contents", "systemInstruction", "tools", "toolConfig", "generationConfig"}

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
	modelled := requestFields
	if tier, ok := rawjson.Text(top["serviceTier"]); ok {
		switch tier {
		case "standard":
			req.Options.ServiceTier = llm.ServiceTierDefault
			modelled = rawjson.With(modelled, "serviceTier")
		case "flex", "priority":
			req.Options.ServiceTier = tier
			modelled = rawjson.With(modelled, "serviceTier")
		case "unspecified":
			modelled = rawjson.With(modelled, "serviceTier")
		}
	}
	req.Origin = rawjson.Origin(format, "", top, modelled, nested)

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
	p := &parser{pending: map[string][]pendingCall{}}
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

func parseGenerationConfig(raw json.RawMessage, o *llm.Options) (json.RawMessage, error) {
	m := object(raw)
	if m == nil {
		return nil, invalid("generationConfig must be an object")
	}
	var g struct {
		Temperature      *number         `json:"temperature"`
		TopP             *number         `json:"topP"`
		MaxOutputTokens  *int32Number    `json:"maxOutputTokens"`
		CandidateCount   json.RawMessage `json:"candidateCount"`
		StopSequences    []string        `json:"stopSequences"`
		ResponseMimeType string          `json:"responseMimeType"`
		ResponseSchema   json.RawMessage `json:"responseSchema"`
		JSONSchema       json.RawMessage `json:"responseJsonSchema"`
	}
	b, _ := json.Marshal(m)
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, invalid("generationConfig: %v", err)
	}
	o.Temperature, o.TopP, o.Stop = g.Temperature.float(), g.TopP.float(), g.StopSequences
	if g.MaxOutputTokens != nil {
		n := int(*g.MaxOutputTokens)
		o.MaxOutputTokens = &n
	}
	modelled := []string{"temperature", "topP", "maxOutputTokens", "stopSequences"}
	var n int32
	if json.Unmarshal(g.CandidateCount, &n) == nil && n >= 1 {
		modelled = append(modelled, "candidateCount")
		o.CandidateCount = int(n)
	}
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

var effortBudgets = []struct {
	max    int
	effort string
}{{0, "none"}, {512, "minimal"}, {1024, "low"}, {8192, "medium"}, {24576, "high"}}

func reasoningEffort(raw json.RawMessage) (string, json.RawMessage) {
	m := object(raw)
	if m == nil {
		return "", nil
	}
	var t struct {
		Level  string       `json:"thinkingLevel"`
		Budget *int32Number `json:"thinkingBudget"`
	}
	b, _ := json.Marshal(m)
	if json.Unmarshal(b, &t) != nil {
		return "", nil
	}
	if level := strings.ToLower(strings.TrimSpace(t.Level)); level != "" && level != "thinking_level_unspecified" {
		return level, rawjson.Rest(m, "thinkingLevel")
	}
	if t.Budget == nil {
		return "", nil
	}
	if *t.Budget < 0 {
		return "auto", rawjson.Encode(m)
	}
	effort := "xhigh"
	for _, e := range effortBudgets {
		if int(*t.Budget) <= e.max {
			effort = e.effort
			break
		}
	}
	return effort, rawjson.Encode(m)
}

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
		switch len(c.Allowed) {
		case 0:
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		case 1:
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: c.Allowed[0]}
			modelled = append(modelled, "allowedFunctionNames")
		default:
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceRequired, Allowed: c.Allowed}
			modelled = append(modelled, "allowedFunctionNames")
		}
	case "VALIDATED":
		if len(c.Allowed) > 0 {
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceAuto, Allowed: c.Allowed}
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
		if k == "googleSearch" {
			out = append(out, llm.Tool{Type: llm.ToolWebSearch, WebSearch: &llm.WebSearchTool{}, Origin: &llm.Origin{Format: format, Path: path + "." + k, Raw: one}})
			continue
		}
		if k == "codeExecution" && !isNull(m[k]) {
			out = append(out, llm.Tool{Type: llm.ToolCodeExecution, Origin: &llm.Origin{Format: format, Path: path + "." + k, Raw: one}})
			continue
		}
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

type parser struct {
	pending map[string][]pendingCall
	seq     int
}

type pendingCall struct {
	id  string
	seq int
}

func (p *parser) earliestPending() (string, bool) {
	name, best := "", -1
	for n, q := range p.pending {
		if len(q) > 0 && (best < 0 || q[0].seq < best) {
			name, best = n, q[0].seq
		}
	}
	if best < 0 {
		return "", false
	}
	id := p.pending[name][0].id
	p.pending[name] = p.pending[name][1:]
	return id, true
}

func syntheticID(kind string, content, part int, name string, payload json.RawMessage) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s|%s", kind, content, part, name, payload)))
	return "call_" + hex.EncodeToString(sum[:12])
}

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
	msg := -1
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
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: pp, Raw: append(json.RawMessage(nil), pr...)}})
		case pm["text"] != nil || pm["inlineData"] != nil || pm["fileData"] != nil:
			if msg < 0 {
				items = append(items, llm.Item{Type: llm.ItemTypeMessage, Role: role, Origin: messageOrigin()})
				msg = len(items) - 1
			}
			items[msg].Content = append(items[msg].Content, parsePart(pr, pm, pp))
			c := items[msg].Content
			items[msg].TextContent = len(c) == 1 && c[0].Type == llm.PartText && (c[0].Origin == nil || len(c[0].Origin.Raw) == 0)
		default:
			item(llm.Item{Type: llm.ItemTypeNative, Origin: &llm.Origin{Format: format, Path: pp, Raw: append(json.RawMessage(nil), pr...)}})
		}
	}
	if len(head.Parts) == 0 {
		return []llm.Item{{Type: llm.ItemTypeMessage, Role: role, Origin: messageOrigin()}}, role, nil
	}
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
	p.pending[fc.Name] = append(p.pending[fc.Name], pendingCall{id: id, seq: p.seq})
	p.seq++
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
	rm := rawjson.Object(resp)
	isError := rm["error"] != nil && !isNull(rm["error"])
	if len(rm) == 1 && rm["content"] != nil {
		out = rm["content"]
	}
	id := strings.TrimSpace(fr.ID)
	queue := p.pending[fr.Name]
	switch {
	case id != "":
		names := []string{fr.Name}
		if fr.Name == "" {
			names = slices.Sorted(maps.Keys(p.pending))
		}
		from, at, first := "", -1, -1
		for _, n := range names {
			for i, c := range p.pending[n] {
				if c.id == id {
					if first < 0 || c.seq < first {
						from, at, first = n, i, c.seq
					}
					break
				}
			}
		}
		if at >= 0 {
			q := p.pending[from]
			p.pending[from] = append(q[:at:at], q[at+1:]...)
		}
	case len(queue) > 0:
		id, p.pending[fr.Name] = queue[0].id, queue[1:]
	default:
		earliest := ""
		ok := false
		if fr.Name == "" {
			earliest, ok = p.earliestPending()
		}
		if ok {
			id = earliest
		} else {
			id = syntheticID("response", ci, pi, fr.Name, out)
		}
	}
	output := ""
	switch {
	case isError:
		output = errorValue(rm["error"])
	case !isNull(out):
		output = string(out)
	}
	it := llm.Item{Type: llm.ItemTypeToolResult, CallID: id, Output: output, IsError: isError}
	modelled := []string{"id", "name"}
	if !isError || len(rm) == 1 {
		modelled = append(modelled, "response")
	}
	var media []json.RawMessage
	if rawjson.Unmarshal(fm["parts"], &media) == nil && len(media) > 0 {
		if output != "" {
			it.Content = append(it.Content, llm.Part{Type: llm.PartText, Text: output})
		}
		for i, raw := range media {
			it.Content = append(it.Content, parsePart(raw, object(raw), fmt.Sprintf("%s.functionResponse.parts[%d]", path, i)))
		}
		modelled = append(modelled, "parts")
	}
	nested := map[string]json.RawMessage{"functionResponse": rawjson.Rest(fm, modelled...)}
	it.Origin = rawjson.Origin(format, path, pm, []string{"functionResponse"}, nested)
	return it, nil
}

func errorValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

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
		p.Origin = rawjson.Origin(format, path, pm, []string{"fileData"}, map[string]json.RawMessage{"fileData": rawjson.Rest(fm, "fileUri")})
		return p
	}
	return native
}

var geminiTypes = map[string]bool{"STRING": true, "NUMBER": true, "INTEGER": true, "BOOLEAN": true, "ARRAY": true, "OBJECT": true, "NULL": true}

// schemaInts are Schema fields that are int64, which ProtoJSON may write as strings.
var schemaInts = []string{"maxItems", "minItems", "maxLength", "minLength", "maxProperties", "minProperties"}

func jsonSchema(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	m, ok := v.(map[string]any)
	if !ok || !convertSchema(m) {
		return raw
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}

func convertSchema(m map[string]any) bool {
	changed := false
	if t, ok := m["type"].(string); ok && geminiTypes[t] {
		m["type"] = strings.ToLower(t)
		changed = true
	}
	if n, ok := m["nullable"].(bool); ok {
		if t, isString := m["type"].(string); n && isString && t != "null" {
			m["type"] = []any{t, "null"}
		}
		delete(m, "nullable")
		changed = true
	}
	for _, k := range schemaInts {
		if s, ok := m[k].(string); ok {
			if n, err := protoInt(s, 64); err == nil {
				m[k] = json.Number(strconv.FormatInt(n, 10))
				changed = true
			}
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for _, p := range props {
			if pm, ok := p.(map[string]any); ok && convertSchema(pm) {
				changed = true
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok && convertSchema(items) {
		changed = true
	}
	if alts, ok := m["anyOf"].([]any); ok {
		for _, a := range alts {
			if am, ok := a.(map[string]any); ok && convertSchema(am) {
				changed = true
			}
		}
	}
	return changed
}

// number is a floating-point field: a JSON number or, as ProtoJSON allows, a string holding one
// (including "NaN" and "Infinity").
type number float64

func (n *number) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("not a number: %s", b)
		}
		*n = number(f)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("not a number: %s", b)
	}
	*n = number(f)
	return nil
}

// int32Number is an int32 field: a JSON number or a string holding one, in any form ProtoJSON
// accepts for an integer (exponents and zero fractions included), refused when it is not a whole
// number in range.
type int32Number int32

func (n *int32Number) UnmarshalJSON(b []byte) error {
	s := string(b)
	var q string
	if json.Unmarshal(b, &q) == nil {
		s = q
	}
	v, err := protoInt(s, 32)
	if err != nil {
		return err
	}
	*n = int32Number(v)
	return nil
}

func protoInt(s string, bits int) (int64, error) {
	f, _, err := big.ParseFloat(strings.TrimSpace(s), 10, 256, big.ToNearestEven)
	if err != nil || f.IsInf() || !f.IsInt() {
		return 0, fmt.Errorf("not a whole number: %q", s)
	}
	v, acc := f.Int64()
	if acc != big.Exact || v < -(1<<(bits-1)) || v > 1<<(bits-1)-1 {
		return 0, fmt.Errorf("out of range for int%d: %q", bits, s)
	}
	return v, nil
}

func (n *number) float() *float64 {
	if n == nil {
		return nil
	}
	f := float64(*n)
	return &f
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
