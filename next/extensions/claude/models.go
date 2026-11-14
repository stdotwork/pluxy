package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsJSON is the model list for every Claude account, with an API key or a subscription login.
// It holds the models today's core lists for the "claude" provider when an account's config
// names no models: core/sdk/cliproxy/service_models.go, case "claude", takes
// registry.GetClaudeModels for both kinds (the claude section of
// core/internal/registry/models/models.json) and owned_by "anthropic". Generated once from that
// catalog, without the fields protocol.Model does not carry. To refresh it by hand, take the claude
// section of models.json in https://github.com/router-for-me/models, the file the old core copied.
//
//go:embed models.json
var modelsJSON []byte

// listModels answers a models call. The list does not depend on the account's credential.
func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(modelsJSON, &list); err != nil {
		return nil, err
	}
	return &list, nil
}
