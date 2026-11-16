package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A count refused under the reject drop policy names what was dropped in the error's Dropped,
// before anything is sent, so core does not take the refusal for an account that cannot count; an
// unsupported answer that is not a drop refusal names nothing.
func TestCountTokensDropRefusalNamesDropped(t *testing.T) {
	n := 32000
	call := sdk.Call{
		Account: protocol.Account{ID: "g", Settings: json.RawMessage(`{"base_url":"http://127.0.0.1:1"}`), Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "gk-test"}},
		Request: llm.Request{
			Model:      "gemini-x",
			Items:      []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
			Options:    llm.Options{MaxOutputTokens: &n},
			Native:     &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(`{}`)},
			DropPolicy: llm.DropReject,
		},
	}
	err := countTokens(context.Background(), call, nil)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !slices.Equal(e.Dropped, []string{"max_tokens"}) {
		t.Fatalf("err = %+v, want unsupported with dropped [max_tokens]", err)
	}
	if e := refusedCall(t, endpointCall(`{}`, llm.FormatOpenAIChat, `{}`)); e.Code != llm.CodeUnsupported || e.Dropped != nil {
		t.Errorf("endpoint refusal = %+v, want unsupported naming nothing dropped", e)
	}
}
