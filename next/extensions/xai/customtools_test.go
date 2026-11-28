package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
)

const (
	sqlTool  = `{"type":"custom","name":"run_sql","description":"Run a query.","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}`
	noteTool = `{"type":"custom","name":"note","description":"Take a note.","format":{"type":"text"}}`
)

const (
	sqlDescription  = "Run a query.\n\n" + customInstructions + "\n\nThe input must follow this lark grammar:\nstart: /.+/"
	noteDescription = "Take a note.\n\n" + customInstructions
)

func flatCustomNS() string {
	return `{"type":"namespace","name":"ns","tools":[` + sqlTool + `,{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`
}

func foldedCustomNS() string {
	return `{"type":"namespace","name":"ns","tools":[` + sqlTool + `,` + lookups(205) + `]}`
}

func wantCustomCall(t *testing.T, got map[string]any, name, ns, input string) {
	t.Helper()
	it := item0(t, got)
	var wantNS any
	if ns != "" {
		wantNS = ns
	}
	if it["type"] != "custom_tool_call" || it["name"] != name || it["namespace"] != wantNS || it["input"] != input || it["call_id"] != "call_1" || it["arguments"] != nil {
		t.Errorf("client got %v, want a custom_tool_call %s in namespace %q with input %q", it, name, ns, input)
	}
}

func customArgs(text string) string { return `{"input":` + jsonString(text) + `}` }

