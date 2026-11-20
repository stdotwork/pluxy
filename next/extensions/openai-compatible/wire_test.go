package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Every client format is translated to every upstream wire. A request that already speaks the
// upstream's wire is passed through instead, which the corpus covers.
func TestBuildBodyTranslatesEveryWire(t *testing.T) {
	sources := []string{
		llm.FormatOpenAIChat,
		llm.FormatOpenAIResponses,
		llm.FormatAnthropicMessages,
		llm.FormatGeminiGenerate,
	}
	targets := []string{
		llm.FormatOpenAIChat,
		llm.FormatOpenAIResponses,
		llm.FormatAnthropicMessages,
		llm.FormatGeminiGenerate,
	}
	for _, source := range sources {
		for _, target := range targets {
			if source == target {
				continue
			}
			t.Run(source+" to "+target, func(t *testing.T) {
				req := llm.Request{
					Model: "test-model",
					Items: []llm.Item{{
						Type: llm.ItemTypeMessage,
						Role: llm.RoleUser,
						Content: []llm.Part{{
							Type: llm.PartText,
							Text: "hello",
						}},
					}},
					Native: &llm.Native{Format: source, Body: json.RawMessage(`{}`)},
				}
				body, tr, err := buildBody(req, target)
				if err != nil {
					t.Fatalf("buildBody: %v", err)
				}
				if tr.From != source || tr.To != target || tr.Passthrough {
					t.Fatalf("translation = %+v, want %s to %s without passthrough", tr, source, target)
				}
				assertWireBody(t, target, body)
			})
		}
	}
}

func assertWireBody(t *testing.T, format string, body []byte) {
	t.Helper()
	switch format {
	case llm.FormatOpenAIChat:
		var got struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("Chat body: %v: %s", err, body)
		}
		if got.Model != "test-model" || got.Stream || len(got.Messages) != 1 ||
			got.Messages[0].Role != llm.RoleUser || string(got.Messages[0].Content) != `"hello"` {
			t.Fatalf("Chat body = %s, want one user message saying hello", body)
		}
	case llm.FormatOpenAIResponses:
		var got struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
			Input  []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("Responses body: %v: %s", err, body)
		}
		if got.Model != "test-model" || got.Stream || len(got.Input) != 1 ||
			got.Input[0].Type != "message" || got.Input[0].Role != llm.RoleUser ||
			len(got.Input[0].Content) != 1 || got.Input[0].Content[0].Type != "input_text" ||
			got.Input[0].Content[0].Text != "hello" {
			t.Fatalf("Responses body = %s, want one user input message saying hello", body)
		}
	case llm.FormatAnthropicMessages:
		var got struct {
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("Messages body: %v: %s", err, body)
		}
		if got.Model != "test-model" || got.Stream || got.MaxTokens != 32000 || len(got.Messages) != 1 ||
			got.Messages[0].Role != llm.RoleUser || len(got.Messages[0].Content) != 1 ||
			got.Messages[0].Content[0].Type != "text" || got.Messages[0].Content[0].Text != "hello" {
			t.Fatalf("Messages body = %s, want one user text block saying hello and the default limit", body)
		}
	case llm.FormatGeminiGenerate:
		var got struct {
			Model   string `json:"model"`
			Content []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GenerateContent body: %v: %s", err, body)
		}
		if got.Model != "test-model" || len(got.Content) != 1 ||
			got.Content[0].Role != "user" || len(got.Content[0].Parts) != 1 ||
			got.Content[0].Parts[0].Text != "hello" {
			t.Fatalf("GenerateContent body = %s, want one user part saying hello", body)
		}
	default:
		t.Fatalf("unknown format %q", format)
	}
}
