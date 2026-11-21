package openairesponses_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A Responses client's custom, namespace and tool_search tools, sent to each format that has none
// of them and restored on the reply.

type wireCall struct{ name, args string }

// wire reads what one upstream format was sent, in the same terms for each format.
type wire struct {
	tools   []string
	calls   []wireCall
	results map[string]string // by call ID
	choice  []string          // the function names the tool choice names
}

type target struct {
	name   string
	format string
	encode func(llm.Request) ([]byte, []llm.Loss, error)
	names  func(llm.Request) map[string]string
	read   func(*testing.T, []byte) wire
	limit  int
}

func targets() []target {
	return []target{
		{"messages", llm.FormatAnthropicMessages, anthropicmessages.EncodeRequest, anthropicmessages.ToolNames, readMessages, 64},
		{"gemini", llm.FormatGeminiGenerate, geminigenerate.EncodeRequest, geminigenerate.ToolNames, readGemini, 128},
		{"chat", llm.FormatOpenAIChat, openaichat.EncodeRequest, openaichat.ToolNames, readChat, 64},
	}
}

func readMessages(t *testing.T, body []byte) wire {
	t.Helper()
	var b struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice struct {
			Name string `json:"name"`
		} `json:"tool_choice"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	mustJSON(t, body, &b)
	w := wire{results: map[string]string{}}
	for _, x := range b.Tools {
		w.tools = append(w.tools, x.Name)
	}
	if b.ToolChoice.Name != "" {
		w.choice = []string{b.ToolChoice.Name}
	}
	for _, m := range b.Messages {
		var blocks []struct {
			Type      string          `json:"type"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, c := range blocks {
			switch c.Type {
			case "tool_use":
				w.calls = append(w.calls, wireCall{c.Name, string(c.Input)})
			case "tool_result":
				var s string
				_ = json.Unmarshal(c.Content, &s)
				w.results[c.ToolUseID] = s
			}
		}
	}
	return w
}

func readGemini(t *testing.T, body []byte) wire {
	t.Helper()
	var b struct {
		Tools []struct {
			Decls []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FC struct {
				Allowed []string `json:"allowedFunctionNames"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
		Contents []struct {
			Parts []struct {
				Call *struct {
					Name string          `json:"name"`
					Args json.RawMessage `json:"args"`
				} `json:"functionCall"`
				Resp *struct {
					ID       string `json:"id"`
					Response struct {
						Result string `json:"result"`
					} `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	mustJSON(t, body, &b)
	w := wire{results: map[string]string{}, choice: b.ToolConfig.FC.Allowed}
	for _, x := range b.Tools {
		for _, d := range x.Decls {
			w.tools = append(w.tools, d.Name)
		}
	}
	for _, c := range b.Contents {
		for _, p := range c.Parts {
			if p.Call != nil {
				w.calls = append(w.calls, wireCall{p.Call.Name, string(p.Call.Args)})
			}
			if p.Resp != nil {
				w.results[p.Resp.ID] = p.Resp.Response.Result
			}
		}
	}
	return w
}

func readChat(t *testing.T, body []byte) wire {
	t.Helper()
	var b struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice json.RawMessage `json:"tool_choice"`
		Messages   []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	mustJSON(t, body, &b)
	w := wire{results: map[string]string{}}
	for _, x := range b.Tools {
		w.tools = append(w.tools, x.Function.Name)
	}
	var choice struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		AllowedTools struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		} `json:"allowed_tools"`
	}
	if json.Unmarshal(b.ToolChoice, &choice) == nil {
		if choice.Function.Name != "" {
			w.choice = []string{choice.Function.Name}
		}
		for _, a := range choice.AllowedTools.Tools {
			w.choice = append(w.choice, a.Function.Name)
		}
	}
	for _, m := range b.Messages {
		for _, c := range m.ToolCalls {
			if c.Type != "function" {
				t.Fatalf("a replayed call went up as %q, not a function: %s", c.Type, body)
			}
			w.calls = append(w.calls, wireCall{c.Function.Name, c.Function.Arguments})
		}
		if m.Role == "tool" {
			var s string
			_ = json.Unmarshal(m.Content, &s)
			w.results[m.ToolCallID] = s
		}
	}
	return w
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v in %s", err, b)
	}
}

