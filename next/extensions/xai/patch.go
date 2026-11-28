package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const applyPatchName = "apply_patch"

const applyPatchParameters = `{"type":"object","properties":{"input":{"type":"string","description":"The complete apply_patch patch text."}},"required":["input"],"additionalProperties":false}`

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

func toolName(t map[string]any) string {
	name, _ := t["name"].(string)
	if name == "" {
		if fn, ok := t["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
	}
	return strings.TrimSpace(name)
}

const customParameters = `{"type":"object","properties":{"input":{"type":"string","description":"The complete input text for the tool."}},"required":["input"],"additionalProperties":false}`

const customInstructions = `Call this function with a JSON object whose input field contains the complete input text for the tool.`

func isCustomTool(t map[string]any) bool {
	return t["type"] == "custom" && strings.TrimSpace(str(t, "name")) != ""
}

func customDescription(t map[string]any) string {
	var b strings.Builder
	if original := strings.TrimSpace(str(t, "description")); original != "" {
		b.WriteString(original)
		b.WriteString("\n\n")
	}
	b.WriteString(customInstructions)
	if f, ok := t["format"].(map[string]any); ok {
		if grammar := str(f, "definition"); grammar != "" {
			b.WriteString("\n\nThe input must follow this")
			if syntax := strings.TrimSpace(str(f, "syntax")); syntax != "" {
				b.WriteString(" " + syntax)
			}
			b.WriteString(" grammar:\n")
			b.WriteString(grammar)
		}
	}
	return b.String()
}

func applyPatchDescription(t map[string]any) string {
	original, _ := t["description"].(string)
	original = strings.ReplaceAll(original, "This is a FREEFORM tool, so do not wrap the patch in JSON.", "")
	var b strings.Builder
	if strings.TrimSpace(original) != "" {
		b.WriteString(original)
		b.WriteString("\n\n")
	}
	b.WriteString(applyPatchInstructions)
	if f, ok := t["format"].(map[string]any); ok {
		if grammar, _ := f["definition"].(string); grammar != "" {
			if strings.Contains(grammar, "*** Environment ID:") {
				b.WriteString("\n\nUse *** Environment ID: as specified by the patch grammar.")
			}
			b.WriteString("\n\nOriginal patch grammar:\n")
			b.WriteString(grammar)
		}
	}
	return b.String()
}

type customTools struct {
	top        map[string]bool
	namespaces map[string]map[string]bool
}

func (p customTools) active() bool { return len(p.top) > 0 || len(p.namespaces) > 0 }

func (p customTools) names(namespace, name string) bool {
	name = strings.TrimSpace(name)
	if namespace = strings.TrimSpace(namespace); namespace != "" {
		return p.namespaces[namespace][name]
	}
	return p.top[name]
}

func (p customTools) list() []string {
	seen := map[string]bool{}
	var out []string
	add := func(set map[string]bool) {
		for name := range set {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	add(p.top)
	for _, set := range p.namespaces {
		add(set)
	}
	return out
}

func (p *customTools) add(namespace, name string) {
	if namespace == "" {
		if p.top == nil {
			p.top = map[string]bool{}
		}
		p.top[name] = true
		return
	}
	if p.namespaces == nil {
		p.namespaces = map[string]map[string]bool{}
	}
	if p.namespaces[namespace] == nil {
		p.namespaces[namespace] = map[string]bool{}
	}
	p.namespaces[namespace][name] = true
}

type patchDecl struct {
	key              string
	name             string
	child            int
	priority, order  int
	direct, isCustom bool
}

func (d patchDecl) precedes(o patchDecl) bool {
	if d.priority != o.priority {
		return d.priority < o.priority
	}
	if d.direct != o.direct {
		return d.direct
	}
	return d.order < o.order
}

func rewriteCustomTools(tools []any, topLevel int) ([]any, customTools) {
	decls := map[[2]int]patchDecl{}
	affected := map[string]bool{}
	winners := map[string]patchDecl{}
	winnerAt := map[string][2]int{}
	order := 0
	declare := func(at [2]int, tm map[string]any, namespace string, name string, direct bool) {
		if name == "" || !isTool(tm) {
			return
		}
		key := name
		if !direct {
			key = qualifyName(namespace, name)
		}
		d := patchDecl{key: key, name: name, child: at[1], order: order, direct: direct, isCustom: isCustomTool(tm)}
		if at[0] >= topLevel {
			d.priority = 1
		}
		order++
		decls[at] = d
		if d.isCustom {
			affected[key] = true
		}
	}
	for i, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		if tm["type"] == namespaceType {
			children, _ := tm["tools"].([]any)
			for j, c := range children {
				if cm, ok := c.(map[string]any); ok {
					declare([2]int{i, j}, cm, strings.TrimSpace(str(tm, "name")), strings.TrimSpace(str(cm, "name")), false)
				}
			}
			continue
		}
		declare([2]int{i, -1}, tm, "", toolName(tm), true)
	}
	if len(affected) == 0 {
		return tools, customTools{}
	}
	for at, d := range decls {
		if w, ok := winners[d.key]; affected[d.key] && (!ok || d.precedes(w)) {
			winners[d.key], winnerAt[d.key] = d, at
		}
	}
	keep := func(at [2]int) bool {
		d, ok := decls[at]
		return !ok || !affected[d.key] || winnerAt[d.key] == at
	}
	var set customTools
	convert := func(at [2]int, tm map[string]any, namespace string) {
		d := decls[at]
		if !d.isCustom || winnerAt[d.key] != at {
			return
		}
		set.add(namespace, d.name)
		parameters, description := customParameters, customDescription(tm)
		if d.name == applyPatchName {
			parameters, description = applyPatchParameters, applyPatchDescription(tm)
		}
		var params any
		_ = json.Unmarshal([]byte(parameters), &params)
		tm["type"], tm["name"], tm["description"], tm["parameters"] = "function", d.name, description, params
		delete(tm, "format")
	}
	out := make([]any, 0, len(tools))
	for i, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok {
			out = append(out, t)
			continue
		}
		if tm["type"] != namespaceType {
			if keep([2]int{i, -1}) {
				convert([2]int{i, -1}, tm, "")
				out = append(out, tm)
			}
			continue
		}
		children, _ := tm["tools"].([]any)
		kept := make([]any, 0, len(children))
		for j, c := range children {
			if keep([2]int{i, j}) {
				if cm, ok := c.(map[string]any); ok {
					convert([2]int{i, j}, cm, strings.TrimSpace(str(tm, "name")))
				}
				kept = append(kept, c)
			}
		}
		if len(kept) != len(children) {
			tm["tools"] = kept
		}
		out = append(out, tm)
	}
	return out, set
}

func (p customTools) rewriteChoice(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if m["type"] == "custom" && p.names(str(m, "namespace"), str(m, "name")) {
		m["type"] = "function"
	}
	if list, ok := m["tools"].([]any); ok {
		for _, c := range list {
			p.rewriteChoice(c)
		}
	}
}

func wrapPatchInput(text string) string {
	b, _ := json.Marshal(map[string]string{"input": text})
	return string(b)
}

func toolRef(name string) string {
	if name == "" {
		return "a custom tool"
	}
	return "custom tool " + name
}

func patchArgumentsError(name string) *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Message: "invalid arguments for " + toolRef(name) + " received from upstream"}
}

