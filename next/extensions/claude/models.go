package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"
	"sync"

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

// limits maps each model in modelsJSON to its output limit (max_completion_tokens).
var limits = sync.OnceValue(func() map[string]int {
	var list struct {
		Models []struct {
			ID                  string `json:"id"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
		} `json:"models"`
	}
	out := map[string]int{}
	if json.Unmarshal(modelsJSON, &list) != nil {
		return out
	}
	for _, m := range list.Models {
		if m.MaxCompletionTokens > 0 {
			out[m.ID] = m.MaxCompletionTokens
		}
	}
	return out
})

// dated is the snapshot date at the end of a Claude model id (claude-sonnet-4-5-20250929).
var dated = regexp.MustCompile(`-\d{8}$`)

// maxOutputTokens returns a model's output limit from the model list, the default max_tokens for a
// request that names none, as LiteLLM and CLIProxyAPI take theirs from their model catalogs. The
// name is read lowercase, without a context window in brackets ([1m]), a Vertex AI version after @
// or a Bedrock prefix up to "anthropic.", and matches a listed id with or without its snapshot
// date. ok is false for a model the list does not have.
func maxOutputTokens(model string) (n int, ok bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	name, _, _ = strings.Cut(name, "[")
	name, _, _ = strings.Cut(name, "@")
	if _, rest, found := strings.Cut(name, "anthropic."); found {
		name = rest
	}
	l := limits()
	if n, ok := l[name]; ok {
		return n, true
	}
	name = dated.ReplaceAllString(name, "")
	for id, n := range l {
		if dated.ReplaceAllString(id, "") == name {
			return n, true
		}
	}
	return 0, false
}
