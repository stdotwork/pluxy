package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func TestListModels(t *testing.T) {
	var (
		gotAuth       string
		gotAPIKey     string
		gotGoogAPIKey string
		status        = http.StatusOK
		body          = `{"object":"list","data":[{"id":"gpt-a","created":10,"owned_by":"org"},{"id":""},{"id":"gpt-a","owned_by":"dup"},{"id":"gpt-b","display_name":"GPT B"}]}`
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotGoogAPIKey = r.Header.Get("X-Goog-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer srv.Close()

	callFor := func(wire string) sdk.ModelsCall {
		s, _ := json.Marshal(map[string]string{"base_url": srv.URL + "/v1", "wire_api": wire})
		return sdk.ModelsCall{
			ID: "1",
			Account: protocol.Account{
				ID:         "acct",
				Settings:   s,
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"},
			},
		}
	}

	list, err := listModels(context.Background(), callFor("chat"))
	if err != nil {
		t.Fatalf("listModels chat: %v", err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	want := []protocol.Model{
		{ID: "gpt-a", Created: 10, OwnedBy: "org"},
		{ID: "gpt-b", DisplayName: "GPT B"},
	}
	if !slices.EqualFunc(list.Models, want, func(a, b protocol.Model) bool {
		return a.ID == b.ID && a.Created == b.Created && a.OwnedBy == b.OwnedBy && a.DisplayName == b.DisplayName
	}) {
		t.Errorf("models = %+v, want %+v", list.Models, want)
	}

	// Messages and Gemini wires send the key in their headers.
	if _, err := listModels(context.Background(), callFor("messages")); err != nil || gotAPIKey != "sk-test" {
		t.Errorf("messages wire: err=%v x-api-key=%q", err, gotAPIKey)
	}
	if _, err := listModels(context.Background(), callFor("gemini")); err != nil || gotGoogAPIKey != "sk-test" {
		t.Errorf("gemini wire: err=%v x-goog-api-key=%q", err, gotGoogAPIKey)
	}

	// Empty data array returns an empty list without error.
	body = `{"object":"list","data":[]}`
	if list, err := listModels(context.Background(), callFor("chat")); err != nil || len(list.Models) != 0 {
		t.Errorf("empty list: %+v, %v", list, err)
	}

	// Gemini's list shape, which a wire_api "gemini" account answers with: the model's resource
	// name is its id without "models/", and its other fields have the model's own places.
	body = `{"models":[{"name":"models/gemini-a","displayName":"Gemini A","description":"A test model","version":"001","inputTokenLimit":1000,"outputTokenLimit":100,"supportedGenerationMethods":["generateContent"]},{"name":"models/gemini-a"},{"name":"gemini-b"}]}`
	gemini, err := listModels(context.Background(), callFor("gemini"))
	if err != nil {
		t.Fatalf("listModels gemini list: %v", err)
	}
	wantGemini := []protocol.Model{
		{ID: "gemini-a", Name: "models/gemini-a", DisplayName: "Gemini A", Description: "A test model", Version: "001",
			InputTokenLimit: 1000, OutputTokenLimit: 100, GenerationMethods: []string{"generateContent"}},
		{ID: "gemini-b"}, // a name without the "models/" prefix leaves Name at its default, the id
	}
	if !reflect.DeepEqual(gemini.Models, wantGemini) {
		t.Errorf("gemini models = %+v, want %+v", gemini.Models, wantGemini)
	}

	// Non-list JSON or HTTP errors fail with *llm.Error.
	body = `{"object":"list"}`
	if _, err := listModels(context.Background(), callFor("chat")); err == nil {
		t.Error("expected error when neither list shape is there")
	}
	status = http.StatusInternalServerError
	body = `{"error":{"message":"server down"}}`
	if _, err := listModels(context.Background(), callFor("chat")); err == nil {
		t.Error("expected error on 500")
	} else if le, ok := err.(*llm.Error); !ok || le.Code != llm.CodeUpstream {
		t.Errorf("500 err = %+v, want CodeUpstream", err)
	}
}
