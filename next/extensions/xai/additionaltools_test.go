package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// A Responses Lite client (the Codex app) sends tools in input items of type additional_tools, and
// xAI does not take those items. The extension runs as a process, as core runs it, against the
// fake xAI: the tests read the body the fake received and the response the client was sent.
type liteRun struct {
	srv *xaitest.Server
	ext *host.Extension
	key string
}

func newLiteRun(t *testing.T) *liteRun {
	t.Helper()
	srv := xaitest.New(t)
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	return &liteRun{srv: srv, ext: ext, key: srv.Key()}
}

// call is the call xAI answers with: the function it names, with its arguments.
type call struct{ name, arguments string }

// send sends request to the extension and has the fake answer with one call. It returns the body
// the fake received and the client's response.
func (r *liteRun) send(t *testing.T, request string, c call) (sent, got map[string]any) {
	t.Helper()
	before := len(r.srv.Requests())
	r.srv.Generate = func(w http.ResponseWriter, _ *http.Request, req xaitest.Request) {
		var in struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(req.Body, &in)
		item := func(status string) string {
			args := ""
			if status == "completed" {
				args = c.arguments
			}
			return `{"id":"fc_1","type":"function_call","status":"` + status + `","call_id":"call_1","name":` + jsonString(c.name) + `,"arguments":` + jsonString(args) + `}`
		}
		if in.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sseOf(created(0), added(1, 0, item("in_progress")), argsDone(2, 0, "fc_1", c.arguments), itemDone(3, 0, item("completed")), completed(4, item("completed"))))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"grok","output":[%s],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, item("completed"))
	}
	req, err := openairesponses.ParseRequest([]byte(request))
	if err != nil {
		t.Fatal(err)
	}
	account := protocol.Account{ID: "xai", Settings: r.srv.Settings(), Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: r.key}}
	s := r.ext.Generate(t.Context(), account, req)
	defer s.Close()
	agg := openairesponses.NewAggregator(req)
	for {
		ev := s.Next()
		if ev.Type == llm.EventError {
			t.Fatal(ev.Error)
		}
		agg.Add(ev)
		if ev.Terminal() {
			break
		}
	}
	reqs := r.srv.Requests()
	if len(reqs) != before+1 {
		t.Fatalf("%d requests reached the fake, want 1", len(reqs)-before)
	}
	if err := json.Unmarshal(reqs[before].Body, &sent); err != nil {
		t.Fatal(err)
	}
	raw, err := agg.Response()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return sent, got
}

// sentTools describes the tools of the body the fake received: type and name, in order.
func sentTools(body map[string]any) []string {
	var out []string
	list, _ := body["tools"].([]any)
	for _, v := range list {
		m, _ := v.(map[string]any)
		out = append(out, fmt.Sprint(m["type"], ":", m["name"]))
	}
	return out
}

// output0 describes the first output item of the client's response: type, name and namespace.
func output0(resp map[string]any) string {
	list, _ := resp["output"].([]any)
	if len(list) == 0 {
		return "no output"
	}
	m, _ := list[0].(map[string]any)
	return fmt.Sprint(m["type"], " ", m["name"], " ns=", m["namespace"])
}

func ntools(prefix string, from, to int) string {
	var s []string
	for i := from; i < to; i++ {
		s = append(s, fmt.Sprintf(`{"type":"function","name":"%s%d","parameters":{"type":"object"}}`, prefix, i))
	}
	return strings.Join(s, ",")
}