func patchError(err error, name string) *llm.Error {
	if errors.Is(err, errPatchTooLarge) {
		return &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, Scope: llm.ScopeRequest, Message: "arguments for " + toolRef(name) + " received from upstream are too large"}
	}
	return patchArgumentsError(name)
}

type patchBridge struct {
	tools   customTools
	needles [][]byte
	calls   []*patchCall
	byID    map[string]*patchCall
	byIndex map[int]*patchCall
	open    map[int]*patchCall
	shift   int
	stream  bool
	first   *patchCall
	budget  int
}

type patchCall struct {
	id, callID string
	name       string
	namespace  string
	index      int
	dec        inputDecoder
	input      string
	added      bool
	inputDone  bool
	itemDone   bool
}

func newPatchBridge(tools customTools) *patchBridge {
	b := &patchBridge{tools: tools, byID: map[string]*patchCall{}, byIndex: map[int]*patchCall{}, open: map[int]*patchCall{}, budget: maxReplyArguments}
	for _, name := range tools.list() {
		b.needles = append(b.needles, []byte(name))
		if quoted, err := json.Marshal(name); err == nil && string(quoted) != `"`+name+`"` {
			b.needles = append(b.needles, quoted[1:len(quoted)-1])
		}
	}
	return b
}

func (b *patchBridge) mentions(raw []byte) bool {
	for _, n := range b.needles {
		if bytes.Contains(raw, n) {
			return true
		}
	}
	return false
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func intOf(m map[string]any, k string) (int, bool) {
	n, ok := m[k].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}

func (b *patchBridge) track(item map[string]any, index int) *patchCall {
	id, callID := str(item, "id"), str(item, "call_id")
	c := &patchCall{id: id, callID: callID, name: strings.TrimSpace(str(item, "name")), namespace: str(item, "namespace"), index: index}
	c.dec.shared = &b.budget
	b.calls = append(b.calls, c)
	if id != "" {
		b.byID[id] = c
	}
	if callID != "" {
		b.byID[callID] = c
	}
	b.byIndex[index] = c
	return c
}

func (b *patchBridge) find(id, callID string, index int, hasIndex bool) *patchCall {
	if c := b.byID[id]; c != nil && id != "" {
		return c
	}
	if c := b.byID[callID]; c != nil && callID != "" {
		return c
	}
	if hasIndex {
		return b.byIndex[index]
	}
	return nil
}

func (b *patchBridge) Transform(ev llm.Event) ([]llm.Event, error) {
	switch ev.Type {
	case llm.EventResponseStart:
		b.stream = len(ev.Raw) > 0
		if bytes.Contains(ev.Raw, []byte(`"response.output_item.added"`)) && b.mentions(ev.Raw) {
			if raw, c := b.begin(ev.Raw); c != nil {
				b.first = c
				ev.Raw = raw
				return []llm.Event{ev}, nil
			}
		}
	case llm.EventItemStart:
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
			switch {
			case len(ev.Raw) > 0 && b.mentions(ev.Raw):
				return b.start(ev)
			case b.first != nil:
				b.open[ev.Index], b.first = b.first, nil
				return []llm.Event{ev}, nil
			}
		}
	case llm.EventToolInputDelta:
		if c := b.open[ev.Index]; c != nil {
			return b.delta(c, ev)
		}
	case llm.EventItemEnd:
		if c := b.open[ev.Index]; c != nil {
			delete(b.open, ev.Index)
			if len(ev.Raw) > 0 {
				return b.end(c, ev)
			}
		}
	case llm.EventNative:
		switch {
		case bytes.Contains(ev.Raw, []byte(`"response.function_call_arguments.done"`)):
			return b.argumentsDone(ev)
		case bytes.Contains(ev.Raw, []byte(`"response.output_item.done"`)) && (len(b.calls) > 0 || b.mentions(ev.Raw)):
			return b.end(nil, ev)
		}
	case llm.EventFinish:
		if len(b.calls) > 0 || b.mentions(ev.Raw) {
			return b.finish(ev)
		}
	}
	return b.pass(ev), nil
}

