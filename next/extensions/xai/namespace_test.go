package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
)

func toolsOf(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var m struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m.Tools
}

func names(tools []map[string]any) string {
	var n []string
	for _, tool := range tools {
		n = append(n, fmt.Sprint(tool["type"], ":", tool["name"]))
	}
	return strings.Join(n, ",")
}

func lookups(n int) string {
	var kids []string
	for i := 0; i < n; i++ {
		kids = append(kids, fmt.Sprintf(`{"type":"function","name":"lookup%d"}`, i))
	}
	return strings.Join(kids, ",")
}

// A namespace's tools are sent as functions named by the namespace and the tool; no prefix is added
// to a name that has it or starts with mcp__; tool_search is left out, and the first of a name stays.
func TestNamespaceFlattened(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[
		{"type":"function","name":"get_time"},
		{"type":"namespace","name":"weather","tools":[
			{"type":"function","name":"get_forecast","description":"f","parameters":{"type":"object","properties":{"c":{"type":"string"}}}},
			{"type":"function","name":"weather__alerts"},
			{"type":"function","name":"mcp__raw"},
			{"type":"custom","name":"get_time"},
			{"type":"tool_search"},
			{"type":"function","name":"get_forecast"}]},
		{"type":"namespace","name":"clock__","tools":[{"type":"function","name":"now"}]}]}`)
	tools := toolsOf(t, got)
	if want := "function:get_time,function:weather__get_forecast,function:weather__alerts,function:mcp__raw,function:weather__get_time,function:clock__now"; names(tools) != want {
		t.Errorf("tools = %s\nwant    %s", names(tools), want)
	}
	if tools[1]["description"] != "f" || tools[1]["parameters"] == nil {
		t.Errorf("the child lost its fields: %v", tools[1])
	}
}

// With 201 tools once flattened a namespace is folded into one function, and its folded tools stay
// when the tools are cut to 200; at 200 it is flattened.
func TestNamespaceFold(t *testing.T) {
	flat := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"namespace","name":"ns","tools":[`+lookups(200)+`]}]}`)
	if tools := toolsOf(t, flat); len(tools) != 200 || tools[0]["name"] != "ns__lookup0" {
		t.Errorf("200 tools in a namespace: %d tools, the first %v", len(tools), tools[0]["name"])
	}
	var plain []string
	for i := 0; i < 205; i++ {
		plain = append(plain, fmt.Sprintf(`{"type":"function","name":"plain%d"}`, i))
	}
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[`+strings.Join(plain, ",")+`,
		{"type":"namespace","name":"ns","description":"The ns.","tools":[{"type":"function","name":"a","description":"does a","parameters":{"type":"object","properties":{"x":{"type":"string"}}}},`+lookups(200)+`]}]}`)
	tools := toolsOf(t, got)
	if len(tools) != maxTools || tools[0]["name"] != "ns" || tools[1]["name"] != "plain0" || tools[maxTools-1]["name"] != "plain198" {
		t.Fatalf("%d tools, the first %v, the second %v; want the dispatcher, then the first 199 plain tools", len(tools), tools[0]["name"], tools[1]["name"])
	}
	d := tools[0]
	desc, _ := d["description"].(string)
	if !strings.HasPrefix(desc, "The ns.\n\nAvailable tools in this namespace:\n- a: does a\n  Parameters: {\"properties\":{\"x\":{\"type\":\"string\"}},\"type\":\"object\"}\n- lookup0\n") {
		t.Errorf("description %.200q", desc)
	}
	params, _ := json.Marshal(d["parameters"])
	var p struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(params, &p)
	if len(p.Properties.Name.Enum) != 201 || p.Properties.Name.Enum[0] != "a" || len(p.Required) != 1 {
		t.Errorf("parameters %s", params)
	}
}

// A call in the history and a tool choice that name a namespace are written the way the tools were
// sent: to the qualified function, or to the dispatcher with the child in its arguments.
func TestNamespaceHistoryAndChoice(t *testing.T) {
	call := `{"type":"function_call","call_id":"c1","namespace":"ns","name":"a","arguments":"{\"x\":\"1\"}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}`
	choice := `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","namespace":"ns","name":"a"},{"type":"function","name":"plain"}]}`
	for _, c := range []struct {
		name, tools, call, choice string
	}{
		{"flattened", `[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"a"}]}]`,
			`{"type":"function_call","call_id":"c1","name":"ns__a","arguments":"{\"x\":\"1\"}"}`,
			`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"ns__a"},{"type":"function","name":"plain"}]}`},
		{"folded", `[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"a"},` + lookups(200) + `]}]`,
			`{"type":"function_call","call_id":"c1","name":"ns","arguments":"{\"arguments\":{\"x\":\"1\"},\"name\":\"a\"}"}`,
			`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"ns"},{"type":"function","name":"plain"}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := responsesBody(t, `{"model":"grok-4.6","input":[`+call+`],"tool_choice":`+choice+`,"tools":`+c.tools+`}`)
			var m struct {
				Input  []json.RawMessage `json:"input"`
				Choice json.RawMessage   `json:"tool_choice"`
			}
			_ = json.Unmarshal(got, &m)
			sameJSON(t, "call", m.Input[0], c.call)
			sameJSON(t, "choice", m.Choice, c.choice)
		})
	}
}

// The Codex app's automation_update gets parameters that accept any object, flattened or already
// named for its namespace; other tools keep theirs.
func TestAutomationUpdateParameters(t *testing.T) {
	big := `{"oneOf":[{"$ref":"#/$defs/A"}],"$defs":{"A":{"type":"object"}}}`
	for name, tools := range map[string]string{
		"in its namespace": `[{"type":"namespace","name":"codex_app","tools":[{"type":"function","name":"automation_update","strict":true,"parameters":` + big + `}]}]`,
		"in codex_apps":    `[{"type":"namespace","name":"codex_apps","tools":[{"type":"function","name":"automation_update","parameters":` + big + `}]}]`,
		"already named":    `[{"type":"function","name":"codex_app__automation_update","parameters":` + big + `}]`,
	} {
		tool := toolsOf(t, responsesBody(t, `{"model":"grok-4.6","input":[],"tools":`+tools+`}`))[0]
		params, _ := json.Marshal(tool["parameters"])
		sameJSON(t, name, params, `{"type":"object","properties":{},"additionalProperties":true}`)
		if tool["strict"] == true {
			t.Errorf("%s: strict is still true", name)
		}
	}
	other := toolsOf(t, responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"namespace","name":"other","tools":[{"type":"function","name":"automation_update","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}]}]}`))[0]
	if params, _ := json.Marshal(other["parameters"]); !strings.Contains(string(params), `"a"`) {
		t.Errorf("automation_update of another namespace lost its parameters: %s", params)
	}
}