func parse(t *testing.T, body string) llm.Request {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func encode(t *testing.T, tg target, req llm.Request) (wire, []llm.Loss) {
	t.Helper()
	b, losses, err := tg.encode(req)
	if err != nil {
		t.Fatal(err)
	}
	return tg.read(t, b), losses
}

func lossAt(losses []llm.Loss, path string) *llm.Loss {
	for i := range losses {
		if losses[i].Path == path {
			return &losses[i]
		}
	}
	return nil
}

// replyEvents is an upstream's one function call, as the extension sends it: under the client's
// name for what it was sent (names undoes the target's name repair).
func replyEvents(tg target, req llm.Request, sentName string, fragments ...string) []llm.Event {
	name := sentName
	if to, ok := tg.names(req)[sentName]; ok {
		name = to // the upstream saw this
	}
	for from, to := range tg.names(req) {
		if to == name {
			name = from
		}
	}
	ev := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "resp_1", Model: "m", Format: tg.format}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_1", Name: name}},
	}
	for _, f := range fragments {
		ev = append(ev, llm.Event{Type: llm.EventToolInputDelta, Index: 0, Delta: f})
	}
	return append(ev, llm.Event{Type: llm.EventItemEnd, Index: 0},
		llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}})
}

type sseEvent struct {
	typ  string
	data map[string]any
}

func stream(t *testing.T, req llm.Request, events []llm.Event) []sseEvent {
	t.Helper()
	var buf bytes.Buffer
	w := openairesponses.NewStreamWriter(&buf, req)
	for _, ev := range events {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	var out []sseEvent
	for _, chunk := range strings.Split(buf.String(), "\n\n") {
		_, data, ok := strings.Cut(chunk, "\ndata: ")
		if !ok {
			continue
		}
		var m map[string]any
		mustJSON(t, []byte(data), &m)
		out = append(out, sseEvent{m["type"].(string), m})
	}
	return out
}

func aggregate(t *testing.T, req llm.Request, events []llm.Event) map[string]any {
	t.Helper()
	a := openairesponses.NewAggregator(req)
	for _, ev := range events {
		a.Add(ev)
	}
	b, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	mustJSON(t, b, &m)
	return m
}

func eventOf(events []sseEvent, typ string) map[string]any {
	for _, e := range events {
		if e.typ == typ {
			return e.data
		}
	}
	return nil
}

func eventsOf(events []sseEvent, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range events {
		if e.typ == typ {
			out = append(out, e.data)
		}
	}
	return out
}

func outputItem(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	out, _ := response["output"].([]any)
	if len(out) != 1 {
		t.Fatalf("output = %v, want one item", response["output"])
	}
	return out[0].(map[string]any)
}

func wantFields(t *testing.T, where string, got map[string]any, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if v == nil {
			if _, has := got[k]; has {
				t.Errorf("%s: %s = %v, want none", where, k, got[k])
			}
			continue
		}
		g, _ := json.Marshal(got[k])
		w, _ := json.Marshal(v)
		if !bytes.Equal(g, w) {
			t.Errorf("%s: %s = %s, want %s", where, k, g, w)
		}
	}
}

const patchTool = `{"type":"custom","name":"apply_patch","description":"Edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}`