// The tools of each additional_tools item are sent in the tools list and the item is not sent; the
// messages around it stay. A custom tool in the item is sent as a function, a namespace in it is
// flattened, and xAI's call to it comes back as the call in its namespace. Expected values are
// today's core's (TestPromoteXAIAdditionalTools, TestXAIExecutorDeduplicatesToolsAcrossAdditionalTools,
// TestPromoteXAIAdditionalTools_DeduplicatesAndPreservesWinningNamespaceRef and
// TestXAIShouldFoldNamespaceTools_DeduplicatesAcrossAdditionalTools).
func TestAdditionalToolsPromoted(t *testing.T) {
	run := newLiteRun(t)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			with := func(body string) string {
				return strings.Replace(body, `{"model":"grok-4.7",`, fmt.Sprintf(`{"model":"grok-4.7","stream":%v,`, stream), 1)
			}

			t.Run("moved to the tools list", func(t *testing.T) {
				sent, got := run.send(t, with(`{"model":"grok-4.7",
					"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
					"input":[
						{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__exa","tools":[{"type":"function","name":"search","parameters":{"type":"object"}}]}]},
						{"role":"user","content":"hello"},
						{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"custom_lookup"}]}]}`),
					call{"mcp__exa__search", "{}"})
				if want := "function:lookup,function:mcp__exa__search,function:custom_lookup"; strings.Join(sentTools(sent), ",") != want {
					t.Errorf("tools = %v, want %s", sentTools(sent), want)
				}
				tools, _ := sent["tools"].([]any)
				if last, _ := tools[len(tools)-1].(map[string]any); last["parameters"] == nil {
					t.Errorf("the custom tool has no parameters: %v", last)
				}
				input, _ := sent["input"].([]any)
				if len(input) != 1 || fmt.Sprint(input[0].(map[string]any)["role"]) != "user" {
					t.Errorf("input = %v, want only the user message", input)
				}
				if want := "function_call search ns=mcp__exa"; output0(got) != want {
					t.Errorf("client got %s, want %s", output0(got), want)
				}
			})

			// A tool the top-level list has beats the same tool in an item, and the first of
			// two items beats the second.
			t.Run("first declaration wins", func(t *testing.T) {
				top := `{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","description":"top declaration","parameters":{"type":"object","properties":{"duration_ms":{"type":"string"}},"required":["duration_ms"]}}]}`
				history := `{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","description":"history declaration","parameters":{"type":"object","properties":{"seconds":{"type":"string"}},"required":["seconds"]}}]}`
				sent, got := run.send(t, with(fmt.Sprintf(`{"model":"grok-4.7","tools":[%s],"input":[{"type":"additional_tools","role":"developer","tools":[%s]},{"role":"user","content":"say hello"}]}`, top, history)),
					call{"clock__sleep", `{"duration_ms":"10"}`})
				tools, _ := sent["tools"].([]any)
				if len(tools) != 1 {
					t.Fatalf("tools = %v, want one clock__sleep", sentTools(sent))
				}
				tool := tools[0].(map[string]any)
				params, _ := json.Marshal(tool["parameters"])
				if tool["name"] != "clock__sleep" || tool["description"] != "top declaration" || !strings.Contains(string(params), "duration_ms") || strings.Contains(string(params), "seconds") {
					t.Errorf("tool = %v", tool)
				}
				if _, ok := sent["input"].([]any); !ok || strings.Contains(fmt.Sprint(sent["input"]), "additional_tools") {
					t.Errorf("input = %v", sent["input"])
				}
				if want := "function_call sleep ns=clock"; output0(got) != want {
					t.Errorf("client got %s, want %s", output0(got), want)
				}

				flat := `{"type":"function","name":"clock__sleep","description":"flat top","parameters":{"type":"object"}}`
				sent, _ = run.send(t, with(fmt.Sprintf(`{"model":"grok-4.7","tools":[%s],"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"hello"}]}`, flat, history)),
					call{"clock__sleep", "{}"})
				if tools, _ = sent["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["description"] != "flat top" {
					t.Errorf("tools = %v, want the flat top-level tool alone", tools)
				}
				sent, _ = run.send(t, with(`{"model":"grok-4.7","input":[
					{"type":"additional_tools","tools":[{"type":"function","name":"f","description":"first"}]},
					{"type":"additional_tools","tools":[{"type":"function","name":"f","description":"second"},{"type":"function","name":"g"}]}]}`),
					call{"f", "{}"})
				if tools, _ = sent["tools"].([]any); len(tools) != 2 || tools[0].(map[string]any)["description"] != "first" || tools[1].(map[string]any)["name"] != "g" {
					t.Errorf("tools = %v, want f (first) and g", tools)
				}
			})

			// 120 tools in a namespace declared at the top level and again in an item are 120, not
			// 240, so nothing is folded; with more tools than xAI takes, the namespace of an item
			// is folded as a top-level one is, one namespace declared twice is one function whose
			// children are those of both, each as its first declaration has it.
			t.Run("counted and folded as top-level tools", func(t *testing.T) {
				ns := `{"type":"namespace","name":"mcp__app","tools":[` + ntools("tool_", 0, 120) + `]}`
				sent, _ := run.send(t, with(fmt.Sprintf(`{"model":"grok-4.7","tools":[%s],"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"hi"}]}`, ns, ns)),
					call{"mcp__app__tool_3", "{}"})
				if tools, _ := sent["tools"].([]any); len(tools) != 120 || sentTools(sent)[0] != "function:mcp__app__tool_0" {
					t.Errorf("%d tools, the first %s; want 120 flattened tools", len(tools), sentTools(sent)[0])
				}

				topNS := `{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","description":"top sleep","parameters":{"type":"object"}}]}`
				histNS := `{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep","description":"history sleep","parameters":{"type":"object"}},{"type":"function","name":"alarm","description":"history alarm","parameters":{"type":"object"}}]},
					{"type":"namespace","name":"mcp__extra","tools":[` + ntools("extra_", 120, 210) + `]}`
				sent, got := run.send(t, with(fmt.Sprintf(`{"model":"grok-4.7","tools":[%s,%s],"input":[{"type":"additional_tools","tools":[%s]},{"role":"user","content":"hi"}]}`, topNS, ns, histNS)),
					call{"clock", `{"name":"alarm","arguments":{}}`})
				if got := sentTools(sent); len(got) != 3 || strings.Join(got, ",") != "function:clock,function:mcp__app,function:mcp__extra" {
					t.Fatalf("%d tools, the first %s; want 3: the functions clock, mcp__app and mcp__extra", len(got), got[0])
				}
				desc, _ := sent["tools"].([]any)[0].(map[string]any)["description"].(string)
				if !strings.Contains(desc, "top sleep") || strings.Contains(desc, "history sleep") || !strings.Contains(desc, "history alarm") {
					t.Errorf("clock description %q: want top sleep and history alarm, not history sleep", desc)
				}
				if want := "function_call alarm ns=clock"; output0(got) != want {
					t.Errorf("client got %s, want %s", output0(got), want)
				}
			})

			t.Run("tool choice names a tool of an item", func(t *testing.T) {
				items := `"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"clock","tools":[{"type":"function","name":"sleep"}]},{"type":"function","name":"lookup"}]},{"role":"user","content":"hi"}]`
				for choice, want := range map[string]string{
					`{"type":"function","namespace":"clock","name":"sleep"}`: `{"type":"function","name":"clock__sleep"}`,
					`{"type":"function","name":"lookup"}`:                    `{"type":"function","name":"lookup"}`,
					`"required"`:                                             `"required"`,
				} {
					sent, _ := run.send(t, with(`{"model":"grok-4.7","tool_choice":`+choice+`,`+items+`}`), call{"lookup", "{}"})
					got, _ := json.Marshal(sent["tool_choice"])
					sameJSON(t, "tool_choice "+choice, got, want)
					if want := "function:clock__sleep,function:lookup"; strings.Join(sentTools(sent), ",") != want {
						t.Errorf("tools = %v, want %s", sentTools(sent), want)
					}
				}
			})
		})
	}
}
