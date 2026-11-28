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

func TestCodeExecutionToGemini(t *testing.T) {
	cases := []struct {
		name        string
		parse       func(t *testing.T) llm.Request
		wantDropped []string
	}{
		{
			name: "responses_auto_container",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"gemini-4-hydrogen",
					"input":"compute sum of first 50 primes",
					"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
		},
		{
			name: "responses_with_unsupported_settings",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"gemini-4-hydrogen",
					"input":"compute",
					"tools":[{"type":"code_interpreter","container":{"type":"auto","memory_limit":"4g","file_ids":["file-1"]}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: []string{"tools[0].container.file_ids", "tools[0].container.memory_limit"},
		},
		{
			name: "messages_20250825",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gemini-4-hydrogen",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"compute"}],
					"tools":[{"type":"code_execution_20250825","name":"code_execution"}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: []string{"max_tokens"},
		},
		{
			name: "messages_with_unsupported_settings",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gemini-4-hydrogen",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"compute"}],
					"tools":[{"type":"code_execution_20260120","name":"code_execution","defer_loading":true,"allowed_callers":["direct"]}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: []string{"max_tokens", "tools[0].allowed_callers", "tools[0].defer_loading"},
		},
	}

	officialSettings := []struct {
		name string
		s    settings
	}{
		{"gemini_api", settings{BaseURL: defaultBaseURL}},
		{"vertex_default", settings{Vertex: &vertexSettings{Project: "p", Location: "us-central1"}}},
		{"vertex_regional", settings{BaseURL: "https://us-central1-aiplatform.googleapis.com", Vertex: &vertexSettings{Project: "p", Location: "us-central1"}}},
		{"vertex_global", settings{BaseURL: "https://aiplatform.googleapis.com", Vertex: &vertexSettings{Project: "p", Location: "global"}}},
	}

	for _, tc := range cases {
		for _, host := range officialSettings {
			t.Run(tc.name+"/"+host.name, func(t *testing.T) {
				req := roundTripReq(t, tc.parse(t))
				body, tr, err := buildBodyOn(req, host.s)
				if err != nil {
					t.Fatalf("%s: %v", host.name, err)
				}
				var got struct {
					Tools []map[string]json.RawMessage `json:"tools"`
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Tools) != 1 || got.Tools[0]["codeExecution"] == nil {
					t.Fatalf("%s: tools = %v, want codeExecution (body: %s)", host.name, got.Tools, body)
				}
				if len(tc.wantDropped) == 0 {
					if tr.Dropped() {
						t.Errorf("%s: unexpected dropped losses: %+v", host.name, tr.Losses)
					}
				} else {
					dropped := tr.DroppedPaths()
					for _, want := range tc.wantDropped {
						if !slices.Contains(dropped, want) {
							t.Errorf("%s: dropped = %v, want %q in %+v", host.name, dropped, want, tr.Losses)
						}
					}
				}
			})
		}

		t.Run(tc.name+"/third_party", func(t *testing.T) {
			req := roundTripReq(t, tc.parse(t))
			for _, s := range []settings{
				{BaseURL: "https://proxy.example.com/v1beta"},
				{BaseURL: "https://proxy.example.com", Vertex: &vertexSettings{Project: "p", Location: "us-central1"}},
			} {
				body, tr, err := buildBodyOn(req, s)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Tools []map[string]json.RawMessage `json:"tools"`
				}
				_ = json.Unmarshal(body, &got)
				if len(got.Tools) != 0 {
					t.Errorf("third-party: expected no tools, got %v", got.Tools)
				}
				if !slices.Contains(tr.DroppedPaths(), "tools[0]") {
					t.Errorf("third-party: dropped = %v, want tools[0] in %+v", tr.DroppedPaths(), tr.Losses)
				}
			}
		})
	}
}

func TestCodeExecutionToGeminiModelGating(t *testing.T) {
	supported := []string{
		"gemini-4-carbon",
		"gemini-4-argon",
		"gemini-4-neon",
		"gemini-4-hydrogen",
		"gemini-3.8-flash",
		"gemini-3.6-flash",
		"gemini-3.5-flash-lite",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-lite",
		"gemini-3-pro-preview",
		"gemini-3-flash-preview",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-2.0-flash",
		"gemini-robotics-er-1.6-preview",
	}
	for _, model := range supported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["codeExecution"] == nil {
			t.Errorf("model %s: tools = %v, want codeExecution", model, got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("model %s: unexpected dropped losses: %+v", model, tr.Losses)
		}
	}

	unsupported := []string{
		"gemini-2.0-flash-lite",
		"gemini-2.5-flash-image",
		"gemini-3-pro-image",
		"gemini-3.1-flash-image",
		"gemini-nano-banana-2.1",
		"gemini-3.8-live",
		"gemini-2.5-flash-native-audio-preview-12-2025",
		"gemini-3.8-flash-tts",
		"gemini-2.5-flash-preview-tts",
		"gemini-embedding-001",
		"gemini-1.5-pro",
		"gemma-3-27b-it",
	}
	for _, model := range unsupported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
		if err != nil {
			t.Fatalf("model %s: %v", model, err)
		}
		var got struct {
			Tools []map[string]json.RawMessage `json:"tools"`
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

func TestCodeExecutionToGeminiCombinations(t *testing.T) {
	reqWS, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-4-hydrogen",
		"input":"search and compute",
		"tools":[
			{"type":"web_search"},
			{"type":"code_interpreter","container":{"type":"auto"}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reqWS = roundTripReq(t, reqWS)
	bodyWS, trWS, err := buildBodyOn(reqWS, settings{BaseURL: defaultBaseURL})
	if err != nil {
		t.Fatal(err)
	}
	var gotWS struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(bodyWS, &gotWS)
	var hasSearch, hasCode bool
	for _, tool := range gotWS.Tools {
		if tool["googleSearch"] != nil {
			hasSearch = true
		}
		if tool["codeExecution"] != nil {
			hasCode = true
		}
	}
	if !hasSearch || !hasCode {
		t.Errorf("web_search + code_execution: hasSearch=%v hasCode=%v (body: %s)", hasSearch, hasCode, bodyWS)
	}
	if trWS.Dropped() {
		t.Errorf("web_search + code_execution: unexpected dropped losses: %+v", trWS.Losses)
	}

	for _, model := range []string{"gemini-2.5-flash", "gemini-4-hydrogen"} {
		reqFn, err := openairesponses.ParseRequest([]byte(`{
			"model":"` + model + `",
			"input":"compute or call weather",
			"tools":[
				{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}},
				{"type":"code_interpreter","container":{"type":"auto"}}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		reqFn = roundTripReq(t, reqFn)
		bodyFn, trFn, err := buildBodyOn(reqFn, settings{BaseURL: defaultBaseURL})
		if err != nil {
			t.Fatal(err)
		}
		var gotFn struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(bodyFn, &gotFn)
		if len(gotFn.Tools) != 1 || gotFn.Tools[0]["functionDeclarations"] == nil || gotFn.Tools[0]["codeExecution"] != nil {
			t.Errorf("model %s with fn: tools = %v, want only functionDeclarations (body: %s)", model, gotFn.Tools, bodyFn)
		}
		if !slices.Contains(trFn.DroppedPaths(), "tools[1]") {
			t.Errorf("model %s with fn: dropped = %v, want tools[1] in %+v", model, trFn.DroppedPaths(), trFn.Losses)
		}
	}
}

func TestCodeExecutionToGeminiToolChoice(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-4-hydrogen",
		"input":"compute",
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
		"tool_choice":{"type":"code_interpreter"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []map[string]json.RawMessage `json:"tools"`
		ToolConfig json.RawMessage              `json:"toolConfig"`
	}
	_ = json.Unmarshal(body, &got)
	if len(got.Tools) != 1 || got.Tools[0]["codeExecution"] == nil {
		t.Fatalf("tools = %v, want codeExecution", got.Tools)
	}
	if len(got.ToolConfig) != 0 {
		t.Errorf("unexpected toolConfig = %s, want omitted", got.ToolConfig)
	}
	if !slices.Contains(tr.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped = %v, want tool_choice in %+v", tr.DroppedPaths(), tr.Losses)
	}

	bodyDrop, trDrop, err := buildBodyOn(req, settings{BaseURL: "https://proxy.example.com/v1beta"})
	if err != nil {
		t.Fatal(err)
	}
	var gotDrop struct {
		ToolConfig json.RawMessage `json:"toolConfig"`
	}
	_ = json.Unmarshal(bodyDrop, &gotDrop)
	if len(gotDrop.ToolConfig) != 0 {
		t.Errorf("dropped host: unexpected toolConfig = %s", gotDrop.ToolConfig)
	}
	if !slices.Contains(trDrop.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped host: dropped = %v, want tool_choice", trDrop.DroppedPaths())
	}

	reqReq, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-4-hydrogen",
		"input":"compute",
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
		"tool_choice":"required"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	reqReq = roundTripReq(t, reqReq)
	bodyReq, trReq, err := buildBodyOn(reqReq, settings{BaseURL: defaultBaseURL})
	if err != nil {
		t.Fatal(err)
	}
	var gotReq struct {
		ToolConfig json.RawMessage `json:"toolConfig"`
	}
	_ = json.Unmarshal(bodyReq, &gotReq)
	if len(gotReq.ToolConfig) != 0 {
		t.Errorf("required: unexpected toolConfig = %s", gotReq.ToolConfig)
	}
	if !slices.Contains(trReq.DroppedPaths(), "tool_choice") {
		t.Errorf("required: dropped = %v, want tool_choice", trReq.DroppedPaths())
	}
}

func TestCodeExecutionGeminiSameFormatByteIdentical(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"compute"}]}],"tools":[{"codeExecution":{}}]}`)
	req, err := geminigenerate.ParseRequest("gemini-4-hydrogen", false, raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, tr, err := buildBodyOn(req, settings{BaseURL: "https://proxy.example.com/v1beta"})
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
