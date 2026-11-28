package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func hasLoss(losses []llm.Loss, kind, path string) bool {
	return slices.ContainsFunc(losses, func(l llm.Loss) bool {
		return l.Kind == kind && l.Path == path
	})
}

func TestReplayedAssistantAnnotations(t *testing.T) {
	const annotated = `{"model":"m","messages":[` +
		`{"role":"user","content":"Hi"},` +
		`{"role":"assistant","content":"Tokyo info","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/tokyo","title":"Tokyo","start_index":0,"end_index":5}}]},` +
		`{"role":"user","content":"More?"}]}`

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
			if tr.Dropped() {
				t.Errorf("host %s: unexpected dropped losses = %v", baseURL, tr.DroppedPaths())
			}
			if !hasLoss(tr.Losses, llm.LossApproximated, "messages[1].annotations") {
				t.Errorf("host %s: losses = %+v, want approximated messages[1].annotations", baseURL, tr.Losses)
			}

			reqReject := chatRequest(t, annotated)
			reqReject.DropPolicy = llm.DropReject
			body, tr, err = upstreamBody(reqReject, llm.FormatOpenAIChat, settings{BaseURL: baseURL})
			if err != nil {
				t.Fatalf("host %s reject policy refused replayed annotations: %v", baseURL, err)
			}
			if strings.Contains(string(body), `"annotations"`) {
				t.Fatalf("host %s reject policy leaked assistant annotations: %s", baseURL, body)
			}
			if tr.Passthrough || tr.Dropped() || !hasLoss(tr.Losses, llm.LossApproximated, "messages[1].annotations") {
				t.Errorf("host %s reject policy translation = %+v, want non-passthrough with approximated messages[1].annotations", baseURL, tr)
			}

			reqEncode := chatRequest(t, annotated)
			reqEncode.Native = nil
			reqEncode.DropPolicy = llm.DropReject
			body, tr, err = upstreamBody(reqEncode, llm.FormatOpenAIChat, settings{BaseURL: baseURL})
			if err != nil {
				t.Fatalf("host %s encode reject policy refused replayed annotations: %v", baseURL, err)
			}
			if strings.Contains(string(body), `"annotations"`) {
				t.Fatalf("host %s encode reject policy leaked assistant annotations: %s", baseURL, body)
			}
			if tr.Dropped() || !hasLoss(tr.Losses, llm.LossApproximated, "messages[1].annotations") {
				t.Errorf("host %s encode reject policy translation = %+v, want approximated messages[1].annotations", baseURL, tr)
			}
		})
	}

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