// Point 1: a custom tool goes up as a function, its replayed call and output as a function call
// and result, and the reply's call comes back as a custom_tool_call with its events.
func TestCustomToolGoesUpAsFunction(t *testing.T) {
	body := `{"model":"m","tools":[` + patchTool + `],"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch"},
		{"type":"custom_tool_call_output","call_id":"c1","output":"Done"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, losses := encode(t, tg, req)
			if !slices.Equal(w.tools, []string{"apply_patch"}) {
				t.Fatalf("tools = %v", w.tools)
			}
			for _, p := range []string{"tools[0]", "input[1]", "input[2]"} {
				if l := lossAt(losses, p); l != nil {
					t.Errorf("reported as lost: %+v", *l)
				}
			}
			if len(w.calls) != 1 || w.calls[0].name != "apply_patch" {
				t.Fatalf("calls = %+v", w.calls)
			}
			var args struct {
				Input string `json:"input"`
			}
			mustJSON(t, []byte(w.calls[0].args), &args)
			if args.Input != "*** Begin Patch\n*** End Patch" {
				t.Errorf("replayed input = %q", args.Input)
			}
			if w.results["c1"] != "Done" {
				t.Errorf("results = %v", w.results)
			}
		})
	}
}

func TestCustomToolDescriptionCarriesGrammar(t *testing.T) {
	req := parse(t, `{"model":"m","tools":[`+patchTool+`,{"type":"custom","name":"note","description":"Write a note.","format":{"type":"grammar","syntax":"regex","definition":"[a-z]+"}}],"input":"go"}`)
	b, _, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			Function struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Parameters  struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}
	mustJSON(t, b, &out)
	if len(out.Tools) != 2 {
		t.Fatalf("tools = %s", b)
	}
	patch, note := out.Tools[0].Function, out.Tools[1].Function
	if strings.Contains(patch.Description, "FREEFORM") || !strings.Contains(patch.Description, "start: patch") || !strings.Contains(patch.Description, "*** Begin Patch") {
		t.Errorf("apply_patch description = %q", patch.Description)
	}
	if !strings.Contains(note.Description, "Write a note.") || !strings.Contains(note.Description, "regex grammar") || !strings.Contains(note.Description, "[a-z]+") {
		t.Errorf("custom description = %q", note.Description)
	}
	if !slices.Equal(patch.Parameters.Required, []string{"input"}) || patch.Parameters.Properties["input"] == nil {
		t.Errorf("apply_patch parameters = %+v", patch.Parameters)
	}
}

func TestCustomToolCallComesBack(t *testing.T) {
	body := `{"model":"m","stream":true,"tools":[` + patchTool + `],"input":"go"}`
	// The fragments split an escape and a multi-byte character.
	fragments := []string{`{"inp`, `ut": "a\`, "nb \xc3", "\xa9\\u00e9\"", ` }`}
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			events := replyEvents(tg, req, "apply_patch", fragments...)
			got := stream(t, req, events)

			added := eventOf(got, "response.output_item.added")
			if added == nil {
				t.Fatal("no added event")
			}
			wantFields(t, "added", added["item"].(map[string]any), map[string]any{
				"type": "custom_tool_call", "name": "apply_patch", "call_id": "call_1", "input": "", "status": "in_progress", "arguments": nil})

			var text strings.Builder
			for _, d := range eventsOf(got, "response.custom_tool_call_input.delta") {
				wantFields(t, "delta", d, map[string]any{"call_id": "call_1", "item_id": added["item"].(map[string]any)["id"]})
				text.WriteString(d["delta"].(string))
			}
			const input = "a\nb éé"
			if text.String() != input {
				t.Errorf("deltas = %q, want %q", text.String(), input)
			}
			done := eventOf(got, "response.custom_tool_call_input.done")
			if done == nil {
				t.Fatal("no input done event")
			}
			wantFields(t, "input done", done, map[string]any{"input": input, "call_id": "call_1"})
			if eventOf(got, "response.function_call_arguments.delta") != nil || eventOf(got, "response.function_call_arguments.done") != nil {
				t.Error("a custom tool call has function arguments events")
			}
			item := eventOf(got, "response.output_item.done")["item"].(map[string]any)
			wantFields(t, "done", item, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "input": input, "status": "completed", "arguments": nil})
			final := outputItem(t, eventOf(got, "response.completed")["response"].(map[string]any))
			wantFields(t, "final", final, map[string]any{"type": "custom_tool_call", "input": input})

			agg := outputItem(t, aggregate(t, req, events))
			wantFields(t, "aggregate", agg, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "call_1", "input": input, "arguments": nil})
		})
	}
}

// Arguments that are not one input string are the model's mistake: the client gets them as the
// tool's input, so the tool can refuse them and the model can try again.
func TestCustomToolArgumentsNotInput(t *testing.T) {
	for _, tc := range []struct{ name, args, want string }{
		{"other field", `{"patch":"x"}`, `{"patch":"x"}`},
		{"second field", `{"input":"x","more":1}`, `{"input":"x","more":1}`},
		{"not a string", `{"input":5}`, `{"input":5}`},
		{"empty object", `{}`, ""},
		{"nothing", ``, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tg := targets()[2]
			req := parse(t, `{"model":"m","stream":true,"tools":[`+patchTool+`],"input":"go"}`)
			var fragments []string
			if tc.args != "" {
				fragments = []string{tc.args[:1], tc.args[1:]}
			}
			got := stream(t, req, replyEvents(tg, req, "apply_patch", fragments...))
			wantFields(t, "input done", eventOf(got, "response.custom_tool_call_input.done"), map[string]any{"input": tc.want})
			wantFields(t, "done", eventOf(got, "response.output_item.done")["item"].(map[string]any), map[string]any{"type": "custom_tool_call", "input": tc.want})
		})
	}
}

