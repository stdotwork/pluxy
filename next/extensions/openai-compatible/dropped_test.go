package main

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A request refused under the reject drop policy names what was dropped in the error's Dropped,
// so core does not take the refusal for an account that cannot count; the refusal of a request
// this account does not translate names nothing.
func TestDropRefusalNamesDropped(t *testing.T) {
	req := llm.Request{
		Model:      "m",
		Items:      []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Options:    llm.Options{ResponseFormat: &llm.ResponseFormat{Type: llm.ResponseFormatJSONObject}},
		Native:     &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{}`)},
		DropPolicy: llm.DropReject,
	}
	_, _, err := buildBody(req, llm.FormatAnthropicMessages)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !slices.Equal(e.Dropped, []string{"response_format"}) {
		t.Fatalf("err = %+v, want unsupported with dropped [response_format]", err)
	}
	req.Native = &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)}
	_, _, err = buildBody(req, llm.FormatAnthropicMessages)
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Dropped != nil {
		t.Errorf("err = %+v, want unsupported naming nothing dropped", err)
	}
}
