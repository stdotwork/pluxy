package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func geminiAPIKeyAccount(baseURL string) protocol.Account {
	return protocol.Account{
		ID:         "gemini-embed",
		Settings:   json.RawMessage(`{"base_url":"` + baseURL + `"}`),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "test-api-key"},
	}
}

func geminiEndpointBody(t *testing.T, events []llm.Event, wantFormat string) (string, *llm.Usage) {
	t.Helper()
	if len(events) < 3 {
		t.Fatalf("events = %d, want at least 3: %+v", len(events), events)
	}
	if events[0].Type != llm.EventResponseStart || events[0].Response == nil || events[0].Response.Format != wantFormat {
		t.Fatalf("first event = %+v, want response.start for %s", events[0], wantFormat)
	}
	var body strings.Builder
	for _, ev := range events[1 : len(events)-1] {
		if ev.Type != llm.EventNative {
			t.Fatalf("middle event = %+v, want native", ev)
		}
		var piece string
		if err := json.Unmarshal(ev.Raw, &piece); err != nil {
			t.Fatalf("body piece %s: %v", ev.Raw, err)
		}
		body.WriteString(piece)
	}
	last := events[len(events)-1]
	if last.Type != llm.EventFinish {
		t.Fatalf("last event = %+v, want finish", last)
	}
	return body.String(), last.Usage
}

func TestGeminiClientEmbedPassthrough(t *testing.T) {
	t.Run("embedContent byte-identical body and reply", func(t *testing.T) {
		const clientBody = `{"content": {"parts": [{"text": "hello world"}]}, "taskType": "RETRIEVAL_QUERY", "outputDimensionality": 4}`
		const upstreamReply = `{"embedding": {"values": [0.1, 0.2, 0.3, 0.4]}, "usageMetadata": {"promptTokenCount": 5}}`

		var gotPath, gotKey, gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotKey = r.Header.Get("X-Goog-Api-Key")
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, upstreamReply)
		}))
		t.Cleanup(srv.Close)

		rec := &recorder{}
		call := sdk.Call{
			Account: geminiAPIKeyAccount(srv.URL),
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiEmbedContent,
					Body:   json.RawMessage(clientBody),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiEmbedContent); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		if gotPath != "/v1beta/models/gemini-embedding-001:embedContent" {
			t.Errorf("path = %q", gotPath)
		}
		if gotKey != "test-api-key" {
			t.Errorf("x-goog-api-key = %q", gotKey)
		}
		if gotBody != clientBody {
			t.Errorf("upstream body = %s, want byte-identical %s", gotBody, clientBody)
		}
		reply, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiEmbedContent)
		if reply != upstreamReply {
			t.Errorf("reply = %s, want byte-identical %s", reply, upstreamReply)
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 5 {
			t.Errorf("usage = %+v, want InputTokens 5", usage)
		}
	})

	t.Run("batchEmbedContents passthrough and alias rewrite", func(t *testing.T) {
		const clientBody = `{"requests":[{"model":"models/client-alias","content":{"parts":[{"text":"a"}]}},{"model":"models/client-alias","content":{"parts":[{"text":"b"}]}}]}`
		const upstreamReply = `{"embeddings":[{"values":[1.0]},{"values":[2.0]}],"usageMetadata":{"promptTokenCount":6}}`

		var gotPath string
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, upstreamReply)
		}))
		t.Cleanup(srv.Close)

		rec := &recorder{}
		call := sdk.Call{
			Account: geminiAPIKeyAccount(srv.URL),
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiBatchEmbedContents,
					Body:   json.RawMessage(clientBody),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiBatchEmbedContents); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		if gotPath != "/v1beta/models/gemini-embedding-001:batchEmbedContents" {
			t.Errorf("path = %q", gotPath)
		}
		var sent struct {
			Requests []struct {
				Model string `json:"model"`
			} `json:"requests"`
		}
		if err := json.Unmarshal(gotBody, &sent); err != nil || len(sent.Requests) != 2 ||
			sent.Requests[0].Model != "models/gemini-embedding-001" || sent.Requests[1].Model != "models/gemini-embedding-001" {
			t.Errorf("upstream body = %s, want rewritten model names", gotBody)
		}
		reply, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents)
		if reply != upstreamReply {
			t.Errorf("reply = %s, want %s", reply, upstreamReply)
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 6 {
			t.Errorf("usage = %+v, want InputTokens 6", usage)
		}
	})
}

