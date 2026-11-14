package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsJSON is the list a Muse Code login or a Meta API key serves, in protocol.ModelList form:
// the models of today's core's catalog (core/internal/registry/models/models.json, "meta"), with
// their metadata. Every one of them is a text model, which the Responses requests serve. To
// refresh it by hand, take the meta section of models.json in https://github.com/router-for-me/models, the file the old
// core copied.
//
//go:embed models.json
var modelsJSON []byte

// models lists the models an account serves when its configuration names none. Today's core lists
// the same models for a login and for an API key (core/sdk/cliproxy/service_models.go, case
// "meta"), and gives configured models owned_by meta, as the catalog does.
func models(_ context.Context, _ sdk.ModelsCall) (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(modelsJSON, &list); err != nil {
		return nil, err
	}
	return &list, nil
}
