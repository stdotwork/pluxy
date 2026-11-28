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
	for _, native := range []string{llm.FormatGeminiGenerate, llm.FormatOpenAIChat} {
		if e := count(call("messages", native)); !slices.Equal(e.Dropped, []string{"response_format"}) {
			t.Errorf("Messages account, %s request: error %+v, want dropped [response_format]", native, e)
		}
	}
	for _, c := range []struct{ wire, native string }{
		{"chat", llm.FormatGeminiGenerate},
		{"responses", llm.FormatGeminiGenerate},
	} {
		if e := count(call(c.wire, c.native)); e.Dropped != nil {
			t.Errorf("%s account, %s request: error %+v, want nothing dropped", c.wire, c.native, e)
		}
	}
}
