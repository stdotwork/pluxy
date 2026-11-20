package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func idTokenWithPlan(plan string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct", "chatgpt_plan_type": plan},
	}) + ".x"
}

func subscriptionWith(idToken string) *protocol.Credential {
	data, _ := json.Marshal(credentialData{IDToken: idToken})
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", Data: data}
}

func closedCodexSettings(t *testing.T) json.RawMessage {
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

// TestListModels checks the built-in fallback list for each kind of account against what today's
// core lists in GET /v1/models (and /v1beta/models) for the same account with no models in its
// config, its gpt-image-* models included.
func TestListModels(t *testing.T) {
	closedSettings := closedCodexSettings(t)
	images := []string{"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst"}
	free := append([]string{"codex-auto-review", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-6-luna", "gpt-6.1-terra"}, images...)
	full := append([]string{"codex-auto-review", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra",
		"gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"}, images...)
	apiKey := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"}
	for _, tc := range []struct {
		name    string
		cred    *protocol.Credential
		ids     []string
		context int // gpt-5.6-luna's context window, which differs between plans
	}{
		{"api key", apiKey, full, 921000},
		{"free", subscriptionWith(idTokenWithPlan("free")), free, 372000},
		{"plus", subscriptionWith(idTokenWithPlan("plus")), full, 372000},
		{"team", subscriptionWith(idTokenWithPlan("team")), full, 372000},
		{"business", subscriptionWith(idTokenWithPlan("business")), full, 372000},
		{"go", subscriptionWith(idTokenWithPlan("go")), full, 372000},
		{"pro", subscriptionWith(idTokenWithPlan("pro")), full, 921000},
		{"other plan", subscriptionWith(idTokenWithPlan("enterprise")), full, 921000},
		{"no plan in the ID token", subscriptionWith(idTokenWithPlan("")), free, 372000},
		{"ID token that does not parse", subscriptionWith("not-a-jwt"), free, 372000},
		{"no ID token", subscriptionWith(""), full, 921000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Settings: closedSettings, Credential: tc.cred}})
			if err != nil {
				t.Fatal(err)
			}
			if list.From != protocol.ModelListFromExtension {
				t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
			}
			if list.OwnedBy != "openai" {
				t.Errorf("owned_by %q, want openai", list.OwnedBy)
			}
			var ids []string
			for _, m := range list.Models {
				ids = append(ids, m.ID)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, tc.ids) {
				t.Errorf("models %v, want %v", ids, tc.ids)
			}
			for _, m := range list.Models {
				if m.ID == "gpt-5.6-luna" && m.ContextLength != tc.context {
					t.Errorf("gpt-5.6-luna context_length %d, want %d", m.ContextLength, tc.context)
				}
			}
		})
	}

	list, _ := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Settings: closedSettings, Credential: apiKey}})
	want := protocol.Model{
		ID: "gpt-6.1-terra", DisplayName: "GPT 6.1 Terra", Version: "gpt-6.0", Created: 1792972800, OwnedBy: "openai",
		Description:   "A fast, efficient model for visual reasoning, precise computer use, and clear, natural writing.",
		ContextLength: 500000, MaxCompletionTokens: 128000,
		InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"},
	}
	for _, m := range list.Models {
		if m.ID == want.ID {
			if !reflect.DeepEqual(m, want) {
				t.Errorf("gpt-6.1-terra is %+v, want %+v", m, want)
			}
			return
		}
	}
	t.Error("gpt-6.1-terra is not listed")
}

func TestListModelsSubscriptionLive(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/codex/models" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		if got := r.URL.Query().Get("client_version"); got != "0.159.0" {
			t.Errorf("client_version = %q, want 0.159.0", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer live-tok" {
			t.Errorf("Authorization = %q, want Bearer live-tok", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "acct" {
			t.Errorf("Chatgpt-Account-Id = %q, want acct", got)
		}
		if got := r.Header.Get("Originator"); got != codexOriginator {
			t.Errorf("Originator = %q, want %q", got, codexOriginator)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "codex-tui/") {
			t.Errorf("User-Agent = %q, want codex-tui/...", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"models": [
				{
					"slug": "gpt-6.1-sol",
					"display_name": "GPT-6.1-Sol",
					"description": "Latest workhorse model",
					"context_window": 500000,
					"input_modalities": ["text", "image"],
					"available_in_plans": ["plus", "pro", "team"]
				},
				{
					"slug": "codex-live-preview",
					"display_name": "Codex Live Preview",
					"description": "Brand new preview model",
					"context_window": 272000,
					"input_modalities": ["text"],
					"available_in_plans": ["plus", "pro"]
				},
				{
					"slug": "codex-pro-only",
					"display_name": "Codex Pro Only",
					"context_window": 1000000,
					"input_modalities": ["text"],
					"available_in_plans": ["pro"]
				}
			]
		}`)
	}))
	defer srv.Close()

	settings, _ := json.Marshal(map[string]string{"base_url": srv.URL + "/backend-api/codex"})
	cred := subscriptionWith(idTokenWithPlan("plus"))
	cred.AccessToken = "live-tok"
	list, err := listModels(context.Background(), sdk.ModelsCall{
		Account: protocol.Account{ID: "sub", Settings: settings, Credential: cred},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if list.From != protocol.ModelListFromAccount {
		t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromAccount)
	}
	if list.OwnedBy != "openai" {
		t.Errorf("owned_by = %q, want openai", list.OwnedBy)
	}

	byID := map[string]protocol.Model{}
	for _, m := range list.Models {
		byID[m.ID] = m
	}
	if _, ok := byID["codex-pro-only"]; ok {
		t.Errorf("codex-pro-only included for plus plan")
	}
	m, ok := byID["codex-live-preview"]
	if !ok {
		t.Fatalf("codex-live-preview missing from %v", list.Models)
	}
	if m.DisplayName != "Codex Live Preview" || m.Description != "Brand new preview model" ||
		m.ContextLength != 272000 || m.OwnedBy != "openai" || !slices.Equal(m.InputModalities, []string{"text"}) {
		t.Errorf("codex-live-preview = %+v", m)
	}
	for _, im := range imageModels {
		if _, ok := byID[im.ID]; !ok {
			t.Errorf("missing image model %s", im.ID)
		}
	}
}

func TestListModelsFallbackOnFailure(t *testing.T) {
	plusIDs := append([]string{"codex-auto-review", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra",
		"gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"},
		"gpt-image-1.5", "gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst")

	cases := []struct {
		name    string
		timeout time.Duration
		handler http.HandlerFunc
	}{
		{
			name: "http_401",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
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
				io.WriteString(w, `{"models": [broken`)
			},
		},
		{
			name: "empty_list",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"models": []}`)
			},
		},
		{
			name: "all_filtered_by_plan",
			handler: func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"models": [{"slug":"pro-only","available_in_plans":["pro"]}]}`)
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
					Credential: subscriptionWith(idTokenWithPlan("plus")),
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
			if !slices.Equal(ids, plusIDs) {
				t.Errorf("models = %v, want %v", ids, plusIDs)
			}
		})
	}

	t.Run("api_key_skips_upstream", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		settings, _ := json.Marshal(map[string]string{"base_url": srv.URL})
		list, err := listModels(context.Background(), sdk.ModelsCall{
			Account: protocol.Account{
				ID:         "apikey",
				Settings:   settings,
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if hits.Load() != 0 {
			t.Errorf("api key hit upstream %d times, want 0", hits.Load())
		}
		if list.From != protocol.ModelListFromExtension {
			t.Errorf("from = %q, want %q", list.From, protocol.ModelListFromExtension)
		}
	})
}
