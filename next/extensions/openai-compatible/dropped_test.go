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
// before anything is sent, so core does not take the refusal for an account that cannot count. An
// account whose upstream has no count endpoint (Chat Completions, Responses) answers unsupported
// naming nothing, so core estimates; so does the refusal of a request this account does not
// translate.
func TestCountTokensDropRefusalNamesDropped(t *testing.T) {
	call := func(wire string, native string) sdk.Call {
		return sdk.Call{
			Account: protocol.Account{ID: "o", Settings: json.RawMessage(`{"base_url":"http://127.0.0.1:1/v1","wire_api":"` + wire + `"}`),
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"}},
			Request: llm.Request{
				Model:      "m",
				Items:      []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
				Options:    llm.Options{ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSONObject}},
				Native:     &llm.Native{Format: native, Body: json.RawMessage(`{}`)},
				DropPolicy: llm.DropReject,
			},
		}
	}
	count := func(c sdk.Call) *llm.Error {
		t.Helper()
		var e *llm.Error
		if err := countTokens(context.Background(), c, nil); !errors.As(err, &e) || e.Code != llm.CodeUnsupported {
			t.Fatalf("err = %v, want unsupported", err)
		}
		return e
	}
	if e := count(call("messages", llm.FormatGeminiGenerate)); !slices.Equal(e.Dropped, []string{"response_format"}) {
		t.Errorf("Messages account: error %+v, want dropped [response_format]", e)
	}
	for _, c := range []struct{ wire, native string }{
		{"chat", llm.FormatGeminiGenerate},
		{"responses", llm.FormatGeminiGenerate},
		{"messages", llm.FormatOpenAIChat},
	} {
		if e := count(call(c.wire, c.native)); e.Dropped != nil {
			t.Errorf("%s account, %s request: error %+v, want nothing dropped", c.wire, c.native, e)
		}
	}
}
