package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func reasoningBody(b *testing.B) ([]byte, llm.Request) {
	b.Helper()
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2500)
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},` +
		`"days":{"type":"integer","enum":[1,2,3,7]}},"required":["city"]}`)
	tokens, temp := 512, 0.7
	req := llm.Request{
		Model: "gpt-5",
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, Content: []llm.Part{{Type: llm.PartText, Text: "You are a helpful assistant."}}, TextContent: true},
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: text}}, TextContent: true},
			{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "weather", Arguments: `{"city":"Oslo","days":2}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_1", Output: "4C, rain"},
			{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "It rains in Oslo."}}, TextContent: true},
		},
		Tools: []llm.Tool{
			{Name: "weather", Description: "Weather for a city", Parameters: schema},
			{Name: "time", Description: "The time in a city", Parameters: schema},
		},
		Options: llm.Options{MaxOutputTokens: &tokens, Temperature: &temp, ReasoningEffort: "high"},
	}
	body, _, err := openaichat.EncodeRequest(req)
	if err != nil {
		b.Fatal(err)
	}
	if len(body) < 100_000 {
		b.Fatalf("body is %d bytes, want about 100 KB", len(body))
	}
	req.Options.ThinkingSuffix = "high"
	return body, req
}

func BenchmarkApplyReasoning(b *testing.B) {
	body, req := reasoningBody(b)
	b.Run("new", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, _, err := applyReasoning(body, req, llm.FormatOpenAIChat, nil)
			if err != nil {
				b.Fatal(err)
			}
			if len(out) != len(body) {
				b.Fatalf("body changed: %d bytes, want %d", len(out), len(body))
			}
		}
	})
	b.Run("old", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, _, err := applyReasoningRef(body, req, llm.FormatOpenAIChat, nil)
			if err != nil {
				b.Fatal(err)
			}
			if len(out) != len(body) {
				b.Fatalf("body changed: %d bytes, want %d", len(out), len(body))
			}
		}
	})
}
