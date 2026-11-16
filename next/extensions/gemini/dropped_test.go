package main

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A request refused under the reject drop policy names what was dropped in the error's Dropped,
// so core does not take the refusal for an account that cannot count; an unsupported answer that
// is not a drop refusal names nothing.
func TestDropRefusalNamesDropped(t *testing.T) {
	n := 32000
	_, _, err := buildBody(llm.Request{
		Model:      "gemini-x",
		Items:      []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options:    llm.Options{MaxOutputTokens: &n},
		Native:     &llm.Native{Format: llm.FormatAnthropicMessages, Body: json.RawMessage(`{}`)},
		DropPolicy: llm.DropReject,
	})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !slices.Equal(e.Dropped, []string{"max_tokens"}) {
		t.Fatalf("err = %+v, want unsupported with dropped [max_tokens]", err)
	}
	if e := refusedCall(t, endpointCall(`{}`, llm.FormatOpenAIChat, `{}`)); e.Code != llm.CodeUnsupported || e.Dropped != nil {
		t.Errorf("endpoint refusal = %+v, want unsupported naming nothing dropped", e)
	}
}
