package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type embedBodyRec struct {
	format      string
	contentType string
	pieces      []string
	finished    bool
	usage       *llm.Usage
}

func (r *embedBodyRec) StartBody(format, contentType string) error {
	r.format = format
	r.contentType = contentType
	return nil
}

func (r *embedBodyRec) Body(chunk string) error {
	r.pieces = append(r.pieces, chunk)
	return nil
}

func (r *embedBodyRec) Finish(_ llm.Finish, usage *llm.Usage) error {
	r.finished = true
	r.usage = usage
	return nil
}

func (r *embedBodyRec) body() string {
	return strings.Join(r.pieces, "")
}

func TestGeminiEmbeddingsOpenAIWire(t *testing.T) {
	t.Run("embedContent translates to /embeddings and back", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5,0.75]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":4,"total_tokens":4}}`)
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
			Request: llm.Request{
				Model: "text-embedding-3-small",
				Native: &llm.Native{
					Format: protocol.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hello"}]},"outputDimensionality":3}`),
				},
			},
		}
		rec := &embedBodyRec{}
		if err := geminiEmbeddingsTo(context.Background(), call, rec, protocol.FormatGeminiEmbedContent); err != nil {
			t.Fatalf("geminiEmbeddingsTo: %v", err)
		}
		reqs := recorded()
		if len(reqs) != 1 || reqs[0].path != "/v1/embeddings" {
			t.Fatalf("upstream requests = %+v", reqs)
		}
		var sent map[string]any
		if err := json.Unmarshal(reqs[0].body, &sent); err != nil {
			t.Fatal(err)
		}
		if sent["model"] != "text-embedding-3-small" || sent["input"] != "hello" || sent["dimensions"] != float64(3) {
			t.Errorf("upstream body = %+v", sent)
		}
		var reply struct {
			Embedding struct {
				Values []float64 `json:"values"`
			} `json:"embedding"`
			UsageMetadata struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(rec.body()), &reply); err != nil {
			t.Fatalf("reply %s: %v", rec.body(), err)
		}
		if !reflect.DeepEqual(reply.Embedding.Values, []float64{0.25, 0.5, 0.75}) || reply.UsageMetadata.PromptTokenCount != 4 {
			t.Fatalf("reply = %+v", reply)
		}
		if rec.usage == nil || rec.usage.InputTokens == nil || *rec.usage.InputTokens != 4 {
			t.Errorf("usage = %+v, want 4", rec.usage)
		}
	})

	t.Run("batchEmbedContents preserves order by index and splits on dimensions", func(t *testing.T) {
		callCount := 0
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			callCount++
			w.Header().Set("Content-Type", "application/json")
			if callCount == 1 {
				// Return data out of order to verify index reordering.
				io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":1,"embedding":[3.0,4.0]},{"object":"embedding","index":0,"embedding":[1.0,2.0]}],"usage":{"prompt_tokens":5}}`)
			} else {
				io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[5.0,6.0,7.0]}],"usage":{"prompt_tokens":2}}`)
			}
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"responses"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
			Request: llm.Request{
				Model: "text-embedding-3-small",
				Native: &llm.Native{
					Format: protocol.FormatGeminiBatchEmbedContents,
					Body: json.RawMessage(`{"requests":[` +
						`{"content":{"parts":[{"text":"a"}]},"outputDimensionality":2},` +
						`{"content":{"parts":[{"text":"b"}]},"outputDimensionality":2},` +
						`{"content":{"parts":[{"text":"c"}]},"outputDimensionality":3}]}`),
				},
			},
		}
		rec := &embedBodyRec{}
		if err := geminiEmbeddingsTo(context.Background(), call, rec, protocol.FormatGeminiBatchEmbedContents); err != nil {
			t.Fatalf("geminiEmbeddingsTo: %v", err)
		}
		if len(recorded()) != 2 {
			t.Fatalf("upstream calls = %d, want 2", len(recorded()))
		}
		var reply struct {
			Embeddings []struct {
				Values []float64 `json:"values"`
			} `json:"embeddings"`
			UsageMetadata struct {
				PromptTokenCount int64 `json:"promptTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal([]byte(rec.body()), &reply); err != nil {
			t.Fatalf("reply %s: %v", rec.body(), err)
		}
		if len(reply.Embeddings) != 3 ||
			!reflect.DeepEqual(reply.Embeddings[0].Values, []float64{1, 2}) ||
			!reflect.DeepEqual(reply.Embeddings[1].Values, []float64{3, 4}) ||
			!reflect.DeepEqual(reply.Embeddings[2].Values, []float64{5, 6, 7}) ||
			reply.UsageMetadata.PromptTokenCount != 7 {
			t.Fatalf("reply = %+v", reply)
		}
	})

	t.Run("gemini wire passes through unchanged", func(t *testing.T) {
		const reqBody = `{"content":{"parts":[{"text":"hi"}]}}`
		const respBody = `{"embedding":{"values":[0.5]},"usageMetadata":{"promptTokenCount":2}}`
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, respBody)
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat-gemini",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1beta","wire_api":"gemini"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "goog-key"},
			},
			Request: llm.Request{
				Model: "gemini-embedding-001",
				Native: &llm.Native{
					Format: protocol.FormatGeminiEmbedContent,
					Body:   json.RawMessage(reqBody),
				},
			},
		}
		rec := &embedBodyRec{}
		if err := geminiEmbeddingsTo(context.Background(), call, rec, protocol.FormatGeminiEmbedContent); err != nil {
			t.Fatalf("geminiEmbeddingsTo: %v", err)
		}
		reqs := recorded()
		if len(reqs) != 1 || reqs[0].path != "/v1beta/models/gemini-embedding-001:embedContent" || string(reqs[0].body) != reqBody {
			t.Fatalf("upstream = %+v", reqs)
		}
		if rec.body() != respBody {
			t.Errorf("reply = %s, want %s", rec.body(), respBody)
		}
	})

	t.Run("messages wire returns retryable credential-scoped unsupported", func(t *testing.T) {
		srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat-messages",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"messages"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
			Request: llm.Request{
				Model: "text-embedding-3-small",
				Native: &llm.Native{
					Format: protocol.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hi"}]}}`),
				},
			},
		}
		err := geminiEmbeddingsTo(context.Background(), call, &embedBodyRec{}, protocol.FormatGeminiEmbedContent)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Fatalf("err = %+v, want retryable CodeUnsupported ScopeCredential", err)
		}
		if len(recorded()) != 0 {
			t.Errorf("upstream got %d requests, want 0", len(recorded()))
		}
	})
}

