package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// TestListModels checks the list against what today's core lists in GET /v1/models and
// /v1beta/models for a Devin account.
func TestListModels(t *testing.T) {
	want := []string{"devin/claude-5-fable", "devin/claude-fable-5-1", "devin/claude-haiku-5-5", "devin/claude-opus-4-6",
		"devin/claude-opus-4-6-1m", "devin/claude-opus-4-7", "devin/claude-opus-4-8", "devin/claude-opus-5",
		"devin/claude-opus-5-5", "devin/claude-sonnet-4-6", "devin/claude-sonnet-4-6-1m", "devin/claude-sonnet-5",
		"devin/claude-sonnet-5-5", "devin/deepseek-v4-1-flash-1017", "devin/deepseek-v4-flash", "devin/deepseek-v4-pro",
		"devin/gemini-3-5-flash", "devin/gemini-3-6-flash", "devin/gemini-3-7-flash", "devin/gemini-3-8-flash",
		"devin/gemini-4-argon", "devin/glm-5-2", "devin/glm-5-2-1m", "devin/glm-5-3", "devin/glm-5-3-flash",
		"devin/gpt-5-4", "devin/gpt-5-4-mini", "devin/gpt-5-5", "devin/gpt-5-6-luna", "devin/gpt-5-6-sol",
		"devin/gpt-5-6-terra", "devin/gpt-6-1-sol", "devin/gpt-6-astra", "devin/gpt-6-luna", "devin/gpt-6-sol",
		"devin/grok-4-6", "devin/grok-4-7", "devin/kimi-k2-6", "devin/kimi-k2-7", "devin/kimi-k3",
		"devin/muse-wonder-1-0", "devin/nemotron-3-ultra", "devin/swe-1-6", "devin/swe-1-6-slow", "devin/swe-1-7",
		"devin/swe-2", "devin/swe-2-lightning"}
	// A session token is held as an API key (a login or the config), and an OAuth credential
	// would be treated the same: core lists the same models for every Devin account.
	for _, cred := range []*protocol.Credential{
		{Kind: protocol.CredentialAPIKey, APIKey: "devin-session-token$x"},
		{Kind: protocol.CredentialOAuth, AccessToken: "t"},
	} {
		list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Credential: cred}})
		if err != nil {
			t.Fatal(err)
		}
		if list.OwnedBy != "devin" {
			t.Errorf("owned_by %q, want devin", list.OwnedBy)
		}
		var ids []string
		for _, m := range list.Models {
			ids = append(ids, m.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, want) {
			t.Errorf("models %v, want %v", ids, want)
		}
		for _, m := range list.Models {
			switch m.ID {
			case "devin/claude-fable-5-1":
				if !reflect.DeepEqual(m, protocol.Model{
					ID: "devin/claude-fable-5-1", DisplayName: "Claude Fable 5.1", OwnedBy: "anthropic",
					ContextLength: 1000000, MaxCompletionTokens: 64000, InputTokenLimit: 1000000, OutputTokenLimit: 64000,
					GenerationMethods: []string{"generateContent", "countTokens"},
					InputModalities:   []string{"text", "image"}, OutputModalities: []string{"text"},
				}) {
					t.Errorf("devin/claude-fable-5-1 is %+v", m)
				}
			case "devin/swe-1-6-slow":
				if m.DisplayName != "SWE-1.6 Slow" || m.OwnedBy != "cognition" || m.ContextLength != 200000 || m.InputTokenLimit != 200000 {
					t.Errorf("devin/swe-1-6-slow is %+v", m)
				}
			}
		}
	}
}
