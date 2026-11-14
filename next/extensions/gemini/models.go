package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// geminiModelsJSON is the model list for a Gemini API-key account, and vertexModelsJSON for a
// Vertex AI account (service account or express-mode key). Each holds the models today's core
// lists when an account's config names no models, with owned_by "google":
// core/sdk/cliproxy/service_models.go, case "gemini" takes registry.GetGeminiModels (the gemini
// section of core/internal/registry/models/models.json), and case "vertex" takes
// registry.GetGeminiVertexModels (the vertex section). To refresh the lists by hand, take those
// sections of models.json in https://github.com/router-for-me/models, the file the old core copied.
//
// Generated once from that catalog, with two differences. The imagen-* models are left out: their
// only generation method is predict, which this extension does not serve. A model the catalog
// gives no generation methods lists generateContent, as today's core shows it. Every name is
// models/<id>, as today's core shows it.
//
//go:embed models.json
var geminiModelsJSON []byte

//go:embed vertex-models.json
var vertexModelsJSON []byte

// listModels answers a models call. An account whose settings name vertex (as generate reads
// them, see readSettings) gets the Vertex list.
func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	raw := geminiModelsJSON
	if s.Vertex != nil {
		raw = vertexModelsJSON
	}
	var list protocol.ModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	return &list, nil
}
