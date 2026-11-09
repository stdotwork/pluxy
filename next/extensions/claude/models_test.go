package main

import (
	"context"
	"slices"
	"testing"

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

func TestListModelsEveryAccountKind(t *testing.T) {
	kinds := map[string]*protocol.Credential{
		"api_key": {Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
		"oauth":   {Kind: protocol.CredentialOAuth, AccessToken: "token-test"},
	}
	for name, cred := range kinds {
		t.Run(name, func(t *testing.T) {
			list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{ID: "a", Credential: cred}})
			if err != nil {
				t.Fatal(err)
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
	list, err := listModels(context.Background(), sdk.ModelsCall{})
	if err != nil {
		t.Fatal(err)
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
