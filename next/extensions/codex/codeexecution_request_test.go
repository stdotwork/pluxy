package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func findCodeInterpreter(tools []map[string]any) map[string]any {
	for _, t := range tools {
		if t["type"] == "code_interpreter" {
			return t
		}
	}
	return nil
}

func TestCodeExecutionToCodex(t *testing.T) {
	cases := []struct {
		name        string
		parse       func(t *testing.T) llm.Request
		wantDropped []string
	}{
		{
			name: "messages_20250825",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gpt-5.4",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"compute 2+2"}],
					"tools":[{"type":"code_execution_20250825","name":"code_execution"}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
		{
			name: "messages_20260120_with_unsupported_settings",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gpt-5.4",
					"max_tokens":1024,
					"container":"cntr_claude_1",
					"messages":[{"role":"user","content":"compute 2+2"}],
					"tools":[{"type":"code_execution_20260120","name":"code_execution","defer_loading":true,"strict":true,"allowed_callers":["direct"],"cache_control":{"type":"ephemeral"}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: []string{"container", "tools[0].allowed_callers", "tools[0].cache_control", "tools[0].defer_loading", "tools[0].strict"},
		},
		{
			name: "messages_20260521_empty_allowed_callers",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gpt-5.4",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"compute 2+2"}],
					"tools":[{"type":"code_execution_20260521","name":"code_execution","allowed_callers":[]}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
		{
			name: "gemini",
			parse: func(t *testing.T) llm.Request {
				req, err := geminigenerate.ParseRequest("gpt-5.4", false, []byte(`{
					"contents":[{"role":"user","parts":[{"text":"compute 2+2"}]}],
					"tools":[{"codeExecution":{}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
	}

	hosts := []struct {
		name string
		s    settings
		a    account
	}{
		{"api_openai", settings{BaseURL: "https://api.openai.com/v1"}, account{}},
	}

	for _, tc := range cases {
		for _, host := range hosts {
			t.Run(tc.name+"/"+host.name, func(t *testing.T) {
				req := roundTripReq(t, tc.parse(t))
				u, err := buildRequest(req, "acc1", host.s, host.a, false)
				if err != nil {
					t.Fatalf("%s: %v", host.name, err)
				}
				var got struct {
					Tools []map[string]any `json:"tools"`
				}
				if err := json.Unmarshal(u.body, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				ci := findCodeInterpreter(got.Tools)
				if ci == nil {
					t.Fatalf("%s: missing code_interpreter in tools %v (body: %s)", host.name, got.Tools, u.body)
				}
				ctr, _ := ci["container"].(map[string]any)
				if ctr == nil || ctr["type"] != "auto" {
					t.Errorf("%s: container = %v, want {type:auto}", host.name, ci["container"])
				}
				if len(tc.wantDropped) == 0 {
					if u.tr.Dropped() {
						t.Errorf("%s: unexpected dropped losses: %+v", host.name, u.tr.Losses)
					}
				} else {
					dropped := u.tr.DroppedPaths()
					for _, want := range tc.wantDropped {
						if !slices.Contains(dropped, want) {
							t.Errorf("%s: dropped = %v, want %q in %+v", host.name, dropped, want, u.tr.Losses)
						}
					}
					reqReject := req
					reqReject.DropPolicy = llm.DropReject
					if _, err := buildRequest(reqReject, "acc1", host.s, host.a, false); err == nil {
						t.Errorf("%s: drop_policy=reject: expected error, got nil", host.name)
					}
				}
			})
		}

		t.Run(tc.name+"/third_party", func(t *testing.T) {
			req := roundTripReq(t, tc.parse(t))
			u, err := buildRequest(req, "acc1", settings{BaseURL: "https://proxy.example.com/v1"}, account{}, false)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			_ = json.Unmarshal(u.body, &got)
			if ci := findCodeInterpreter(got.Tools); ci != nil {
				t.Errorf("third-party: unexpected code_interpreter tool %v", ci)
			}
			if !u.tr.Dropped() {
				t.Errorf("third-party: expected dropped loss, got %+v", u.tr.Losses)
			}
		})
	}
}

func TestCodeExecutionToCodexModelGating(t *testing.T) {
	supported := []string{
		"gpt-6.2-astra",
		"gpt-6.2-sol",
		"gpt-6.1-sol",
		"gpt-6.1-terra",
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-5.6-sol",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4",
		"gpt-5.4-2026-03-05",
		"gpt-5.4-mini",
		"gpt-5.2",
		"gpt-5",
		"gpt-4.1",
		"gpt-4o",
		"o3",
		"o4-mini",
	}
	for _, model := range supported {
		req, err := anthropicmessages.ParseRequest([]byte(`{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"run"}],"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		u, err := buildRequest(req, "acc1", settings{BaseURL: "https://api.openai.com/v1"}, account{}, false)
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(u.body, &got)
		if findCodeInterpreter(got.Tools) == nil {
			t.Errorf("model %s: missing code_interpreter in %v", model, got.Tools)
		}
		if u.tr.Dropped() {
			t.Errorf("model %s: unexpected dropped losses: %+v", model, u.tr.Losses)
		}
	}

	unsupported := []string{
		"gpt-5.4-pro",
		"gpt-5.2-pro",
		"gpt-5.3-codex",
		"gpt-5.2-codex",
		"gpt-5-pro",
		"o1",
		"o1-pro",
		"o3-pro",
		"gpt-4",
		"gpt-4o-search-preview",
		"gpt-image-1",
		"codex-auto-review",
	}
	for _, model := range unsupported {
		req, err := anthropicmessages.ParseRequest([]byte(`{"model":"` + model + `","max_tokens":1024,"messages":[{"role":"user","content":"run"}],"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		u, err := buildRequest(req, "acc1", settings{BaseURL: "https://api.openai.com/v1"}, account{}, false)
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(u.body, &got)
		if ci := findCodeInterpreter(got.Tools); ci != nil {
			t.Errorf("model %s: unexpected code_interpreter %v", model, ci)
		}
		if !u.tr.Dropped() {
			t.Errorf("model %s: expected dropped loss, got %+v", model, u.tr.Losses)
		}
	}
}

func TestCodeExecutionToCodexWithFunctionsAndWebSearch(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"search and compute"}],
		"tools":[
			{"name":"get_weather","description":"weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}},
			{"type":"web_search_20250305","name":"web_search"},
			{"type":"code_execution_20250825","name":"code_execution"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	u, err := buildRequest(req, "acc1", settings{BaseURL: "https://api.openai.com/v1"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(u.body, &got)
	var hasFn, hasWS, hasCI bool
	for _, tool := range got.Tools {
		switch tool["type"] {
		case "function":
			hasFn = true
		case "web_search":
			hasWS = true
		case "code_interpreter":
			hasCI = true
		}
	}
	if !hasFn || !hasWS || !hasCI {
		t.Errorf("missing tool(s): fn=%v ws=%v ci=%v (tools: %v)", hasFn, hasWS, hasCI, got.Tools)
	}
	if u.tr.Dropped() {
		t.Errorf("unexpected dropped losses: %+v", u.tr.Losses)
	}
}

func TestCodeExecutionToCodexToolChoice(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"compute"}],
		"tools":[{"type":"code_execution_20250825","name":"code_execution"}],
		"tool_choice":{"type":"tool","name":"code_execution"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	u, err := buildRequest(req, "acc1", settings{BaseURL: "https://api.openai.com/v1"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	_ = json.Unmarshal(u.body, &got)
	if got.ToolChoice["type"] != "code_interpreter" {
		t.Errorf("tool_choice = %v, want {type:code_interpreter} (body: %s)", got.ToolChoice, u.body)
	}
	if u.tr.Dropped() {
		t.Errorf("unexpected dropped losses: %+v", u.tr.Losses)
	}

	uDrop, err := buildRequest(req, "acc1", settings{BaseURL: "https://proxy.example.com/v1"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var gotDrop struct {
		Tools      []map[string]any `json:"tools"`
		ToolChoice any              `json:"tool_choice"`
	}
	_ = json.Unmarshal(uDrop.body, &gotDrop)
	if ci := findCodeInterpreter(gotDrop.Tools); ci != nil {
		t.Errorf("dropped: unexpected code_interpreter %v", ci)
	}
	if gotDrop.ToolChoice != nil {
		t.Errorf("dropped: unexpected tool_choice = %v", gotDrop.ToolChoice)
	}
	if !slices.Contains(uDrop.tr.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped: dropped = %v, want tool_choice in %+v", uDrop.tr.DroppedPaths(), uDrop.tr.Losses)
	}

	reqAny, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4-pro",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"compute"}],
		"tools":[{"type":"code_execution_20250825","name":"code_execution"}],
		"tool_choice":{"type":"any"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reqAny = roundTripReq(t, reqAny)
	uAny, err := buildRequest(reqAny, "acc1", settings{BaseURL: "https://api.openai.com/v1"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var gotAny struct {
		ToolChoice any `json:"tool_choice"`
	}
	_ = json.Unmarshal(uAny.body, &gotAny)
	if gotAny.ToolChoice != nil {
		t.Errorf("required dropped: unexpected tool_choice = %v", gotAny.ToolChoice)
	}
	if !slices.Contains(uAny.tr.DroppedPaths(), "tool_choice") {
		t.Errorf("required dropped: dropped = %v, want tool_choice", uAny.tr.DroppedPaths())
	}
}

func TestCodeExecutionCodexSameFormatPreserved(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto","memory_limit":"4g","file_ids":["file-1"]}}],"tool_choice":{"type":"code_interpreter"}}`)
	req, err := openairesponses.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	u, err := buildRequest(req, "acc1", settings{BaseURL: "https://proxy.example.com/v1"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !u.tr.Passthrough {
		t.Errorf("expected passthrough")
	}
	var got struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice json.RawMessage   `json:"tool_choice"`
	}
	if err := json.Unmarshal(u.body, &got); err != nil {
		t.Fatal(err)
	}
	wantTool := `{"type":"code_interpreter","container":{"type":"auto","memory_limit":"4g","file_ids":["file-1"]}}`
	found := false
	for _, tool := range got.Tools {
		if bytes.Equal(tool, []byte(wantTool)) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("native tool changed:\ngot:  %s\nwant: %s", got.Tools, wantTool)
	}
	wantChoice := `{"type":"code_interpreter"}`
	if !bytes.Equal(got.ToolChoice, []byte(wantChoice)) {
		t.Errorf("native tool_choice changed:\ngot:  %s\nwant: %s", got.ToolChoice, wantChoice)
	}
}

func TestCodeExecutionNotSentToChatGPT(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{"model":"gpt-5.5","max_tokens":9,
		"messages":[{"role":"user","content":"compute"}],
		"tools":[{"type":"code_execution_20250825","name":"code_execution"}],
		"tool_choice":{"type":"tool","name":"code_execution"}}`))
	if err != nil {
		t.Fatal(err)
	}
	u, err := buildRequest(roundTripReq(t, req), "acc1", settings{BaseURL: "https://chatgpt.com/backend-api/codex"}, account{}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []map[string]any `json:"tools"`
		ToolChoice any              `json:"tool_choice"`
	}
	if err := json.Unmarshal(u.body, &got); err != nil {
		t.Fatal(err)
	}
	if findCodeInterpreter(got.Tools) != nil {
		t.Errorf("code_interpreter sent to the ChatGPT backend: %s", u.body)
	}
	if got.ToolChoice != nil {
		t.Errorf("tool_choice = %v, want none", got.ToolChoice)
	}
	if dropped := u.tr.DroppedPaths(); !slices.Contains(dropped, "tools[0]") || !slices.Contains(dropped, "tool_choice") {
		t.Errorf("dropped = %v, want tools[0] and tool_choice", dropped)
	}
}

func TestCodeExecutionOutputsIncludeCodex(t *testing.T) {
	include := func(t *testing.T, req llm.Request, base string) ([]string, bool) {
		t.Helper()
		u, err := buildRequest(roundTripReq(t, req), "acc1", settings{BaseURL: base}, account{}, false)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools   []map[string]any `json:"tools"`
			Include []string         `json:"include"`
		}
		if err := json.Unmarshal(u.body, &got); err != nil {
			t.Fatal(err)
		}
		return got.Include, findCodeInterpreter(got.Tools) != nil
	}
	messages, err := anthropicmessages.ParseRequest([]byte(`{"model":"gpt-5.4","max_tokens":9,
		"messages":[{"role":"user","content":"compute"}],
		"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	const outputs = "code_interpreter_call.outputs"
	for base, wantTool := range map[string]bool{
		"https://api.openai.com/v1":             true,
		"https://chatgpt.com/backend-api/codex": false,
		"https://proxy.example.com/v1":          false,
	} {
		inc, tool := include(t, messages, base)
		if tool != wantTool {
			t.Fatalf("%s: code tool sent = %v, want %v", base, tool, wantTool)
		}
		if got := slices.Contains(inc, outputs); got != wantTool {
			t.Errorf("%s: include = %v, outputs asked for = %v, want %v", base, inc, got, wantTool)
		}
		if !slices.Contains(inc, "reasoning.encrypted_content") {
			t.Errorf("%s: include = %v lost reasoning.encrypted_content", base, inc)
		}
	}

	native, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-5.4","input":"run","include":["` + outputs + `"],"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if inc, _ := include(t, native, "https://api.openai.com/v1"); slices.Contains(inc, outputs) {
		t.Errorf("same-format request: include = %v, want it filtered as before", inc)
	}
}
