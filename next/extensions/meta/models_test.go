package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The models a Muse Code login and a Meta API key serve are the models of today's core's Meta list
// (core/internal/registry/models/models.json). Checked against today's core with /v1/models.
func TestModelsForEachAccountKind(t *testing.T) {
	want := []string{
		"muse-spark-1.1", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.3",
		"muse-spark-1.3-contributor", "muse-wonder-1.0", "muse-wonder-1.0-contributor",
		"muse-wonder-full-1.0", "muse-wonder-full-1.0-contributor",
	}
	spark13 := protocol.Model{
		ID: "muse-spark-1.3", Name: "muse-spark-1.3", DisplayName: "Muse Spark 1.3",
		Description: "Meta Muse Spark 1.3 flagship reasoning and agentic coding model",
		Created:     1788307200, OwnedBy: "meta", ContextLength: 1048576, MaxCompletionTokens: 65536,
		InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"},
	}
	for _, tc := range []struct {
		name    string
		account protocol.Account
	}{
		{"api key", protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}},
		{"muse code login", protocol.Account{ID: "b", Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "t", RefreshToken: "r"}}},
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
			if list.OwnedBy != "meta" {
				t.Errorf("owned_by = %q, want meta", list.OwnedBy)
			}
			i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "muse-spark-1.3" })
			if i < 0 || !reflect.DeepEqual(list.Models[i], spark13) {
				t.Errorf("muse-spark-1.3 = %+v, want %+v", list.Models[i], spark13)
			}
		})
	}
}
