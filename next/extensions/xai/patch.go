package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Codex's patch tool, apply_patch, is a custom (freeform) tool: the model writes the patch text as
// the tool's input, in a grammar the client gives. xAI takes function tools only, so, as today's
// core does (internal/runtime/executor/helps/apply_patch_responses.go and the bridge in
// internal/translator/common), the extension:
//
//   - sends the tool as a function whose one argument, input, holds the patch text, with a
//     description that tells the model so and keeps the client's grammar;
//   - sends the tool's calls in the history as function calls with that argument, and a
//     tool_choice that names the tool as a function choice;
//   - turns xAI's function call back into a custom tool call in the reply, with the patch text as
//     the call's input and the events of a custom tool call (patchBridge).
//
// Only a Responses client declares a custom tool, so only its reply is turned back.

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

func isCustomApplyPatch(t map[string]any) bool {
	name, _ := t["name"].(string)
	return t["type"] == "custom" && strings.TrimSpace(name) == applyPatchName
}

// applyPatchDescription is the function's description: the client's own, minus its note that the
// tool takes no JSON, then the instructions for the input field and the client's grammar.
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

// rewriteApplyPatchTools sends the custom apply_patch tool as a function. When the request
// declares the name more than once (a function and a custom tool, say) only the first declaration
// stays, as in today's core, since xAI takes one tool of a name. It reports whether the tool the
// client will call is the custom one, which is when its calls have to be turned back.
func rewriteApplyPatchTools(tools []any) ([]any, bool) {
	custom := false
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok && isCustomApplyPatch(tm) {
			custom = true
		}
	}
	if !custom {
		return tools, false
	}
	out := make([]any, 0, len(tools))
	won, active := false, false
	for _, t := range tools {
		tm, ok := t.(map[string]any)
		if !ok || (tm["type"] != "function" && tm["type"] != "custom") || toolName(tm) != applyPatchName {
			out = append(out, t)
			continue
		}
		if won {
			continue
		}
		won = true
		if isCustomApplyPatch(tm) {
			active = true
			var params any
			_ = json.Unmarshal([]byte(applyPatchParameters), &params)
			tm["type"], tm["name"], tm["description"], tm["parameters"] = "function", applyPatchName, applyPatchDescription(tm), params
			delete(tm, "format")
		}
		out = append(out, tm)
	}
	return out, active
}

// rewriteApplyPatchChoice makes a tool_choice that names the custom apply_patch tool name it as a
// function, also inside the tools of an allowed_tools choice.
func rewriteApplyPatchChoice(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if name, _ := m["name"].(string); m["type"] == "custom" && strings.TrimSpace(name) == applyPatchName {
		m["type"] = "function"
	}
	if list, ok := m["tools"].([]any); ok {
		for _, c := range list {
			rewriteApplyPatchChoice(c)
		}
	}
}

// wrapPatchInput returns the arguments of a function call that carries patch text.
func wrapPatchInput(text string) string {
	b, _ := json.Marshal(map[string]string{"input": text})
	return string(b)
}

func patchArgumentsError() *llm.Error {
	return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "invalid apply_patch tool arguments received from upstream"}
}

// patchError is the error that ends a reply whose apply_patch arguments broke a rule. It says
// which kind of rule and never repeats the arguments.
func patchError(err error) *llm.Error {
	if errors.Is(err, errPatchTooLarge) {
		return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeEndpoint, Message: "apply_patch tool arguments received from upstream are too large"}
	}
	return patchArgumentsError()
}

// patchBridge turns the function calls to apply_patch in one xAI reply into custom tool calls. It
// reads the events the Responses decoder made, each with the upstream event in Raw, and changes
// what a Responses client is sent: the call's items, its input events, and the output of the
// final response. Events that are not about an apply_patch call pass unchanged. Use one bridge
// per reply.
type patchBridge struct {
	calls   []*patchCall
	byID    map[string]*patchCall // by item ID and by call ID
	byIndex map[int]*patchCall    // by output_index
	open    map[int]*patchCall    // by the model's item index, while the item is open
	// shift is how many events the bridge added, which move the sequence numbers after them.
	shift int
	// stream is true when the reply came as stream events; a reply read as one response has no
	// events to complete.
	stream bool
	// first is the call whose added event opened the stream, which the decoder reports on
	// response.start; its item.start, which has no raw, is the next one to claim it.
	first *patchCall
	// budget is how many more argument bytes the reply's calls may have.
	budget int
}

type patchCall struct {
	id, callID string
	index      int
	dec        inputDecoder
	input      string
	added      bool // the client has the item's added event
	inputDone  bool // the client has the input.done event
	itemDone   bool // the client has the item's done event
}