const mcpNamespace = `{"type":"namespace","name":"mcp__linear__","description":"Linear issues.","tools":[
	{"type":"function","name":"create_issue","description":"Create an issue.","parameters":{"type":"object","properties":{"title":{"type":"string"}}}},
	{"type":"custom","name":"note","description":"Write a note."}]}`

// Point 2: a namespace's tools are flattened, replayed calls flatten the same way, and the reply is
// restored to name plus namespace.
func TestNamespaceFlattened(t *testing.T) {
	body := `{"model":"m","tools":[` + mcpNamespace + `,{"type":"namespace","name":"crm","tools":[{"type":"function","name":"find","parameters":{"type":"object"}}]}],"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"create_issue","namespace":"mcp__linear__","arguments":"{\"title\":\"x\"}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"},
		{"type":"custom_tool_call","call_id":"c2","name":"note","namespace":"mcp__linear__","input":"hi"},
		{"type":"custom_tool_call_output","call_id":"c2","output":"noted"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, losses := encode(t, tg, req)
			want := []string{"mcp__linear__create_issue", "mcp__linear__note", "crm__find"}
			if !slices.Equal(w.tools, want) {
				t.Fatalf("tools = %v, want %v", w.tools, want)
			}
			if len(w.calls) != 2 || w.calls[0].name != "mcp__linear__create_issue" || w.calls[1].name != "mcp__linear__note" {
				t.Fatalf("calls = %+v", w.calls)
			}
			for _, l := range losses {
				if strings.Contains(l.Path, "namespace") || l.Path == "tools[0]" || l.Path == "tools[1]" {
					t.Errorf("reported as lost: %+v", l)
				}
			}

			events := replyEvents(tg, req, "mcp__linear__create_issue", `{"title":"y"}`)
			item := eventOf(stream(t, req, events), "response.output_item.done")["item"].(map[string]any)
			wantFields(t, "done", item, map[string]any{"type": "function_call", "name": "create_issue", "namespace": "mcp__linear__", "arguments": `{"title":"y"}`})
			agg := outputItem(t, aggregate(t, req, events))
			wantFields(t, "aggregate", agg, map[string]any{"type": "function_call", "name": "create_issue", "namespace": "mcp__linear__"})

			custom := replyEvents(tg, req, "mcp__linear__note", `{"input":"text"}`)
			item = eventOf(stream(t, req, custom), "response.output_item.done")["item"].(map[string]any)
			wantFields(t, "custom done", item, map[string]any{"type": "custom_tool_call", "name": "note", "namespace": "mcp__linear__", "input": "text"})
		})
	}
}

// The Codex client's default namespace, functions, is the same tool as the plain name: it adds no
// prefix, and the call goes back in the namespace the client declared.
func TestDefaultNamespaceAddsNoPrefix(t *testing.T) {
	body := `{"model":"m","input":[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"functions","description":"d","tools":[` + patchTool + `,
		{"type":"function","name":"shell","parameters":{"type":"object"}}]}]},
		{"type":"message","role":"user","content":"go"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, _ := encode(t, tg, req)
			if !slices.Equal(w.tools, []string{"apply_patch", "shell"}) {
				t.Fatalf("tools = %v", w.tools)
			}
			item := outputItem(t, aggregate(t, req, replyEvents(tg, req, "shell", `{}`)))
			wantFields(t, "shell", item, map[string]any{"type": "function_call", "name": "shell", "namespace": "functions"})
		})
	}
}

