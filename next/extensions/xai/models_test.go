package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The models an API key and a Grok login serve are the text models of today's core's xAI list
// (core/internal/registry/models/models.json, with the xAI builtins of core/internal/registry), less
// its image, video and speech models. Checked against today's core with /v1/models.
func TestModelsForEachAccountKind(t *testing.T) {
	want := []string{
		"grok-3-mini", "grok-3-mini-fast", "grok-4.20-0309-non-reasoning", "grok-4.20-0309-reasoning",
		"grok-4.20-multi-agent-0309", "grok-4.3", "grok-4.5", "grok-4.6", "grok-4.7", "grok-4.7-build-fast",
		"grok-build-0.1", "grok-composer-2.5-fast",
	}
	grok47 := protocol.Model{
		ID: "grok-4.7", Name: "grok-4.7", DisplayName: "Grok 4.7",
		Description: "SpaceXAI's frontier model for coding, agentic tasks, and knowledge work.",
		Created:     1789948800, OwnedBy: "xai", ContextLength: 500000, MaxCompletionTokens: 500000,
		InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"},
	}
	for _, tc := range []struct {
		name    string
		account protocol.Account
	}{
		{"api key", protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}},
		{"grok login", protocol.Account{ID: "b", Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "t"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := models(context.Background(), sdk.ModelsCall{ID: "call", Account: tc.account})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, m := range list.Models {
				ids = append(ids, m.ID)
			}
			slices.Sort(ids)
			if !reflect.DeepEqual(ids, want) {
				t.Errorf("models:\n got %v\nwant %v", ids, want)
			}
			if list.OwnedBy != "xai" {
				t.Errorf("owned_by = %q, want xai", list.OwnedBy)
			}
			i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "grok-4.7" })
			if i < 0 || !reflect.DeepEqual(list.Models[i], grok47) {
				t.Errorf("grok-4.7 = %+v, want %+v", list.Models[i], grok47)
			}
		})
	}
}
