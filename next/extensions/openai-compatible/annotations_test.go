package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestReplayedAssistantAnnotations(t *testing.T) {
	const annotated = `{"model":"m","messages":[` +
		`{"role":"user","content":"Hi"},` +
		`{"role":"assistant","content":"Tokyo info","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/tokyo","title":"Tokyo","start_index":0,"end_index":5}}]},` +
		`{"role":"user","content":"More?"}]}`

	// Hosts not documented to accept assistant annotations on Chat Completions strip them and
	// report a dropped loss, so drop_policy=reject refuses the request.
	for _, baseURL := range []string{
		"https://api.mistral.ai/v1",
		"https://api.openai.com/v1",
		"https://api.deepseek.com",
		"https://llm.example.com/v1",
	} {
		t.Run("strip_"+baseURL, func(t *testing.T) {
			req := chatRequest(t, annotated)
			body, tr, err := upstreamBody(req, llm.FormatOpenAIChat, settings{BaseURL: baseURL})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), `"annotations"`) {
				t.Fatalf("host %s received assistant annotations: %s", baseURL, body)
			}
			if tr.Passthrough {
				t.Errorf("host %s: expected Passthrough=false after stripping annotations", baseURL)
			}
			if !slices.Contains(tr.DroppedPaths(), "messages[1].annotations") {
				t.Errorf("host %s: dropped paths = %v, want messages[1].annotations", baseURL, tr.DroppedPaths())
			}

			reqReject := chatRequest(t, annotated)
			reqReject.DropPolicy = llm.DropReject
			_, _, err = upstreamBody(reqReject, llm.FormatOpenAIChat, settings{BaseURL: baseURL})
			var le *llm.Error
			if !errors.As(err, &le) || le.Code != llm.CodeUnsupported || !slices.Contains(le.Dropped, "messages[1].annotations") {
				t.Fatalf("host %s reject policy: err = %v, want CodeUnsupported with messages[1].annotations", baseURL, err)
			}
		})
	}

	// OpenRouter documents replaying assistant message annotations on /chat/completions: the
	// client's body stays byte-identical with no losses.
	t.Run("keep_openrouter", func(t *testing.T) {
		req := chatRequest(t, annotated)
		req.DropPolicy = llm.DropReject
		body, tr, err := upstreamBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://openrouter.ai/api/v1"})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, []byte(annotated)) {
			t.Errorf("openrouter body changed:\n got: %s\nwant: %s", body, annotated)
		}
		if !tr.Passthrough || len(tr.Losses) != 0 {
			t.Errorf("openrouter translation = %+v, want passthrough with no losses", tr)
		}
	})

	// A request whose user text merely mentions "annotations" stays byte-identical on strict hosts.
	t.Run("plain_mention_untouched", func(t *testing.T) {
		const mention = `{"model":"m","messages":[{"role":"user","content":"what are \"annotations\"?"},{"role":"assistant","content":"metadata"}]}`
		for _, baseURL := range []string{"https://api.openai.com/v1", "https://llm.example.com/v1"} {
			body, tr, err := upstreamBody(chatRequest(t, mention), llm.FormatOpenAIChat, settings{BaseURL: baseURL})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, []byte(mention)) || !tr.Passthrough || len(tr.Losses) != 0 {
				t.Errorf("host %s altered request without assistant annotations: body=%s tr=%+v", baseURL, body, tr)
			}
		}
	})
}