func (b *patchBridge) pass(ev llm.Event) []llm.Event {
	if b.shift != 0 && len(ev.Raw) > 0 {
		if m, err := decodeObject(ev.Raw); err == nil {
			ev.Raw = b.encode(m)
		}
	}
	return []llm.Event{ev}
}

func (b *patchBridge) encode(m map[string]any) json.RawMessage {
	if n, ok := intOf(m, "sequence_number"); ok {
		m["sequence_number"] = n + b.shift
	}
	out, err := marshal(m)
	if err != nil {
		return nil
	}
	return out
}

func (b *patchBridge) synth(trigger map[string]any, adds []map[string]any, replaces bool) []llm.Event {
	seq, numbered := intOf(trigger, "sequence_number")
	var out []llm.Event
	for i, m := range adds {
		if numbered {
			m["sequence_number"] = seq + b.shift
		}
		if !replaces || i < len(adds)-1 {
			b.shift++
		}
		raw, err := marshal(m)
		if err != nil {
			continue
		}
		out = append(out, llm.Event{Type: llm.EventNative, Raw: raw})
	}
	return out
}

func (b *patchBridge) isPatchCall(item map[string]any) bool {
	return str(item, "type") == "function_call" && b.tools.names(str(item, "namespace"), str(item, "name"))
}

func toCustomItem(item map[string]any, input string) {
	item["type"] = "custom_tool_call"
	delete(item, "arguments")
	item["input"] = input
}

func (b *patchBridge) begin(raw json.RawMessage) (json.RawMessage, *patchCall) {
	m, err := decodeObject(raw)
	if err != nil || str(m, "type") != "response.output_item.added" {
		return nil, nil
	}
	item, _ := m["item"].(map[string]any)
	if item == nil || !b.isPatchCall(item) {
		return nil, nil
	}
	index, _ := intOf(m, "output_index")
	c := b.track(item, index)
	c.added = true
	toCustomItem(item, "")
	return b.encode(m), c
}

