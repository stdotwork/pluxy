package conformance

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformanceGeminiEmbeddings checks protocol minor 36's Gemini embedding endpoint format
// constants and endpoint event invariants.
func TestConformanceGeminiEmbeddings(t *testing.T) {
	if protocol.Minor < host.GeminiEmbeddingsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.GeminiEmbeddingsMinor)
	}
	if llm.FormatGeminiEmbedContent != "gemini.embed_content" {
		t.Fatalf("FormatGeminiEmbedContent = %q, want gemini.embed_content", llm.FormatGeminiEmbedContent)
	}
	if llm.FormatGeminiBatchEmbedContents != "gemini.batch_embed_contents" {
		t.Fatalf("FormatGeminiBatchEmbedContents = %q, want gemini.batch_embed_contents", llm.FormatGeminiBatchEmbedContents)
	}

	for _, format := range []string{llm.FormatGeminiEmbedContent, llm.FormatGeminiBatchEmbedContents} {
		t.Run(format, func(t *testing.T) {
			rawReply, _ := json.Marshal(llm.EndpointReply{ContentType: "application/json"})
			piece, _ := json.Marshal(`{"embedding":{"values":[0.25,0.5]}}`)
			evs := []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: format, Raw: rawReply}},
				{Type: llm.EventNative, Raw: piece},
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
			}
			var start llm.EndpointReply
			if err := json.Unmarshal(evs[0].Response.Raw, &start); err != nil || start.ContentType != "application/json" {
				t.Fatalf("start = %+v, err = %v", start, err)
			}
			if evs[0].Response.Format != format {
				t.Fatalf("Response.Format = %q, want %q", evs[0].Response.Format, format)
			}
			var body string
			if err := json.Unmarshal(evs[1].Raw, &body); err != nil || !json.Valid([]byte(body)) {
				t.Fatalf("body piece = %q, err = %v", body, err)
			}
			if len(evs[1].Raw) >= protocol.MaxFrame {
				t.Fatalf("piece size = %d, want < %d", len(evs[1].Raw), protocol.MaxFrame)
			}
		})
	}
}
