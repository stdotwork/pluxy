package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// geminiAPIKeyModelIDs is the list today's core returns from GET /v1/models for a Gemini API-key
// account when the account names no models (14 models).
var geminiAPIKeyModelIDs = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash-preview",
	"gemini-3-pro-image-preview",
	"gemini-3-pro-preview",
	"gemini-3.1-flash-image-preview",
	"gemini-3.1-flash-lite-preview",
	"gemini-3.1-pro-preview",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.8-flash",
	"gemini-4-argon",
}

// vertexModelIDs is the list today's core returns for a Vertex AI account (express-mode key or
// service account) when the account names no models: 21 models, less the 5 imagen-* models that
// only have the predict method.
var vertexModelIDs = []string{
	"gemini-2.5-flash",
	"gemini-2.5-flash-image",
	"gemini-2.5-flash-lite",
	"gemini-2.5-pro",
	"gemini-3-flash",
	"gemini-3-pro",
	"gemini-3-pro-image",
	"gemini-3.1-flash-image",
	"gemini-3.1-flash-lite",
	"gemini-3.1-pro",
	"gemini-3.1-pro-preview",
	"gemini-3.5-flash",
	"gemini-3.5-flash-lite",
	"gemini-3.6-flash",
	"gemini-3.8-flash",
	"gemini-4-argon",
}

func listedIDs(t *testing.T, settings string) ([]string, *protocol.ModelList) {
	t.Helper()
	call := sdk.ModelsCall{Account: protocol.Account{ID: "a", Settings: json.RawMessage(settings),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}}}
	list, err := listModels(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list.Models {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids, list
}

func TestListModelsGeminiAPIKey(t *testing.T) {
	ids, list := listedIDs(t, "")
	if !slices.Equal(ids, geminiAPIKeyModelIDs) {
		t.Errorf("models = %v, want %v", ids, geminiAPIKeyModelIDs)
	}
	if list.OwnedBy != "google" {
		t.Errorf("owned_by = %q, want google", list.OwnedBy)
	}
	i := slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "gemini-2.5-pro" })
	if i < 0 {
		t.Fatal("gemini-2.5-pro is not listed")
	}
	m := list.Models[i]
	if m.Name != "models/gemini-2.5-pro" || m.DisplayName != "Gemini 2.5 Pro" || m.Version != "2.5" || m.Created != 1750118400 ||
		m.InputTokenLimit != 1048576 || m.OutputTokenLimit != 65536 || m.OwnedBy != "google" ||
		!slices.Equal(m.GenerationMethods, []string{"generateContent", "countTokens", "createCachedContent", "batchGenerateContent"}) ||
		!slices.Equal(m.InputModalities, []string{"text", "image", "audio", "video"}) || !slices.Equal(m.OutputModalities, []string{"text"}) {
		t.Errorf("gemini-2.5-pro = %+v", m)
	}
	// A catalog entry with no generation methods lists generateContent, as today's core shows.
	i = slices.IndexFunc(list.Models, func(m protocol.Model) bool { return m.ID == "gemini-3.8-flash" })
	if i < 0 {
		t.Fatal("gemini-3.8-flash is not listed")
	}
	if m := list.Models[i]; m.Name != "models/gemini-3.8-flash" || !slices.Equal(m.GenerationMethods, []string{"generateContent"}) || m.ContextLength != 1048576 {
		t.Errorf("gemini-3.8-flash = %+v", m)
	}
}

func TestListModelsVertex(t *testing.T) {
	// An express-mode key and a service account both have the vertex settings.
	for _, settings := range []string{`{"vertex":{"location":"global"}}`, `{"vertex":{"project":"p","location":"us-central1"}}`} {
		ids, list := listedIDs(t, settings)
		if !slices.Equal(ids, vertexModelIDs) {
			t.Errorf("settings %s: models = %v, want %v", settings, ids, vertexModelIDs)
		}
		if list.OwnedBy != "google" {
			t.Errorf("settings %s: owned_by = %q, want google", settings, list.OwnedBy)
		}
	}
}

func TestListModelsVertexImagenLeftOut(t *testing.T) {
	ids, _ := listedIDs(t, `{"vertex":{"location":"global"}}`)
	for _, id := range ids {
		if strings.HasPrefix(id, "imagen") {
			t.Errorf("imagen model %s is listed; its only method is predict", id)
		}
	}
}