func newPatchBridge() *patchBridge {
	return &patchBridge{byID: map[string]*patchCall{}, byIndex: map[int]*patchCall{}, open: map[int]*patchCall{}, budget: maxReplyArguments}
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

func (b *patchBridge) track(id, callID string, index int) *patchCall {
	c := &patchCall{id: id, callID: callID, index: index}
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

// find returns the call an event or an item belongs to.
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

// Transform returns the events to send in place of ev.
func (b *patchBridge) Transform(ev llm.Event) ([]llm.Event, error) {
	switch ev.Type {
	case llm.EventResponseStart:
		b.stream = len(ev.Raw) > 0
		// A stream that does not open with response.created has its first event's raw here.
		if bytes.Contains(ev.Raw, []byte(`"response.output_item.added"`)) && bytes.Contains(ev.Raw, []byte(applyPatchName)) {
			if raw, c := b.begin(ev.Raw); c != nil {
				b.first = c
				ev.Raw = raw
				return []llm.Event{ev}, nil
			}
		}
	case llm.EventItemStart:
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall && ev.Item.Name == applyPatchName {
			switch {
			case len(ev.Raw) > 0:
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
		case bytes.Contains(ev.Raw, []byte(`"response.output_item.done"`)) && (len(b.calls) > 0 || bytes.Contains(ev.Raw, []byte(applyPatchName))):
			return b.end(nil, ev)
		}
	case llm.EventFinish:
		if len(b.calls) > 0 || bytes.Contains(ev.Raw, []byte(applyPatchName)) {
			return b.finish(ev)
		}
	}
	return b.pass(ev), nil
}

// pass sends ev as it is, with its sequence number moved past the events the bridge added.
func (b *patchBridge) pass(ev llm.Event) []llm.Event {
	if b.shift != 0 && len(ev.Raw) > 0 {
		if m, err := decodeObject(ev.Raw); err == nil {
			ev.Raw = b.encode(m)
		}
	}
	return []llm.Event{ev}
}

// encode writes an upstream event the bridge changed, moving its sequence number.
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

// synth makes native events of the events the bridge adds, numbering them from the sequence number
// of the event they come with (when it has one) and moving the numbers after them. With replaces
// true, the last of them takes the place of that event; otherwise that event follows them.
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

// toCustomItem rewrites a function call item as a custom tool call with the given input.
func toCustomItem(item map[string]any, input string) {
	item["type"] = "custom_tool_call"
	delete(item, "arguments")
	item["input"] = input
}

// begin starts the call an added event opens and returns the event as the client is sent it, or
// nil when the event is not the added event of a call to apply_patch.
func (b *patchBridge) begin(raw json.RawMessage) (json.RawMessage, *patchCall) {
	m, err := decodeObject(raw)
	if err != nil || str(m, "type") != "response.output_item.added" {
		return nil, nil
	}
	item, _ := m["item"].(map[string]any)
	if item == nil || str(item, "type") != "function_call" || str(item, "name") != applyPatchName {
		return nil, nil
	}
	index, _ := intOf(m, "output_index")
	c := b.track(str(item, "id"), str(item, "call_id"), index)
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
		return nil, patchArgumentsError()
	}
	text, err := c.dec.Push(str(m, "delta"))
	if err != nil {
		return nil, patchError(err)
	}
	if text == "" {
		ev.Raw = nil // the fragment holds no patch text yet; the client gets no event
		return []llm.Event{ev}, nil
	}
	ev.Raw = b.encode(b.inputEvent("delta", c, m, "delta", text))
	return []llm.Event{ev}, nil
}

// inputEvent turns an upstream arguments event into the custom tool call input event of the same
// kind, or makes one.
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

// settleInput checks the final arguments of a call and returns the input events the client does
// not have yet: the last of the text, then the end of the input. arguments is empty and has is
// false when the event has none, which leaves the fragments as they came.
func (b *patchBridge) settleInput(c *patchCall, arguments string, has bool) ([]map[string]any, error) {
	if c.inputDone && !has {
		return nil, nil // the arguments were checked when they were reported whole
	}
	input, tail, err := c.dec.Finish(arguments, has)
	if err != nil {
		return nil, patchError(err)
	}
	if c.inputDone {
		if input != c.input {
			return nil, patchArgumentsError() // the call's text changed after it was completed
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

// end handles the done event of an item, which may be one the stream never opened (c is nil then
// and the item says whether it is a call to apply_patch).
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
			if str(item, "type") != "function_call" || str(item, "name") != applyPatchName {
				return b.pass(ev), nil
			}
			c = b.track(str(item, "id"), str(item, "call_id"), index)
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

// settle brings a call up to date with the final item xAI reports for it. It rewrites item as a
// custom tool call and returns the events the client has not got yet: the added event and the
// input events.
func (b *patchBridge) settle(c *patchCall, item map[string]any) ([]map[string]any, error) {
	if str(item, "type") != "function_call" {
		return nil, patchArgumentsError()
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

// finish rewrites the output of the final response, which holds the calls as items, and first
// completes the calls the stream did not finish.
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
	// A call the stream opened that the final output does not list is added to it, as the call it
	// was, so the client gets it however the final response was written.
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
		stub := map[string]any{"type": "function_call", "status": "completed", "name": applyPatchName, "id": c.id, "call_id": c.callID}
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
			if str(item, "name") != applyPatchName {
				continue
			}
			c = b.track(str(item, "id"), str(item, "call_id"), i)
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
			return nil, patchArgumentsError() // a call that never got its arguments
		}
	}
	if !b.stream {
		adds = nil // a reply read as one response has no events to complete
	}
	out := b.synth(m, adds, false)
	ev.Raw = b.encode(m)
	return append(out, ev), nil
}

// replyBridge changes the events of one reply for the client: the namespace restorer first, which
// changes events one for one, then the apply_patch bridge. Either may be nil.
type replyBridge struct {
	patch *patchBridge
	ns    *nsRestorer
}

func (r *replyBridge) Transform(ev llm.Event) ([]llm.Event, error) {
	if r.ns != nil {
		var err error
		if ev, err = r.ns.Transform(ev); err != nil {
			return nil, err
		}
	}
	if r.patch == nil {
		return []llm.Event{ev}, nil
	}
	return r.patch.Transform(ev)
}

// transformer changes the events of a reply.
type transformer interface {
	Transform(llm.Event) ([]llm.Event, error)
}

// bridgeSender sends the events a bridge makes of each event it is given.
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