func (b *patchBridge) start(ev llm.Event) ([]llm.Event, error) {
	raw, c := b.begin(ev.Raw)
	if c == nil {
		return b.pass(ev), nil
	}
	b.open[ev.Index] = c
	ev.Raw = raw
	return []llm.Event{ev}, nil
}

func (b *patchBridge) delta(c *patchCall, ev llm.Event) ([]llm.Event, error) {
	m, err := decodeObject(ev.Raw)
	if err != nil {
		return nil, patchArgumentsError(c.name)
	}
	text, err := c.dec.Push(str(m, "delta"))
	if err != nil {
		return nil, patchError(err, c.name)
	}
	if text == "" {
		ev.Raw = nil
		return []llm.Event{ev}, nil
	}
	ev.Raw = b.encode(b.inputEvent("delta", c, m, "delta", text))
	return []llm.Event{ev}, nil
}

func (b *patchBridge) inputEvent(kind string, c *patchCall, m map[string]any, field, text string) map[string]any {
	e := map[string]any{}
	if m != nil {
		e = m
	}
	delete(e, "delta")
	delete(e, "arguments")
	e["type"] = "response.custom_tool_call_input." + kind
	e["item_id"], e["call_id"], e["output_index"], e[field] = c.id, c.callID, c.index, text
	return e
}

func (b *patchBridge) argumentsDone(ev llm.Event) ([]llm.Event, error) {
	m, err := decodeObject(ev.Raw)
	if err != nil || str(m, "type") != "response.function_call_arguments.done" {
		return b.pass(ev), nil
	}
	index, hasIndex := intOf(m, "output_index")
	c := b.find(str(m, "item_id"), str(m, "call_id"), index, hasIndex)
	if c == nil {
		return b.pass(ev), nil
	}
	adds, err := b.settleInput(c, str(m, "arguments"), m["arguments"] != nil)
	if err != nil {
		return nil, err
	}
	return b.synth(m, adds, true), nil
}

func (b *patchBridge) settleInput(c *patchCall, arguments string, has bool) ([]map[string]any, error) {
	if c.inputDone && !has {
		return nil, nil
	}
	input, tail, err := c.dec.Finish(arguments, has)
	if err != nil {
		return nil, patchError(err, c.name)
	}
	if c.inputDone {
		if input != c.input {
			return nil, patchArgumentsError(c.name)
		}
		return nil, nil
	}
	c.inputDone, c.input = true, input
	var adds []map[string]any
	if tail != "" {
		adds = append(adds, b.inputEvent("delta", c, nil, "delta", tail))
	}
	return append(adds, b.inputEvent("done", c, nil, "input", input)), nil
}

func (b *patchBridge) end(c *patchCall, ev llm.Event) ([]llm.Event, error) {
	m, err := decodeObject(ev.Raw)
	if err != nil || str(m, "type") != "response.output_item.done" {
		return b.pass(ev), nil
	}
	item, _ := m["item"].(map[string]any)
	if item == nil {
		return b.pass(ev), nil
	}
	if c == nil {
		index, hasIndex := intOf(m, "output_index")
		if c = b.find(str(item, "id"), str(item, "call_id"), index, hasIndex); c == nil {
			if !b.isPatchCall(item) {
				return b.pass(ev), nil
			}
			c = b.track(item, index)
		}
	}
	adds, err := b.settle(c, item)
	if err != nil {
		return nil, err
	}
	c.itemDone = true
	out := b.synth(m, adds, false)
	ev.Raw = b.encode(m)
	return append(out, ev), nil
}

func (b *patchBridge) settle(c *patchCall, item map[string]any) ([]map[string]any, error) {
	if str(item, "type") != "function_call" {
		return nil, patchArgumentsError(c.name)
	}
	arguments, has := item["arguments"].(string)
	adds, err := b.settleInput(c, arguments, has)
	if err != nil {
		return nil, err
	}
	toCustomItem(item, c.input)
	if c.id != "" {
		item["id"] = c.id
	}
	if c.callID != "" {
		item["call_id"] = c.callID
	}
	if !c.added {
		c.added = true
		added := map[string]any{}
		for k, v := range item {
			added[k] = v
		}
		added["input"], added["status"] = "", "in_progress"
		adds = append([]map[string]any{{"type": "response.output_item.added", "output_index": c.index, "item": added}}, adds...)
	}
	return adds, nil
}

