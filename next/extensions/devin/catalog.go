package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// catalogJSON is the model list of every Devin account, as today's core lists it in
// GET /v1/models: the models of core/internal/registry/models/devin_models.json and the SWE-1.6
// Slow model core adds (core/internal/registry/devin_models.go), each named devin/<model> as in
// today's core. The extension takes the name with or without the devin/ prefix (modelID). A model
// has the token limits and generation methods core fills in: the input limit is the context
// length, the output limit the longest reply, and the methods generateContent and countTokens.
//
//go:embed models.json
var catalogJSON []byte

var catalog = func() []protocol.Model {
	var m []protocol.Model
	if err := json.Unmarshal(catalogJSON, &m); err != nil {
		panic("devin: models.json: " + err.Error())
	}
	return m
}()

// listModels lists the models of an account. Today's core lists the same models for every Devin
// account (core/sdk/cliproxy/service_models.go, case "devin"). Its Devin accounts have no models
// of their own in the config, so the owned_by of the account's configured models is the provider's.
func listModels(context.Context, sdk.ModelsCall) (*protocol.ModelList, error) {
	return &protocol.ModelList{Models: catalog, OwnedBy: "devin"}, nil
}
