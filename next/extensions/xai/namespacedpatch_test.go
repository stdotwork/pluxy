package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Codex's custom apply_patch tool can be declared inside a namespace (the Codex app does). As for
// the top-level tool, today's core sends it as the patch function and turns xAI's call back into a
// custom tool call, in its namespace. Expected values are today's core's
// (TestXAIApplyPatchDispatcherEvidenceLifecycle and the namespace cases of its apply_patch tests):
// the dispatcher's history call is {"arguments":{"input":...},"name":"apply_patch"}, the call comes
// back as a custom_tool_call in the namespace with the patch text as input, and a dispatcher's
// argument fragments are no input fragments of the patch.

const patchNSTool = `{"type":"custom","name":"apply_patch","description":"Edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
	"format":{"type":"grammar","syntax":"lark","definition":"start: patch\npatch: /.+/"}}`

// foldedNS is a namespace of more tools than xAI takes, so it is folded into one function.
func foldedNS() string {
	return `{"type":"namespace","name":"ns","tools":[` + patchNSTool + `,` + lookups(205) + `]}`
}

func flatNS() string {
	return `{"type":"namespace","name":"ns","tools":[` + patchNSTool + `,{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
}

// item0 is the first output item of the client's response.
func item0(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	list, _ := resp["output"].([]any)
	if len(list) == 0 {
		t.Fatalf("no output in %v", resp)
	}
	m, _ := list[0].(map[string]any)
	return m
}

func wantPatchCall(t *testing.T, got map[string]any, ns string) {
	t.Helper()
	it := item0(t, got)
	if it["type"] != "custom_tool_call" || it["name"] != "apply_patch" || it["namespace"] != ns || it["input"] != patchText || it["call_id"] != "call_1" || it["arguments"] != nil {
		t.Errorf("client got %v, want a custom_tool_call apply_patch in namespace %q with the patch text", it, ns)
	}
}

func TestNamespacedApplyPatchDeclaration(t *testing.T) {
	run := newLiteRun(t)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			with := func(tools string, more string) string {
				return fmt.Sprintf(`{"model":"grok-4.7","stream":%v,%s"input":[{"role":"user","content":"edit"}],"tools":[%s]}`, stream, more, tools)
			}

			// Flattened: the child is the function ns__apply_patch with the patch rules, and the
			// call to it comes back as the custom call in the namespace.
			t.Run("flattened", func(t *testing.T) {
				sent, got := run.send(t, with(flatNS(), ""), call{"ns__apply_patch", patchArgs})
				tools, _ := sent["tools"].([]any)
				if len(tools) != 2 {
					t.Fatalf("tools = %v", sentTools(sent))
				}
				tool := tools[0].(map[string]any)
				params, _ := json.Marshal(tool["parameters"])
				desc, _ := tool["description"].(string)
				if tool["type"] != "function" || tool["name"] != "ns__apply_patch" || tool["format"] != nil ||
					!strings.Contains(desc, "Call this function with a JSON object whose input field") || !strings.Contains(desc, "Original patch grammar:\nstart: patch") || strings.Contains(desc, "FREEFORM") {
					t.Errorf("tool = %v", tool)
				}
				sameJSON(t, "parameters", params, applyPatchParameters)
				wantPatchCall(t, got, "ns")
			})

			// Folded: the namespace is one function; its description gives the child the patch
			// rules, and the call to the child comes back as the custom call.
			t.Run("folded", func(t *testing.T) {
				for _, args := range []string{
					`{"name":"apply_patch","arguments":` + patchArgs + `}`,
					`{"name":"apply_patch","arguments":` + jsonString(patchArgs) + `}`,
				} {
					sent, got := run.send(t, with(foldedNS(), ""), call{"ns", args})
					if got := sentTools(sent); strings.Join(got, ",") != "function:ns" {
						t.Fatalf("tools = %v, want the function ns", got)
					}
					desc, _ := sent["tools"].([]any)[0].(map[string]any)["description"].(string)
					if !strings.Contains(desc, "- apply_patch: Edit files. \n\nCall this function with a JSON object whose input field") ||
						!strings.Contains(desc, `"required":["input"]`) || strings.Contains(desc, "FREEFORM") {
						t.Errorf("description %q: want apply_patch with the patch rules and the input schema", desc)
					}
					wantPatchCall(t, got, "ns")
				}
				// Another child of that namespace is an ordinary call.
				_, got := run.send(t, with(foldedNS(), ""), call{"ns", `{"name":"lookup0","arguments":{"x":1}}`})
				if it := item0(t, got); it["type"] != "function_call" || it["name"] != "lookup0" || it["namespace"] != "ns" || it["arguments"] != `{"x":1}` {
					t.Errorf("client got %v, want a function_call lookup0 in ns", it)
				}
			})

			// A namespace of an additional_tools item is promoted first, so it is the same.
			t.Run("promoted from an additional_tools item", func(t *testing.T) {
				sent, got := run.send(t, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,"input":[{"type":"additional_tools","role":"developer","tools":[%s]},{"role":"user","content":"edit"}]}`, stream, flatNS()),
					call{"ns__apply_patch", patchArgs})
				if want := "function:ns__apply_patch,function:ns__lookup"; strings.Join(sentTools(sent), ",") != want {
					t.Errorf("tools = %v, want %s", sentTools(sent), want)
				}
				if tool := sent["tools"].([]any)[0].(map[string]any); tool["parameters"] == nil || fmt.Sprint(tool["parameters"]) != fmt.Sprint(map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string", "description": "The complete apply_patch patch text."}}, "required": []any{"input"}, "additionalProperties": false}) {
					t.Errorf("parameters = %v, want the patch input schema", tool["parameters"])
				}
				wantPatchCall(t, got, "ns")

				sent, got = run.send(t, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"edit"}]}`, stream, foldedNS()),
					call{"ns", `{"name":"apply_patch","arguments":` + patchArgs + `}`})
				if got := sentTools(sent); strings.Join(got, ",") != "function:ns" {
					t.Errorf("tools = %v, want the function ns", got)
				}
				wantPatchCall(t, got, "ns")
			})

			// A name belongs to the namespace it is declared in: a top-level function called
			// apply_patch and the namespace's custom one are two tools.
			t.Run("only the namespace's tool", func(t *testing.T) {
				_, got := run.send(t, with(`{"type":"function","name":"apply_patch","parameters":{"type":"object"}},`+flatNS(), ""), call{"apply_patch", "{}"})
				if it := item0(t, got); it["type"] != "function_call" || it["name"] != "apply_patch" || it["namespace"] != nil {
					t.Errorf("the top-level function's call came back as %v", it)
				}
				_, got = run.send(t, with(`{"type":"function","name":"apply_patch","parameters":{"type":"object"}},`+flatNS(), ""), call{"ns__apply_patch", patchArgs})
				wantPatchCall(t, got, "ns")
			})

			// When a namespace declares apply_patch twice, the first stays, as with a top-level
			// tool: a function first is the ordinary function, and the custom one after it is gone.
			t.Run("first declaration wins", func(t *testing.T) {
				fn := `{"type":"namespace","name":"ns","tools":[{"type":"function","name":"apply_patch","description":"mine","parameters":{"type":"object"}},` + patchNSTool + `]}`
				sent, got := run.send(t, with(fn, ""), call{"ns__apply_patch", "{}"})
				if tools, _ := sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["description"] != "mine" {
					t.Errorf("tools = %v, want the function alone", sent["tools"])
				}
				if it := item0(t, got); it["type"] != "function_call" || it["namespace"] != "ns" {
					t.Errorf("client got %v, want a function call", it)
				}
				cu := `{"type":"namespace","name":"ns","tools":[` + patchNSTool + `,{"type":"function","name":"apply_patch","description":"mine","parameters":{"type":"object"}}]}`
				sent, got = run.send(t, with(cu, ""), call{"ns__apply_patch", patchArgs})
				if tools, _ := sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "ns__apply_patch" || tools[0].(map[string]any)["description"] == "mine" {
					t.Errorf("tools = %v, want the patch function alone", sent["tools"])
				}
				wantPatchCall(t, got, "ns")
			})

			t.Run("tool choice names it", func(t *testing.T) {
				for _, c := range []struct{ tools, choice, want string }{
					{flatNS(), `{"type":"custom","namespace":"ns","name":"apply_patch"}`, `{"type":"function","name":"ns__apply_patch"}`},
					{foldedNS(), `{"type":"custom","namespace":"ns","name":"apply_patch"}`, `{"type":"function","name":"ns"}`},
					{flatNS(), `{"type":"allowed_tools","mode":"required","tools":[{"type":"custom","namespace":"ns","name":"apply_patch"},{"type":"function","namespace":"ns","name":"lookup"}]}`,
						`{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"ns__apply_patch"},{"type":"function","name":"ns__lookup"}]}`},
					// A custom choice that names another namespace's tool is none of the patch tool's.
					{flatNS(), `{"type":"custom","namespace":"other","name":"apply_patch"}`, `{"type":"custom","namespace":"other","name":"apply_patch"}`},
				} {
					sent, _ := run.send(t, with(c.tools, `"tool_choice":`+c.choice+`,`), call{"ns__apply_patch", patchArgs})
					got, _ := json.Marshal(sent["tool_choice"])
					sameJSON(t, "tool_choice "+c.choice, got, c.want)
				}
			})
		})
	}
}

// A call to the namespace's apply_patch in the history, with its output, is a function call that
// carries the text in input, written as the tools were sent.
func TestNamespacedApplyPatchHistory(t *testing.T) {
	history := `"input":[{"role":"user","content":"edit"},
		{"type":"custom_tool_call","call_id":"c1","namespace":"ns","name":"apply_patch","input":"old"},
		{"type":"custom_tool_call_output","call_id":"c1","output":"Done!"}]`
	for _, c := range []struct{ name, tools, wantName, wantArgs string }{
		{"flattened", flatNS(), "ns__apply_patch", `{"input":"old"}`},
		{"folded", foldedNS(), "ns", `{"arguments":{"input":"old"},"name":"apply_patch"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := responsesBody(t, `{"model":"grok-4.6",`+history+`,"tools":[`+c.tools+`]}`)
			var m struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.Unmarshal(got, &m); err != nil || len(m.Input) != 3 {
				t.Fatalf("input: %v: %s", err, got)
			}
			call := m.Input[1]
			if call["type"] != "function_call" || call["name"] != c.wantName || call["arguments"] != c.wantArgs || call["call_id"] != "c1" || call["namespace"] != nil || call["input"] != nil {
				t.Errorf("call = %v, want a function_call %s with arguments %s", call, c.wantName, c.wantArgs)
			}
			if out := m.Input[2]; out["type"] != "function_call_output" || out["call_id"] != "c1" || out["output"] != "Done!" {
				t.Errorf("output = %v", out)
			}
		})
	}
}

