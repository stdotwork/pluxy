package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsOwner is the owned_by of models an account's config lists itself, as in today's core.
const modelsOwner = "openai"

// modelsJSON holds the model list of each ChatGPT plan, copied from the codex-free, codex-team,
// codex-plus and codex-pro lists of today's core (core/internal/registry/models/models.json; to
// refresh them by hand, take those sections of models.json in https://github.com/router-for-me/models, the file the
// old core copied).
// Every list ends with the image models (imageModels), as today's core adds its gpt-image-*
// models to every list (WithCodexBuiltins in core/internal/registry/model_definitions.go): they
// serve the Images API.
//
//go:embed models.json
var modelsJSON []byte

var planModels = func() map[string][]protocol.Model {
	var m map[string][]protocol.Model
	if err := json.Unmarshal(modelsJSON, &m); err != nil {
		panic("codex: models.json: " + err.Error())
	}
	for plan, list := range m {
		m[plan] = append(list, imageModels...)
	}
	return m
}()

// listModels lists the models an account serves when its config lists none, following
// core/sdk/cliproxy/service_models.go: an API key gets the pro list, and a subscription gets the
// list of its plan (team, business and go share the team list; any other plan gets the pro list).
// The plan is read from the ID token's chatgpt_plan_type as core/internal/watcher/synthesizer/file.go
// does: an ID token that does not parse or has no plan means free, and a credential with no ID
// token has no plan, so it gets the pro list. A SIWC account gets its plan's list without the
// image models, since it does not serve the Images API.
func listModels(_ context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	plan := "pro"
	if c := call.Account.Credential; c != nil && c.Kind == protocol.CredentialOAuth {
		var d credentialData
		if len(c.Data) > 0 {
			_ = json.Unmarshal(c.Data, &d)
		}
		if d.IDToken != "" {
			claims, _ := parseIDToken(d.IDToken)
			switch p := strings.ToLower(strings.TrimSpace(claims.planType)); p {
			case "", "free":
				plan = "free"
			case "plus", "team":
				plan = p
			case "business", "go":
				plan = "team"
			}
		}
	}
	models := planModels[plan]
	if isSIWC(call.Account.Credential) {
		models = slices.DeleteFunc(slices.Clone(models), func(m protocol.Model) bool { return imageModel(m.ID) != "" })
	}
	return &protocol.ModelList{Models: models, OwnedBy: modelsOwner}, nil
}
