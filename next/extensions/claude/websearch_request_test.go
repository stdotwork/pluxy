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
		name             string
		parse            func(t *testing.T) llm.Request
		check            func(t *testing.T, tool map[string]any)
		keepOnCustomHost bool
	}{
		{
			name:             "responses",
			keepOnCustomHost: true,
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
			name: "responses_preview",
			parse: func(t *testing.T) llm.Request {
				req, err := openairesponses.ParseRequest([]byte(`{
					"model":"claude-sonnet-4-6",
					"input":"latest news",
					"tools":[{"type":"web_search_preview","user_location":{"type":"approximate","country":"US","city":"SF"}}]
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
			if tc.keepOnCustomHost {
				if len(customGot.Tools) != 1 {
					t.Errorf("custom host: len(tools) = %d, want 1 (body: %s)", len(customGot.Tools), customBody)
				}
				if customTr.Dropped() {
					t.Errorf("custom host: unexpected dropped loss: %+v", customTr.Losses)
				}
			} else {
				if len(customGot.Tools) != 0 {
					t.Errorf("custom host: len(tools) = %d, want 0 (body: %s)", len(customGot.Tools), customBody)
				}
				if !customTr.Dropped() {
					t.Errorf("custom host: expected dropped loss, got %+v", customTr.Losses)
				}
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

func TestWebSearchResponsesCustomBaseURL(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-test",
		"input":"latest news",
		"tools":[{"type":"web_search","max_uses":3,"filters":{"allowed_domains":["example.com"]},"user_location":{"type":"approximate","country":"US","city":"SF"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)

	body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0]["type"] != "web_search_20250305" {
		t.Fatalf("ToolWebSearch custom host: tools = %v, want web_search_20250305 (body: %s)", got.Tools, body)
	}
	if tr.Dropped() {
		t.Errorf("ToolWebSearch custom host: unexpected dropped losses: %+v", tr.Losses)
	}

	nativeReq := req
	nativeReq.Tools = []llm.Tool{{Type: llm.ToolNative, Origin: req.Tools[0].Origin}}
	bodyN, trN, err := buildBodyWith(nativeReq, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, nativeReq, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var gotN struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(bodyN, &gotN); err != nil {
		t.Fatal(err)
	}
	if len(gotN.Tools) != 1 || gotN.Tools[0]["type"] != "web_search_20250305" {
		t.Fatalf("ToolNative custom host: tools = %v, want web_search_20250305 (body: %s)", gotN.Tools, bodyN)
	}
	if trN.Dropped() {
		t.Errorf("ToolNative custom host: unexpected dropped losses: %+v", trN.Losses)
	}

	prevReq, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"latest news",
		"tools":[{"type":"web_search_preview"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	prevReq = roundTripRequest(t, prevReq)
	bodyP, trP, err := buildBodyWith(prevReq, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, prevReq, "https://proxy.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	var gotP struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(bodyP, &gotP); err != nil {
		t.Fatal(err)
	}
	if len(gotP.Tools) != 0 || !trP.Dropped() {
		t.Errorf("web_search_preview custom host: tools=%v dropped=%v, want empty tools and dropped loss", gotP.Tools, trP.Dropped())
	}
}

func TestWebSearchClaudeModelGate(t *testing.T) {
	supported := []string{
		"claude-sonnet-4-5-20250929",
		"claude-opus-4-5-20251101",
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-6",
		"claude-opus-4-6",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-opus-5",
		"claude-sonnet-5",
		"claude-opus-5-5",
		"claude-sonnet-5-5",
		"claude-haiku-5-5",
		"claude-fable-5-2",
		"claude-mythos-5-2",
		"claude-mythos-preview",
	}
	unsupported := []string{
		"claude-3-haiku-20240307",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-haiku-20241022",
		"claude-3-7-sonnet-20250219",
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-opus-4-1-20250805",
		"custom-model",
	}
	for _, model := range supported {
		req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"web_search_options":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripRequest(t, req)
		body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["type"] != "web_search_20250305" {
			t.Errorf("supported model %s: tools=%v, want web_search_20250305", model, got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("supported model %s: unexpected dropped losses %+v", model, tr.Losses)
		}
	}
	for _, model := range unsupported {
		req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"web_search_options":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripRequest(t, req)
		body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 0 || !tr.Dropped() {
			t.Errorf("unsupported model %s: tools=%v dropped=%v, want no tool and dropped loss", model, got.Tools, tr.Dropped())
		}
	}
}

func TestWebSearchResponsesToClaudeRejectPolicyAllowsExtraFields(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"hi",
		"tools":[{"type":"web_search","search_context_size":"low","return_token_budget":"unlimited"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripRequest(t, req)
	req.DropPolicy = llm.DropReject

	body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, "https://proxy.example.com"))
	if err != nil {
		t.Fatalf("reject policy refused existing Responses->Claude web_search: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(body, &got)
	if len(got.Tools) != 1 || got.Tools[0]["type"] != "web_search_20250305" {
		t.Fatalf("tools = %v, want web_search_20250305", got.Tools)
	}
	if tr.Dropped() {
		t.Errorf("expected no dropped losses, got %+v", tr.Losses)
	}
	var approx []string
	for _, l := range tr.Losses {
		if l.Kind == llm.LossApproximated {
			approx = append(approx, l.Path)
		}
	}
	if len(approx) != 2 || approx[0] != "tools[0].return_token_budget" || approx[1] != "tools[0].search_context_size" {
		t.Errorf("approximated paths = %v, want [tools[0].return_token_budget tools[0].search_context_size] (losses: %+v)", approx, tr.Losses)
	}

	chatReq, err := openaichat.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":"hi"}],
		"web_search_options":{"search_context_size":"low"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	chatReq = roundTripRequest(t, chatReq)
	chatReq.DropPolicy = llm.DropReject
	if _, _, err := buildBodyWith(chatReq, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, chatReq, defaultBaseURL)); err == nil {
		t.Errorf("expected reject policy to refuse Chat search_context_size on Claude, got nil")
	}
}
