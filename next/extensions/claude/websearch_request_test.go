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

func roundTripRequest(t *testing.T, req llm.Request) llm.Request {
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

func TestWebSearchToClaude(t *testing.T) {
	cases := []struct {
		name  string
		parse func(t *testing.T) llm.Request
		check func(t *testing.T, tool map[string]any)
	}{
		{
			name: "responses",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"claude-sonnet-4-6",
					"input":"latest news",
					"tools":[{"type":"web_search","max_uses":3,"filters":{"allowed_domains":["example.com"]},"user_location":{"type":"approximate","country":"US","city":"SF"}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			check: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
					t.Errorf("tool = %v, want web_search_20250305", tool)
				}
				if tool["max_uses"] != float64(3) {
					t.Errorf("max_uses = %v, want 3", tool["max_uses"])
				}
				if _, ok := tool["allowed_domains"]; !ok {
					t.Errorf("missing allowed_domains in %v", tool)
				}
				if _, ok := tool["user_location"]; !ok {
					t.Errorf("missing user_location in %v", tool)
				}
			},
		},
		{
			name: "chat",
			parse: func(t *testing.T) llm.Request {
				req, err := openaichat.ParseRequest([]byte(`{
					"model":"claude-sonnet-4-6",
					"messages":[{"role":"user","content":"latest news"}],
					"web_search_options":{"user_location":{"type":"approximate","approximate":{"country":"AU","city":"Sydney"}}}
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			check: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
					t.Errorf("tool = %v, want web_search_20250305", tool)
				}
				loc, _ := tool["user_location"].(map[string]any)
				if loc["country"] != "AU" || loc["city"] != "Sydney" {
					t.Errorf("user_location = %v, want AU/Sydney", loc)
				}
			},
		},
		{
			name: "gemini",
			parse: func(t *testing.T) llm.Request {
				req, err := geminigenerate.ParseRequest("claude-sonnet-4-6", false, []byte(`{
					"contents":[{"role":"user","parts":[{"text":"latest news"}]}],
					"tools":[{"googleSearch":{}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			check: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
					t.Errorf("tool = %v, want web_search_20250305", tool)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := roundTripRequest(t, tc.parse(t))
			// Official Anthropic host gets the translated tool.
			body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
			if err != nil {
				t.Fatalf("official host: %v", err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if len(got.Tools) != 1 {
				t.Fatalf("official host: len(tools) = %d, want 1 (body: %s)", len(got.Tools), body)
			}
			tc.check(t, got.Tools[0])
			if tr.Dropped() {
				t.Errorf("official host: unexpected dropped losses: %+v", tr.Losses)
			}

			// Third-party base_url gets no tool and a loss.
			customBody, customTr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, "https://proxy.example.com"))
			if err != nil {
				t.Fatalf("custom host: %v", err)
			}
			var customGot struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(customBody, &customGot); err != nil {
				t.Fatalf("unmarshal custom body: %v", err)
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

func TestWebSearchClaudeSameFormatByteIdentical(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}]}`)
	req, err := anthropicmessages.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)
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
