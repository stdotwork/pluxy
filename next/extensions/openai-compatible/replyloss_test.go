package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A client in the upstream's own format is given the upstream's body, which holds the citations the
// decoder had to leave out of the model, unless the reply is built again from the model's events:
// a Gemini stream sent whole, and a Responses reply sent whole as a stream. Only then is the loss
// real and logged.
func TestReplyLossLoggedOnlyWhenTheClientLosesTheCitations(t *testing.T) {
	title := strings.Repeat("t", 20<<10)
	const n = 300 // 6 MiB of citations, over the 4 MiB a reply keeps
	var chunks, supports, anns []string
	for i := range n {
		chunks = append(chunks, fmt.Sprintf(`{"web":{"uri":"https://a.example/%d","title":%q}}`, i, title))
		supports = append(supports, fmt.Sprintf(`{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[%d]}`, i))
		anns = append(anns, fmt.Sprintf(`{"type":"url_citation","url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}`, i, title))
	}
	geminiChunk := fmt.Sprintf(`{"responseId":"g","modelVersion":"m","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Alpha beta."}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[%s],"groundingSupports":[%s]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
		strings.Join(chunks, ","), strings.Join(supports, ","))
	responsesBody := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"m","output":[{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Alpha beta.","annotations":[%s]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		strings.Join(anns, ","))
	var responsesStream strings.Builder
	for _, ev := range []string{
		`{"type":"response.created","response":{"id":"r","model":"m","status":"in_progress"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Alpha beta."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Alpha beta.","annotations":[` + strings.Join(anns, ",") + `]}]}}`,
		`{"type":"response.completed","response":` + responsesBody + `}`,
	} {
		fmt.Fprintf(&responsesStream, "data: %s\n\n", ev)
	}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0] {
		case "gemini-sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", geminiChunk)
		case "gemini-json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, geminiChunk)
		case "responses-json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, responsesBody)
		case "responses-sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, responsesStream.String())
		}
	}))
	t.Cleanup(up.Close)

	var logs logBuffer
	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: &logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	geminiClient := func(stream bool) llm.Request {
		req, err := geminigenerate.ParseRequest("m", stream, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	responsesClient := func(stream bool) llm.Request {
		req, err := openairesponses.ParseRequest([]byte(fmt.Sprintf(`{"model":"m","input":"hi","stream":%v}`, stream)))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	for _, c := range []struct {
		name     string
		upstream string
		wire     string
		req      llm.Request
		wantLoss bool
	}{
		{"Gemini stream, whole Gemini client", "gemini-sse", "gemini", geminiClient(false), true},
		{"Gemini stream, streaming Gemini client", "gemini-sse", "gemini", geminiClient(true), false},
		{"Gemini whole, whole Gemini client", "gemini-json", "gemini", geminiClient(false), false},
		{"Responses whole, streaming Responses client", "responses-json", "responses", responsesClient(true), true},
		{"Responses whole, whole Responses client", "responses-json", "responses", responsesClient(false), false},
		{"Responses stream, streaming Responses client", "responses-sse", "responses", responsesClient(true), false},
		{"Responses stream, whole Responses client", "responses-sse", "responses", responsesClient(false), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := len(logs.String())
			settings, _ := json.Marshal(map[string]string{"base_url": up.URL + "/" + c.upstream + "/v1", "wire_api": c.wire})
			s := ext.Generate(context.Background(), protocol.Account{
				ID: "a", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			}, c.req)
			defer s.Close()
			var ev llm.Event
			for ev = s.Next(); !ev.Terminal(); ev = s.Next() {
			}
			if ev.Type != llm.EventFinish {
				t.Fatalf("terminal event %+v, want finish", ev)
			}
			got := strings.Contains(logs.String()[before:], "reply loss: dropped citations")
			if got != c.wantLoss {
				t.Errorf("citation loss logged = %v, want %v; log: %s", got, c.wantLoss, logs.String()[before:])
			}
		})
	}
}
