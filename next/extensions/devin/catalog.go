package main

import (
	"context"
	_ "embed"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

//go:embed models.json
var catalogJSON []byte

var catalog = func() []protocol.Model {
	var m []protocol.Model
	if err := json.Unmarshal(catalogJSON, &m); err != nil {
		panic("devin: models.json: " + err.Error())
	}
	return m
}()

func listModels(context.Context, sdk.ModelsCall) (*protocol.ModelList, error) {
	return &protocol.ModelList{Models: catalog, OwnedBy: "devin"}, nil
}