// Point 3: an additional_tools item's tools count as the request's.
func TestAdditionalTools(t *testing.T) {
	body := `{"model":"m","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}],"input":[
		{"type":"additional_tools","role":"developer","tools":[` + patchTool + `,` + mcpNamespace + `]},
		{"type":"message","role":"user","content":"go"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, losses := encode(t, tg, req)
			want := []string{"read", "apply_patch", "mcp__linear__create_issue", "mcp__linear__note"}
			if !slices.Equal(w.tools, want) {
				t.Fatalf("tools = %v, want %v", w.tools, want)
			}
			if l := lossAt(losses, "input[0]"); l != nil {
				t.Errorf("reported as lost: %+v", *l)
			}
			item := outputItem(t, aggregate(t, req, replyEvents(tg, req, "apply_patch", `{"input":"p"}`)))
			wantFields(t, "reply", item, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "input": "p"})
		})
	}
}

const clientSearch = `{"type":"tool_search","execution":"client","description":"Find tools.","parameters":{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"number"}},"required":["query"],"additionalProperties":false}}`

const deferredNamespace = `{"type":"namespace","name":"mcp__cal__","description":"Calendar.","tools":[
	{"type":"function","name":"create_event","defer_loading":true,"parameters":{"type":"object"}},
	{"type":"function","name":"list_events","parameters":{"type":"object"}}]}`

// Point 4: a tool_search the client runs is a function; its calls are restored, its replayed call
// and output go up as a call and result, and the tools it loaded become callable.
func TestClientToolSearch(t *testing.T) {
	first := `{"model":"m","tools":[` + clientSearch + `,` + deferredNamespace + `],"input":"go"}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, first)
			w, losses := encode(t, tg, req)
			want := []string{"tool_search", "mcp__cal__list_events"}
			if !slices.Equal(w.tools, want) {
				t.Fatalf("a deferred tool is declared before a search loads it: tools = %v, want %v", w.tools, want)
			}
			if l := lossAt(losses, "tools[0]"); l != nil {
				t.Errorf("reported as lost: %+v", *l)
			}

			events := replyEvents(tg, req, "tool_search", `{"query":"cal`, `endar","limit":2}`)
			got := stream(t, req, events)
			added := eventOf(got, "response.output_item.added")["item"].(map[string]any)
			wantFields(t, "added", added, map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "call_1", "status": "in_progress", "arguments": map[string]any{}})
			if eventOf(got, "response.function_call_arguments.delta") != nil {
				t.Error("a tool_search call has function arguments events")
			}
			args := map[string]any{"query": "calendar", "limit": 2}
			item := eventOf(got, "response.output_item.done")["item"].(map[string]any)
			wantFields(t, "done", item, map[string]any{"type": "tool_search_call", "execution": "client", "call_id": "call_1", "status": "completed", "arguments": args, "name": nil})
			agg := outputItem(t, aggregate(t, req, events))
			wantFields(t, "aggregate", agg, map[string]any{"type": "tool_search_call", "execution": "client", "arguments": args})

			next := parse(t, `{"model":"m","tools":[`+clientSearch+`,`+deferredNamespace+`],"input":[
				{"type":"message","role":"user","content":"go"},
				{"type":"tool_search_call","call_id":"s1","execution":"client","status":"completed","arguments":{"query":"calendar"}},
				{"type":"tool_search_output","call_id":"s1","execution":"client","status":"completed","tools":[
					{"type":"namespace","name":"mcp__cal__","description":"Calendar.","tools":[{"type":"function","name":"create_event","defer_loading":true,"parameters":{"type":"object"}}]}]}]}`)
			w, losses = encode(t, tg, next)
			want = []string{"tool_search", "mcp__cal__list_events", "mcp__cal__create_event"}
			if !slices.Equal(w.tools, want) {
				t.Fatalf("tools after a search = %v, want %v", w.tools, want)
			}
			if len(w.calls) != 1 || w.calls[0].name != "tool_search" || !strings.Contains(w.calls[0].args, "calendar") {
				t.Fatalf("calls = %+v", w.calls)
			}
			if !strings.Contains(w.results["s1"], "mcp__cal__create_event") {
				t.Errorf("result = %q, want it to name the loaded tool", w.results["s1"])
			}
			for _, p := range []string{"input[1]", "input[2]"} {
				if l := lossAt(losses, p); l != nil {
					t.Errorf("reported as lost: %+v", *l)
				}
			}
			loaded := outputItem(t, aggregate(t, next, replyEvents(tg, next, "mcp__cal__create_event", `{}`)))
			wantFields(t, "loaded", loaded, map[string]any{"type": "function_call", "name": "create_event", "namespace": "mcp__cal__"})
		})
	}
}

