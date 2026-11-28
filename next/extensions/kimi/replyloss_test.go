package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNonStreamNamelessToolCallLogged(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")

	run := func(t *testing.T, replyBody string) ([]llm.Event, string) {
		t.Helper()
		srv := kimitest.New(t)
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req kimitest.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, replyBody)
		}
		var logs syncBuffer
		ext, err := host.Start(manifest, host.Options{Log: &logs})
		if err != nil {
			t.Fatal(err)
		}
		access, _ := srv.Grant()
		acct := protocol.Account{
			ID:         "kimi",
			Settings:   srv.Settings(),
			Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access},
		}
		req := llm.Request{
			Model: "kimi-k3",
			Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}},
		}
		s := ext.Generate(context.Background(), acct, req)
		var events []llm.Event
		for {
			ev := s.Next()
			events = append(events, ev)
			if ev.Terminal() {
				break
			}
		}
		s.Close()
		ext.Stop()
		return events, logs.String()
	}

	t.Run("nameless tool call logs reply loss", func(t *testing.T) {
		body := `{"id":"chatcmpl-1","object":"chat.completion","model":"k3","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_a","type":"function","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		events, logs := run(t, body)
		last := events[len(events)-1]
		if last.Type != llm.EventFinish || last.Finish == nil || last.Finish.Reason != llm.FinishStop {
			t.Fatalf("terminal event = %+v, want finish stop", last)
		}
		for _, ev := range events {
			if ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
				t.Errorf("nameless tool call was not dropped: %+v", ev.Item)
			}
		}
		if !strings.Contains(logs, "reply loss: dropped choices[0].message.tool_calls[0]") {
			t.Errorf("logs = %q, want reply loss for choices[0].message.tool_calls[0]", logs)
		}
	})

	t.Run("named tool call logs no reply loss", func(t *testing.T) {
		body := `{"id":"chatcmpl-2","object":"chat.completion","model":"k3","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_b","type":"function","function":{"name":"get_weather","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
		events, logs := run(t, body)
		last := events[len(events)-1]
		if last.Type != llm.EventFinish || last.Finish == nil || last.Finish.Reason != llm.FinishToolCalls {
			t.Fatalf("terminal event = %+v, want finish tool_calls", last)
		}
		if strings.Contains(logs, "reply loss:") {
			t.Errorf("unexpected reply loss logged for valid tool call: %q", logs)
		}
	})
}

func TestNonStreamResponsesReplyLossLogged(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")

	run := func(t *testing.T, replyBody string) ([]llm.Event, string, string) {
		t.Helper()
		srv := kimitest.New(t)
		var gotPath string
		srv.Generate = func(w http.ResponseWriter, r *http.Request, req kimitest.Request) {
			gotPath = req.Path
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, replyBody)
		}
		var logs syncBuffer
		ext, err := host.Start(manifest, host.Options{Log: &logs})
		if err != nil {
			t.Fatal(err)
		}
		access, _ := srv.Grant()
		acct := protocol.Account{
			ID:         "kimi",
			Settings:   srv.Settings(),
			Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access},
		}
		req, err := openairesponses.ParseRequest([]byte(`{"model":"kimi-k3","input":"Hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		s := ext.Generate(context.Background(), acct, req)
		var events []llm.Event
		for {
			ev := s.Next()
			events = append(events, ev)
			if ev.Terminal() {
				break
			}
		}
		s.Close()
		ext.Stop()
		return events, logs.String(), gotPath
	}

	t.Run("dropped image_generation_call logs reply loss", func(t *testing.T) {
		body := `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"k3","output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":""}]}`
		events, logs, gotPath := run(t, body)
		if gotPath != "/coding/v1/responses" {
			t.Fatalf("upstream path = %q, want /coding/v1/responses", gotPath)
		}
		last := events[len(events)-1]
		if last.Type != llm.EventFinish || last.Finish == nil || last.Finish.Reason != llm.FinishStop {
			t.Fatalf("terminal event = %+v, want finish stop", last)
		}
		for _, ev := range events {
			if ev.Type == llm.EventMedia {
				t.Errorf("empty image_generation_call was not dropped: %+v", ev.Media)
			}
		}
		if !strings.Contains(logs, "reply loss: dropped output[0] (empty image_generation_call result)") {
			t.Errorf("logs = %q, want reply loss for output[0]", logs)
		}
	})

	t.Run("normal responses reply logs no reply loss", func(t *testing.T) {
		body := `{"id":"resp_2","object":"response","created_at":1,"status":"completed","model":"k3","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}]}`
		events, logs, gotPath := run(t, body)
		if gotPath != "/coding/v1/responses" {
			t.Fatalf("upstream path = %q, want /coding/v1/responses", gotPath)
		}
		last := events[len(events)-1]
		if last.Type != llm.EventFinish || last.Finish == nil || last.Finish.Reason != llm.FinishStop {
			t.Fatalf("terminal event = %+v, want finish stop", last)
		}
		if strings.Contains(logs, "reply loss:") {
			t.Errorf("unexpected reply loss logged for normal reply: %q", logs)
		}
	})
}
