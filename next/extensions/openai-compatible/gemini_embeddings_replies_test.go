package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// embedAttempt runs one Gemini-format embedding call on an account with the wire and returns what
// the extension sent, its error and the requests the fake upstream got.
func embedAttempt(t *testing.T, wire, model, format, body string, handle func(w http.ResponseWriter, n int)) (*embedBodyRec, int, error) {
	t.Helper()
	n := 0
	srv, recorded := newCompactUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		n++
		w.Header().Set("Content-Type", "application/json")
		handle(w, n)
	})
	base := srv.URL + "/v1"
	if wire == "gemini" {
		base = srv.URL + "/v1beta"
	}
	rec := &embedBodyRec{}
	err := geminiEmbeddingsTo(context.Background(), sdk.Call{
		Account: protocol.Account{
			ID:         "compat",
			Settings:   json.RawMessage(`{"base_url":"` + base + `","wire_api":"` + wire + `"}`),
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
		},
		Request: llm.Request{Model: model, Native: &llm.Native{Format: format, Body: json.RawMessage(body)}},
	}, rec, format)
	return rec, len(recorded()), err
}

// A 200 that is not a full answer becomes an error, and nothing of it reaches the client.
func TestGeminiWireEmbedRefusesShortReplies(t *testing.T) {
	const two = `{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`
	const one = `{"content":{"parts":[{"text":"a"}]}}`
	for _, tc := range []struct{ name, format, body, reply string }{
		{"batch with too few embeddings", llm.FormatGeminiBatchEmbedContents, two, `{"embeddings":[{"values":[1,2]}]}`},
		{"single with an error object", llm.FormatGeminiEmbedContent, one, `{"error":{"code":500,"message":"backend failed","status":"INTERNAL"}}`},
		{"single without an embedding", llm.FormatGeminiEmbedContent, one, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, err := embedAttempt(t, "gemini", "gemini-embedding-001", tc.format, tc.body, func(w http.ResponseWriter, _ int) { io.WriteString(w, tc.reply) })
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUpstream || e.Status != http.StatusBadGateway || e.Scope != llm.ScopeRequest {
				t.Fatalf("err = %v, want a 502 upstream error on the request", err)
			}
			if rec.format != "" || rec.finished || len(rec.pieces) != 0 {
				t.Errorf("recorded %+v, want nothing sent to the client", rec)
			}
		})
	}
}

// A field of embedContentConfig that /embeddings cannot carry is refused naming the field,
// retryable on the account so routing tries another, and is not dropped.
func TestChatWireEmbedRefusesUnknownConfig(t *testing.T) {
	rec, calls, err := embedAttempt(t, "chat", "text-embedding-3-small", llm.FormatGeminiEmbedContent,
		`{"content":{"parts":[{"text":"a"}]},"embedContentConfig":{"autoTruncate":false}}`,
		func(w http.ResponseWriter, _ int) {
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]}]}`)
		})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable || e.Status != http.StatusBadRequest ||
		!strings.Contains(e.Message, "embedContentConfig.autoTruncate") {
		t.Fatalf("err = %v, want a retryable 400 unsupported naming embedContentConfig.autoTruncate", err)
	}
	if calls != 0 || rec.finished {
		t.Errorf("upstream calls = %d, finished = %v, want none", calls, rec.finished)
	}
}

// A batch with an item that is not an object is refused, so a client's model name is never left
// in a body the extension could not rewrite.
func TestGeminiWireEmbedBatchWithNonObjectItem(t *testing.T) {
	for _, body := range []string{
		`{"requests":[{"model":"models/my-private-alias","content":{"parts":[{"text":"a"}]}},7]}`,
		`{"requests":"models/my-private-alias"}`,
	} {
		_, calls, err := embedAttempt(t, "gemini", "gemini-embedding-001", llm.FormatGeminiBatchEmbedContents, body, func(w http.ResponseWriter, _ int) {
			io.WriteString(w, `{"embeddings":[{"values":[1]},{"values":[2]}]}`)
		})
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest {
			t.Errorf("%s: err = %v, want a 400 on the request", body, err)
		}
		if calls != 0 {
			t.Errorf("%s: upstream calls = %d, want none (the alias must not be sent)", body, calls)
		}
	}
}

// When one of several upstream calls reports no usage, no total is reported: a sum over only the
// calls that reported would understate it.
func TestChatWireEmbedUsageNeedsEveryCall(t *testing.T) {
	const batch = `{"requests":[` +
		`{"content":{"parts":[{"text":"a"}]},"outputDimensionality":2},` +
		`{"content":{"parts":[{"text":"b"}]},"outputDimensionality":3}]}`
	rec, calls, err := embedAttempt(t, "chat", "text-embedding-3-small", llm.FormatGeminiBatchEmbedContents, batch, func(w http.ResponseWriter, n int) {
		if n == 1 {
			io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2]}],"usage":{"prompt_tokens":5}}`)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1,2,3]}]}`)
	})
	if err != nil || calls != 2 {
		t.Fatalf("err = %v, calls = %d, want two successful calls", err, calls)
	}
	if rec.usage != nil || strings.Contains(rec.body(), "usageMetadata") {
		t.Errorf("usage = %+v, body = %s, want no usage when one call reported none", rec.usage, rec.body())
	}
}

// A 200 with an error object does not carry the upstream's text to the client: it can name the
// account's project, which an ordinary error reply has masked.
func TestGeminiWireEmbedErrorBodyDoesNotLeakProject(t *testing.T) {
	const reply = `{"error":{"code":403,"message":"Permission denied on resource project projects/acme-prod-4821.","status":"PERMISSION_DENIED"}}`
	rec, _, err := embedAttempt(t, "gemini", "gemini-embedding-001", llm.FormatGeminiEmbedContent, `{"content":{"parts":[{"text":"a"}]}}`,
		func(w http.ResponseWriter, _ int) { io.WriteString(w, reply) })
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUpstream {
		t.Fatalf("err = %v, want an upstream error", err)
	}
	if got := fmt.Sprintf("%+v %s", e, e.Raw); strings.Contains(got, "acme-prod-4821") || rec.finished {
		t.Errorf("error %s, finished %v: the project reached the client or the reply was sent", got, rec.finished)
	}
}
