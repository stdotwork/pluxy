package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func roundTripReq(t *testing.T, req llm.Request) llm.Request {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var out llm.Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return out
}

func TestWebSearchToGemini(t *testing.T) {
	cases := []struct {
		name        string
		parse       func(t *testing.T) llm.Request
		wantDropped bool
	}{
		{
			name: "responses_plain",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"gemini-2.5-flash",
					"input":"latest news",
					"tools":[{"type":"web_search"}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: false,
		},
		{
			name: "messages_with_unsupported_settings",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gemini-2.5-flash",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"latest news"}],
					"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"allowed_domains":["example.com"]}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: true, // max_uses, allowed_domains (plus max_tokens from Messages)
		},
		{
			name: "chat",
			parse: func(t *testing.T) llm.Request {
				req, err := openaichat.ParseRequest([]byte(`{
					"model":"gemini-2.5-flash",
					"messages":[{"role":"user","content":"latest news"}],
					"web_search_options":{}
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := roundTripReq(t, tc.parse(t))
			// Official Google host gets googleSearch.
			body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
			if err != nil {
				t.Fatalf("official host: %v", err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(got.Tools) != 1 || got.Tools[0]["googleSearch"] == nil {
				t.Fatalf("official host: tools = %v, want googleSearch (body: %s)", got.Tools, body)
			}
			if tr.Dropped() != tc.wantDropped {
				t.Errorf("official host: dropped = %v, want %v (losses: %+v)", tr.Dropped(), tc.wantDropped, tr.Losses)
			}

			// Third-party base_url gets no tool and a loss.
			customBody, customTr, err := buildBodyOn(req, settings{BaseURL: "https://proxy.example.com"})
			if err != nil {
				t.Fatalf("custom host: %v", err)
			}
			var customGot struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(customBody, &customGot); err != nil {
				t.Fatalf("unmarshal custom: %v", err)
			}
			if len(customGot.Tools) != 0 {
				t.Errorf("custom host: len(tools) = %d, want 0 (body: %s)", len(customGot.Tools), customBody)
			}
			if !customTr.Dropped() {
				t.Errorf("custom host: expected dropped loss, got %+v", customTr.Losses)
			}
		})
	}
}

func TestWebSearchGeminiWithFunctionToolsDropsSearch(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-2.5-flash",
		"input":"weather and news",
		"tools":[
			{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{}}},
			{"type":"web_search"}
		]
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
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0]["functionDeclarations"] == nil || got.Tools[0]["googleSearch"] != nil {
		t.Fatalf("want only functionDeclarations, got %s", body)
	}
	if !tr.Dropped() {
		t.Errorf("expected dropped loss for search tool beside functionDeclarations, got %+v", tr.Losses)
	}
}

func TestWebSearchGeminiSameFormatByteIdentical(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"googleSearch":{}}]}`)
	req, err := geminigenerate.ParseRequest("gemini-2.5-flash", false, raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough {
		t.Fatalf("expected passthrough")
	}
	if !bytes.Equal(body, raw) {
		t.Errorf("same-format body changed:\ngot:  %s\nwant: %s", body, raw)
	}
}

func TestWebSearchGeminiModelGate(t *testing.T) {
	supported := []string{
		"gemini-2.0-flash",
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-2.5-flash-lite",
		"gemini-3-flash-preview",
		"gemini-3-pro-image-preview",
		"gemini-3.1-pro-preview",
		"gemini-3.1-flash-image-preview",
		"gemini-3.5-flash-lite",
		"gemini-3.6-flash",
		"gemini-3.8-flash",
		"gemini-4-hydrogen",
		"gemini-4-neon",
		"gemini-4-argon",
		"gemini-4-carbon",
		"gemini-robotics-er-2-preview",
		"gemini-nano-banana-2.1",
	}
	unsupported := []string{
		"gemini-1.5-pro",
		"gemini-1.5-flash",
		"gemini-2.0-flash-lite",
		"gemini-2.5-flash-image",
		"gemma-3-27b-it",
		"gemini-embedding-001",
		"custom-model",
	}
	for _, model := range supported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"hi","tools":[{"type":"web_search"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var got struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["googleSearch"] == nil {
			t.Errorf("supported model %s: tools=%v, want googleSearch", model, got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("supported model %s: unexpected dropped losses %+v", model, tr.Losses)
		}
	}
	for _, model := range unsupported {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"hi","tools":[{"type":"web_search"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBodyOn(req, settings{BaseURL: defaultBaseURL})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var got struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 0 || !tr.Dropped() {
			t.Errorf("unsupported model %s: tools=%v dropped=%v, want no tool and dropped loss", model, got.Tools, tr.Dropped())
		}
	}
}

func TestWebSearchGeminiVertexHosts(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{"model":"gemini-2.5-flash","input":"hi","tools":[{"type":"web_search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)

	allowed := []struct {
		name string
		s    settings
	}{
		{"vertex_default", settings{Vertex: &vertexSettings{Project: "p", Location: "us-central1"}}},
		{"vertex_global_base", settings{BaseURL: "https://aiplatform.googleapis.com", Vertex: &vertexSettings{}}},
		{"vertex_regional_base", settings{BaseURL: "https://us-central1-aiplatform.googleapis.com", Vertex: &vertexSettings{}}},
		{"vertex_eu_regional_base", settings{BaseURL: "https://europe-west4-aiplatform.googleapis.com", Vertex: &vertexSettings{}}},
		{"vertex_us_rep", settings{BaseURL: "https://aiplatform.us.rep.googleapis.com", Vertex: &vertexSettings{}}},
		{"vertex_eu_rep", settings{BaseURL: "https://aiplatform.eu.rep.googleapis.com", Vertex: &vertexSettings{}}},
	}
	for _, tc := range allowed {
		t.Run(tc.name, func(t *testing.T) {
			body, tr, err := buildBodyOn(req, tc.s)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			_ = json.Unmarshal(body, &got)
			if len(got.Tools) != 1 || got.Tools[0]["googleSearch"] == nil {
				t.Errorf("%s: tools=%v, want googleSearch (body: %s)", tc.name, got.Tools, body)
			}
			if tr.Dropped() {
				t.Errorf("%s: unexpected dropped losses: %+v", tc.name, tr.Losses)
			}
		})
	}

	denied := []struct {
		name string
		s    settings
	}{
		{"vertex_custom_host", settings{BaseURL: "https://proxy.example.com", Vertex: &vertexSettings{}}},
		{"vertex_spoofed_host", settings{BaseURL: "https://evil.com-aiplatform.googleapis.com", Vertex: &vertexSettings{}}},
		{"vertex_suffix_spoof", settings{BaseURL: "https://aiplatform.googleapis.com.evil.com", Vertex: &vertexSettings{}}},
	}
	for _, tc := range denied {
		t.Run(tc.name, func(t *testing.T) {
			body, tr, err := buildBodyOn(req, tc.s)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			_ = json.Unmarshal(body, &got)
			if len(got.Tools) != 0 || !tr.Dropped() {
				t.Errorf("%s: tools=%v dropped=%v, want no tool and dropped loss", tc.name, got.Tools, tr.Dropped())
			}
		})
	}
}
