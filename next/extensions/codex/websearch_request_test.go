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
			wantDropped: true,
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

func TestWebSearchCodexChatGPTBackendBareTool(t *testing.T) {
	msgReq, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"latest news"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["example.com"],"user_location":{"type":"approximate","country":"US","city":"NYC"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	msgReq = roundTripReq(t, msgReq)

	chatgptSettings := settings{BaseURL: "https://chatgpt.com/backend-api/codex"}
	ur, err := buildRequest(msgReq, "", chatgptSettings, account{}, false)
	if err != nil {
		t.Fatalf("chatgpt.com: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(ur.body, &got); err != nil {
		t.Fatal(err)
	}
	var wsTool map[string]any
	for _, tool := range got.Tools {
		if tool["type"] == "web_search" {
			wsTool = tool
			break
		}
	}
	if wsTool == nil {
		t.Fatalf("chatgpt.com: missing web_search tool in %s", ur.body)
	}
	if len(wsTool) != 1 {
		t.Errorf("chatgpt.com: want bare {type:web_search}, got %v", wsTool)
	}
	dropped := ur.tr.DroppedPaths()
	hasAllowed, hasLoc := false, false
	for _, p := range dropped {
		if p == "tools[0].allowed_domains" {
			hasAllowed = true
		}
		if p == "tools[0].user_location" {
			hasLoc = true
		}
	}
	if !hasAllowed || !hasLoc {
		t.Errorf("chatgpt.com: dropped = %v, want tools[0].allowed_domains and tools[0].user_location", dropped)
	}

	chatReq, err := openaichat.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"latest news"}],
		"web_search_options":{"search_context_size":"low"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	chatReq = roundTripReq(t, chatReq)
	urChat, err := buildRequest(chatReq, "", chatgptSettings, account{}, false)
	if err != nil {
		t.Fatalf("chatgpt.com chat: %v", err)
	}
	var gotChat struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(urChat.body, &gotChat)
	var wsChat map[string]any
	for _, tool := range gotChat.Tools {
		if tool["type"] == "web_search" {
			wsChat = tool
			break
		}
	}
	if wsChat == nil || len(wsChat) != 1 {
		t.Errorf("chatgpt.com chat: want bare {type:web_search}, got %v", wsChat)
	}
	hasSize := false
	for _, p := range urChat.tr.DroppedPaths() {
		if p == "web_search_options.search_context_size" {
			hasSize = true
		}
	}
	if !hasSize {
		t.Errorf("chatgpt.com chat: dropped = %v, want web_search_options.search_context_size", urChat.tr.DroppedPaths())
	}
}