// The events of a streamed call. A flattened call's fragments are the patch text's; a folded
// namespace's are the dispatcher's wrapper, which is not patch text, and its added event cannot say
// which child is called. So the events of a call to a folded namespace that has a custom apply_patch
// are held until the arguments name the child; for the patch tool the client then gets the item and
// the whole input, no input fragment, as today's core sends it (and never a function_call event).
// The events keep xAI's sequence numbers, which skip the fragments left out.
func TestNamespacedApplyPatchStreamedReply(t *testing.T) {
	text := fmt.Sprintf("%q", patchText)
	item := func(name, status, args string) string {
		return `{"id":"fc_1","type":"function_call","status":"` + status + `","call_id":"call_1","name":"` + name + `","arguments":` + jsonString(args) + `}`
	}
	t.Run("flattened", func(t *testing.T) {
		body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + flatNS() + `]}`
		i := strings.Index(patchArgs, `\ud83d`) + 3
		got, err := replyStream(t, body, sseOf(created(0), added(1, 0, item("ns__apply_patch", "in_progress", "")),
			argsDelta(2, 0, "fc_1", patchArgs[:20]), argsDelta(3, 0, "fc_1", patchArgs[20:i]), argsDelta(4, 0, "fc_1", patchArgs[i:]),
			argsDone(5, 0, "fc_1", patchArgs), itemDone(6, 0, item("ns__apply_patch", "completed", patchArgs)), completed(7, item("ns__apply_patch", "completed", patchArgs))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
			`2 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="*** Begin "`,
			`3 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="Patch\n*** Add File: a.txt\n+hi \"you\" 中"`,
			`4 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="😀\n*** End Patch\n"`,
			`5 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
			`6 response.output_item.done [0] custom_tool_call apply_patch ns=ns input=` + text,
			`7 response.completed output: custom_tool_call apply_patch ns=ns input=` + text,
		})
	})
	t.Run("folded", func(t *testing.T) {
		body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + foldedNS() + `]}`
		wrapper := `{"name":"apply_patch","arguments":` + patchArgs + `}`
		child := item("ns", "completed", wrapper)
		got, err := replyStream(t, body, sseOf(created(0), added(1, 0, item("ns", "in_progress", "")),
			argsDelta(2, 0, "fc_1", wrapper[:30]), argsDelta(3, 0, "fc_1", wrapper[30:]),
			argsDone(4, 0, "fc_1", wrapper), itemDone(5, 0, child), completed(6, child)))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
			`4 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
			`5 response.output_item.done [0] custom_tool_call apply_patch ns=ns input=` + text,
			`6 response.completed output: custom_tool_call apply_patch ns=ns input=` + text,
		})
		// An ordinary child of the namespace is a function call, with the events xAI sent, as today's
		// core replays them: they come when the arguments name the child, which its added event has.
		other := `{"name":"lookup0","arguments":{"x":1}}`
		got, err = replyStream(t, body, sseOf(created(0), added(1, 0, item("ns", "in_progress", "")), argsDelta(2, 0, "fc_1", other),
			argsDone(3, 0, "fc_1", other), itemDone(4, 0, item("ns", "completed", other)), completed(5, item("ns", "completed", other))))
		if err != nil {
			t.Fatal(err)
		}
		want := `function_call lookup0 ns=ns arguments="{\"x\":1}"`
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] function_call lookup0 ns=ns arguments=""`,
			`2 response.function_call_arguments.delta [0] delta="{\"name\":\"lookup0\",\"arguments\":{\"x\":1}}"`,
			`3 response.function_call_arguments.done [0] arguments="{\"x\":1}"`,
			`4 response.output_item.done [0] ` + want,
			`5 response.completed output: ` + want,
		})
		// A call that has no events but the final response is restored the same way.
		got, err = replyStream(t, body, sseOf(created(0), completed(1, child)))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
			`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text,
			`3 response.output_item.done [0] custom_tool_call apply_patch ns=ns input=` + text,
			`4 response.completed output: custom_tool_call apply_patch ns=ns input=` + text,
		})
	})
}