func TestGeminiClientEmbedVertex(t *testing.T) {
	t.Run("gemini-embedding-2 uses embedContent with embedContentConfig", func(t *testing.T) {
		var mu sync.Mutex
		var paths []string
		var bodies []map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var b map[string]any
			_ = json.Unmarshal(raw, &b)
			mu.Lock()
			paths = append(paths, r.URL.Path)
			bodies = append(bodies, b)
			idx := len(paths)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if idx == 1 {
				io.WriteString(w, `{"embedding":{"values":[0.1,0.2]},"usageMetadata":{"promptTokenCount":3}}`)
			} else {
				io.WriteString(w, `{"embedding":{"values":[0.3,0.4]},"usageMetadata":{"promptTokenCount":4}}`)
			}
		}))
		t.Cleanup(srv.Close)

		settings := json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`)
		acct := protocol.Account{
			ID:         "vertex-embed",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vertex-key"},
		}
		rec := &recorder{}
		call := sdk.Call{
			Account: acct,
			Request: llm.Request{
				Model: "gemini-embedding-2",
				Native: &llm.Native{
					Format: llm.FormatGeminiBatchEmbedContents,
					Body: json.RawMessage(`{"requests":[` +
						`{"content":{"parts":[{"text":"first"}]},"taskType":"RETRIEVAL_DOCUMENT","title":"Doc 1","outputDimensionality":2},` +
						`{"content":{"parts":[{"text":"second"}]},"outputDimensionality":2}]}`),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiBatchEmbedContents); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		if len(paths) != 2 || !strings.HasSuffix(paths[0], "/gemini-embedding-2:embedContent") || !strings.HasSuffix(paths[1], "/gemini-embedding-2:embedContent") {
			t.Fatalf("paths = %v, want 2 :embedContent calls", paths)
		}
		cfg, _ := bodies[0]["embedContentConfig"].(map[string]any)
		if cfg["taskType"] != "RETRIEVAL_DOCUMENT" || cfg["title"] != "Doc 1" || cfg["outputDimensionality"] != float64(2) {
			t.Errorf("first embedContentConfig = %+v", cfg)
		}
		reply, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents)
		var out struct {
			Embeddings []struct {
				Values []float64 `json:"values"`
			} `json:"embeddings"`
			UsageMetadata struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(reply), &out); err != nil {
			t.Fatalf("reply %s: %v", reply, err)
		}
		if len(out.Embeddings) != 2 || !reflect.DeepEqual(out.Embeddings[0].Values, []float64{0.1, 0.2}) ||
			!reflect.DeepEqual(out.Embeddings[1].Values, []float64{0.3, 0.4}) || out.UsageMetadata.PromptTokenCount != 7 {
			t.Fatalf("reply = %+v", out)
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 7 {
			t.Errorf("usage = %+v, want 7", usage)
		}
	})

	t.Run("gemini-embedding-001 uses single-instance predict", func(t *testing.T) {
		var mu sync.Mutex
		var paths []string
		var instanceCounts []int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var req vertexPredictEmbedRequest
			_ = json.Unmarshal(raw, &req)
			mu.Lock()
			paths = append(paths, r.URL.Path)
			instanceCounts = append(instanceCounts, len(req.Instances))
			idx := len(paths)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if idx == 1 {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[1.0,2.0],"statistics":{"token_count":2}}}]}`)
			} else {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[3.0,4.0],"statistics":{"token_count":3}}}]}`)
			}
		}))
		t.Cleanup(srv.Close)

		settings := json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`)
		acct := protocol.Account{
			ID:         "vertex-embed",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vertex-key"},
		}
		rec := &recorder{}
		call := sdk.Call{
			Account: acct,
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiBatchEmbedContents,
					Body: json.RawMessage(`{"requests":[` +
						`{"content":{"parts":[{"text":"a"}]},"taskType":"RETRIEVAL_QUERY","outputDimensionality":2},` +
						`{"content":{"parts":[{"text":"b"}]},"taskType":"RETRIEVAL_DOCUMENT","title":"T"}]}`),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiBatchEmbedContents); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		if !reflect.DeepEqual(instanceCounts, []int{1, 1}) {
			t.Fatalf("instanceCounts = %v, want [1 1]", instanceCounts)
		}
		if !strings.HasSuffix(paths[0], "/gemini-embedding-001:predict") {
			t.Errorf("path = %q, want :predict", paths[0])
		}
		reply, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents)
		if !strings.Contains(reply, `"promptTokenCount":5`) || !strings.Contains(reply, `[1,2]`) || !strings.Contains(reply, `[3,4]`) {
			t.Fatalf("reply = %s", reply)
		}
		if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 5 {
			t.Errorf("usage = %+v, want 5", usage)
		}
	})

	t.Run("text-embedding-005 batches predict and splits on outputDimensionality", func(t *testing.T) {
		var mu sync.Mutex
		var instanceCounts []int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var req vertexPredictEmbedRequest
			_ = json.Unmarshal(raw, &req)
			mu.Lock()
			instanceCounts = append(instanceCounts, len(req.Instances))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if len(req.Instances) == 2 {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[0.1]}},{"embeddings":{"values":[0.2]}}]}`)
			} else {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[0.3,0.4]}}]}`)
			}
		}))
		t.Cleanup(srv.Close)

		settings := json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`)
		acct := protocol.Account{
			ID:         "vertex-embed",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vertex-key"},
		}
		rec := &recorder{}
		call := sdk.Call{
			Account: acct,
			Request: llm.Request{
				Model: "text-embedding-005",
				Native: &llm.Native{
					Format: llm.FormatGeminiBatchEmbedContents,
					Body: json.RawMessage(`{"requests":[` +
						`{"content":{"parts":[{"text":"a"}]},"outputDimensionality":1},` +
						`{"content":{"parts":[{"text":"b"}]},"outputDimensionality":1},` +
						`{"content":{"parts":[{"text":"c"}]},"outputDimensionality":2}]}`),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiBatchEmbedContents); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		if !reflect.DeepEqual(instanceCounts, []int{2, 1}) {
			t.Fatalf("instanceCounts = %v, want [2 1]", instanceCounts)
		}
	})
}

func TestGeminiClientEmbedRefusalsAndScope(t *testing.T) {
	t.Run("non-text part on Vertex predict returns retryable credential-scoped unsupported", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("upstream should not be called: %s %s", r.Method, r.URL.Path)
		}))
		t.Cleanup(srv.Close)

		settings := json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`)
		acct := protocol.Account{
			ID:         "vertex-embed",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vertex-key"},
		}
		call := sdk.Call{
			Account: acct,
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}`),
				},
			},
		}
		err := geminiEmbedTo(context.Background(), call, &recorder{}, llm.FormatGeminiEmbedContent)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Fatalf("err = %+v, want retryable CodeUnsupported ScopeCredential", err)
		}
		if !strings.Contains(e.Message, "inlineData") {
			t.Errorf("message = %q, want it to name inlineData", e.Message)
		}
	})

	t.Run("bad upstream reply is ScopeRequest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `not json`)
		}))
		t.Cleanup(srv.Close)

		call := sdk.Call{
			Account: geminiAPIKeyAccount(srv.URL),
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hi"}]}}`),
				},
			},
		}
		err := geminiEmbedTo(context.Background(), call, &recorder{}, llm.FormatGeminiEmbedContent)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest {
			t.Fatalf("err = %+v, want CodeUpstream ScopeRequest", err)
		}
	})

	t.Run("reply is chunked into bounded native frames", func(t *testing.T) {
		prev := embeddingBodyChunkSize
		embeddingBodyChunkSize = 32
		t.Cleanup(func() { embeddingBodyChunkSize = prev })

		const upstreamReply = `{"embedding":{"values":[0.125,0.25,0.5,1.0,1.5,2.5,3.5,4.5]},"usageMetadata":{"promptTokenCount":4}}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, upstreamReply)
		}))
		t.Cleanup(srv.Close)

		rec := &recorder{}
		call := sdk.Call{
			Account: geminiAPIKeyAccount(srv.URL),
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: llm.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hi"}]}}`),
				},
			},
		}
		if err := geminiEmbedTo(context.Background(), call, rec, llm.FormatGeminiEmbedContent); err != nil {
			t.Fatalf("geminiEmbedTo: %v", err)
		}
		nativeCount := 0
		for _, ev := range rec.events {
			if ev.Type == llm.EventNative {
				nativeCount++
				var piece string
				_ = json.Unmarshal(ev.Raw, &piece)
				if len(piece) > 32 {
					t.Errorf("piece len = %d, want <= 32", len(piece))
				}
			}
		}
		if nativeCount < 2 {
			t.Fatalf("nativeCount = %d, want >= 2", nativeCount)
		}
		reply, _ := geminiEndpointBody(t, rec.events, llm.FormatGeminiEmbedContent)
		if reply != upstreamReply {
			t.Errorf("reassembled reply = %s, want %s", reply, upstreamReply)
		}
	})
}
