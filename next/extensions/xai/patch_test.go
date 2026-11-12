package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
)

const grammar = "start: patch\npatch: /.+/"

// patchRequest is a Responses request that declares Codex's custom apply_patch tool.
const patchRequest = `{"model":"grok-4.6","stream":true,"input":[{"role":"user","content":"Edit it."}],"tools":[
	{"type":"custom","name":"apply_patch","description":"Edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
	 "format":{"type":"grammar","syntax":"lark","definition":"start: patch\npatch: /.+/"}}]}`

func TestApplyPatchDeclaration(t *testing.T) {
	got := responsesBody(t, patchRequest)
	var m struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(got, &m); err != nil || len(m.Tools) != 1 {
		t.Fatalf("tools: %v: %s", err, got)
	}
	tool := m.Tools[0]
	if tool["type"] != "function" || tool["name"] != "apply_patch" {
		t.Errorf("tool = %v, want a function named apply_patch", tool)
	}
	if _, ok := tool["format"]; ok {
		t.Errorf("the grammar is still in the tool's format field: %v", tool)
	}
	want := "Edit files. \n\nCall this function with a JSON object whose input field contains the complete patch text.\n" +
		"Use the Codex apply_patch format, not a conventional git unified diff.\n" +
		"Start with *** Begin Patch and end with *** End Patch.\n" +
		"Use *** Add File: path, *** Delete File: path, or *** Update File: path.\n" +
		"Every added-file content line starts with +.\n" +
		"For updates, use @@; context lines start with one space, removed lines with -, and added lines with +.\n" +
		"Use *** Move to: path for a rename and *** End of File when required by the patch grammar.\n" +
		"Example input:\n*** Begin Patch\n*** Update File: src/main.go\n@@\n-old\n+new\n*** End Patch\n\n" +
		"Original patch grammar:\n" + grammar
	if tool["description"] != want {
		t.Errorf("description:\n got %q\nwant %q", tool["description"], want)
	}
	params, _ := json.Marshal(tool["parameters"])
	sameJSON(t, "parameters", params, `{"type":"object","properties":{"input":{"type":"string","description":"The complete apply_patch patch text."}},"required":["input"],"additionalProperties":false}`)
}