func TestCustomToolDeclaration(t *testing.T) {
	run := newLiteRun(t)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			with := func(tools string, more string) string {
				return fmt.Sprintf(`{"model":"grok-4.7","stream":%v,%s"input":[{"role":"user","content":"go"}],"tools":[%s]}`, stream, more, tools)
			}

			t.Run("top-level", func(t *testing.T) {
				tools := sqlTool + `,` + noteTool + `,{"type":"function","name":"f","parameters":{"type":"object"}}`
				sent, got := run.send(t, with(tools, ""), call{"run_sql", customArgs("select 1")})
				if want := "function:run_sql,function:note,function:f"; strings.Join(sentTools(sent), ",") != want {
					t.Fatalf("tools = %v, want %s", sentTools(sent), want)
				}
				list := sent["tools"].([]any)
				for i, want := range []struct{ name, description string }{{"run_sql", sqlDescription}, {"note", noteDescription}} {
					tool := list[i].(map[string]any)
					params, _ := json.Marshal(tool["parameters"])
					sameJSON(t, want.name+" parameters", params, customParameters)
					if tool["format"] != nil || tool["description"] != want.description {
						t.Errorf("%s = %v, want description %q and no format", want.name, tool, want.description)
					}
				}
				wantCustomCall(t, got, "run_sql", "", "select 1")

				_, got = run.send(t, with(tools, ""), call{"note", customArgs("remember\nthis")})
				wantCustomCall(t, got, "note", "", "remember\nthis")

				_, got = run.send(t, with(tools, ""), call{"f", `{"a":1}`})
				if it := item0(t, got); it["type"] != "function_call" || it["name"] != "f" || it["arguments"] != `{"a":1}` {
					t.Errorf("client got %v, want a function_call f", it)
				}
			})

			t.Run("bare", func(t *testing.T) {
				sent, got := run.send(t, with(`{"type":"custom","name":"bare"}`, ""), call{"bare", customArgs("x")})
				if d := sent["tools"].([]any)[0].(map[string]any)["description"]; d != customInstructions {
					t.Errorf("description = %q, want the instructions alone", d)
				}
				wantCustomCall(t, got, "bare", "", "x")
			})

			t.Run("flattened in a namespace", func(t *testing.T) {
				sent, got := run.send(t, with(flatCustomNS(), ""), call{"ns__run_sql", customArgs("select 2")})
				if want := "function:ns__run_sql,function:ns__lookup"; strings.Join(sentTools(sent), ",") != want {
					t.Fatalf("tools = %v, want %s", sentTools(sent), want)
				}
				tool := sent["tools"].([]any)[0].(map[string]any)
				params, _ := json.Marshal(tool["parameters"])
				sameJSON(t, "parameters", params, customParameters)
				if tool["format"] != nil || tool["description"] != sqlDescription {
					t.Errorf("tool = %v", tool)
				}
				wantCustomCall(t, got, "run_sql", "ns", "select 2")
				_, got = run.send(t, with(flatCustomNS(), ""), call{"ns__lookup", `{}`})
				if it := item0(t, got); it["type"] != "function_call" || it["name"] != "lookup" || it["namespace"] != "ns" {
					t.Errorf("client got %v, want a function_call lookup in ns", it)
				}
			})

			t.Run("folded in a namespace", func(t *testing.T) {
				for _, args := range []string{
					`{"name":"run_sql","arguments":` + customArgs("select 3") + `}`,
					`{"name":"run_sql","arguments":` + jsonString(customArgs("select 3")) + `}`,
				} {
					sent, got := run.send(t, with(foldedCustomNS(), ""), call{"ns", args})
					if got := sentTools(sent); strings.Join(got, ",") != "function:ns" {
						t.Fatalf("tools = %v, want the function ns", got)
					}
					desc, _ := sent["tools"].([]any)[0].(map[string]any)["description"].(string)
					if !strings.Contains(desc, "- run_sql: "+sqlDescription) || !strings.Contains(desc, `"required":["input"]`) {
						t.Errorf("description %q: want run_sql with the input rules and schema", desc)
					}
					wantCustomCall(t, got, "run_sql", "ns", "select 3")
				}
				_, got := run.send(t, with(foldedCustomNS(), ""), call{"ns", `{"name":"lookup0","arguments":{"x":1}}`})
				if it := item0(t, got); it["type"] != "function_call" || it["name"] != "lookup0" || it["namespace"] != "ns" || it["arguments"] != `{"x":1}` {
					t.Errorf("client got %v, want a function_call lookup0 in ns", it)
				}
			})

			t.Run("promoted from an additional_tools item", func(t *testing.T) {
				sent, got := run.send(t, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,"input":[{"type":"additional_tools","role":"developer","tools":[%s]},{"role":"user","content":"go"}]}`, stream, sqlTool+","+noteTool),
					call{"note", customArgs("n")})
				if want := "function:run_sql,function:note"; strings.Join(sentTools(sent), ",") != want {
					t.Errorf("tools = %v, want %s", sentTools(sent), want)
				}
				if d := sent["tools"].([]any)[0].(map[string]any)["description"]; d != sqlDescription {
					t.Errorf("description = %q", d)
				}
				wantCustomCall(t, got, "note", "", "n")

				_, got = run.send(t, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"go"}]}`, stream, flatCustomNS()),
					call{"ns__run_sql", customArgs("q")})
				wantCustomCall(t, got, "run_sql", "ns", "q")
			})

			t.Run("tool choice names it", func(t *testing.T) {
				for _, c := range []struct{ tools, choice, want string }{
					{sqlTool, `{"type":"custom","name":"run_sql"}`, `{"type":"function","name":"run_sql"}`},
					{sqlTool + `,` + noteTool, `{"type":"allowed_tools","mode":"auto","tools":[{"type":"custom","name":"run_sql"},{"type":"custom","name":"note"}]}`,
						`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"run_sql"},{"type":"function","name":"note"}]}`},
					{flatCustomNS(), `{"type":"custom","namespace":"ns","name":"run_sql"}`, `{"type":"function","name":"ns__run_sql"}`},
					{foldedCustomNS(), `{"type":"custom","namespace":"ns","name":"run_sql"}`, `{"type":"function","name":"ns"}`},
					{flatCustomNS(), `{"type":"allowed_tools","mode":"required","tools":[{"type":"custom","namespace":"ns","name":"run_sql"},{"type":"function","namespace":"ns","name":"lookup"}]}`,
						`{"type":"allowed_tools","mode":"required","tools":[{"type":"function","name":"ns__run_sql"},{"type":"function","name":"ns__lookup"}]}`},
					{sqlTool, `{"type":"custom","name":"other"}`, `{"type":"custom","name":"other"}`},
					{flatCustomNS(), `{"type":"custom","namespace":"other","name":"run_sql"}`, `{"type":"custom","namespace":"other","name":"run_sql"}`},
				} {
					sent, _ := run.send(t, with(c.tools, `"tool_choice":`+c.choice+`,`), call{"run_sql", customArgs("q")})
					got, _ := json.Marshal(sent["tool_choice"])
					sameJSON(t, "tool_choice "+c.choice, got, c.want)
				}
			})

			t.Run("first declaration wins", func(t *testing.T) {
				fn := `{"type":"function","name":"run_sql","description":"mine","parameters":{"type":"object"}}`
				sent, got := run.send(t, with(fn+`,`+sqlTool, ""), call{"run_sql", "{}"})
				if tools, _ := sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["description"] != "mine" {
					t.Errorf("tools = %v, want the function alone", sent["tools"])
				}
				if it := item0(t, got); it["type"] != "function_call" {
					t.Errorf("client got %v, want a function call", it)
				}
				sent, got = run.send(t, with(sqlTool+`,`+fn, ""), call{"run_sql", customArgs("q")})
				if tools, _ := sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["description"] != sqlDescription {
					t.Errorf("tools = %v, want the custom tool's function alone", sent["tools"])
				}
				wantCustomCall(t, got, "run_sql", "", "q")

				sent, got = run.send(t, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"go"}],"tools":[%s]}`, stream, sqlTool, fn), call{"run_sql", "{}"})
				if tools, _ := sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["description"] != "mine" {
					t.Errorf("tools = %v, want the listed function alone", sent["tools"])
				}
				if it := item0(t, got); it["type"] != "function_call" {
					t.Errorf("client got %v, want a function call", it)
				}

				both := sqlTool + `,` + flatCustomNS()
				_, got = run.send(t, with(both, ""), call{"run_sql", customArgs("top")})
				wantCustomCall(t, got, "run_sql", "", "top")
				_, got = run.send(t, with(both, ""), call{"ns__run_sql", customArgs("child")})
				wantCustomCall(t, got, "run_sql", "ns", "child")
			})
		})
	}
}

func TestCustomToolHistory(t *testing.T) {
	type items struct {
		Input []map[string]any `json:"input"`
	}
	read := func(t *testing.T, body string) []map[string]any {
		t.Helper()
		got := responsesBody(t, body)
		var m items
		if err := json.Unmarshal(got, &m); err != nil {
			t.Fatalf("input: %v: %s", err, got)
		}
		return m.Input
	}
	t.Run("top-level", func(t *testing.T) {
		in := read(t, `{"model":"grok-4.6","input":[
			{"type":"custom_tool_call","call_id":"c1","name":"run_sql","input":"select 1"},
			{"type":"custom_tool_call_output","call_id":"c1","output":"ok"},
			{"type":"custom_tool_call","call_id":"c2","name":"note","input":"{\"a\":1}"}],"tools":[`+sqlTool+`,`+noteTool+`]}`)
		if len(in) != 3 {
			t.Fatalf("input = %v", in)
		}
		for i, want := range []string{`{"input":"select 1"}`, "", `{"input":"{\"a\":1}"}`} {
			if i == 1 {
				if in[1]["type"] != "function_call_output" || in[1]["call_id"] != "c1" || in[1]["output"] != "ok" {
					t.Errorf("output = %v", in[1])
				}
				continue
			}
			if in[i]["type"] != "function_call" || in[i]["arguments"] != want || in[i]["input"] != nil || in[i]["namespace"] != nil {
				t.Errorf("call %d = %v, want a function_call with arguments %s", i, in[i], want)
			}
		}
	})
	t.Run("a tool that is not declared custom keeps its rule", func(t *testing.T) {
		in := read(t, `{"model":"grok-4.6","input":[
			{"type":"custom_tool_call","call_id":"c1","name":"other","input":"{\"a\":1}"},
			{"type":"custom_tool_call","call_id":"c2","name":"f","input":"{\"b\":2}"}],"tools":[`+sqlTool+`,{"type":"function","name":"f","parameters":{"type":"object"}}]}`)
		if in[0]["arguments"] != `{"a":1}` || in[1]["arguments"] != `{"b":2}` {
			t.Errorf("calls = %v", in)
		}
	})
	t.Run("in a namespace", func(t *testing.T) {
		history := `"input":[{"type":"custom_tool_call","call_id":"c1","namespace":"ns","name":"run_sql","input":"old"}`
		for _, c := range []struct{ name, tools, wantName, wantArgs string }{
			{"flattened", flatCustomNS(), "ns__run_sql", `{"input":"old"}`},
			{"folded", foldedCustomNS(), "ns", `{"arguments":{"input":"old"},"name":"run_sql"}`},
		} {
			in := read(t, `{"model":"grok-4.6",`+history+`],"tools":[`+c.tools+`]}`)
			if call := in[0]; call["type"] != "function_call" || call["name"] != c.wantName || call["arguments"] != c.wantArgs || call["namespace"] != nil || call["input"] != nil {
				t.Errorf("%s: call = %v, want a function_call %s with arguments %s", c.name, call, c.wantName, c.wantArgs)
			}
		}
	})
	t.Run("input that is not a string", func(t *testing.T) {
		req := `{"model":"grok-4.6","input":[{"type":"custom_tool_call","call_id":"c","name":"run_sql","input":{"x":1}}],"tools":[` + sqlTool + `]}`
		parsed, err := openairesponses.ParseRequest([]byte(req))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := buildBody(parsed); err == nil || !strings.Contains(err.Error(), "run_sql history input must be a string") {
			t.Errorf("error = %v, want one that says the input must be a string", err)
		}
	})
}

func TestCustomToolStreamedReply(t *testing.T) {
	item := func(name, status, args string) string {
		return `{"id":"fc_1","type":"function_call","status":"` + status + `","call_id":"call_1","name":"` + name + `","arguments":` + jsonString(args) + `}`
	}
	args := customArgs("select 1")
	t.Run("top-level", func(t *testing.T) {
		body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + sqlTool + `]}`
		got, err := replyStream(t, body, sseOf(created(0), added(1, 0, item("run_sql", "in_progress", "")),
			argsDelta(2, 0, "fc_1", args[:13]), argsDelta(3, 0, "fc_1", args[13:]),
			argsDone(4, 0, "fc_1", args), itemDone(5, 0, item("run_sql", "completed", args)), completed(6, item("run_sql", "completed", args))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call run_sql input=""`,
			`2 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="sel"`,
			`3 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="ect 1"`,
			`4 response.custom_tool_call_input.done [0] fc_1 call=call_1 input="select 1"`,
			`5 response.output_item.done [0] custom_tool_call run_sql input="select 1"`,
			`6 response.completed output: custom_tool_call run_sql input="select 1"`,
		})
		got, err = replyStream(t, body, sseOf(created(0), completed(1, item("run_sql", "completed", args))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call run_sql input=""`,
			`2 response.custom_tool_call_input.done [0] fc_1 call=call_1 input="select 1"`,
			`3 response.output_item.done [0] custom_tool_call run_sql input="select 1"`,
			`4 response.completed output: custom_tool_call run_sql input="select 1"`,
		})
	})
	t.Run("a function and the custom tool of one request", func(t *testing.T) {
		body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + sqlTool + `,{"type":"function","name":"f","parameters":{"type":"object"}}]}`
		got, err := replyStream(t, body, sseOf(created(0), added(1, 0, item("f", "in_progress", "")), argsDone(2, 0, "fc_1", `{"a":1}`),
			itemDone(3, 0, item("f", "completed", `{"a":1}`)), completed(4, item("f", "completed", `{"a":1}`))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] function_call f arguments=""`,
			`2 response.function_call_arguments.done [0] arguments="{\"a\":1}"`,
			`3 response.output_item.done [0] function_call f arguments="{\"a\":1}"`,
			`4 response.completed output: function_call f arguments="{\"a\":1}"`,
		})
	})
	t.Run("in a namespace", func(t *testing.T) {
		flat := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + flatCustomNS() + `]}`
		got, err := replyStream(t, flat, sseOf(created(0), added(1, 0, item("ns__run_sql", "in_progress", "")),
			argsDelta(2, 0, "fc_1", args[:13]), argsDelta(3, 0, "fc_1", args[13:]),
			argsDone(4, 0, "fc_1", args), itemDone(5, 0, item("ns__run_sql", "completed", args)), completed(6, item("ns__run_sql", "completed", args))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call run_sql ns=ns input=""`,
			`2 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="sel"`,
			`3 response.custom_tool_call_input.delta [0] fc_1 call=call_1 delta="ect 1"`,
			`4 response.custom_tool_call_input.done [0] fc_1 call=call_1 input="select 1"`,
			`5 response.output_item.done [0] custom_tool_call run_sql ns=ns input="select 1"`,
			`6 response.completed output: custom_tool_call run_sql ns=ns input="select 1"`,
		})

		folded := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + foldedCustomNS() + `]}`
		wrapper := `{"name":"run_sql","arguments":` + args + `}`
		got, err = replyStream(t, folded, sseOf(created(0), added(1, 0, item("ns", "in_progress", "")),
			argsDelta(2, 0, "fc_1", wrapper[:30]), argsDelta(3, 0, "fc_1", wrapper[30:]),
			argsDone(4, 0, "fc_1", wrapper), itemDone(5, 0, item("ns", "completed", wrapper)), completed(6, item("ns", "completed", wrapper))))
		if err != nil {
			t.Fatal(err)
		}
		expect(t, got, []string{
			`0 response.created `,
			`1 response.output_item.added [0] custom_tool_call run_sql ns=ns input=""`,
			`4 response.custom_tool_call_input.done [0] fc_1 call=call_1 input="select 1"`,
			`5 response.output_item.done [0] custom_tool_call run_sql ns=ns input="select 1"`,
			`6 response.completed output: custom_tool_call run_sql ns=ns input="select 1"`,
		})
	})
	t.Run("arguments without input", func(t *testing.T) {
		body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + sqlTool + `]}`
		_, err := replyStream(t, body, sseOf(created(0), added(1, 0, item("run_sql", "in_progress", "")), argsDone(2, 0, "fc_1", `{"query":"x"}`),
			itemDone(3, 0, item("run_sql", "completed", `{"query":"x"}`)), completed(4, item("run_sql", "completed", `{"query":"x"}`))))
		if err == nil || !strings.Contains(err.Error(), "custom tool run_sql") || strings.Contains(err.Error(), "query") {
			t.Errorf("error %v, want one that names run_sql and does not repeat the arguments", err)
		}
	})
}

func TestCustomToolBridgeLeavesSearchCalls(t *testing.T) {
	body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[` + sqlTool + `]}`
	search := `{"id":"trace","type":"custom_tool_call","status":"completed","call_id":"xs_call_1","name":"x_keyword_search","input":"internal query"}`
	args := customArgs("select 1")
	call := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"run_sql","arguments":` + jsonString(args) + `}`
	got, err := replyStream(t, body, sseOf(created(0), completed(1, search, call)))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range got {
		if strings.Contains(line, "x_keyword_search") && !strings.Contains(line, "response.completed") {
			t.Errorf("an event about the search call: %s", line)
		}
	}
	want := `response.completed output: custom_tool_call x_keyword_search input="internal query"; custom_tool_call run_sql input="select 1"`
	if last := got[len(got)-1]; !strings.HasSuffix(last, want) {
		t.Errorf("last event %q, want one ending %q", last, want)
	}
}
