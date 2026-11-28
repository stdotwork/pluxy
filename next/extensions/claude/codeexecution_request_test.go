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

func TestCodeExecutionToClaude(t *testing.T) {
	cases := []struct {
		name  string
		parse func(t *testing.T) llm.Request
	}{
		{
			name: "responses_auto_container",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"claude-sonnet-4-6",
					"input":"compute the 20th fibonacci number",
					"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
		{
			name: "responses_empty_lists",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"claude-sonnet-4-6",
					"input":"compute 2+2",
					"tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":[]},"allowed_callers":[]}]
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
				req, err := geminigenerate.ParseRequest("claude-sonnet-4-6", false, []byte(`{
					"contents":[{"role":"user","parts":[{"text":"compute 2+2"}]}],
					"tools":[{"codeExecution":{}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
		{
			name: "gemini_snake_case",
			parse: func(t *testing.T) llm.Request {
				req, err := geminigenerate.ParseRequest("claude-sonnet-4-6", false, []byte(`{
					"contents":[{"role":"user","parts":[{"text":"compute 2+2"}]}],
					"tools":[{"code_execution":{}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := roundTripRequest(t, tc.parse(t))
			body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
			if err != nil {
				t.Fatalf("official host: %v", err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.Tools) != 1 || got.Tools[0]["type"] != "code_execution_20250825" || got.Tools[0]["name"] != "code_execution" {
				t.Fatalf("official host: tools = %v, want code_execution_20250825 (body: %s)", got.Tools, body)
			}
			if tr.Dropped() {
				t.Errorf("official host: unexpected dropped losses: %+v", tr.Losses)
			}

			body3p, tr3p, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, "https://proxy.example.com"))
			if err != nil {
				t.Fatalf("custom host: %v", err)
			}
			var got3p struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(body3p, &got3p); err != nil {
				t.Fatalf("unmarshal custom: %v", err)
			}
			if len(got3p.Tools) != 0 {
				t.Errorf("custom host: expected no tools, got %v", got3p.Tools)
			}
			if !tr3p.Dropped() {
				t.Errorf("custom host: expected dropped loss, got %+v", tr3p.Losses)
			}
		})
	}
}

func TestCodeExecutionToClaudeUnsupportedSettings(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantPaths []string
	}{
		{
			name:      "container_id",
			raw:       `{"model":"claude-sonnet-4-6","input":"run","tools":[{"type":"code_interpreter","container":"cntr_abc123"}]}`,
			wantPaths: []string{"tools[0].container"},
		},
		{
			name:      "container_file_ids_memory_network_and_callers",
			raw:       `{"model":"claude-sonnet-4-6","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":["file-1"],"memory_limit":"4g","network_policy":{"type":"disabled"}},"allowed_callers":["programmatic"]}]}`,
			wantPaths: []string{"tools[0].allowed_callers", "tools[0].container.file_ids", "tools[0].container.memory_limit", "tools[0].container.network_policy"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			req = roundTripRequest(t, req)
			body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Tools) != 1 || got.Tools[0]["type"] != "code_execution_20250825" {
				t.Fatalf("tools = %v, want code_execution_20250825", got.Tools)
			}
			dropped := tr.DroppedPaths()
			for _, want := range tc.wantPaths {
				if !slices.Contains(dropped, want) {
					t.Errorf("dropped = %v, want %q in losses %+v", dropped, want, tr.Losses)
				}
			}
			reqReject := req
			reqReject.DropPolicy = llm.DropReject
			if _, _, err := buildBodyWith(reqReject, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, reqReject, defaultBaseURL)); err == nil {
				t.Errorf("drop_policy=reject: expected error for unsupported settings, got nil")
			}
		})
	}
}

func TestCodeExecutionToClaudeModelGating(t *testing.T) {
	supported := []string{
		"claude-sonnet-4-5-20250929",
		"claude-opus-4-5-20251101",
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-6",
		"claude-opus-4-6",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-opus-5-5",
		"claude-sonnet-5-5",
		"claude-haiku-5-5",
		"claude-fable-5-2",
		"claude-mythos-5-2",
		"claude-mythos-preview",
	}
	for _, model := range supported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripRequest(t, req)
		body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["type"] != "code_execution_20250825" {
			t.Errorf("model %s: tools = %v, want code_execution_20250825", model, got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("model %s: unexpected dropped losses: %+v", model, tr.Losses)
		}
	}

	unsupported := []string{
		"claude-sonnet-4-20250514",
		"claude-opus-4-1-20250805",
		"claude-3-7-sonnet-20250219",
		"claude-3-5-sonnet-20241022",
		"gpt-5.4",
	}
	for _, model := range unsupported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripRequest(t, req)
		body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 0 {
			t.Errorf("model %s: expected no tools, got %v", model, got.Tools)
		}
		if !tr.Dropped() {
			t.Errorf("model %s: expected dropped loss, got %+v", model, tr.Losses)
		}
	}
}