// A call to apply_patch in the history carries its text in the input argument, also when the text
// is itself a JSON object, and its output goes as a function call output. A call to a custom tool
// the request does not declare keeps the rule it had.
func TestApplyPatchHistory(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[
		{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"{\"input\":\"raw\"}"},
		{"type":"custom_tool_call_output","call_id":"c1","output":"Done!"},
		{"type":"custom_tool_call","call_id":"c2","name":"apply_patch","input":"*** Begin Patch\n*** End Patch\n"}]}`)
	sameJSON(t, "history", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[
		{"type":"function_call","call_id":"c1","name":"apply_patch","arguments":"{\"input\":\"{\\\"input\\\":\\\"raw\\\"}\"}"},
		{"type":"function_call_output","call_id":"c1","output":"Done!"},
		{"type":"function_call","call_id":"c2","name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n*** End Patch\\n\"}"}]}`)

	req, err := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[{"type":"custom_tool_call","call_id":"c","name":"apply_patch","input":{"x":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildBody(req); err == nil || !strings.Contains(err.Error(), "apply_patch history input must be a string") {
		t.Errorf("a history call whose input is not a string: error %v, want one saying so", err)
	}
}

func TestApplyPatchToolChoice(t *testing.T) {
	for _, c := range []struct{ choice, want string }{
		{`{"type":"custom","name":"apply_patch"}`, `{"type":"function","name":"apply_patch"}`},
		{`{"type":"allowed_tools","mode":"auto","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"f"}]}`,
			`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"apply_patch"},{"type":"function","name":"f"}]}`},
		{`"required"`, `"required"`},
	} {
		got := responsesBody(t, `{"model":"grok-4.6","input":[],"tool_choice":`+c.choice+`,"tools":[{"type":"custom","name":"apply_patch"}]}`)
		var m struct {
			Choice json.RawMessage `json:"tool_choice"`
		}
		_ = json.Unmarshal(got, &m)
		sameJSON(t, "tool_choice "+c.choice, m.Choice, c.want)
	}
}

// xAI takes one tool of a name, so the first declaration of apply_patch stays; the reply is turned
// back only when the one that stays is the custom tool.
func TestApplyPatchDuplicates(t *testing.T) {
	for _, c := range []struct {
		name, tools string
		active      bool
		want        []string
	}{
		{"custom first", `[{"type":"custom","name":"apply_patch"},{"type":"function","name":"apply_patch","parameters":{"type":"object"}},{"type":"function","name":"other"}]`,
			true, []string{"apply_patch", "other"}},
		{"function first", `[{"type":"function","name":"apply_patch","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch"}]`,
			false, []string{"apply_patch"}},
		{"function only", `[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]`, false, []string{"apply_patch"}},
		{"another custom tool", `[{"type":"custom","name":"run_shell"}]`, true, []string{"run_shell"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[],"tools":` + c.tools + `}`))
			if err != nil {
				t.Fatal(err)
			}
			body, _, patch, err := buildRequest(req, false)
			if err != nil {
				t.Fatal(err)
			}
			if (patch != nil) != c.active {
				t.Errorf("reply turned back = %v, want %v", patch != nil, c.active)
			}
			var m struct {
				Tools []struct{ Type, Name string } `json:"tools"`
			}
			_ = json.Unmarshal(body, &m)
			var names []string
			for _, tool := range m.Tools {
				if tool.Type != "function" {
					t.Errorf("tool %v is not a function", tool)
				}
				names = append(names, tool.Name)
			}
			if strings.Join(names, ",") != strings.Join(c.want, ",") {
				t.Errorf("tools = %v, want %v", names, c.want)
			}
		})
	}
}

// replyStream reads an upstream stream through the Responses decoder and the bridge, the way
// generate does, and returns what a Responses client of req is sent: its events, one line each,
// with the sequence number, the type and what the event is about.
func replyStream(t *testing.T, requestBody, upstream string) ([]string, error) {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	_, _, patch, err := buildRequest(req, false)
	if err != nil {
		t.Fatal(err)
	}
	if patch == nil {
		t.Fatal("the request has nothing to turn back in the reply")
	}
	var out bytes.Buffer
	w := openairesponses.NewStreamWriter(&out, req)
	err = relayStream(context.Background(), strings.NewReader(upstream), bridgeSender{b: patch, next: writerSender{w}}, openairesponses.NewStreamDecoder())
	return summarize(t, &out), err
}

type writerSender struct{ w *openairesponses.StreamWriter }

func (s writerSender) Send(ev llm.Event) error { return s.w.Write(ev) }

// summarize reads the events a client was sent.
func summarize(t *testing.T, stream *bytes.Buffer) []string {
	t.Helper()
	r := sse.NewReader(stream)
	var lines []string
	for {
		e, err := r.Next()
		if err != nil {
			return lines
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(e.Data), &m); err != nil {
			t.Fatalf("event data %q: %v", e.Data, err)
		}
		if e.Name != m["type"] {
			t.Errorf("event name %q, data type %v", e.Name, m["type"])
		}
		lines = append(lines, fmt.Sprintf("%v %v %s", m["sequence_number"], m["type"], detail(m)))
	}
}

func detail(m map[string]any) string {
	item := func(v any) string {
		it, _ := v.(map[string]any)
		d := fmt.Sprintf("%v %v", it["type"], it["name"])
		if v, ok := it["namespace"]; ok {
			d += fmt.Sprintf(" ns=%v", v)
		}
		if v, ok := it["input"]; ok {
			d += fmt.Sprintf(" input=%q", v)
		}
		if v, ok := it["arguments"]; ok {
			d += fmt.Sprintf(" arguments=%q", v)
		}
		return d
	}
	switch m["type"] {
	case "response.output_item.added", "response.output_item.done":
		return fmt.Sprintf("[%v] %s", m["output_index"], item(m["item"]))
	case "response.custom_tool_call_input.delta":
		return fmt.Sprintf("[%v] %v call=%v delta=%q", m["output_index"], m["item_id"], m["call_id"], m["delta"])
	case "response.custom_tool_call_input.done":
		return fmt.Sprintf("[%v] %v call=%v input=%q", m["output_index"], m["item_id"], m["call_id"], m["input"])
	case "response.function_call_arguments.delta":
		return fmt.Sprintf("[%v] delta=%q", m["output_index"], m["delta"])
	case "response.function_call_arguments.done":
		return fmt.Sprintf("[%v] arguments=%q", m["output_index"], m["arguments"])
	case "response.completed", "response.incomplete":
		resp, _ := m["response"].(map[string]any)
		var items []string
		out, _ := resp["output"].([]any)
		for _, o := range out {
			items = append(items, item(o))
		}
		return "output: " + strings.Join(items, "; ")
	}
	return ""
}

func sseOf(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var m struct{ Type string }
		_ = json.Unmarshal([]byte(e), &m)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", m.Type, e)
	}
	return b.String()
}

const (
	patchText = "*** Begin Patch\n*** Add File: a.txt\n+hi \"you\" 中\U0001F600\n*** End Patch\n"
	// patchArgs is the text as a model may write it: with \u escapes.
	patchArgs = `{"input":"*** Begin Patch\n*** Add File: a.txt\n+hi \"you\" \u4e2d\ud83d\ude00\n*** End Patch\n"}`
)

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func callItem(status, arguments string) string {
	return `{"id":"fc_1","type":"function_call","status":"` + status + `","call_id":"call_1","name":"apply_patch","arguments":` + jsonString(arguments) + `}`
}

func created(seq int) string {
	return fmt.Sprintf(`{"type":"response.created","sequence_number":%d,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"grok","output":[]}}`, seq)
}

func completed(seq int, items ...string) string {
	return fmt.Sprintf(`{"type":"response.completed","sequence_number":%d,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"grok","output":[%s],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, seq, strings.Join(items, ","))
}

func added(seq, index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.added","sequence_number":%d,"output_index":%d,"item":%s}`, seq, index, item)
}

func itemDone(seq, index int, item string) string {
	return fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":%d,"output_index":%d,"item":%s}`, seq, index, item)
}

func argsDelta(seq, index int, id, d string) string {
	return fmt.Sprintf(`{"type":"response.function_call_arguments.delta","sequence_number":%d,"output_index":%d,"item_id":"%s","delta":%s}`, seq, index, id, jsonString(d))
}

func argsDone(seq, index int, id, args string) string {
	return fmt.Sprintf(`{"type":"response.function_call_arguments.done","sequence_number":%d,"output_index":%d,"item_id":"%s","arguments":%s}`, seq, index, id, jsonString(args))
}

func expect(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The reply of a streamed call: the item is added as a custom tool call, the arguments' fragments
// become input fragments of patch text (whole characters, though the fragments split an escape and
// a surrogate pair), the arguments' end becomes the end of the input, and the item and the final
// response hold the call as a custom tool call. xAI's sequence numbers are kept.
func TestApplyPatchStreamedReply(t *testing.T) {
	i := strings.Index(patchArgs, `\ud83d`) + 3
	frags := []string{patchArgs[:20], patchArgs[20:i], patchArgs[i:]}
	got, err := replyStream(t, patchRequest, sseOf(created(0), added(1, 0, callItem("in_progress", "")),
		argsDelta(2, 0, "fc_1", frags[0]), argsDelta(3, 0, "fc_1", frags[1]), argsDelta(4, 0, "fc_1", frags[2]),
		argsDone(5, 0, "fc_1", patchArgs), itemDone(6, 0, callItem("completed", patchArgs)), completed(7, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`2 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="*** Begin "`,
		`3 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="Patch\n*** Add File: a.txt\n+hi \"you\" 中"`,
		`4 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="😀\n*** End Patch\n"`,
		`5 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + fmt.Sprintf("%q", patchText),
		`6 response.output_item.done [0] custom_tool_call apply_patch input=` + fmt.Sprintf("%q", patchText),
		`7 response.completed output: custom_tool_call apply_patch input=` + fmt.Sprintf("%q", patchText),
	})
}

// A call that comes whole in the arguments' end event gets no fragments, only the end of its input.
func TestApplyPatchSnapshotReply(t *testing.T) {
	got, err := replyStream(t, patchRequest, sseOf(created(0), added(1, 0, callItem("in_progress", "")),
		argsDone(2, 0, "fc_1", patchArgs), itemDone(3, 0, callItem("completed", patchArgs)), completed(4, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + fmt.Sprintf("%q", patchText),
		`3 response.output_item.done [0] custom_tool_call apply_patch input=` + fmt.Sprintf("%q", patchText),
		`4 response.completed output: custom_tool_call apply_patch input=` + fmt.Sprintf("%q", patchText),
	})
}

// A stream that has only the item's done event, or only the final response, still gives the client
// the whole life of the call, and the events after the added ones are numbered past them.
func TestApplyPatchReplyWithoutItsEvents(t *testing.T) {
	text := fmt.Sprintf("%q", patchText)
	got, err := replyStream(t, patchRequest, sseOf(created(0), itemDone(1, 0, callItem("completed", patchArgs)), completed(2, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
		`3 response.output_item.done [0] custom_tool_call apply_patch input=` + text,
		`4 response.completed output: custom_tool_call apply_patch input=` + text,
	})
	got, err = replyStream(t, patchRequest, sseOf(created(0), completed(1, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
		`3 response.output_item.done [0] custom_tool_call apply_patch input=` + text,
		`4 response.completed output: custom_tool_call apply_patch input=` + text,
	})
}

// A function call to another tool, in the same reply, goes to the client as xAI sent it.
func TestApplyPatchReplyBesideAnotherCall(t *testing.T) {
	other := `{"id":"fc_2","type":"function_call","status":"completed","call_id":"call_2","name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}`
	otherAdded := `{"id":"fc_2","type":"function_call","status":"in_progress","call_id":"call_2","name":"get_weather","arguments":""}`
	got, err := replyStream(t, patchRequest, sseOf(created(0),
		added(1, 0, callItem("in_progress", "")), added(2, 1, otherAdded),
		argsDelta(3, 0, "fc_1", patchArgs), argsDelta(4, 1, "fc_2", `{"city":"Oslo"}`),
		argsDone(5, 0, "fc_1", patchArgs), argsDone(6, 1, "fc_2", `{"city":"Oslo"}`),
		itemDone(7, 0, callItem("completed", patchArgs)), itemDone(8, 1, other),
		completed(9, callItem("completed", patchArgs), other)))
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("%q", patchText)
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`2 response.output_item.added [1] function_call get_weather arguments=""`,
		`3 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta=` + text,
		`4 response.function_call_arguments.delta [1] delta="{\"city\":\"Oslo\"}"`,
		`5 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
		`6 response.function_call_arguments.done [1] arguments="{\"city\":\"Oslo\"}"`,
		`7 response.output_item.done [0] custom_tool_call apply_patch input=` + text,
		`8 response.output_item.done [1] function_call get_weather arguments="{\"city\":\"Oslo\"}"`,
		`9 response.completed output: custom_tool_call apply_patch input=` + text + `; function_call get_weather arguments="{\"city\":\"Oslo\"}"`,
	})
}

// Arguments that are not one input string end the reply with an error, whenever they are seen,
// and nothing of the call reaches the client as a function call.
func TestApplyPatchBadArguments(t *testing.T) {
	for name, c := range map[string]struct{ frag, final string }{
		"not an object":    {`"x"`, `"x"`},
		"another field":    {`{"patch":"x"}`, `{"patch":"x"}`},
		"not a string":     {`{"input":3}`, `{"input":3}`},
		"second field":     {`{"input":"x","y":1}`, `{"input":"x","y":1}`},
		"trailing text":    {`{"input":"x"} x`, `{"input":"x"} x`},
		"lone low escape":  {`{"input":"\ude00"}`, `{"input":"\ude00"}`},
		"cut short":        {`{"input":"x`, `{"input":"x`},
		"final differs":    {`{"input":"abc"}`, `{"input":"xyz"}`},
		"final is shorter": {`{"input":"abc"}`, `{"input":"ab"}`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := replyStream(t, patchRequest, sseOf(created(0), added(1, 0, callItem("in_progress", "")),
				argsDelta(2, 0, "fc_1", c.frag), argsDone(3, 0, "fc_1", c.final),
				itemDone(4, 0, callItem("completed", c.final)), completed(5, callItem("completed", c.final))))
			var e *llm.Error
			if err == nil || !strings.Contains(err.Error(), "apply_patch") {
				t.Fatalf("error %v, want one about apply_patch arguments; events %v", err, got)
			}
			if e, _ = err.(*llm.Error); e == nil || e.Code != llm.CodeUpstream || strings.Contains(e.Message, c.final) {
				t.Errorf("error %+v: want an upstream error that does not repeat the arguments", err)
			}
			for _, line := range got {
				if strings.Contains(line, "function_call_arguments") || strings.Contains(line, "function_call apply_patch") {
					t.Errorf("the client was sent %q", line)
				}
			}
		})
	}
}

// A reply read as one response is checked and turned back before any of it is sent.
func TestApplyPatchNonStreamedReply(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(patchRequest))
	if err != nil {
		t.Fatal(err)
	}
	_, _, patch, _ := buildRequest(req, false)
	body := func(item string) []byte {
		return []byte(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"grok","output":[` + item + `],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}
	events, err := openairesponses.DecodeResponse(body(callItem("completed", patchArgs)))
	if err != nil {
		t.Fatal(err)
	}
	events, err = convert(patch, events)
	if err != nil {
		t.Fatal(err)
	}
	agg := openairesponses.NewAggregator(req)
	for _, ev := range events {
		agg.Add(ev)
	}
	out, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || len(resp.Output) != 1 {
		t.Fatalf("response %s: %v", out, err)
	}
	if it := resp.Output[0]; it["type"] != "custom_tool_call" || it["input"] != patchText || it["name"] != "apply_patch" || it["call_id"] != "call_1" || it["id"] != "fc_1" || it["arguments"] != nil {
		t.Errorf("output item %v, want the apply_patch custom tool call with its text", it)
	}

	_, _, patch, _ = buildRequest(req, false)
	events, err = openairesponses.DecodeResponse(body(callItem("completed", `{"input":3}`)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := convert(patch, events); err == nil {
		t.Error("a non-streamed reply with bad arguments was accepted")
	}
}

// Without a custom apply_patch tool in the request, a function named apply_patch is an ordinary
// call and is left as xAI sent it.
func TestApplyPatchFunctionIsNotTurnedBack(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, patch, err := buildRequest(req, false); err != nil || patch != nil {
		t.Errorf("bridge %v, error %v; want no bridge", patch, err)
	}
}

// A call's arguments are bounded, white space included: a reply that sends more than the bound ends
// with an error that does not repeat them, and the white space is not kept.
func TestInputDecoderBoundsArguments(t *testing.T) {
	var d inputDecoder
	if _, err := d.Push(`{`); err != nil {
		t.Fatal(err)
	}
	ws := strings.Repeat(" ", 1<<20)
	var err error
	sent := 0
	for sent < 40 && err == nil {
		_, err = d.Push(ws)
		sent++
	}
	if err == nil {
		t.Fatalf("%d MiB of white space in the arguments of one call was accepted", sent)
	}
}

// The upstream may open with the call's added event, with no response.created before it. The call is
// then turned back from its first event on, once.
func TestApplyPatchReplyWithoutCreated(t *testing.T) {
	text := fmt.Sprintf("%q", patchText)
	got, err := replyStream(t, patchRequest, sseOf(added(0, 0, callItem("in_progress", "")), argsDelta(1, 0, "fc_1", patchArgs),
		argsDone(2, 0, "fc_1", patchArgs), itemDone(3, 0, callItem("completed", patchArgs)), completed(4, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.output_item.added [0] custom_tool_call apply_patch input=""`,
		`1 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta=` + text,
		`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
		`3 response.output_item.done [0] custom_tool_call apply_patch input=` + text,
		`4 response.completed output: custom_tool_call apply_patch input=` + text,
	})
}

// A call that was opened and given its arguments but never got its done event is finished when the
// reply ends, and is in the final output even when the final response lists none.
func TestApplyPatchCallWithoutItsDoneEvent(t *testing.T) {
	text := fmt.Sprintf("%q", patchText)
	for name, output := range map[string][]string{"empty output": nil, "other items only": {`{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[]}`}} {
		t.Run(name, func(t *testing.T) {
			got, err := replyStream(t, patchRequest, sseOf(created(0), added(1, 0, callItem("in_progress", "")), argsDelta(2, 0, "fc_1", patchArgs),
				argsDone(3, 0, "fc_1", patchArgs), completed(4, output...)))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 6 || !strings.HasPrefix(got[3], `3 response.custom_tool_call_input.done`) || !strings.HasPrefix(got[4], `4 response.output_item.done [0] custom_tool_call apply_patch input=`+text) ||
				!strings.Contains(got[5], "custom_tool_call apply_patch input="+text) || !strings.HasPrefix(got[5], "5 response.completed") {
				t.Errorf("events:\n%s", strings.Join(got, "\n"))
			}
		})
	}
	// Arguments that never completed cannot be finished: the reply fails.
	_, err := replyStream(t, patchRequest, sseOf(created(0), added(1, 0, callItem("in_progress", "")), argsDelta(2, 0, "fc_1", `{"input":"abc`), completed(3)))
	if err == nil || !strings.Contains(err.Error(), "apply_patch") {
		t.Errorf("error %v, want one about apply_patch arguments", err)
	}
}

// The bounds apply to one call and to the calls of a reply together, and end the reply with an
// error that says the arguments are too large and does not repeat them.
func TestApplyPatchReplyBudget(t *testing.T) {
	oldCall, oldReply := maxCallArguments, maxReplyArguments
	t.Cleanup(func() { maxCallArguments, maxReplyArguments = oldCall, oldReply })
	maxCallArguments, maxReplyArguments = 1000, 1500
	secondCall := func(i int, n int) string {
		return fmt.Sprintf(`{"id":"fc_%d","type":"function_call","status":"in_progress","call_id":"call_%d","name":"apply_patch","arguments":""}`, i, i) + "\x00" + strings.Repeat("x", n)
	}
	call := func(i, n int) (string, string) {
		item := strings.SplitN(secondCall(i, n), "\x00", 2)[0]
		return added(i*2+1, i, item), argsDelta(i*2+2, i, fmt.Sprintf("fc_%d", i), `{"input":"`+strings.Repeat("x", n))
	}
	for name, sizes := range map[string][]int{"one call": {1100}, "two calls": {800, 800}} {
		t.Run(name, func(t *testing.T) {
			events := []string{created(0)}
			for i, n := range sizes {
				a, d := call(i, n)
				events = append(events, a, d)
			}
			_, err := replyStream(t, patchRequest, sseOf(events...))
			if err == nil || !strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "xxxx") {
				t.Errorf("error %v, want one that says the arguments are too large", err)
			}
		})
	}
	// Within the bounds the reply goes through.
	a, d := call(0, 800)
	if _, err := replyStream(t, patchRequest, sseOf(created(0), a, d, argsDone(3, 0, "fc_0", `{"input":"`+strings.Repeat("x", 800)+`"}`),
		itemDone(4, 0, callItem("completed", `{"input":"`+strings.Repeat("x", 800)+`"}`)), completed(5))); err != nil {
		t.Errorf("a call within the bounds: %v", err)
	}
	// Final arguments longer than the fragments that came before count in full.
	t.Run("final arguments", func(t *testing.T) {
		events := []string{created(0)}
		for i := range 2 {
			item := strings.SplitN(secondCall(i, 0), "\x00", 2)[0]
			id := fmt.Sprintf("fc_%d", i)
			events = append(events, added(i*3+1, i, item), argsDelta(i*3+2, i, id, `{`),
				argsDone(i*3+3, i, id, `{"input":"`+strings.Repeat("x", 900)+`"}`))
		}
		_, err := replyStream(t, patchRequest, sseOf(events...))
		if err == nil || !strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "xxxx") {
			t.Errorf("error %v, want one that says the arguments are too large", err)
		}
	})
}

// White space before the patch text is not kept.
func TestInputDecoderKeepsNoWhiteSpace(t *testing.T) {
	var d inputDecoder
	if _, err := d.Push(`{` + strings.Repeat(" ", 5000)); err != nil {
		t.Fatal(err)
	}
	if kept := len(d.pending) + d.input.Len(); kept != 0 {
		t.Errorf("%d bytes kept after white space", kept)
	}
}
