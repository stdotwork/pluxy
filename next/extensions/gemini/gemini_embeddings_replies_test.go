package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// embedAttempt runs one Gemini-format embedding call on a fake upstream and returns what the
// extension sent, its error and how many calls the upstream got.
func embedAttempt(t *testing.T, vertex bool, model, format, body string, handle func(w http.ResponseWriter, n int)) (*recorder, error, int) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		w.Header().Set("Content-Type", "application/json")
		handle(w, n)
	}))
	t.Cleanup(srv.Close)
	acct := geminiAPIKeyAccount(srv.URL)
	if vertex {
		acct = protocol.Account{
			ID:         "vertex-embed",
			Settings:   json.RawMessage(`{"base_url":"` + srv.URL + `","vertex":{"project":"p","location":"us-central1"}}`),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vertex-key"},
		}
	}
	rec := &recorder{}
	err := geminiEmbedTo(context.Background(), sdk.Call{Account: acct, Request: llm.Request{
		Model:  model,
		Native: &llm.Native{Format: format, Body: json.RawMessage(body)},
	}}, rec, format)
	return rec, err, int(calls.Load())
}

// A 200 that is not a full answer becomes an error, and nothing of it reaches the client.
func TestGeminiEmbedRefusesShortReplies(t *testing.T) {
	const two = `{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`
	const one = `{"content":{"parts":[{"text":"a"}]}}`
	for _, tc := range []struct {
		name, model, format, body, reply string
		vertex                           bool
	}{
		{"batch with too few embeddings", "gemini-embedding-001", llm.FormatGeminiBatchEmbedContents, two, `{"embeddings":[{"values":[1,2]}]}`, false},
		{"batch with an embedding without values", "gemini-embedding-001", llm.FormatGeminiBatchEmbedContents, two, `{"embeddings":[{"values":[1]},{}]}`, false},
		{"single with an error object", "gemini-embedding-001", llm.FormatGeminiEmbedContent, one, `{"error":{"code":500,"message":"backend failed","status":"INTERNAL"}}`, false},
		{"single without an embedding", "gemini-embedding-001", llm.FormatGeminiEmbedContent, one, `{}`, false},
		{"vertex single with an error object", "gemini-embedding-2", llm.FormatGeminiEmbedContent, one, `{"error":{"code":500,"message":"backend failed"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err, _ := embedAttempt(t, tc.vertex, tc.model, tc.format, tc.body, func(w http.ResponseWriter, _ int) { io.WriteString(w, tc.reply) })
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Status != http.StatusBadGateway || e.Scope != llm.ScopeRequest {
				t.Fatalf("err = %v, want a 502 upstream error on the request", err)
			}
			if len(rec.events) != 0 {
				t.Errorf("events = %+v, want nothing sent to the client", rec.events)
			}
		})
	}
	t.Run("a full reply passes through unchanged", func(t *testing.T) {
		const reply = `{"embeddings":[{"values":[1,2]},{"values":[3,4]}]}`
		rec, err, _ := embedAttempt(t, false, "gemini-embedding-001", llm.FormatGeminiBatchEmbedContents, two, func(w http.ResponseWriter, _ int) { io.WriteString(w, reply) })
		if err != nil {
			t.Fatal(err)
		}
		if body, _ := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents); body != reply {
			t.Errorf("body = %s, want %s", body, reply)
		}
	})
}

// A field of embedContentConfig that :predict cannot carry is refused naming the field, retryable
// on the account so routing tries another, and is not dropped.
func TestGeminiEmbedPredictRefusesUnknownConfig(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"single", llm.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"a"}]},"embedContentConfig":{"autoTruncate":false}}`},
		{"batch", llm.FormatGeminiBatchEmbedContents, `{"requests":[{"content":{"parts":[{"text":"a"}]},"embedContentConfig":{"taskType":"RETRIEVAL_QUERY","autoTruncate":false}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err, calls := embedAttempt(t, true, "gemini-embedding-001", tc.format, tc.body, func(w http.ResponseWriter, _ int) {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[1]}}]}`)
			})
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable || e.Status != http.StatusBadRequest ||
				!strings.Contains(e.Message, "embedContentConfig.autoTruncate") {
				t.Fatalf("err = %v, want a retryable 400 unsupported naming embedContentConfig.autoTruncate", err)
			}
			if calls != 0 || len(rec.events) != 0 {
				t.Errorf("upstream calls = %d, events = %d, want none", calls, len(rec.events))
			}
		})
	}
	t.Run("the fields it carries are still sent", func(t *testing.T) {
		_, err, calls := embedAttempt(t, true, "gemini-embedding-001", llm.FormatGeminiEmbedContent,
			`{"content":{"parts":[{"text":"a"}]},"embedContentConfig":{"taskType":"RETRIEVAL_QUERY","title":"t","outputDimensionality":1,"autoTruncate":null}}`,
			func(w http.ResponseWriter, _ int) {
				io.WriteString(w, `{"predictions":[{"embeddings":{"values":[1]}}]}`)
			})
		if err != nil || calls != 1 {
			t.Fatalf("err = %v, calls = %d, want one successful call", err, calls)
		}
	})
}