func nsCall(name, args string) string {
	return `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"` + name + `","arguments":` + jsonString(args) + `}`
}

const nsRequest = `{"model":"grok-4.6","stream":true,"input":[],"tools":[{"type":"namespace","name":"weather","tools":[{"type":"function","name":"get_forecast"}]}]}`

// A flattened namespace's call goes to the client as the call to the tool in its namespace, in
// every event that shows the item.
func TestNamespaceFlattenedReply(t *testing.T) {
	args := `{"city":"Oslo"}`
	added1 := `{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"weather__get_forecast","arguments":""}`
	got, err := replyStream(t, nsRequest, sseOf(created(0), added(1, 0, added1), argsDelta(2, 0, "fc_1", args), argsDone(3, 0, "fc_1", args),
		itemDone(4, 0, nsCall("weather__get_forecast", args)), completed(5, nsCall("weather__get_forecast", args))))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("function_call get_forecast ns=weather arguments=%q", args)
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] function_call get_forecast ns=weather arguments=""`,
		`2 response.function_call_arguments.delta [0] delta=` + fmt.Sprintf("%q", args),
		`3 response.function_call_arguments.done [0] arguments=` + fmt.Sprintf("%q", args),
		`4 response.output_item.done [0] ` + want,
		`5 response.completed output: ` + want,
	})
}

// A folded namespace's call goes to the client as the call to the child, with its own arguments,
// when the arguments name one of the namespace's tools; otherwise it stays the call to the namespace.
func TestNamespaceFoldedReply(t *testing.T) {
	body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"a"},` + lookups(200) + `]}]}`
	wrapper := `{"name":"a","arguments":{"x":1}}`
	started := `{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"ns","arguments":""}`
	got, err := replyStream(t, body, sseOf(created(0), added(1, 0, started), argsDelta(2, 0, "fc_1", wrapper), argsDone(3, 0, "fc_1", wrapper),
		itemDone(4, 0, nsCall("ns", wrapper)), completed(5, nsCall("ns", wrapper))))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{
		`0 response.created `,
		`1 response.output_item.added [0] function_call ns ns=ns arguments=""`,
		`2 response.function_call_arguments.delta [0] delta=` + fmt.Sprintf("%q", wrapper),
		`3 response.function_call_arguments.done [0] arguments="{\"x\":1}"`,
		`4 response.output_item.done [0] function_call a ns=ns arguments="{\"x\":1}"`,
		`5 response.completed output: function_call a ns=ns arguments="{\"x\":1}"`,
	})
	for _, c := range []struct{ name, args, wantName, wantArgs string }{
		{"a string of arguments", `{"name":"a","arguments":"{\"x\":2}"}`, "a", `{"x":2}`},
		{"no arguments field", `{"name":"a","x":3}`, "a", `{"x":3}`},
		{"no arguments at all", `{"name":"a"}`, "a", `{}`},
		{"not a tool of the namespace", `{"name":"zzz","arguments":{}}`, "ns", `{"name":"zzz","arguments":{}}`},
		{"not a call", `{"x":1}`, "ns", `{"x":1}`},
		{"not JSON", `nope`, "ns", `nope`},
	} {
		got, err := replyStream(t, body, sseOf(created(0), itemDone(1, 0, nsCall("ns", c.args)), completed(2, nsCall("ns", c.args))))
		if err != nil {
			t.Fatal(c.name, err)
		}
		want := fmt.Sprintf("function_call %s ns=ns arguments=%q", c.wantName, c.wantArgs)
		expect(t, got, []string{`0 response.created `, `1 response.output_item.done [0] ` + want, `2 response.completed output: ` + want})
	}
}

