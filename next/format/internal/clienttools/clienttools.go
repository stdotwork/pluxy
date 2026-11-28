package clienttools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/format/internal/responsesinput"
	"github.com/stdotwork/pluxy/next/llm"
)

const responses = llm.FormatOpenAIResponses

const SearchName = "tool_search"

// defaultNamespace is the namespace the Codex client puts its plain tools in on Responses Lite. A
// call in it is the same tool as a call with no namespace (codex-rs/protocol/src/tool_name.rs).
const defaultNamespace = "functions"

type Kind int

const (
	KindFunction Kind = iota
	KindCustom
	KindSearch
)

type Ref struct {
	Kind      Kind
	Namespace string
	Name      string
}

const (
	customParameters     = `{"type":"object","properties":{"input":{"type":"string","description":"The complete input text for the tool."}},"required":["input"],"additionalProperties":false}`
	customInstructions   = `Call this function with a JSON object whose input field contains the complete input text for the tool.`
	applyPatchName       = "apply_patch"
	applyPatchParameters = `{"type":"object","properties":{"input":{"type":"string","description":"The complete apply_patch patch text."}},"required":["input"],"additionalProperties":false}`
)

const applyPatchInstructions = `Call this function with a JSON object whose input field contains the complete patch text.
Use the Codex apply_patch format, not a conventional git unified diff.
Start with *** Begin Patch and end with *** End Patch.
Use *** Add File: path, *** Delete File: path, or *** Update File: path.
Every added-file content line starts with +.
For updates, use @@; context lines start with one space, removed lines with -, and added lines with +.
Use *** Move to: path for a rename and *** End of File when required by the patch grammar.
Example input:
*** Begin Patch
*** Update File: src/main.go
@@
-old
+new
*** End Patch`

