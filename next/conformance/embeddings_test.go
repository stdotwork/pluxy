package conformance

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// TestConformanceGeminiEmbeddings checks protocol minor 36's Gemini embedding endpoint format
// constants and endpoint event invariants.
func TestConformanceGeminiEmbeddings(t *testing.T) {
	if protocol.Minor < 36 {
		t.Fatalf("protocol.Minor = %d, want >= 36", protocol.Minor)
	}
	if protocol.FormatGeminiEmbedContent != "gemini.embed_content" {
		t.Fatalf("FormatGeminiEmbedContent = %q, want gemini.embed_content", protocol.FormatGeminiEmbedContent)
	}
	if protocol.FormatGeminiBatchEmbedContents != "gemini.batch_embed_contents" {
		t.Fatalf("FormatGeminiBatchEmbedContents = %q, want gemini.batch_embed_contents", protocol.FormatGeminiBatchEmbedContents)
	}

	for _, format := range []string{protocol.FormatGeminiEmbedContent, protocol.FormatGeminiBatchEmbedContents} {
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
