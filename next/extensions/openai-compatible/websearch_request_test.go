package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
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

func TestWebSearchOpenAICompatibleChatModels(t *testing.T) {
	// Search model gpt-5-search-api at api.openai.com gets web_search_options.
	req, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5-search-api",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"latest news"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2,"user_location":{"type":"approximate","country":"US","city":"SF"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, tr, err := buildBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		WebSearchOptions map[string]any `json:"web_search_options"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.WebSearchOptions == nil {
		t.Fatalf("gpt-5-search-api: missing web_search_options in %s", body)
	}
	// max_uses is not supported by Chat web_search_options, so it must be reported as dropped.
	if !tr.Dropped() {
		t.Errorf("gpt-5-search-api: expected dropped loss for max_uses, got %+v", tr.Losses)
	}

	// Non-search Chat model (gpt-5) at api.openai.com must NOT get web_search_options.
	reqNonSearch := req
	reqNonSearch.Model = "gpt-5"
	body2, tr2, err := buildBody(reqNonSearch, llm.FormatOpenAIChat, settings{BaseURL: "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var got2 struct {
		WebSearchOptions map[string]any `json:"web_search_options"`
	}
	if err := json.Unmarshal(body2, &got2); err != nil {
		t.Fatal(err)
	}
	if got2.WebSearchOptions != nil {
		t.Errorf("gpt-5: unexpected web_search_options in %s", body2)
	}
	if !tr2.Dropped() {
		t.Errorf("gpt-5: expected dropped loss, got %+v", tr2.Losses)
	}

	// Third-party host gets no web_search_options even on gpt-5-search-api.
	body3, tr3, err := buildBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://proxy.example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var got3 struct {
		WebSearchOptions map[string]any `json:"web_search_options"`
	}
	if err := json.Unmarshal(body3, &got3); err != nil {
		t.Fatal(err)
	}
	if got3.WebSearchOptions != nil {
		t.Errorf("third-party host: unexpected web_search_options in %s", body3)
	}
	if !tr3.Dropped() {
		t.Errorf("third-party host: expected dropped loss, got %+v", tr3.Losses)
	}
}

func TestWebSearchOpenAICompatibleSameFormatByteIdentical(t *testing.T) {
	raw := []byte(`{"model":"gpt-5-search-api","messages":[{"role":"user","content":"hi"}],"web_search_options":{"user_location":{"type":"approximate","approximate":{"country":"US"}}}}`)
	req, err := openaichat.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	req = roundTripReq(t, req)
	body, tr, err := buildBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://api.openai.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough {
		t.Fatalf("expected passthrough")
	}
	if !bytes.Equal(body, raw) {
		t.Errorf("same-format body changed:\ngot:  %s\nwant: %s", body, raw)
	}

	// Also verify Responses target on third-party host drops search tool.
	respReq, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-5","input":"hi","tools":[{"type":"web_search"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	respReq = roundTripReq(t, respReq)
	body4, tr4, err := buildBody(respReq, llm.FormatAnthropicMessages, settings{BaseURL: "https://proxy.example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var got4 struct {
		Tools []any `json:"tools"`
	}
	if err := json.Unmarshal(body4, &got4); err != nil {
		t.Fatal(err)
	}
	if len(got4.Tools) != 0 || !tr4.Dropped() {
		t.Errorf("third-party anthropic: tools=%v dropped=%v", got4.Tools, tr4.Dropped())
	}
}