func TestCodeExecutionToClaudeWithFunctionsAndWebSearch(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"search and compute",
		"tools":[
			{"type":"function","name":"get_weather","description":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}},
			{"type":"web_search"},
			{"type":"code_interpreter","container":{"type":"auto"}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)
	body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 3 {
		t.Fatalf("tools = %v, want 3 tools (body: %s)", got.Tools, body)
	}
	var hasFn, hasWS, hasCE bool
	for _, tool := range got.Tools {
		switch {
		case tool["name"] == "get_weather":
			hasFn = true
		case tool["type"] == "web_search_20250305":
			hasWS = true
		case tool["type"] == "code_execution_20250825" && tool["name"] == "code_execution":
			hasCE = true
		}
	}
	if !hasFn || !hasWS || !hasCE {
		t.Errorf("missing expected tool(s): fn=%v ws=%v ce=%v (tools: %v)", hasFn, hasWS, hasCE, got.Tools)
	}
	if tr.Dropped() {
		t.Errorf("unexpected dropped losses: %+v", tr.Losses)
	}
}

func TestCodeExecutionToClaudeToolChoice(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"compute",
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
		"tool_choice":{"type":"code_interpreter"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)
	body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	_ = json.Unmarshal(body, &got)
	if got.ToolChoice["type"] != "tool" || got.ToolChoice["name"] != "code_execution" {
		t.Errorf("tool_choice = %v, want {type:tool, name:code_execution} (body: %s)", got.ToolChoice, body)
	}
	if tr.Dropped() {
		t.Errorf("unexpected dropped losses: %+v", tr.Losses)
	}

	reqNoForce := req
	reqNoForce.Model = "claude-opus-5-5"
	bodyNF, trNF, err := buildBodyWith(reqNoForce, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, reqNoForce, defaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	var gotNF struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	_ = json.Unmarshal(bodyNF, &gotNF)
	if gotNF.ToolChoice["type"] != "auto" {
		t.Errorf("claude-opus-5-5 tool_choice = %v, want {type:auto} (body: %s)", gotNF.ToolChoice, bodyNF)
	}
	if trNF.Dropped() {
		t.Errorf("claude-opus-5-5: unexpected dropped losses: %+v", trNF.Losses)
	}

	bodyDrop, trDrop, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var gotDrop struct {
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	_ = json.Unmarshal(bodyDrop, &gotDrop)
	if len(gotDrop.Tools) != 0 {
		t.Errorf("dropped host: tools = %v, want empty", gotDrop.Tools)
	}
	if gotDrop.ToolChoice != nil {
		t.Errorf("dropped host: unexpected tool_choice = %v", gotDrop.ToolChoice)
	}
	if !slices.Contains(trDrop.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped host: dropped = %v, want tool_choice in %+v", trDrop.DroppedPaths(), trDrop.Losses)
	}

	reqWithFn, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"compute",
		"tools":[
			{"type":"function","name":"get_weather","parameters":{"type":"object"}},
			{"type":"code_interpreter","container":{"type":"auto"}}
		],
		"tool_choice":{"type":"code_interpreter"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reqWithFn = roundTripRequest(t, reqWithFn)
	bodyFn, trFn, err := buildBodyWith(reqWithFn, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, reqWithFn, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var gotFn struct {
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	_ = json.Unmarshal(bodyFn, &gotFn)
	if len(gotFn.Tools) != 1 || gotFn.Tools[0]["name"] != "get_weather" {
		t.Errorf("dropped beside fn: tools = %v, want only get_weather", gotFn.Tools)
	}
	if gotFn.ToolChoice != nil {
		t.Errorf("dropped beside fn: unexpected tool_choice = %v", gotFn.ToolChoice)
	}
	if !slices.Contains(trFn.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped beside fn: dropped = %v, want tool_choice", trFn.DroppedPaths())
	}

	reqReq, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"compute",
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
		"tool_choice":"required"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reqReq = roundTripRequest(t, reqReq)
	bodyReq, trReq, err := buildBodyWith(reqReq, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, reqReq, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var gotReq struct {
		ToolChoice map[string]any `json:"tool_choice"`
	}
	_ = json.Unmarshal(bodyReq, &gotReq)
	if gotReq.ToolChoice != nil {
		t.Errorf("required dropped: unexpected tool_choice = %v", gotReq.ToolChoice)
	}
	if !slices.Contains(trReq.DroppedPaths(), "tool_choice") {
		t.Errorf("required dropped: dropped = %v, want tool_choice", trReq.DroppedPaths())
	}
}

func TestCodeExecutionClaudeSameFormatByteIdentical(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"compute"}],"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"tool_choice":{"type":"tool","name":"code_execution"}}`)
	req, err := anthropicmessages.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)
	body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough {
		t.Errorf("expected passthrough")
	}
	if !bytes.Equal(body, raw) {
		t.Errorf("native body changed:\ngot:  %s\nwant: %s", body, raw)
	}
}
