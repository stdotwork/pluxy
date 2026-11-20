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

// claudeModelIDs is the list today's core returns from GET /v1/models for an API-key account and
// for a subscription login, when the account names no models (19 models, the same for both).
var claudeModelIDs = []string{
	"claude-3-5-haiku-20241022",
	"claude-3-7-sonnet-20250219",
	"claude-fable-5",
	"claude-fable-5-1",
	"claude-haiku-4-5-20251001",
	"claude-haiku-5-5",
	"claude-opus-4-1-20250805",
	"claude-opus-4-20250514",
	"claude-opus-4-5-20251101",
	"claude-opus-4-6",
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-opus-5",
	"claude-opus-5-5",
	"claude-sonnet-4-20250514",
	"claude-sonnet-4-5-20250929",
	"claude-sonnet-4-6",
	"claude-sonnet-5",
	"claude-sonnet-5-5",
}

func closedClaudeSettings(t *testing.T) json.RawMessage {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	baseURL := s.URL
	s.Close()
	raw, err := json.Marshal(map[string]string{"base_url": baseURL})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestListModelsEveryAccountKind(t *testing.T) {
	settings := closedClaudeSettings(t)
	kinds := map[string]*protocol.Credential{
		"api_key": {Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
		"oauth":   {Kind: protocol.CredentialOAuth, AccessToken: "token-test"},
	}
	for name, cred := range kinds {
		t.Run(name, func(t *testing.T) {
			list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{ID: "a", Settings: settings, Credential: cred}})
			if err != nil {
				t.Fatal(err)
			}
			if list.From != protocol.ModelListFromExtension {
				t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
			}
			if list.OwnedBy != "anthropic" {
				t.Errorf("owned_by = %q, want anthropic", list.OwnedBy)
			}
			var ids []string
			for _, m := range list.Models {
				ids = append(ids, m.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, claudeModelIDs) {
				t.Errorf("models = %v, want %v", ids, claudeModelIDs)
			}
		})
	}
}

func TestListModelsMetadata(t *testing.T) {
	list, err := listModels(context.Background(), sdk.ModelsCall{
		Account: protocol.Account{ID: "a", Settings: closedClaudeSettings(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if list.From != protocol.ModelListFromExtension {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
	}
	i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "claude-opus-5-5" })
	if i < 0 {
		t.Fatal("claude-opus-5-5 is not listed")
	}
	m := list.Models[i]
	if m.Created != 1790035200 || m.OwnedBy != "anthropic" || m.DisplayName != "Claude Opus 5.5" ||
		m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 ||
		!slices.Equal(m.InputModalities, []string{"text", "image"}) || !slices.Equal(m.OutputModalities, []string{"text"}) {
		t.Errorf("claude-opus-5-5 = %+v", m)
	}
}

func TestListModelsFromUpstreamPagination(t *testing.T) {
	var pageCalls atomic.Int32
	var seenQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != "sk-ant-api-live" {
			t.Errorf("X-Api-Key = %q, want sk-ant-api-live", r.Header.Get("X-Api-Key"))
		}
		if r.Header.Get("Anthropic-Version") != anthropicVersion {
			t.Errorf("Anthropic-Version = %q, want %q", r.Header.Get("Anthropic-Version"), anthropicVersion)
		}
		if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "claude") {
			t.Errorf("unexpected synthetic User-Agent %q", r.Header.Get("User-Agent"))
		}
		seenQueries = append(seenQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch pageCalls.Add(1) {
		case 1:
			if r.URL.Query().Get("after_id") != "" {
				t.Errorf("first page had after_id=%q", r.URL.Query().Get("after_id"))
			}
			if r.URL.Query().Get("limit") != "1000" {
				t.Errorf("first page limit=%q, want 1000", r.URL.Query().Get("limit"))
			}
			io.WriteString(w, `{
				"data": [
					{"type":"model","id":"claude-opus-4-6","display_name":"Claude Opus 4.6","created_at":"2026-02-04T00:00:00Z","max_input_tokens":1000000,"max_tokens":128000},
					{"type":"model","id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6","created_at":"2026-02-17T00:00:00Z","max_input_tokens":200000,"max_tokens":64000}
				],
				"has_more": true,
				"first_id": "claude-opus-4-6",
				"last_id": "claude-sonnet-4-6"
			}`)
		case 2:
			if r.URL.Query().Get("after_id") != "claude-sonnet-4-6" {
				t.Errorf("second page after_id=%q, want claude-sonnet-4-6", r.URL.Query().Get("after_id"))
			}
			if r.URL.Query().Get("limit") != "1000" {
				t.Errorf("second page limit=%q, want 1000", r.URL.Query().Get("limit"))
			}
			io.WriteString(w, `{
				"data": [
					{"type":"model","id":"claude-future-9","display_name":"Claude Future 9","created_at":"2026-11-01T12:30:00Z","max_input_tokens":500000,"max_tokens":32000}
				],
				"has_more": false,
				"first_id": "claude-future-9",
				"last_id": "claude-future-9"
			}`)
		default:
			t.Errorf("unexpected extra page request %d", pageCalls.Load())
		}
	}))
	defer srv.Close()

	settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
	list, err := listModels(context.Background(), sdk.ModelsCall{
		Account: protocol.Account{
			ID:         "live",
			Settings:   settings,
			Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-ant-api-live"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageCalls.Load() != 2 {
		t.Fatalf("page calls = %d, want 2 (queries: %v)", pageCalls.Load(), seenQueries)
	}
	if list.From != protocol.ModelListFromAccount {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromAccount)
	}
	if list.OwnedBy != "anthropic" {
		t.Errorf("owned_by = %q, want anthropic", list.OwnedBy)
	}
	if len(list.Models) != 3 {
		t.Fatalf("got %d models, want 3: %+v", len(list.Models), list.Models)
	}
	if m := list.Models[0]; m.ID != "claude-opus-4-6" || m.DisplayName != "Claude Opus 4.6" ||
		m.OwnedBy != "anthropic" || m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 ||
		m.Created != 1770163200 {
		t.Errorf("page 1 model 0 = %+v", m)
	}
	if m := list.Models[2]; m.ID != "claude-future-9" || m.DisplayName != "Claude Future 9" ||
		m.OwnedBy != "anthropic" || m.ContextLength != 500000 || m.MaxCompletionTokens != 32000 ||
		m.Created != 1793536200 {
		t.Errorf("page 2 model 2 = %+v", m)
	}
}

func TestListModelsOAuthAndSetupToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cred  *protocol.Credential
		token string
		allow bool
	}{
		{
			name:  "oauth_accepted",
			cred:  &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "oauth-acc-token"},
			token: "oauth-acc-token",
			allow: true,
		},
		{
			name:  "oauth_rejected_falls_back",
			cred:  &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "oauth-acc-token"},
			token: "oauth-acc-token",
			allow: false,
		},
		{
			name:  "setup_token_accepted",
			cred:  &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-ant-oat01-setup"},
			token: "sk-ant-oat01-setup",
			allow: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer "+tc.token {
					t.Errorf("Authorization = %q, want Bearer %s", got, tc.token)
				}
				if got := r.Header.Get("Anthropic-Beta"); !strings.Contains(got, oauthBeta) {
					t.Errorf("Anthropic-Beta = %q, want %s", got, oauthBeta)
				}
				if got := r.Header.Get("X-Api-Key"); got != "" {
					t.Errorf("unexpected X-Api-Key %q on bearer auth", got)
				}
				if !tc.allow {
					w.WriteHeader(http.StatusUnauthorized)
					io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"OAuth not allowed"}}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data":[{"type":"model","id":"claude-live-oauth","display_name":"Live OAuth","max_input_tokens":200000,"max_tokens":16384}],"has_more":false}`)
			}))
			defer srv.Close()

			settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
			list, err := listModels(context.Background(), sdk.ModelsCall{
				Account: protocol.Account{ID: "oauth", Settings: settings, Credential: tc.cred},
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.allow {
				if list.From != protocol.ModelListFromAccount {
					t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromAccount)
				}
				if len(list.Models) != 1 || list.Models[0].ID != "claude-live-oauth" {
					t.Errorf("models = %+v", list.Models)
				}
				return
			}
			if list.From != protocol.ModelListFromExtension {
				t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
			}
			if len(list.Models) != len(claudeModelIDs) {
				t.Errorf("fallback len = %d, want %d", len(list.Models), len(claudeModelIDs))
			}
		})
	}
}

func TestListModelsFallbackOnFailure(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		handler http.HandlerFunc
	}{
		{
			name: "http_401",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"type":"authentication_error","message":"bad key"}}`)
			},
		},
		{
			name: "http_500",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name: "malformed_json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data": [broken`)
			},
		},
		{
			name: "empty_list",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"data": [], "has_more": false}`)
			},
		},
		{
			name:    "timeout",
			timeout: 40 * time.Millisecond,
			handler: func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-time.After(2 * time.Second):
				}
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
				Account: protocol.Account{
					ID:         "fail",
					Settings:   settings,
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-ant-api-test"},
				},
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
			if !slices.Equal(ids, claudeModelIDs) {
				t.Errorf("models = %v, want %v", ids, claudeModelIDs)
			}
		})
	}
}
