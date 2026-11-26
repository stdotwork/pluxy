package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestGeminiEmbedRoutes(t *testing.T) {
	t.Run("embedContent", func(t *testing.T) {
		wantReply := `{"embedding":{"values":[0.25,0.5]}}`
		h, tried, seen := endpointHandler(t, func(_ string, req llm.Request) []llm.Event {
			raw, _ := json.Marshal(llm.EndpointReply{ContentType: "application/json"})
			bodyPiece, _ := json.Marshal(wantReply)
			return []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: req.Native.Format, Raw: raw}},
				{Type: llm.EventNative, Raw: bodyPiece},
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
			}
		})

		reqBody := `{"content":{"parts":[{"text":"hello"}]}}`
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-embedding-001:embedContent?key=k", strings.NewReader(reqBody))
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
		}
		if w.Body.String() != wantReply {
			t.Fatalf("body = %s, want %s", w.Body.String(), wantReply)
		}
		if len(*tried) != 1 || (*tried)[0] != "a" {
			t.Fatalf("tried = %v, want [a]", *tried)
		}
		if len(*seen) != 1 {
			t.Fatalf("seen = %d requests, want 1", len(*seen))
		}
		got := (*seen)[0]
		if got.Model != "gemini-embedding-001" {
			t.Errorf("Model = %q, want gemini-embedding-001", got.Model)
		}
		if got.Native == nil || got.Native.Format != "gemini.embed_content" {
			t.Errorf("Native.Format = %v, want gemini.embed_content", got.Native)
		}
		if string(got.Native.Body) != reqBody {
			t.Errorf("Native.Body = %s, want %s", got.Native.Body, reqBody)
		}
	})

	t.Run("batchEmbedContents", func(t *testing.T) {
		wantReply := `{"embeddings":[{"values":[0.1]},{"values":[0.2]}]}`
		h, _, seen := endpointHandler(t, func(_ string, req llm.Request) []llm.Event {
			raw, _ := json.Marshal(llm.EndpointReply{ContentType: "application/json"})
			bodyPiece, _ := json.Marshal(wantReply)
			return []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: req.Native.Format, Raw: raw}},
				{Type: llm.EventNative, Raw: bodyPiece},
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
			}
		})

		reqBody := `{"requests":[{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"a"}]}},{"model":"models/gemini-embedding-001","content":{"parts":[{"text":"b"}]}}]}`
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-embedding-001:batchEmbedContents", strings.NewReader(reqBody))
		r.Header.Set("X-Goog-Api-Key", "k")
		h.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body.String())
		}
		if w.Body.String() != wantReply {
			t.Fatalf("body = %s, want %s", w.Body.String(), wantReply)
		}
		if len(*seen) != 1 {
			t.Fatalf("seen = %d requests, want 1", len(*seen))
		}
		got := (*seen)[0]
		if got.Model != "gemini-embedding-001" {
			t.Errorf("Model = %q, want gemini-embedding-001", got.Model)
		}
		if got.Native == nil || got.Native.Format != "gemini.batch_embed_contents" {
			t.Errorf("Native.Format = %v, want gemini.batch_embed_contents", got.Native)
		}
	})

	t.Run("unsupported account falls through to second account", func(t *testing.T) {
		wantReply := `{"embedding":{"values":[1.0]}}`
		h, tried, _ := endpointHandler(t, func(account string, req llm.Request) []llm.Event {
			if account == "a" {
				return []llm.Event{{Type: llm.EventError, Error: &llm.Error{
					Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "unsupported",
				}}}
			}
			raw, _ := json.Marshal(llm.EndpointReply{ContentType: "application/json"})
			bodyPiece, _ := json.Marshal(wantReply)
			return []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: req.Native.Format, Raw: raw}},
				{Type: llm.EventNative, Raw: bodyPiece},
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
			}
		})

		w := postEndpoint(h, "/v1beta/models/gemini-embedding-001:embedContent", `{"content":{"parts":[{"text":"hi"}]}}`, nil)
		if w.Code != http.StatusOK || w.Body.String() != wantReply {
			t.Fatalf("status = %d, body = %s, want 200 %s", w.Code, w.Body.String(), wantReply)
		}
		if len(*tried) != 2 || (*tried)[0] != "a" || (*tried)[1] != "b" {
			t.Fatalf("tried = %v, want [a b]", *tried)
		}
	})

	t.Run("missing model in path returns Google-shaped 400", func(t *testing.T) {
		h, tried, _ := endpointHandler(t, func(_ string, _ llm.Request) []llm.Event {
			t.Error("endpoint should not be called when path has no model")
			return nil
		})
		w := postEndpoint(h, "/v1beta/models/:embedContent", `{"content":{"parts":[{"text":"hi"}]}}`, nil)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body.String())
		}
		if len(*tried) != 0 {
			t.Fatalf("tried = %v, want none", *tried)
		}
		var errResp struct {
			Error struct {
				Code   int    `json:"code"`
				Status string `json:"status"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil || errResp.Error.Code != 400 || errResp.Error.Status != "INVALID_ARGUMENT" {
			t.Fatalf("error body = %s, want Google INVALID_ARGUMENT 400", w.Body.String())
		}
	})
}