func SentName(namespace, name string) string {
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	if namespace == "" || namespace == defaultNamespace || name == "" || strings.HasPrefix(name, "mcp__") {
		return name
	}
	prefix := namespace
	if !strings.HasSuffix(prefix, "__") {
		prefix += "__"
	}
	if strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

func Lower(req llm.Request) (llm.Request, []llm.Loss) {
	if !needed(req) {
		return req, nil
	}
	p := analyze(req)
	out := req
	out.Tools = p.tools
	out.Items = lowerItems(req.Items)
	p.lowerChoice(&out)
	return out, p.losses
}

func Refs(req llm.Request) map[string]Ref {
	if !needed(req) {
		return nil
	}
	return analyze(req).refs
}

type head struct {
	Type      string `json:"type"`
	Execution string `json:"execution"`
	CallID    string `json:"call_id"`
}

func headOf(raw []byte) head {
	var h head
	_ = json.Unmarshal(raw, &h)
	return h
}

func fromResponses(o *llm.Origin) bool { return o != nil && o.Format == responses }

func lowersItem(raw []byte) bool {
	h := headOf(raw)
	switch h.Type {
	case "additional_tools", "custom_tool_call", "custom_tool_call_output":
		return true
	case "tool_search_call", "tool_search_output":
		return h.Execution == "client" && h.CallID != ""
	}
	return false
}

func needed(req llm.Request) bool {
	var seen map[string]bool
	for i := range req.Tools {
		t := &req.Tools[i]
		if !fromResponses(t.Origin) {
			continue
		}
		if t.Type == llm.ToolNative {
			h := headOf(t.Origin.Raw)
			if h.Type == "custom" || h.Type == "namespace" || h.Type == "tool_search" && h.Execution == "client" {
				return true
			}
			continue
		}
		if !t.IsFunction() {
			continue
		}
		if bytes.Contains(t.Origin.Raw, []byte("defer_loading")) {
			return true
		}
		if seen == nil {
			seen = make(map[string]bool, len(req.Tools))
		}
		if seen[t.Name] {
			return true
		}
		seen[t.Name] = true
	}
	for i := range req.Items {
		it := &req.Items[i]
		if !fromResponses(it.Origin) {
			continue
		}
		switch it.Type {
		case llm.ItemTypeNative:
			if lowersItem(it.Origin.Raw) {
				return true
			}
		case llm.ItemTypeToolCall, llm.ItemTypeToolResult:
			if bytes.Contains(it.Origin.Raw, []byte(`"namespace"`)) {
				return true
			}
		}
	}
	return false
}

type decl struct {
	sent          string
	tool          llm.Tool
	ref           Ref
	path          string
	priority      int
	direct        bool
	deferred      bool
	order         int
	nsDescription string
}

func (d *decl) precedes(o *decl) bool {
	if d.priority != o.priority {
		return d.priority < o.priority
	}
	if d.direct != o.direct {
		return d.direct
	}
	return d.order < o.order
}

type entry struct {
	d    *decl
	tool llm.Tool
}

type plan struct {
	entries []entry
	tools   []llm.Tool
	refs    map[string]Ref
	sent    map[string]bool
	losses  []llm.Loss
	order   int
}

func (p *plan) lose(kind, path, detail string) {
	p.losses = llm.AddLoss(p.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

func (p *plan) add(d *decl) {
	d.order = p.order
	p.order++
	p.entries = append(p.entries, entry{d: d})
}

func analyze(req llm.Request) *plan {
	p := &plan{refs: map[string]Ref{}, sent: map[string]bool{}}
	for i, t := range req.Tools {
		switch {
		case !fromResponses(t.Origin):
			p.entries = append(p.entries, entry{tool: t})
		case t.Type == llm.ToolNative:
			path := t.Origin.Path
			if path == "" {
				path = fmt.Sprintf("tools[%d]", i)
			}
			if !p.spec(t.Origin.Raw, path, 0) {
				p.entries = append(p.entries, entry{tool: t})
			}
		case !t.IsFunction():
			p.entries = append(p.entries, entry{tool: t})
		default:
			m := rawjson.Object(t.Origin.Raw)
			var deferred bool
			_ = json.Unmarshal(m["defer_loading"], &deferred)
			lowered := t
			if _, ok := m["defer_loading"]; ok {
				o := *t.Origin
				o.Raw = rawjson.Rest(m, "defer_loading")
				lowered.Origin = &o
			}
			p.add(&decl{sent: t.Name, tool: lowered, ref: Ref{Name: t.Name}, path: t.Origin.Path, direct: true, deferred: deferred})
		}
	}
	for _, it := range req.Items {
		if it.Type != llm.ItemTypeNative || !fromResponses(it.Origin) {
			continue
		}
		var body struct {
			Type  string            `json:"type"`
			Tools []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(it.Origin.Raw, &body) != nil {
			continue
		}
		switch {
		case body.Type == "additional_tools":
			for i, raw := range body.Tools {
				path := fmt.Sprintf("%s.tools[%d]", it.Origin.Path, i)
				if !p.spec(raw, path, 1) {
					p.entries = append(p.entries, entry{tool: llm.Tool{Type: llm.ToolNative,
						Origin: &llm.Origin{Format: responses, Path: path, Raw: append(json.RawMessage(nil), raw...)}}})
				}
			}
		case body.Type == "tool_search_output" && lowersItem(it.Origin.Raw):
			for i, raw := range body.Tools {
				path := fmt.Sprintf("%s.tools[%d]", it.Origin.Path, i)
				if !p.spec(raw, path, 2) {
					p.lose(llm.LossDropped, path, "a tool_search result that is not a function, custom or namespace tool")
				}
			}
		}
	}
	p.settle()
	return p
}

func (p *plan) spec(raw json.RawMessage, path string, priority int) bool {
	m := rawjson.Object(raw)
	var typ string
	_ = json.Unmarshal(m["type"], &typ)
	switch typ {
	case "function", "custom":
		d := member(m, typ, "", path)
		if d == nil {
			return false
		}
		d.priority, d.direct = priority, true
		p.add(d)
		return true
	case "tool_search":
		if headOf(raw).Execution != "client" {
			return false
		}
		var description string
		_ = json.Unmarshal(m["description"], &description)
		tool := llm.Tool{Name: SearchName, Description: description, Parameters: m["parameters"],
			Origin: rawjson.Origin(responses, path, m, []string{"type", "execution", "description", "parameters"}, nil)}
		p.add(&decl{sent: SearchName, tool: tool, ref: Ref{Kind: KindSearch, Name: SearchName}, path: path, priority: priority, direct: true})
		return true
	case "namespace":
		var ns struct {
			Name        string            `json:"name"`
			Description string            `json:"description"`
			Tools       []json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(raw, &ns) != nil || strings.TrimSpace(ns.Name) == "" {
			return false
		}
		name := strings.TrimSpace(ns.Name)
		for i, c := range ns.Tools {
			childPath := fmt.Sprintf("%s.tools[%d]", path, i)
			cm := rawjson.Object(c)
			var childType string
			_ = json.Unmarshal(cm["type"], &childType)
			var d *decl
			if childType == "function" || childType == "custom" {
				d = member(cm, childType, name, childPath)
			}
			if d == nil {
				p.lose(llm.LossDropped, childPath, "a namespace member that is not a named function or custom tool")
				continue
			}
			d.priority, d.nsDescription = priority, strings.TrimSpace(ns.Description)
			p.add(d)
		}
		return true
	}
	return false
}

func member(m map[string]json.RawMessage, typ, namespace, path string) *decl {
	var f struct {
		name, description string
		strict            *bool
		deferred          bool
	}
	_ = json.Unmarshal(m["name"], &f.name)
	_ = json.Unmarshal(m["description"], &f.description)
	_ = json.Unmarshal(m["strict"], &f.strict)
	_ = json.Unmarshal(m["defer_loading"], &f.deferred)
	name := strings.TrimSpace(f.name)
	if name == "" {
		return nil
	}
	d := &decl{sent: SentName(namespace, name), path: path, deferred: f.deferred}
	if typ == "custom" {
		d.ref = Ref{Kind: KindCustom, Namespace: namespace, Name: name}
		d.tool = llm.Tool{Name: d.sent, Description: customDescription(name, f.description, m["format"]), Parameters: customSchema(name),
			Origin: rawjson.Origin(responses, path, m, []string{"type", "name", "description", "format", "defer_loading"}, nil)}
		return d
	}
	d.ref = Ref{Kind: KindFunction, Namespace: namespace, Name: name}
	d.tool = llm.Tool{Name: d.sent, Description: f.description, Parameters: m["parameters"], Strict: f.strict,
		Origin: rawjson.Origin(responses, path, m, []string{"type", "name", "description", "parameters", "strict", "defer_loading"}, nil)}
	return d
}

func customSchema(name string) json.RawMessage {
	if name == applyPatchName {
		return json.RawMessage(applyPatchParameters)
	}
	return json.RawMessage(customParameters)
}

func customDescription(name, description string, format json.RawMessage) string {
	var f struct {
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	_ = json.Unmarshal(format, &f)
	var b strings.Builder
	if name == applyPatchName {
		description = strings.ReplaceAll(description, "This is a FREEFORM tool, so do not wrap the patch in JSON.", "")
		if strings.TrimSpace(description) != "" {
			b.WriteString(description)
			b.WriteString("\n\n")
		}
		b.WriteString(applyPatchInstructions)
		if f.Definition != "" {
			if strings.Contains(f.Definition, "*** Environment ID:") {
				b.WriteString("\n\nUse *** Environment ID: as specified by the patch grammar.")
			}
			b.WriteString("\n\nOriginal patch grammar:\n")
			b.WriteString(f.Definition)
		}
		return b.String()
	}
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString(d)
		b.WriteString("\n\n")
	}
	b.WriteString(customInstructions)
	if f.Definition != "" {
		b.WriteString("\n\nThe input must follow this")
		if s := strings.TrimSpace(f.Syntax); s != "" {
			b.WriteString(" " + s)
		}
		b.WriteString(" grammar:\n")
		b.WriteString(f.Definition)
	}
	return b.String()
}

func (p *plan) settle() {
	clientSearch := false
	for _, e := range p.entries {
		if e.d != nil && e.d.ref.Kind == KindSearch {
			clientSearch = true
		}
	}
	hidden := func(d *decl) bool { return clientSearch && d.deferred && d.priority < 2 }
	winners := map[string]*decl{}
	for _, e := range p.entries {
		if e.d == nil || hidden(e.d) {
			continue
		}
		if w := winners[e.d.sent]; w == nil || e.d.precedes(w) {
			winners[e.d.sent] = e.d
		}
	}
	described := map[string]bool{}
	for _, e := range p.entries {
		if e.d == nil {
			p.tools = append(p.tools, e.tool)
			continue
		}
		d := e.d
		if hidden(d) {
			continue
		}
		if winners[d.sent] != d {
			if d.priority < 2 {
				p.lose(llm.LossDropped, d.path, "another tool has the same name once flattened: "+d.sent)
			}
			continue
		}
		if d.deferred && !clientSearch && d.priority < 2 {
			p.lose(llm.LossApproximated, rawjson.Join(d.path, "defer_loading"), "no search can load it on this upstream, so it is declared from the start")
		}
		tool := d.tool
		if ns := d.ref.Namespace; ns != "" && d.nsDescription != "" && !described[ns] {
			described[ns] = true
			tool.Description = strings.TrimSpace("Tools in " + ns + ": " + d.nsDescription + "\n\n" + tool.Description)
		}
		p.tools = append(p.tools, tool)
		p.sent[d.sent] = true
		if d.ref.Kind != KindFunction || d.ref.Namespace != "" {
			p.refs[d.sent] = d.ref
		}
	}
}

func inputArguments(input string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(map[string]string{"input": input}) != nil {
		return "{}"
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func lowerItems(items []llm.Item) []llm.Item {
	out := make([]llm.Item, 0, len(items))
	for _, it := range items {
		if !fromResponses(it.Origin) {
			out = append(out, it)
			continue
		}
		switch it.Type {
		case llm.ItemTypeToolCall, llm.ItemTypeToolResult:
			out = append(out, unnamespaced(it))
		case llm.ItemTypeNative:
			if !lowersItem(it.Origin.Raw) {
				out = append(out, it)
				continue
			}
			if lowered, ok := lowerNative(it); ok {
				out = append(out, lowered)
			}
		default:
			out = append(out, it)
		}
	}
	return out
}

func unnamespaced(it llm.Item) llm.Item {
	if !bytes.Contains(it.Origin.Raw, []byte(`"namespace"`)) {
		return it
	}
	m := rawjson.Object(it.Origin.Raw)
	var namespace string
	_ = json.Unmarshal(m["namespace"], &namespace)
	if it.Type == llm.ItemTypeToolCall {
		it.Name = SentName(namespace, it.Name)
	}
	o := *it.Origin
	o.Raw = rawjson.Rest(m, "namespace")
	if it.Type == llm.ItemTypeToolResult {
		o.Raw = rawjson.Rest(m, "namespace", "name")
	}
	it.Origin = &o
	return it
}

func lowerNative(it llm.Item) (llm.Item, bool) {
	m := rawjson.Object(it.Origin.Raw)
	path := it.Origin.Path
	field := func(k string) string {
		var v string
		_ = json.Unmarshal(m[k], &v)
		return v
	}
	callID := field("call_id")
	bookkeeping := []string{"type", "id", "status"}
	switch field("type") {
	case "custom_tool_call":
		var input string
		if json.Unmarshal(m["input"], &input) != nil {
			input = string(m["input"])
		}
		return llm.Item{Type: llm.ItemTypeToolCall, ID: it.ID, CallID: callID, Name: SentName(field("namespace"), field("name")), Arguments: inputArguments(input),
			Origin: rawjson.Origin(responses, path, m, append(bookkeeping, "call_id", "name", "namespace", "input"), nil)}, true
	case "custom_tool_call_output":
		out := llm.Item{Type: llm.ItemTypeToolResult, ID: it.ID, CallID: callID}
		modelled := append(bookkeeping, "call_id", "name", "namespace")
		if responsesinput.ToolOutput(&out, m["output"], path) {
			modelled = append(modelled, "output")
		}
		out.Origin = rawjson.Origin(responses, path, m, modelled, nil)
		return out, true
	case "tool_search_call":
		return llm.Item{Type: llm.ItemTypeToolCall, ID: it.ID, CallID: callID, Name: SearchName, Arguments: searchArguments(m["arguments"]),
			Origin: rawjson.Origin(responses, path, m, append(bookkeeping, "call_id", "execution", "arguments"), nil)}, true
	case "tool_search_output":
		var tools []json.RawMessage
		_ = json.Unmarshal(m["tools"], &tools)
		return llm.Item{Type: llm.ItemTypeToolResult, ID: it.ID, CallID: callID, Output: searchOutput(tools),
			Origin: rawjson.Origin(responses, path, m, append(bookkeeping, "call_id", "execution", "tools"), nil)}, true
	}
	return llm.Item{}, false
}

func searchArguments(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}"
	}
	return string(raw)
}

func searchOutput(tools []json.RawMessage) string {
	var names []string
	for _, raw := range tools {
		var t struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(raw, &t) != nil {
			continue
		}
		if t.Type != "namespace" {
			if t.Name != "" {
				names = append(names, SentName("", t.Name))
			}
			continue
		}
		for _, c := range t.Tools {
			if c.Name != "" {
				names = append(names, SentName(t.Name, c.Name))
			}
		}
	}
	if len(names) == 0 {
		return "No tools matched the search."
	}
	return "Loaded these tools, which are now callable: " + strings.Join(names, ", ")
}

func (p *plan) lowerChoice(req *llm.Request) {
	if req.Options.ToolChoice != nil || !fromResponses(req.Origin) {
		return
	}
	m := rawjson.Object(req.Origin.Raw)
	raw, ok := m["tool_choice"]
	if !ok {
		return
	}
	var c struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Mode      string `json:"mode"`
		Tools     []struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			Function  struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return
	}
	var choice *llm.ToolChoice
	switch {
	case c.Type == "custom" || c.Type == "function" && c.Namespace != "":
		if sent := SentName(c.Namespace, c.Name); p.sent[sent] {
			choice = &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: sent}
		}
	case c.Type == "allowed_tools" && (c.Mode == llm.ToolChoiceAuto || c.Mode == llm.ToolChoiceRequired) && len(c.Tools) > 0:
		names := make([]string, 0, len(c.Tools))
		for _, t := range c.Tools {
			name := t.Name
			if name == "" {
				name = t.Function.Name
			}
			sent := SentName(t.Namespace, name)
			if t.Type != "function" && t.Type != "custom" || !p.sent[sent] {
				return
			}
			names = append(names, sent)
		}
		choice = &llm.ToolChoice{Mode: c.Mode, Allowed: names}
	}
	if choice == nil {
		return
	}
	req.Options.ToolChoice = choice
	o := *req.Origin
	o.Raw = rawjson.Rest(m, "tool_choice")
	req.Origin = &o
}

func CustomInput(arguments string) (input string, ok bool) {
	var d InputDecoder
	d.Push(arguments)
	if !d.Whole() {
		return "", false
	}
	return d.input.String(), true
}

func NotWholeError(name string, usage *llm.Usage) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Usage: usage,
		Message: "invalid arguments for custom tool " + name + " received from upstream"}
}

func SearchCallArguments(arguments string) (json.RawMessage, bool) {
	raw := bytes.TrimSpace([]byte(arguments))
	if rawjson.Object(raw) == nil {
		return json.RawMessage("{}"), false
	}
	return json.RawMessage(raw), true
}

func OutputPath(index int, field string) string {
	path := "output[" + strconv.Itoa(index) + "]"
	if field == "" {
		return path
	}
	return rawjson.Join(path, field)
}
