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

func TestWebSearchToCodex(t *testing.T) {
	cases := []struct {
		name        string
		parse       func(t *testing.T) llm.Request
		wantDropped bool
		checkTool   func(t *testing.T, tool map[string]any)
	}{
		{
			name: "messages_with_unsupported_settings",
			parse: func(t *testing.T) llm.Request {
				req, err := anthropicmessages.ParseRequest([]byte(`{
					"model":"gpt-5.4",
					"max_tokens":1024,
					"messages":[{"role":"user","content":"latest news"}],
					"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"allowed_domains":["example.com"],"blocked_domains":["bad.com"],"user_location":{"type":"approximate","country":"US","city":"NYC"}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			wantDropped: true, // max_uses and blocked_domains not supported by Responses
			checkTool: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search" {
					t.Errorf("type = %v, want web_search", tool["type"])
				}
				filters, _ := tool["filters"].(map[string]any)
				if filters == nil || len(filters["allowed_domains"].([]any)) != 1 {
					t.Errorf("filters = %v, want allowed_domains", filters)
				}
				loc, _ := tool["user_location"].(map[string]any)
				if loc["country"] != "US" || loc["city"] != "NYC" {
					t.Errorf("user_location = %v", loc)
				}
			},
		},
		{
			name: "chat",
			parse: func(t *testing.T) llm.Request {
				req, err := openaichat.ParseRequest([]byte(`{
					"model":"gpt-5.4",
					"messages":[{"role":"user","content":"latest news"}],
					"web_search_options":{"user_location":{"type":"approximate","approximate":{"country":"GB","city":"London"}}}
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			checkTool: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search" {
					t.Errorf("type = %v, want web_search", tool["type"])
				}
				loc, _ := tool["user_location"].(map[string]any)
				if loc["country"] != "GB" || loc["city"] != "London" {
					t.Errorf("user_location = %v", loc)
				}
			},
		},
		{
			name: "gemini",
			parse: func(t *testing.T) llm.Request {
				req, err := geminigenerate.ParseRequest("gpt-5.4", false, []byte(`{
					"contents":[{"role":"user","parts":[{"text":"latest news"}]}],
					"tools":[{"googleSearch":{}}]
				}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			checkTool: func(t *testing.T, tool map[string]any) {
				if tool["type"] != "web_search" {
					t.Errorf("type = %v, want web_search", tool["type"])
				}
			},
		},
	}

	officialAcc := account{}
	officialSettings := settings{BaseURL: "https://api.openai.com/v1"}
	customSettings := settings{BaseURL: "https://proxy.example.com/v1"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := roundTripReq(t, tc.parse(t))
			ur, err := buildRequest(req, "", officialSettings, officialAcc, false)
			if err != nil {
				t.Fatalf("official host: %v", err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(ur.body, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			var wsTool map[string]any
			for _, tool := range got.Tools {
				if tool["type"] == "web_search" {
					wsTool = tool
					break
				}
			}
			if wsTool == nil {
				t.Fatalf("official host: missing web_search tool in %s", ur.body)
			}
			tc.checkTool(t, wsTool)
			if ur.tr.Dropped() != tc.wantDropped {
				t.Errorf("official host: dropped = %v, want %v (losses: %+v)", ur.tr.Dropped(), tc.wantDropped, ur.tr.Losses)
			}

			// Third-party base_url gets no tool and a loss.
			customUR, err := buildRequest(req, "", customSettings, officialAcc, false)
			if err != nil {
				t.Fatalf("custom host: %v", err)
			}
			var customGot struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(customUR.body, &customGot); err != nil {
				t.Fatalf("unmarshal custom: %v", err)
			}
			for _, tool := range customGot.Tools {
				if tool["type"] == "web_search" {
					t.Errorf("custom host: unexpected web_search tool in %s", customUR.body)
				}
			}
			if !customUR.tr.Dropped() {
				t.Errorf("custom host: expected dropped loss, got %+v", customUR.tr.Losses)
			}
		})
	}
}

func TestWebSearchResponsesSameFormatUnchanged(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","input":"hi","tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"]}}]}`)
	req, err := openairesponses.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, losses, err := openairesponses.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("unexpected losses: %+v", losses)
	}
	if !bytes.Contains(body, []byte(`{"type":"web_search","filters":{"allowed_domains":["example.com"]}}`)) {
		t.Errorf("same-format tool changed: %s", body)
	}
}
