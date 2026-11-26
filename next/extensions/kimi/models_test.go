package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The models a Kimi Code login and a Kimi Code API key serve are the models of today's core's Kimi
// list (core/internal/registry/models/models.json, the old core's copy of models.json in
// https://github.com/router-for-me/models). Checked against today's core with /v1/models.
func TestModelsForEachAccountKind(t *testing.T) {
	want := []string{
		"kimi-k2", "kimi-k2-thinking", "kimi-k2.5", "kimi-k2.6", "kimi-k2.7-code", "kimi-k2.7-code-highspeed",
		"kimi-k2.8", "kimi-k2.8-code", "kimi-k3", "kimi-k3-256k", "kimi-k3.1-pro", "kimi-k3.1-pro-highspeed",
	}
	k25 := protocol.Model{
		ID: "kimi-k2.5", DisplayName: "Kimi K2.5",
		Description: "Kimi K2.5 - Native multimodal agentic model with text, image, and video input; supports thinking and non-thinking modes",
		Created:     1769472000, OwnedBy: "moonshot", ContextLength: 262144, MaxCompletionTokens: 32768,
		InputModalities: []string{"text", "image", "video"}, OutputModalities: []string{"text"},
	}
	for _, tc := range []struct {
		name    string
		account protocol.Account
	}{
		{"api key", protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}},
		{"login", protocol.Account{ID: "b", Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "t"}}},
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
			if list.OwnedBy != "moonshot" {
				t.Errorf("owned_by = %q, want moonshot", list.OwnedBy)
			}
			i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "kimi-k2.5" })
			if i < 0 || !reflect.DeepEqual(list.Models[i], k25) {
				t.Errorf("kimi-k2.5 = %+v, want %+v", list.Models[i], k25)
			}
		})
	}
}