// A call to a folded namespace that has a custom apply_patch is turned back however its events
// come: with no arguments done event, with no done item, with only the final response. Whatever the
// events, the client is never sent a function_call event for it.
func TestNamespacedApplyPatchFoldedEventShapes(t *testing.T) {
	body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + foldedNS() + `]}`
	wrapper := `{"name":"apply_patch","arguments":` + patchArgs + `}`
	text := fmt.Sprintf("%q", patchText)
	item := func(status, args string) string {
		return `{"id":"fc_1","type":"function_call","status":"` + status + `","call_id":"call_1","name":"ns","arguments":` + jsonString(args) + `}`
	}
	patchItem := `custom_tool_call apply_patch ns=ns input=` + text
	noFunctionCall := func(t *testing.T, got []string) {
		t.Helper()
		for _, line := range got {
			if strings.Contains(line, "function_call") {
				t.Errorf("the client was sent a function call event: %s", line)
			}
		}
	}
	for _, c := range []struct {
		name     string
		upstream string
		want     []string
	}{
		{"no arguments done event", sseOf(created(0), added(1, 0, item("in_progress", "")), argsDelta(2, 0, "fc_1", wrapper), itemDone(3, 0, item("completed", wrapper)), completed(4, item("completed", wrapper))),
			[]string{`0 response.created `, `1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
				`3 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text, `4 response.output_item.done [0] ` + patchItem, `5 response.completed output: ` + patchItem}},
		{"no done item", sseOf(created(0), added(1, 0, item("in_progress", "")), argsDelta(2, 0, "fc_1", wrapper), argsDone(3, 0, "fc_1", wrapper), completed(4, item("completed", wrapper))),
			[]string{`0 response.created `, `1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
				`3 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text, `4 response.output_item.done [0] ` + patchItem, `5 response.completed output: ` + patchItem}},
		{"opened and never finished", sseOf(created(0), added(1, 0, item("in_progress", "")), argsDelta(2, 0, "fc_1", wrapper), completed(3, item("completed", wrapper))),
			[]string{`0 response.created `, `1 response.output_item.added [0] custom_tool_call apply_patch ns=ns input=""`,
				`3 response.custom_tool_call_input.done [0] fc_1 call=call_1 input=` + text, `4 response.output_item.done [0] ` + patchItem, `5 response.completed output: ` + patchItem}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := replyStream(t, body, c.upstream)
			if err != nil {
				t.Fatal(err)
			}
			noFunctionCall(t, got)
			expect(t, got, c.want)
		})
	}
	t.Run("another namespace is not held", func(t *testing.T) {
		// Only a namespace that has the custom tool is held; its siblings stream as they always did.
		two := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + foldedNS() + `,{"type":"namespace","name":"other","tools":[` + lookups(205) + `]}]}`
		args := `{"name":"lookup0","arguments":{"x":1}}`
		got, err := replyStream(t, two, sseOf(created(0), `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_2","type":"function_call","status":"in_progress","call_id":"call_2","name":"other","arguments":""}}`,
			argsDelta(2, 0, "fc_2", args)))
		if err == nil {
			t.Fatal("the stream has no end, so the reply should not finish")
		}
		expect(t, got, []string{`0 response.created `, `1 response.output_item.added [0] function_call other ns=other arguments=""`, `2 response.function_call_arguments.delta [0] delta="{\"name\":\"lookup0\",\"arguments\":{\"x\":1}}"`})
	})
	t.Run("opening event", func(t *testing.T) {
		// The first event of a stream cannot be held; the reply ends with an error instead of
		// sending the call as a function call.
		_, err := replyStream(t, body, sseOf(added(0, 0, item("in_progress", "")), argsDone(1, 0, "fc_1", wrapper), completed(2, item("completed", wrapper))))
		if err == nil || !strings.Contains(err.Error(), "before response.created") {
			t.Errorf("error = %v", err)
		}
	})
}

