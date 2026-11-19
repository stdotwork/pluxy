package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// geminiAPIKeyModelIDs is the list today's core returns from GET /v1/models for a Gemini API-key
// account when the account names no models (14 models).
var geminiAPIKeyModelIDs = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3-pro-image-preview",
	"gemini-3-pro-preview",
	"gemini-3.1-flash-image-preview",
	"gemini-3.1-flash-lite-preview",
	"gemini-3.1-pro-preview",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.8-flash",
	"gemini-4-argon",
}

// vertexModelIDs is the list today's core returns for a Vertex AI account (express-mode key or
// service account) when the account names no models: 21 models, less the 5 imagen-* models that
// only have the predict method.
var vertexModelIDs = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-image",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash",
	"gemini-3-pro",
	"gemini-3-pro-image",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-lite",
	"gemini-3.1-pro",
	"gemini-3.1-pro-preview",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.8-flash",
	"gemini-4-argon",
}

func closedGeminiURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	return u
}

func withClosedBaseURL(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	m := map[string]any{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := m["base_url"]; !ok {
		m["base_url"] = closedGeminiURL(t)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func listedIDs(t *testing.T, settings string) ([]string, *protocol.ModelList) {
	t.Helper()
	call := sdk.ModelsCall{Account: protocol.Account{ID: "a", Settings: withClosedBaseURL(t, settings),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}}
	list, err := listModels(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list.Models {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids, list
}

func TestListModelsGeminiAPIKey(t *testing.T) {
	ids, list := listedIDs(t, "")
	if list.From != protocol.ModelListFromExtension {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
	}
	if !slices.Equal(ids, geminiAPIKeyModelIDs) {
		t.Errorf("models = %v, want %v", ids, geminiAPIKeyModelIDs)
	}
	if list.OwnedBy != "google" {
		t.Errorf("owned_by = %q, want google", list.OwnedBy)
	}
	i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "gemini-2.5-pro" })
	if i < 0 {
		t.Fatal("gemini-2.5-pro is not listed")
	}
	m := list.Models[i]
	if m.Name != "models/gemini-2.5-pro" || m.DisplayName != "Gemini 2.5 Pro" || m.Version != "2.5" || m.Created != 1750118400 ||
		m.InputTokenLimit != 1048576 || m.OutputTokenLimit != 65536 || m.OwnedBy != "google" ||
		!slices.Equal(m.GenerationMethods, []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"}) ||
		!slices.Equal(m.InputModalities, []string{"text", "image", "audio", "video"}) || !slices.Equal(m.OutputModalities, []string{"text"}) {
		t.Errorf("gemini-2.5-pro = %+v", m)
	}
	// A catalog entry with no generation methods lists generateContent, as today's core shows.
	i = slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "gemini-3.8-flash" })
	if i < 0 {
		t.Fatal("gemini-3.8-flash is not listed")
	}
	if m := list.Models[i]; m.Name != "models/gemini-3.8-flash" || !slices.Equal(m.GenerationMethods, []string{"generateContent"}) || m.ContextLength != 1048576 {
		t.Errorf("gemini-3.8-flash = %+v", m)
	}
}

func TestListModelsVertex(t *testing.T) {
	// An express-mode key and a service account both have the vertex settings.
	for _, settings := range []string{`{"vertex":{"location":"global"}}`, `{"vertex":{"project":"p","location":"us-central1"}}`} {
		ids, list := listedIDs(t, settings)
		if list.From != protocol.ModelListFromExtension {
			t.Errorf("settings %s: from = %q, want %q", settings, list.From, protocol.ModelListFromExtension)
		}
		if !slices.Equal(ids, vertexModelIDs) {
			t.Errorf("settings %s: models = %v, want %v", settings, ids, vertexModelIDs)
		}
		if list.OwnedBy != "google" {
			t.Errorf("settings %s: owned_by = %q, want google", settings, list.OwnedBy)
		}
	}
}

func TestListModelsVertexImagenLeftOut(t *testing.T) {
	ids, _ := listedIDs(t, `{"vertex":{"location":"global"}}`)
	for _, id := range ids {
		if strings.HasPrefix(id, "imagen") {
			t.Errorf("imagen model %s is listed; its only method is predict", id)
		}
	}
}

func TestListModelsFromUpstreamPagination(t *testing.T) {
	var pageCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1beta/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Goog-Api-Key") != "AIza-live-key" {
			t.Errorf("X-Goog-Api-Key = %q, want AIza-live-key", r.Header.Get("X-Goog-Api-Key"))
		}
		if r.URL.Query().Get("pageSize") != "1000" {
			t.Errorf("pageSize = %q, want 1000", r.URL.Query().Get("pageSize"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch pageCalls.Add(1) {
		case 1:
			if tok := r.URL.Query().Get("pageToken"); tok != "" {
				t.Errorf("page 1 pageToken = %q, want empty", tok)
			}
			io.WriteString(w, `{
				"models": [
					{
						"name": "models/gemini-2.5-pro",
						"version": "2.5",
						"displayName": "Gemini 2.5 Pro",
						"description": "Most capable thinking model",
						"inputTokenLimit": 1048576,
						"outputTokenLimit": 65536,
						"supportedGenerationMethods": ["generateContent", "countTokens"]
					},
					{
						"name": "models/imagen-4.0-generate-001",
						"version": "001",
						"displayName": "Imagen 4",
						"supportedGenerationMethods": ["predict"]
					}
				],
				"nextPageToken": "page-2-token"
			}`)
		case 2:
			if tok := r.URL.Query().Get("pageToken"); tok != "page-2-token" {
				t.Errorf("page 2 pageToken = %q, want page-2-token", tok)
			}
			io.WriteString(w, `{
				"models": [
					{
						"name": "models/gemini-future-4",
						"version": "001",
						"displayName": "Gemini Future 4",
						"inputTokenLimit": 2097152,
						"outputTokenLimit": 65536
					}
				]
			}`)
		default:
			t.Errorf("unexpected extra page request %d", pageCalls.Load())
		}
	}))
	defer srv.Close()

	settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
	list, err := listModels(context.Background(), sdk.ModelsCall{
		Account: protocol.Account{
			ID:         "gem",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "AIza-live-key"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageCalls.Load() != 2 {
		t.Fatalf("page calls = %d, want 2", pageCalls.Load())
	}
	if list.From != protocol.ModelListFromAccount {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromAccount)
	}
	if list.OwnedBy != "google" {
		t.Errorf("owned_by = %q, want google", list.OwnedBy)
	}
	if len(list.Models) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(list.Models), list.Models)
	}
	if m := list.Models[0]; m.ID != "gemini-2.5-pro" || m.Name != "models/gemini-2.5-pro" ||
		m.DisplayName != "Gemini 2.5 Pro" || m.Version != "2.5" || m.OwnedBy != "google" ||
		m.InputTokenLimit != 1048576 || m.ContextLength != 1048576 ||
		m.OutputTokenLimit != 65536 || m.MaxCompletionTokens != 65536 ||
		!slices.Equal(m.GenerationMethods, []string{"generateContent", "countTokens"}) {
		t.Errorf("model 0 = %+v", m)
	}
	if m := list.Models[1]; m.ID != "gemini-future-4" || m.Name != "models/gemini-future-4" ||
		m.DisplayName != "Gemini Future 4" || m.InputTokenLimit != 2097152 || m.ContextLength != 2097152 ||
		m.OutputTokenLimit != 65536 || m.MaxCompletionTokens != 65536 {
		t.Errorf("model 1 = %+v", m)
	}
}

func TestListModelsVertexNeverQueriesUpstream(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	settings, _ := json.Marshal(map[string]any{
		"base_url": srv.URL,
		"vertex":   map[string]any{"location": "global"},
	})
	list, err := listModels(context.Background(), sdk.ModelsCall{
		Account: protocol.Account{
			ID:         "vtx",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "vtx-key"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Errorf("vertex hit upstream %d times, want 0", hits.Load())
	}
	if list.From != protocol.ModelListFromExtension {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
	}
}

func TestListModelsGeminiFallbackOnFailure(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		cred    *protocol.Credential
		handler http.HandlerFunc
	}{
		{
			name: "http_401",
			cred: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "bad"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}`)
			},
		},
		{
			name: "http_500",
			cred: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "malformed_json",
			cred: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"models": [broken`)
			},
		},
		{
			name: "empty_list",
			cred: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"models": []}`)
			},
		},
		{
			name: "only_predict_models",
			cred: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"models": [{"name":"models/imagen-4.0","supportedGenerationMethods":["predict"]}]}`)
			},
		},
		{
			name:    "timeout",
			timeout: 40 * time.Millisecond,
			cred:    &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
			},
		},
		{
			name: "missing_credential",
			cred: nil,
			handler: func(w http.ResponseWriter, r *http.Request) {
				t.Error("unexpected upstream hit without credential")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			list, err := listModels(ctx, sdk.ModelsCall{
				Account: protocol.Account{ID: "fail", Settings: settings, Credential: tc.cred},
			})
			if err != nil {
				t.Fatal(err)
			}
			if list.From != protocol.ModelListFromExtension {
				t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
			}
			var ids []string
			for _, m := range list.Models {
				ids = append(ids, m.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, geminiAPIKeyModelIDs) {
				t.Errorf("models = %v, want %v", ids, geminiAPIKeyModelIDs)
			}
		})
	}
}