func (b *patchBridge) finish(ev llm.Event) ([]llm.Event, error) {
	m, err := decodeObject(ev.Raw)
	if err != nil {
		return b.pass(ev), nil
	}
	resp := m
	if r, ok := m["response"].(map[string]any); ok {
		resp = r
	}
	output, _ := resp["output"].([]any)
	listed := map[*patchCall]bool{}
	for _, raw := range output {
		if item, ok := raw.(map[string]any); ok {
			if c := b.find(str(item, "id"), str(item, "call_id"), 0, false); c != nil {
				listed[c] = true
			}
		}
	}
	for _, c := range b.calls {
		if listed[c] {
			continue
		}
		stub := map[string]any{"type": "function_call", "status": "completed", "name": c.name, "id": c.id, "call_id": c.callID}
		if c.namespace != "" {
			stub["namespace"] = c.namespace
		}
		at := min(max(c.index, 0), len(output))
		output = append(output[:at], append([]any{stub}, output[at:]...)...)
		resp["output"] = output
	}
	var adds []map[string]any
	for i, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || str(item, "type") != "function_call" {
			continue
		}
		c := b.find(str(item, "id"), str(item, "call_id"), 0, false)
		if c == nil {
			if !b.isPatchCall(item) {
				continue
			}
			c = b.track(item, i)
		}
		more, err := b.settle(c, item)
		if err != nil {
			return nil, err
		}
		adds = append(adds, more...)
		if !c.itemDone {
			c.itemDone = true
			done := map[string]any{}
			for k, v := range item {
				done[k] = v
			}
			adds = append(adds, map[string]any{"type": "response.output_item.done", "output_index": c.index, "item": done})
		}
	}
	for _, c := range b.calls {
		if !c.inputDone {
			return nil, patchArgumentsError(c.name)
		}
	}
	if !b.stream {
		adds = nil
	}
	out := b.synth(m, adds, false)
	ev.Raw = b.encode(m)
	return append(out, ev), nil
}

type dispatchHold struct {
	dispatchers map[string]bool
	patch       customTools
	refs        map[string]*nsRef
	ns          *nsRestorer
	byIndex     map[int]*heldCall
	byID        map[string]*heldCall
	calls       []*heldCall
}

type heldCall struct {
	id, callID string
	namespace  string
	events     []llm.Event
	bytes      int
	decided    bool
	patch      bool
	name       string
}

func newDispatchHold(dispatchers map[string]bool, patch customTools, refs map[string]*nsRef, ns *nsRestorer) *dispatchHold {
	return &dispatchHold{dispatchers: dispatchers, patch: patch, refs: refs, ns: ns, byIndex: map[int]*heldCall{}, byID: map[string]*heldCall{}}
}

func (h *dispatchHold) dispatcherItem(raw json.RawMessage) (map[string]any, map[string]any) {
	m, err := decodeObject(raw)
	if err != nil || str(m, "type") != "response.output_item.added" {
		return nil, nil
	}
	item, _ := m["item"].(map[string]any)
	if item == nil || str(item, "type") != "function_call" || !h.dispatchers[strings.TrimSpace(str(item, "name"))] {
		return nil, nil
	}
	return m, item
}

func (h *dispatchHold) find(id, callID string) *heldCall {
	if c := h.byID[id]; c != nil && id != "" {
		return c
	}
	if callID != "" {
		return h.byID[callID]
	}
	return nil
}

func (h *dispatchHold) decide(c *heldCall, arguments string) []llm.Event {
	c.decided = true
	child, _, ok := unwrapDispatcher(arguments, h.refs[c.namespace])
	if ok && h.ns != nil {
		for _, id := range []string{c.id, c.callID} {
			if id != "" {
				h.ns.children[id] = child
			}
		}
	}
	c.patch = ok && h.patch.names(c.namespace, child)
	if c.patch {
		c.name = child
	}
	events := c.events
	c.events = nil
	if c.patch {
		kept := events[:0]
		for _, ev := range events {
			if ev.Type != llm.EventToolInputDelta {
				kept = append(kept, ev)
			}
		}
		events = kept
	}
	return events
}