// A tool_search the provider would run has no counterpart upstream: it stays a reported loss, and
// the tools it would have loaded are declared so the model can still call them.
func TestHostedToolSearchIsReported(t *testing.T) {
	body := `{"model":"m","tools":[{"type":"tool_search"},` + deferredNamespace + `],"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"tool_search_call","call_id":"s1","execution":"server","status":"completed","arguments":{"query":"x"}},
		{"type":"tool_search_output","call_id":"s1","execution":"server","status":"completed","tools":[]}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			w, losses := encode(t, tg, parse(t, body))
			if slices.Contains(w.tools, "tool_search") {
				t.Errorf("a hosted tool_search went up as a function: %v", w.tools)
			}
			if !slices.Contains(w.tools, "mcp__cal__create_event") {
				t.Errorf("tools = %v, want the deferred tool declared", w.tools)
			}
			for _, p := range []string{"tools[0]", "input[1]", "input[2]"} {
				if l := lossAt(losses, p); l == nil || l.Kind != llm.LossDropped {
					t.Errorf("%s: loss = %+v, want dropped; all: %+v", p, l, losses)
				}
			}
		})
	}
}

// Point 5: names that are the same once flattened are one tool, with xAI's precedence: the tools
// list before an additional_tools item, then a top-level tool before a namespace's, then the first.
func TestDuplicateToolNames(t *testing.T) {
	body := `{"model":"m","tools":[
		{"type":"namespace","name":"ns","tools":[{"type":"function","name":"dup","description":"child","parameters":{"type":"object"}}]},
		{"type":"function","name":"ns__dup","description":"top","parameters":{"type":"object"}},
		{"type":"function","name":"twice","description":"first","parameters":{"type":"object"}},
		{"type":"custom","name":"twice","description":"second"}],
		"input":[{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"ns__dup","description":"later"},{"type":"function","name":"extra","parameters":{"type":"object"}}]},
		{"type":"message","role":"user","content":"go"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			b, losses, err := tg.encode(req)
			if err != nil {
				t.Fatal(err)
			}
			w := tg.read(t, b)
			want := []string{"ns__dup", "twice", "extra"}
			if !slices.Equal(w.tools, want) {
				t.Fatalf("tools = %v, want %v", w.tools, want)
			}
			if !bytes.Contains(b, []byte(`"top"`)) || bytes.Contains(b, []byte(`"child"`)) || bytes.Contains(b, []byte(`"later"`)) || bytes.Contains(b, []byte(`"second"`)) {
				t.Errorf("the wrong declarations stayed: %s", b)
			}
			for _, p := range []string{"tools[0].tools[0]", "tools[3]", "input[0].tools[0]"} {
				if l := lossAt(losses, p); l == nil || l.Kind != llm.LossDropped {
					t.Errorf("%s: loss = %+v, want dropped; all: %+v", p, l, losses)
				}
			}
			// The winner is a plain function, so its call is not restored to the namespace.
			item := outputItem(t, aggregate(t, req, replyEvents(tg, req, "ns__dup", `{}`)))
			wantFields(t, "reply", item, map[string]any{"type": "function_call", "name": "ns__dup", "namespace": nil})
		})
	}
}

// Point 6: a tool choice or allowed list that names one of these tools names its function.
func TestToolChoiceNamesFlattenedFunction(t *testing.T) {
	tools := `"tools":[` + patchTool + `,` + mcpNamespace + `,{"type":"function","name":"read","parameters":{"type":"object"}}]`
	for _, tc := range []struct {
		name, choice string
		want         []string
	}{
		{"custom", `{"type":"custom","name":"apply_patch"}`, []string{"apply_patch"}},
		{"namespaced function", `{"type":"function","name":"create_issue","namespace":"mcp__linear__"}`, []string{"mcp__linear__create_issue"}},
		{"namespaced custom", `{"type":"custom","name":"note","namespace":"mcp__linear__"}`, []string{"mcp__linear__note"}},
		{"allowed list", `{"type":"allowed_tools","mode":"required","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"create_issue","namespace":"mcp__linear__"}]}`,
			[]string{"apply_patch", "mcp__linear__create_issue"}},
	} {
		for _, tg := range targets() {
			t.Run(tc.name+"/"+tg.name, func(t *testing.T) {
				req := parse(t, `{"model":"m",`+tools+`,"tool_choice":`+tc.choice+`,"input":"go"}`)
				w, losses := encode(t, tg, req)
				if l := lossAt(losses, "tool_choice"); l != nil {
					t.Errorf("reported as lost: %+v", *l)
				}
				if tg.name == "messages" && len(tc.want) > 1 {
					// Messages has no allowed list: only the allowed tools are defined.
					if !slices.Equal(w.tools, tc.want) {
						t.Errorf("tools = %v, want %v", w.tools, tc.want)
					}
					return
				}
				if !slices.Equal(w.choice, tc.want) {
					t.Errorf("choice = %v, want %v", w.choice, tc.want)
				}
			})
		}
	}
}