// A batch with an item that is not an object is refused, so a client's model name is never left
// in a body the extension could not rewrite.
func TestGeminiEmbedBatchWithNonObjectItem(t *testing.T) {
	for _, body := range []string{
		`{"requests":[{"model":"models/my-private-alias","content":{"parts":[{"text":"a"}]}},7]}`,
		`{"requests":"models/my-private-alias"}`,
	} {
		rec, err, calls := embedAttempt(t, false, "gemini-embedding-001", llm.FormatGeminiBatchEmbedContents, body, func(w http.ResponseWriter, _ int) {
			io.WriteString(w, `{"embeddings":[{"values":[1]},{"values":[2]}]}`)
		})
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest {
			t.Errorf("%s: err = %v, want a 400 on the request", body, err)
		}
		if calls != 0 || len(rec.events) != 0 {
			t.Errorf("%s: upstream calls = %d, want none (the alias must not be sent)", body, calls)
		}
	}
}

// When one of several upstream calls reports no usage, no total is reported: a sum over only the
// calls that reported would understate it.
func TestGeminiEmbedUsageNeedsEveryCall(t *testing.T) {
	two := `{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`
	predict := func(w http.ResponseWriter, n int) {
		if n == 1 {
			io.WriteString(w, `{"predictions":[{"embeddings":{"values":[1],"statistics":{"token_count":5}}}]}`)
			return
		}
		io.WriteString(w, `{"predictions":[{"embeddings":{"values":[2]}}]}`)
	}
	embedContent := func(w http.ResponseWriter, n int) {
		if n == 1 {
			io.WriteString(w, `{"embedding":{"values":[1]},"usageMetadata":{"promptTokenCount":5}}`)
			return
		}
		io.WriteString(w, `{"embedding":{"values":[2]}}`)
	}
	for _, tc := range []struct {
		name, model string
		handle      func(http.ResponseWriter, int)
	}{
		{"predict, one call per input", "gemini-embedding-001", predict},
		{"embedContent, one call per input", "gemini-embedding-2", embedContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, err, calls := embedAttempt(t, true, tc.model, llm.FormatGeminiBatchEmbedContents, two, tc.handle)
			if err != nil || calls != 2 {
				t.Fatalf("err = %v, calls = %d, want two successful calls", err, calls)
			}
			body, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents)
			if usage != nil || strings.Contains(body, "usageMetadata") {
				t.Errorf("usage = %+v, body = %s, want no usage when one call reported none", usage, body)
			}
		})
	}
	t.Run("every call reporting still sums", func(t *testing.T) {
		rec, err, _ := embedAttempt(t, true, "gemini-embedding-2", llm.FormatGeminiBatchEmbedContents, two, func(w http.ResponseWriter, n int) {
			io.WriteString(w, `{"embedding":{"values":[1]},"usageMetadata":{"promptTokenCount":3}}`)
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, usage := geminiEndpointBody(t, rec.events, llm.FormatGeminiBatchEmbedContents); usage == nil || usage.InputTokens == nil || *usage.InputTokens != 6 {
			t.Errorf("usage = %+v, want 6 input tokens", usage)
		}
	})
}

// The same holds for OpenAI's /v1/embeddings on a Gemini account, which splits 101 inputs into two
// :batchEmbedContents calls.
func TestGeminiOpenAIEmbeddingsUsageNeedsEveryCall(t *testing.T) {
	inputs, _ := json.Marshal(strings.Split(strings.Repeat("x,", 100)+"x", ","))
	body := `{"model":"gemini-embedding-001","input":` + string(inputs) + `}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Requests []json.RawMessage `json:"requests"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		embeddings := strings.TrimSuffix(strings.Repeat(`{"values":[1]},`, len(req.Requests)), ",")
		w.Header().Set("Content-Type", "application/json")
		if len(req.Requests) == 100 {
			io.WriteString(w, `{"embeddings":[`+embeddings+`],"usageMetadata":{"promptTokenCount":5}}`)
			return
		}
		io.WriteString(w, `{"embeddings":[`+embeddings+`]}`)
	}))
	t.Cleanup(srv.Close)
	rec := &recorder{}
	err := embeddingsTo(context.Background(), sdk.Call{Account: geminiAPIKeyAccount(srv.URL), Request: llm.Request{
		Model:  "gemini-embedding-001",
		Native: &llm.Native{Format: llm.FormatOpenAIEmbeddings, Body: json.RawMessage(body)},
	}}, rec)
	if err != nil {
		t.Fatal(err)
	}
	last := rec.events[len(rec.events)-1]
	if last.Type != llm.EventFinish || last.Usage != nil {
		t.Errorf("last event = %+v, want a finish without usage, as one call reported none", last)
	}
}
