package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func benchRequest() llm.Request {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 2500)
	schema := json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},` +
		`"days":{"type":"integer","enum":[1,2,3,7]}},"required":["city"]}`)
	return llm.Request{
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
		Options: llm.Options{MaxOutputTokens: ptr(512), Temperature: ptrF(0.7), ReasoningEffort: "high"},
	}
}

func BenchmarkEncodeRequest(b *testing.B) {
	req := benchRequest()
	b.Run("new", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := EncodeRequest(req); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("old", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := encodeRequestRef(req); err != nil {
				b.Fatal(err)
			}
		}
	})
}
