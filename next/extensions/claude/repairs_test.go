package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func chatRequest(model string, items ...llm.Item) llm.Request {
	return llm.Request{Model: model, Items: items, Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)}}
}

func chatText(role, text string, turn int) llm.Item {
	return llm.Item{Type: llm.ItemTypeMessage, Role: role, Content: []llm.Part{{Type: llm.PartText, Text: text}},
		Origin: &llm.Origin{Format: llm.FormatOpenAIChat, Path: "messages[" + itoa(turn) + "]"}}
}

func TestBuildBodyDefaultsMaxTokensToModel(t *testing.T) {
	limit := 1000
	for _, c := range []struct {
		model string
		set   *int
		want  int
	}{
		{"claude-opus-4-6", nil, 128000},
		{"claude-sonnet-4-5", nil, 64000},
		{"claude-sonnet-4-5-20250929", nil, 64000},
		{"claude-haiku-4-5@20251001", nil, 64000},
		{"claude-3-7-sonnet-20250219", nil, 8192},
		{"claude-opus-4-7[1m]", nil, 128000},
		{"claude-test", nil, 32000},
		{"claude-opus-4-6", &limit, 1000},
	} {
		req := chatRequest(c.model, chatText(llm.RoleUser, "Hi", 0))
		req.Options.MaxOutputTokens = c.set
		body, _, err := buildBody(req)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &got)
		if got.MaxTokens != c.want {
			t.Errorf("%s (client limit %v): max_tokens %d, want %d", c.model, c.set, got.MaxTokens, c.want)
		}
	}
}

func TestReadSettingsTrimsVersionPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://claude.example/v1":          "https://claude.example",
		"https://claude.example/v1/":         "https://claude.example",
		"https://claude.example/v1/messages": "https://claude.example",
		"https://claude.example/api/v1":      "https://claude.example/api",
		"https://claude.example/v1beta":      "https://claude.example/v1beta",
		"https://claude.example/anthropic/":  "https://claude.example/anthropic",
	} {
		raw, _ := json.Marshal(map[string]string{"base_url": in})
		s, err := readSettings(raw)
		if err != nil {
			t.Fatal(err)
		}
		if s.BaseURL != want {
			t.Errorf("base_url %q read as %q, want %q", in, s.BaseURL, want)
		}
	}
}

func TestBuildBodyRepairsPassthroughTurns(t *testing.T) {
	in := `{"model":"claude-opus-4-6","max_tokens":10,"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Sure:"}]}`
	body, tr, err := buildBody(llm.Request{Model: "claude-opus-4-6", Native: &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(in)}})
	if err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(body, json.RawMessage(`{"model":"claude-opus-4-6","max_tokens":10,"messages":[{"role":"user","content":"Hi"}]}`)) {
		t.Errorf("body %s; want the trailing assistant turn dropped", body)
	}
	if !tr.Passthrough || !slices.ContainsFunc(tr.Losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "messages[1]" }) {
		t.Errorf("translation %+v; want a passthrough reporting messages[1] dropped", tr)
	}
}

func startExtension(t *testing.T) *host.Extension {
	t.Helper()
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	return ext
}

func events(t *testing.T, s *host.Stream) []llm.Event {
	t.Helper()
	var evs []llm.Event
	deadline := time.After(30 * time.Second)
	for {
		ch := make(chan llm.Event, 1)
		go func() { ch <- s.Next() }()
		select {
		case ev := <-ch:
			evs = append(evs, ev)
			if ev.Terminal() {
				return evs
			}
		case <-deadline:
			s.Close()
			t.Fatalf("no terminal event; got %+v", evs)
		}
	}
}

func TestPrefillDroppedThroughExtension(t *testing.T) {
	srv := claudetest.New(t)
	var settings map[string]string
	_ = json.Unmarshal(srv.Settings("/v1"), &settings)
	settings["base_url"] = srv.URL + "/v1"
	raw, _ := json.Marshal(settings)
	account := protocol.Account{ID: "claude", Settings: raw, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}}

	ext := startExtension(t)
	req := chatRequest("claude-opus-4-6", chatText(llm.RoleUser, "Name a colour.", 0), chatText(llm.RoleAssistant, "The colour is", 1))
	evs := events(t, ext.Generate(context.Background(), account, req))

	last := evs[len(evs)-1]
	if last.Type != llm.EventFinish {
		t.Fatalf("call ended with %+v, want a finished reply", last)
	}
	var start *llm.ResponseInfo
	for _, ev := range evs {
		if ev.Type == llm.EventResponseStart {
			start = ev.Response
		}
	}
	if start == nil || start.Translation == nil ||
		!slices.ContainsFunc(start.Translation.Losses, func(l llm.Loss) bool { return l.Kind == llm.LossDropped && l.Path == "messages[1]" }) {
		t.Errorf("response.start %+v; want the translation to report messages[1] dropped", start)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(reqs))
	}
	if reqs[0].Prefix != "" {
		t.Errorf("request went to %s/v1/messages, want /v1/messages", reqs[0].Prefix)
	}
	var sent struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(reqs[0].Body, &sent)
	if n := len(sent.Messages); n == 0 || sent.Messages[n-1].Role != "user" {
		t.Errorf("upstream body %s; want it to end on the user's turn", reqs[0].Body)
	}
}