// A name belongs to its namespace, and the winner of a name is today's core's: the first, and a
// top-level tool before a namespace's child. Both the top-level and the namespace's tool are the
// patch tool when both are custom.
func TestNamespacedApplyPatchWinners(t *testing.T) {
	run := newLiteRun(t)
	t.Run("top-level and namespace tool, both custom", func(t *testing.T) {
		tools := patchNSTool + `,` + flatNS()
		sent, got := run.send(t, `{"model":"grok-4.7","stream":true,"input":[{"role":"user","content":"edit"}],"tools":[`+tools+`]}`, call{"apply_patch", patchArgs})
		if want := "function:apply_patch,function:ns__apply_patch,function:ns__lookup"; strings.Join(sentTools(sent), ",") != want {
			t.Errorf("tools = %v, want %s", sentTools(sent), want)
		}
		if it := item0(t, got); it["type"] != "custom_tool_call" || it["namespace"] != nil || it["input"] != patchText {
			t.Errorf("top-level call came back as %v", it)
		}
		_, got = run.send(t, `{"model":"grok-4.7","stream":true,"input":[{"role":"user","content":"edit"}],"tools":[`+tools+`]}`, call{"ns__apply_patch", patchArgs})
		wantPatchCall(t, got, "ns")
	})
	t.Run("a top-level function of the qualified name wins", func(t *testing.T) {
		tools := flatNS() + `,{"type":"function","name":"ns__apply_patch","description":"mine","parameters":{"type":"object"}}`
		sent, got := run.send(t, `{"model":"grok-4.7","stream":false,"input":[{"role":"user","content":"edit"}],"tools":[`+tools+`]}`, call{"ns__apply_patch", "{}"})
		var mine int
		for _, tl := range sent["tools"].([]any) {
			if tm := tl.(map[string]any); tm["name"] == "ns__apply_patch" && tm["description"] == "mine" {
				mine++
			}
		}
		if n := len(sent["tools"].([]any)); n != 2 || mine != 1 {
			t.Errorf("tools = %v, want the function and ns__lookup", sentTools(sent))
		}
		if it := item0(t, got); it["type"] != "function_call" {
			t.Errorf("client got %v, want a function call", it)
		}
	})
}

// The namespace of a patch call in the history is kept only when the request has namespace tools,
// which write it as the tools were sent; without them the call is written as it always was.
func TestNamespacedApplyPatchHistoryWithoutNamespaceTools(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[{"type":"custom_tool_call","call_id":"c1","namespace":"ns","name":"apply_patch","input":"old"}],"tools":[`+patchNSTool+`]}`)
	var m struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal(got, &m); err != nil || len(m.Input) != 1 {
		t.Fatalf("input: %v: %s", err, got)
	}
	if call := m.Input[0]; call["type"] != "function_call" || call["name"] != "apply_patch" || call["namespace"] != nil || call["arguments"] != `{"input":"old"}` {
		t.Errorf("call = %v", call)
	}
}
