package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsJSON is the list a Kimi Code login or API key serves, in protocol.ModelList form: the
// models of today's core's catalog (core/internal/registry/models/models.json, "kimi"), with their
// metadata.
//
//go:embed models.json
var modelsJSON []byte

// models lists the models an account serves when its configuration names none. Today's core lists
// the same models for every Kimi account, whatever its kind (core/sdk/cliproxy/service_models.go,
// case "kimi"), and it has no configured models for Kimi, so owned_by is the catalog's moonshot.
func models(_ context.Context, _ sdk.ModelsCall) (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(modelsJSON, &list); err != nil {
		return nil, err
	}
	return &list, nil
}