// A choice naming a tool the request does not declare stays a reported loss rather than naming a
// function upstream would refuse.
func TestToolChoiceUnknownToolStaysLost(t *testing.T) {
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, `{"model":"m","tools":[`+patchTool+`],"tool_choice":{"type":"custom","name":"nope"},"input":"go"}`)
			w, losses := encode(t, tg, req)
			if slices.Contains(w.choice, "nope") {
				t.Errorf("choice = %v", w.choice)
			}
			if lossAt(losses, "tool_choice") == nil {
				t.Errorf("no loss for the choice: %+v", losses)
			}
		})
	}
}

// A name past a target's limit is repaired for that target, and the client still gets its own.
func TestRestoredNameSurvivesNameRepair(t *testing.T) {
	long := strings.Repeat("a", 150)
	ns := "mcp__" + strings.Repeat("s", 40) + "__"
	body := `{"model":"m","tools":[{"type":"namespace","name":"` + ns + `","tools":[{"type":"function","name":"` + long + `","parameters":{"type":"object"}},{"type":"custom","name":"` + long + `b"}]},
		{"type":"custom","name":"` + long + `c"}],"input":[{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"` + long + `","namespace":"` + ns + `","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, _ := encode(t, tg, req)
			if len(w.tools) != 3 {
				t.Fatalf("tools = %v", w.tools)
			}
			seen := map[string]bool{}
			for _, name := range w.tools {
				if len(name) > tg.limit {
					t.Errorf("sent name of %d bytes, over the limit of %d", len(name), tg.limit)
				}
				seen[name] = true
			}
			if len(seen) != 3 {
				t.Errorf("repaired names collide: %v", w.tools)
			}
			if len(w.calls) != 1 || w.calls[0].name != w.tools[0] {
				t.Errorf("the replayed call went up as %+v, the tool as %s", w.calls, w.tools[0])
			}
			back := map[string]string{}
			for from, to := range tg.names(req) {
				back[to] = from
			}
			for i, want := range []map[string]any{
				{"type": "function_call", "name": long, "namespace": ns},
				{"type": "custom_tool_call", "name": long + "b", "namespace": ns},
				{"type": "custom_tool_call", "name": long + "c", "namespace": nil},
			} {
				client, ok := back[w.tools[i]]
				if !ok {
					client = w.tools[i]
				}
				ev := []llm.Event{
					{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "resp_1", Model: "m", Format: tg.format}},
					{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_1", Name: client}},
					{Type: llm.EventToolInputDelta, Index: 0, Delta: `{"input":"x"}`},
					{Type: llm.EventItemEnd, Index: 0},
					{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}},
				}
				wantFields(t, want["name"].(string)[:1], outputItem(t, aggregate(t, req, ev)), want)
			}
		})
	}
}

// A reply from a Responses upstream is the upstream's own: its calls are not rewritten, whatever
// the request declared.
func TestResponsesUpstreamIsNotRestored(t *testing.T) {
	req := parse(t, `{"model":"m","tools":[`+patchTool+`,`+mcpNamespace+`],"input":"go"}`)
	got := aggregate(t, req, []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "resp_1", Model: "m", Format: llm.FormatOpenAIResponses}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "call_1", Name: "apply_patch"}},
		{Type: llm.EventToolInputDelta, Index: 0, Delta: `{"a":1}`},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishToolCalls}},
	})
	wantFields(t, "item", outputItem(t, got), map[string]any{"type": "function_call", "name": "apply_patch", "arguments": `{"a":1}`})
}

// A request with only plain function tools is sent as it was.
func TestPlainFunctionsUnchanged(t *testing.T) {
	body := `{"model":"m","tools":[{"type":"function","name":"read","description":"Read.","parameters":{"type":"object"}}],"input":[
		{"type":"message","role":"user","content":"go"},
		{"type":"function_call","call_id":"c1","name":"read","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	for _, tg := range targets() {
		t.Run(tg.name, func(t *testing.T) {
			req := parse(t, body)
			w, losses := encode(t, tg, req)
			if !slices.Equal(w.tools, []string{"read"}) || len(w.calls) != 1 || w.calls[0].name != "read" {
				t.Errorf("wire = %+v", w)
			}
			if len(losses) != 0 {
				t.Errorf("losses = %+v", losses)
			}
			if names := tg.names(req); len(names) != 0 {
				t.Errorf("names = %v", names)
			}
		})
	}
}
