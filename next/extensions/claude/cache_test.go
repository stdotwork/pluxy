package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestReadSettingsCacheTTL(t *testing.T) {
	for _, c := range []struct {
		raw     string
		want    string
		wantErr bool
	}{
		{"", cacheTTL5m, false},
		{`{}`, cacheTTL5m, false},
		{`{"cache_ttl":"5m"}`, cacheTTL5m, false},
		{`{"cache_ttl":" 1H "}`, cacheTTL1h, false},
		{`{"cache_ttl":"24h"}`, "", true},
		{`{"cache_ttl":"bogus"}`, "", true},
	} {
		s, err := readSettings(json.RawMessage(c.raw))
		if c.wantErr {
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeCredential {
				t.Errorf("readSettings(%s) err = %v, want invalid_request/credential", c.raw, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("readSettings(%s): %v", c.raw, err)
		}
		if s.CacheTTL != c.want {
			t.Errorf("readSettings(%s).CacheTTL = %q, want %q", c.raw, s.CacheTTL, c.want)
		}
	}
}

func TestCacheBreakpointsWithTTL(t *testing.T) {
	in := `{"system":[{"type":"text","text":"S"}],"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	want5m := `{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	want1h := `{"system":[{"type":"text","text":"S","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`

	got5m, err := addCacheBreakpointsTTL([]byte(in), cacheTTL5m)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got5m, json.RawMessage(want5m)) {
		t.Errorf("5m:\n got %s\nwant %s", got5m, want5m)
	}

	got1h, err := addCacheBreakpointsTTL([]byte(in), cacheTTL1h)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got1h, json.RawMessage(want1h)) {
		t.Errorf("1h:\n got %s\nwant %s", got1h, want1h)
	}

	topMarked := `{"cache_control":{"type":"ephemeral"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	gotTop, err := addCacheBreakpointsTTL([]byte(topMarked), cacheTTL1h)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(gotTop, json.RawMessage(topMarked)) {
		t.Errorf("top-level marker overwritten:\n got %s\nwant %s", gotTop, topMarked)
	}
}

func TestBuildBodyWithCacheTTLAndPost(t *testing.T) {
	srv := claudetest.New(t)
	s, err := readSettings(srv.Settings(""))
	if err != nil {
		t.Fatal(err)
	}
	s.CacheTTL = cacheTTL1h
	cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}

	chatBody := `{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Hello"}]}`
	req, err := openaichat.ParseRequest([]byte(chatBody))
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := buildBodyWithTTL(req, s.CacheTTL)
	if err != nil {
		t.Fatal(err)
	}
	n := len(srv.Requests())
	resp, err := post(context.Background(), sdk.Call{Account: protocol.Account{ID: "a", Credential: cred}, Request: req}, s, "/v1/messages", body, false, false)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	got := srv.Requests()[n]
	if beta := got.Header.Get("Anthropic-Beta"); beta != extendedCacheTTLBeta {
		t.Errorf("Anthropic-Beta = %q, want %q", beta, extendedCacheTTLBeta)
	}
	want := `{"model":"claude-sonnet-4-6","max_tokens":64000,"stream":false,"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
	if !jsonEqual(got.Body, json.RawMessage(want)) {
		t.Errorf("upstream body:\n got %s\nwant %s", got.Body, want)
	}

	// Passthrough Messages body keeps its own markers untouched even under cache_ttl "1h".
	passthroughIn := `{"model":"claude-sonnet-4-6","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	ptBody, tr, err := buildBodyWithTTL(llm.Request{Model: "claude-sonnet-4-6", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(passthroughIn)}}, cacheTTL1h)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough || string(ptBody) != passthroughIn {
		t.Errorf("passthrough changed under cache_ttl 1h: %s", ptBody)
	}
}

func TestChatCacheControlMapping(t *testing.T) {
	t.Run("text part cache_control reaches Claude", func(t *testing.T) {
		in := `{
			"model": "claude-sonnet-4-6",
			"messages": [
				{"role": "system", "content": [{"type": "text", "text": "Rules"}]},
				{"role": "user", "content": [{"type": "text", "text": "Check", "cache_control": {"type": "ephemeral", "ttl": "1h"}}]},
				{"role": "assistant", "content": "Working."},
				{"role": "user", "content": "Follow up"}
			]
		}`
		req, err := openaichat.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		body, tr, err := buildBodyWithTTL(req, cacheTTL5m)
		if err != nil {
			t.Fatalf("unexpected drop reject: %v", err)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("losses = %+v, want none", tr.Losses)
		}
		want := `{
			"model": "claude-sonnet-4-6",
			"max_tokens": 64000,
			"stream": false,
			"system": [{"type": "text", "text": "Rules"}],
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "Check", "cache_control": {"type": "ephemeral", "ttl": "1h"}}]},
				{"role": "assistant", "content": [{"type": "text", "text": "Working."}]},
				{"role": "user", "content": [{"type": "text", "text": "Follow up"}]}
			]
		}`
		if !jsonEqual(body, json.RawMessage(want)) {
			t.Errorf("got:\n%s\nwant:\n%s", body, want)
		}
	})

	t.Run("tool result cache_control reaches Claude", func(t *testing.T) {
		in := `{
			"model": "claude-sonnet-4-6",
			"tools": [{"type": "function", "function": {"name": "a", "parameters": {"type": "object"}}}],
			"messages": [
				{"role": "user", "content": "Check"},
				{"role": "assistant", "content": null, "tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "a", "arguments": "{}"}}
				]},
				{"role": "tool", "tool_call_id": "call_1", "cache_control": {"type": "ephemeral"}, "content": "res1"},
				{"role": "user", "content": "Follow up"}
			]
		}`
		req, err := openaichat.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		body, tr, err := buildBodyWithTTL(req, cacheTTL5m)
		if err != nil {
			t.Fatalf("unexpected drop reject: %v", err)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("losses = %+v, want none", tr.Losses)
		}
		want := `{
			"model": "claude-sonnet-4-6",
			"max_tokens": 64000,
			"stream": false,
			"tools": [{"name": "a", "input_schema": {"type": "object", "properties": {}}}],
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "Check"}]},
				{"role": "assistant", "content": [{"type": "tool_use", "id": "call_1", "name": "a", "input": {}}]},
				{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "call_1", "content": "res1", "cache_control": {"type": "ephemeral"}}]},
				{"role": "user", "content": [{"type": "text", "text": "Follow up"}]}
			]
		}`
		if !jsonEqual(body, json.RawMessage(want)) {
			t.Errorf("got:\n%s\nwant:\n%s", body, want)
		}
	})

	t.Run("invalid cache_control is not carried and triggers DropReject", func(t *testing.T) {
		in := `{
			"model": "claude-sonnet-4-6",
			"messages": [{"role": "user", "content": [{"type": "text", "text": "hi", "cache_control": {"type": "bad"}}]}]
		}`
		req, err := openaichat.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		_, _, err = buildBodyWithTTL(req, cacheTTL5m)
		var e *llm.Error
		if !errors.As(err, &e) || !slices.Contains(e.Dropped, "messages[0].content[0].cache_control") {
			t.Errorf("err = %v, want messages[0].content[0].cache_control reported dropped", err)
		}
	})
}

func TestResponsesPromptCacheMapping(t *testing.T) {
	t.Run("24h retention sets 1h ttl and passes DropReject as approximated", func(t *testing.T) {
		in := `{"model":"claude-sonnet-4-6","instructions":"Be brief.","input":"Hi","prompt_cache_retention":"24h"}`
		req, err := openairesponses.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		body, tr, err := buildBodyWithTTL(req, cacheTTL5m)
		if err != nil {
			t.Fatalf("unexpected DropReject failure: %v", err)
		}
		if len(tr.Losses) != 1 || tr.Losses[0].Kind != llm.LossApproximated || tr.Losses[0].Path != "prompt_cache_retention" {
			t.Errorf("losses = %+v, want single approximated prompt_cache_retention", tr.Losses)
		}
		want := `{"model":"claude-sonnet-4-6","max_tokens":64000,"stream":false,"system":[{"type":"text","text":"Be brief.","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
		if !jsonEqual(body, json.RawMessage(want)) {
			t.Errorf("got:\n%s\nwant:\n%s", body, want)
		}
	})

	t.Run("in_memory overrides 1h account default to 5m with no losses", func(t *testing.T) {
		in := `{"model":"claude-sonnet-4-6","input":"Hi","prompt_cache_retention":"in_memory"}`
		req, err := openairesponses.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		body, tr, err := buildBodyWithTTL(req, cacheTTL1h)
		if err != nil {
			t.Fatalf("unexpected DropReject failure: %v", err)
		}
		if len(tr.Losses) != 0 {
			t.Errorf("losses = %+v, want none", tr.Losses)
		}
		want := `{"model":"claude-sonnet-4-6","max_tokens":64000,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}]}`
		if !jsonEqual(body, json.RawMessage(want)) {
			t.Errorf("got:\n%s\nwant:\n%s", body, want)
		}
	})

	t.Run("prompt_cache_key remains dropped", func(t *testing.T) {
		in := `{"model":"claude-sonnet-4-6","input":"Hi","prompt_cache_key":"k1"}`
		req, err := openairesponses.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		_, _, err = buildBodyWithTTL(req, cacheTTL5m)
		var e *llm.Error
		if !errors.As(err, &e) || !slices.Contains(e.Dropped, "prompt_cache_key") {
			t.Errorf("err = %v, want prompt_cache_key dropped", err)
		}
	})

	t.Run("prompt_cache_options ttl 24h maps to 1h", func(t *testing.T) {
		in := `{"model":"claude-sonnet-4-6","input":"Hi","prompt_cache_options":{"ttl":"24h"}}`
		req, err := openairesponses.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = llm.DropReject
		body, tr, err := buildBodyWithTTL(req, cacheTTL5m)
		if err != nil {
			t.Fatalf("unexpected DropReject failure: %v", err)
		}
		if len(tr.Losses) != 1 || tr.Losses[0].Kind != llm.LossApproximated || tr.Losses[0].Path != "prompt_cache_options" {
			t.Errorf("losses = %+v, want single approximated prompt_cache_options", tr.Losses)
		}
		want := `{"model":"claude-sonnet-4-6","max_tokens":64000,"stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
		if !jsonEqual(body, json.RawMessage(want)) {
			t.Errorf("got:\n%s\nwant:\n%s", body, want)
		}
	})
}
