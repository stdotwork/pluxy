package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

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

// TestListModels checks the list for each kind of account against what today's core lists in
// GET /v1/models (and /v1beta/models) for the same account with no models in its config, minus the
// gpt-image-* models.
func TestListModels(t *testing.T) {
	free := []string{"codex-auto-review", "gpt-5.6-luna", "gpt-5.6-terra", "gpt-6-luna", "gpt-6.1-terra"}
	full := []string{"codex-auto-review", "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-6-astra",
		"gpt-6-luna", "gpt-6-sol", "gpt-6.1-sol", "gpt-6.1-terra"}
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
			list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Credential: tc.cred}})
			if err != nil {
				t.Fatal(err)
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

	list, _ := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Credential: apiKey}})
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