func TestGeminiEmbeddingsOpenAIRefusalsAndScope(t *testing.T) {
	cases := []struct {
		name      string
		format    string
		body      string
		suffix    string
		wantField string
	}{
		{"non-text part", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}}]}}`, "", "inlineData"},
		{"taskType", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"hi"}]},"taskType":"RETRIEVAL_QUERY"}`, "", "taskType"},
		{"title", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"hi"}]},"title":"Doc"}`, "", "title"},
		{"bad dimensions", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"hi"}]},"outputDimensionality":0}`, "", "outputDimensionality"},
		{"empty requests", protocol.FormatGeminiBatchEmbedContents, `{"requests":[]}`, "", "requests"},
		{"streaming", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"hi"}]},"stream":true}`, "", "stream"},
		{"thinking suffix", protocol.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"hi"}]}}`, "high", "thinking suffix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				w.WriteHeader(http.StatusInternalServerError)
			})
			call := sdk.Call{
				Account: protocol.Account{
					ID:         "compat",
					Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`),
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
				},
				Request: llm.Request{
					Model:   "text-embedding-3-small",
					Options: llm.Options{ThinkingSuffix: tc.suffix},
					Native:  &llm.Native{Format: tc.format, Body: json.RawMessage(tc.body)},
				},
			}
			err := geminiEmbeddingsTo(context.Background(), call, &embedBodyRec{}, tc.format)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest {
				t.Fatalf("err = %+v, want 400 CodeInvalidRequest ScopeRequest", err)
			}
			if !strings.Contains(strings.ToLower(e.Message), strings.ToLower(tc.wantField)) {
				t.Errorf("message = %q, want it to name %q", e.Message, tc.wantField)
			}
			if len(recorded()) != 0 {
				t.Errorf("upstream got %d requests, want 0", len(recorded()))
			}
		})
	}

	t.Run("bad upstream reply is ScopeRequest", func(t *testing.T) {
		srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"index":9,"embedding":[1.0]}]}`)
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
			Request: llm.Request{
				Model: "text-embedding-3-small",
				Native: &llm.Native{
					Format: protocol.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hi"}]}}`),
				},
			},
		}
		err := geminiEmbeddingsTo(context.Background(), call, &embedBodyRec{}, protocol.FormatGeminiEmbedContent)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Scope != llm.ScopeRequest {
			t.Fatalf("err = %+v, want CodeUpstream ScopeRequest", err)
		}
	})

	t.Run("reply is chunked into bounded pieces", func(t *testing.T) {
		prev := embeddingBodyChunkSize
		embeddingBodyChunkSize = 24
		t.Cleanup(func() { embeddingBodyChunkSize = prev })

		srv, _ := newCompactUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"index":0,"embedding":[0.125,0.25,0.5,1.0,1.5,2.5]}],"usage":{"prompt_tokens":3}}`)
		})
		call := sdk.Call{
			Account: protocol.Account{
				ID:         "compat",
				Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `/v1","wire_api":"chat"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
			Request: llm.Request{
				Model: "text-embedding-3-small",
				Native: &llm.Native{
					Format: protocol.FormatGeminiEmbedContent,
					Body:   json.RawMessage(`{"content":{"parts":[{"text":"hi"}]}}`),
				},
			},
		}
		rec := &embedBodyRec{}
		if err := geminiEmbeddingsTo(context.Background(), call, rec, protocol.FormatGeminiEmbedContent); err != nil {
			t.Fatalf("geminiEmbeddingsTo: %v", err)
		}
		if len(rec.pieces) < 2 {
			t.Fatalf("pieces = %d, want >= 2", len(rec.pieces))
		}
		for i, p := range rec.pieces {
			if len(p) > 24 {
				t.Errorf("piece[%d] len = %d, want <= 24", i, len(p))
			}
		}
	})
}