func (h *dispatchHold) Transform(ev llm.Event) ([]llm.Event, error) {
	switch ev.Type {
	case llm.EventResponseStart:
		if bytes.Contains(ev.Raw, []byte(`"response.output_item.added"`)) {
			if _, item := h.dispatcherItem(ev.Raw); item != nil {
				return nil, &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "upstream opened with a call to a namespace that has a custom tool, before response.created"}
			}
		}
	case llm.EventItemStart:
		if len(ev.Raw) == 0 {
			break
		}
		if _, item := h.dispatcherItem(ev.Raw); item != nil {
			c := &heldCall{id: str(item, "id"), callID: str(item, "call_id"), namespace: strings.TrimSpace(str(item, "name")), events: []llm.Event{ev}}
			h.calls = append(h.calls, c)
			h.byIndex[ev.Index] = c
			for _, id := range []string{c.id, c.callID} {
				if id != "" {
					h.byID[id] = c
				}
			}
			return nil, nil
		}
	case llm.EventToolInputDelta:
		c := h.byIndex[ev.Index]
		switch {
		case c == nil:
		case !c.decided:
			if c.bytes += len(ev.Delta); c.bytes > maxCallArguments {
				return nil, patchError(errPatchTooLarge, "")
			}
			c.events = append(c.events, ev)
			return nil, nil
		case c.patch:
			if ev.Delta != "" {
				return nil, patchArgumentsError(c.name)
			}
			return nil, nil
		}
	case llm.EventItemEnd:
		c := h.byIndex[ev.Index]
		if c == nil {
			break
		}
		if c.decided {
			delete(h.byIndex, ev.Index)
			break
		}
		if len(ev.Raw) == 0 {
			c.events = append(c.events, ev)
			return nil, nil
		}
		delete(h.byIndex, ev.Index)
		m, err := decodeObject(ev.Raw)
		if err != nil {
			return nil, patchArgumentsError("")
		}
		item, _ := m["item"].(map[string]any)
		return append(h.decide(c, str(item, "arguments")), ev), nil
	case llm.EventNative:
		if !bytes.Contains(ev.Raw, []byte(`"response.function_call_arguments.done"`)) {
			break
		}
		m, err := decodeObject(ev.Raw)
		if err != nil || str(m, "type") != "response.function_call_arguments.done" {
			break
		}
		if c := h.find(str(m, "item_id"), str(m, "call_id")); c != nil && !c.decided {
			return append(h.decide(c, str(m, "arguments")), ev), nil
		}
	case llm.EventFinish:
		if len(h.calls) == 0 {
			break
		}
		var out []llm.Event
		m, err := decodeObject(ev.Raw)
		if err != nil {
			m = nil
		}
		resp := m
		if r, ok := m["response"].(map[string]any); ok {
			resp = r
		}
		output, _ := resp["output"].([]any)
		for _, c := range h.calls {
			if c.decided {
				continue
			}
			arguments := ""
			for _, raw := range output {
				if item, ok := raw.(map[string]any); ok && h.find(str(item, "id"), str(item, "call_id")) == c {
					arguments = str(item, "arguments")
				}
			}
			out = append(out, h.decide(c, arguments)...)
		}
		return append(out, ev), nil
	}
	return []llm.Event{ev}, nil
}

type replyBridge struct {
	hold  *dispatchHold
	patch *patchBridge
	ns    *nsRestorer
}

func (r *replyBridge) Transform(ev llm.Event) ([]llm.Event, error) {
	events := []llm.Event{ev}
	if r.hold != nil {
		var err error
		if events, err = r.hold.Transform(ev); err != nil {
			return nil, err
		}
	}
	var out []llm.Event
	for _, e := range events {
		if r.ns != nil {
			var err error
			if e, err = r.ns.Transform(e); err != nil {
				return nil, err
			}
		}
		if r.patch == nil {
			out = append(out, e)
			continue
		}
		more, err := r.patch.Transform(e)
		if err != nil {
			return nil, err
		}
		out = append(out, more...)
	}
	return out, nil
}

type transformer interface {
	Transform(llm.Event) ([]llm.Event, error)
}

type bridgeSender struct {
	b    transformer
	next interface{ Send(llm.Event) error }
}

func (s bridgeSender) Send(ev llm.Event) error {
	out, err := s.b.Transform(ev)
	if err != nil {
		return err
	}
	for _, e := range out {
		if err := s.next.Send(e); err != nil {
			return err
		}
	}
	return nil
}