// A reply read as one response is restored too, and a request with an apply_patch tool and a
// namespace turns back both.
func TestNamespaceNonStreamedAndWithPatch(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[],"tools":[
		{"type":"custom","name":"apply_patch"},
		{"type":"namespace","name":"weather","tools":[{"type":"function","name":"get_forecast"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _, bridge, err := buildRequest(req, false)
	if err != nil || bridge == nil || bridge.patch == nil || bridge.ns == nil {
		t.Fatalf("bridge %+v, error %v; want both", bridge, err)
	}
	patchItem := callItem("completed", patchArgs)
	body := `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"grok","output":[` + nsCall("weather__get_forecast", `{"c":"o"}`) + `,` + patchItem + `]}`
	events, err := openairesponses.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	events, err = convert(bridge, events)
	if err != nil {
		t.Fatal(err)
	}
	agg := openairesponses.NewAggregator(req)
	for _, ev := range events {
		agg.Add(ev)
	}
	out, _ := agg.Response()
	var resp struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.Unmarshal(out, &resp); err != nil || len(resp.Output) != 2 {
		t.Fatalf("%s: %v", out, err)
	}
	if a, b := resp.Output[0], resp.Output[1]; a["name"] != "get_forecast" || a["namespace"] != "weather" || a["arguments"] != `{"c":"o"}` || b["type"] != "custom_tool_call" || b["input"] != patchText {
		t.Errorf("output %v", resp.Output)
	}
}

// A request without namespace tools has nothing to restore.
func TestNoNamespacesNoRestorer(t *testing.T) {
	req, _ := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f"}]}`))
	if _, _, bridge, err := buildRequest(req, false); err != nil || bridge != nil {
		t.Errorf("bridge %v, error %v; want none", bridge, err)
	}
}

// The upstream may open with the call's added event, with no response.created before it. The call
// is then restored from its first event on.
func TestNamespaceReplyWithoutCreated(t *testing.T) {
	args := `{"city":"Oslo"}`
	started := `{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"weather__get_forecast","arguments":""}`
	got, err := replyStream(t, nsRequest, sseOf(added(0, 0, started), argsDelta(1, 0, "fc_1", args), argsDone(2, 0, "fc_1", args),
		itemDone(3, 0, nsCall("weather__get_forecast", args)), completed(4, nsCall("weather__get_forecast", args))))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("function_call get_forecast ns=weather arguments=%q", args)
	expect(t, got, []string{
		`0 response.output_item.added [0] function_call get_forecast ns=weather arguments=""`,
		`1 response.function_call_arguments.delta [0] delta=` + fmt.Sprintf("%q", args),
		`2 response.function_call_arguments.done [0] arguments=` + fmt.Sprintf("%q", args),
		`3 response.output_item.done [0] ` + want,
		`4 response.completed output: ` + want,
	})
}

// A history call and a tool choice in a namespace go to the namespace's function, not to a plain
// function that has the namespace's name, when the namespace was flattened.
func TestNamespaceNamedLikeAFunction(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[{"type":"function_call","call_id":"c1","namespace":"ns","name":"a","arguments":"{\"x\":1}"}],
		"tool_choice":{"type":"function","namespace":"ns","name":"a"},
		"tools":[{"type":"function","name":"ns"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"a"}]}]}`)
	var m struct {
		Input  []json.RawMessage `json:"input"`
		Choice json.RawMessage   `json:"tool_choice"`
	}
	_ = json.Unmarshal(got, &m)
	sameJSON(t, "call", m.Input[0], `{"type":"function_call","call_id":"c1","name":"ns__a","arguments":"{\"x\":1}"}`)
	sameJSON(t, "choice", m.Choice, `{"type":"function","name":"ns__a"}`)
	if n := names(toolsOf(t, got)); n != "function:ns,function:ns__a" {
		t.Errorf("tools = %s", n)
	}
}

// A namespace declared more than once is one folded function that has the children of every
// declaration, each child as its first declaration has it.
func TestNamespaceDeclaredTwice(t *testing.T) {
	body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[
		{"type":"namespace","name":"ns","description":"First.","tools":[` + lookups(201) + `]},
		{"type":"namespace","name":"ns","description":"Second.","tools":[{"type":"function","name":"lookup0","description":"again"},{"type":"function","name":"last_child"}]}]}`
	tools := toolsOf(t, responsesBody(t, body))
	if len(tools) != 1 || tools[0]["name"] != "ns" {
		t.Fatalf("tools = %s, want one folded function", names(tools))
	}
	params, _ := json.Marshal(tools[0]["parameters"])
	var p struct {
		Properties struct {
			Name struct {
				Enum []string `json:"enum"`
			} `json:"name"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(params, &p)
	enum := p.Properties.Name.Enum
	if len(enum) != 202 || enum[0] != "lookup0" || enum[201] != "last_child" {
		t.Errorf("%d children, the last %q; want 202, the last last_child", len(enum), enum[len(enum)-1])
	}
	if d, _ := tools[0]["description"].(string); !strings.HasPrefix(d, "First.\n\n") || strings.Contains(d, "again") {
		t.Errorf("description %.80q: want the first declaration's, with lookup0 as it first had it", d)
	}
	// A call to the child only the second declaration has comes back as that child.
	wrapper := `{"name":"last_child","arguments":{}}`
	got, err := replyStream(t, body, sseOf(created(0), itemDone(1, 0, nsCall("ns", wrapper)), completed(2, nsCall("ns", wrapper))))
	if err != nil {
		t.Fatal(err)
	}
	want := `function_call last_child ns=ns arguments="{}"`
	expect(t, got, []string{`0 response.created `, `1 response.output_item.done [0] ` + want, `2 response.completed output: ` + want})
}

// A function in a namespace that is called apply_patch is an ordinary function, whatever the
// top-level custom apply_patch tool is: it is not given the patch rules.
func TestNamespacedApplyPatchIsNotThePatchTool(t *testing.T) {
	body := `{"model":"grok-4.6","stream":true,"input":[],"tools":[{"type":"custom","name":"apply_patch"},
		{"type":"namespace","name":"ns","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}]}`
	started := `{"id":"fc_9","type":"function_call","status":"in_progress","call_id":"call_9","name":"ns__apply_patch","arguments":""}`
	other := `{"id":"fc_9","type":"function_call","status":"completed","call_id":"call_9","name":"ns__apply_patch","arguments":"{}"}`
	got, err := replyStream(t, body, sseOf(created(0), added(1, 0, started), argsDone(2, 0, "fc_9", "{}"), itemDone(3, 0, other), completed(4, other)))
	if err != nil {
		t.Fatal(err)
	}
	want := `function_call apply_patch ns=ns arguments="{}"`
	expect(t, got, []string{`0 response.created `, `1 response.output_item.added [0] function_call apply_patch ns=ns arguments=""`,
		`2 response.function_call_arguments.done [0] arguments="{}"`, `3 response.output_item.done [0] ` + want, `4 response.completed output: ` + want})
	// Without any earlier event for it, the final response restores it the same way.
	got, err = replyStream(t, body, sseOf(created(0), completed(1, other)))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, []string{`0 response.created `, `1 response.completed output: ` + want})
	// The top-level tool still is the patch tool.
	text := fmt.Sprintf("%q", patchText)
	got, err = replyStream(t, body, sseOf(created(0), added(1, 0, callItem("in_progress", "")), argsDone(2, 0, "fc_1", patchArgs), itemDone(3, 0, callItem("completed", patchArgs)), completed(4, callItem("completed", patchArgs))))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || !strings.HasSuffix(got[4], "custom_tool_call apply_patch input="+text) {
		t.Errorf("events:\n%s", strings.Join(got, "\n"))
	}
}

// The fold decision counts the x_search tool the extension is going to inject, as today's core
// counts it: 200 tools in a namespace are flattened, and with the injected tool they are 201 and
// folded. A request that has x_search of its own gets no injected tool, so nothing is added.
func TestNamespaceFoldCountsInjectedSearch(t *testing.T) {
	build := func(body string, inject bool) []map[string]any {
		t.Helper()
		req, err := openairesponses.ParseRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		out, _, err := buildSearchBody(req, inject)
		if err != nil {
			t.Fatal(err)
		}
		return toolsOf(t, out)
	}
	ns := func(n int, more string) string {
		return `{"model":"grok-4.6","input":[],"tools":[{"type":"namespace","name":"ns","tools":[` + lookups(n) + `]}` + more + `]}`
	}
	if got := build(ns(200, ""), false); len(got) != 200 || got[0]["name"] != "ns__lookup0" {
		t.Errorf("200 tools, no injection: %d tools, the first %v", len(got), got[0]["name"])
	}
	got := build(ns(200, ""), true)
	if len(got) != 2 || got[0]["name"] != "ns" || got[1]["type"] != "x_search" {
		t.Errorf("200 tools and an injected x_search: %d tools, the first %v; want the dispatcher and x_search", len(got), got[0]["name"])
	}
	if got := build(ns(199, ""), true); len(got) != 200 || got[0]["name"] != "ns__lookup0" || got[199]["type"] != "x_search" {
		t.Errorf("199 tools and an injected x_search: %d tools, the first %v, the last %v", len(got), got[0]["name"], got[len(got)-1]["type"])
	}
	if got := build(ns(199, `,{"type":"x_search"}`), true); len(got) != 200 || got[0]["name"] != "ns__lookup0" || got[199]["type"] != "x_search" {
		t.Errorf("199 tools and the client's x_search: %d tools, the first %v", len(got), got[0]["name"])
	}
}
