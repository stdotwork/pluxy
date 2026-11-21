package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	// modelsOwner is the owned_by of models an account's config lists itself, as in today's core.
	modelsOwner = "openai"
	// codexClientVersion is the default Codex CLI version in the User-Agent and the ChatGPT
	// backend model list's client_version; older or missing versions omit newer models.
	codexClientVersion = "0.159.0"
	// modelsTimeout stays under core's 15 s model-list read, so a hung provider still yields the built-in list.
	modelsTimeout = 10 * time.Second
	modelsMaxBody = 8 << 20
)

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

// listModels lists the models an account serves when its config lists none. A ChatGPT
// subscription reads its backend's /models catalog with the account's credential; on failure or
// empty results, and for API-key and SIWC accounts (whose OpenAI /v1/models list has no token
// limits and mixes non-Codex models), it falls back to the built-in plan list: an API key gets
// the pro list, and a subscription or SIWC login gets the list of its plan (team, business and
// go share the team list; a SIWC account leaves out the image models).
func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if a, err := accountOf(call.Account.Credential); err == nil && a.subscription {
		if models, ok := fetchCodexModels(ctx, s, a); ok {
			return &protocol.ModelList{
				Models:  models,
				OwnedBy: modelsOwner,
				From:    protocol.ModelListFromAccount,
			}, nil
		}
	}
	return builtinModels(call.Account.Credential), nil
}

func builtinModels(c *protocol.Credential) *protocol.ModelList {
	plan := "pro"
	if c != nil && c.Kind == protocol.CredentialOAuth {
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
	if isSIWC(c) {
		models = slices.DeleteFunc(slices.Clone(models), func(m protocol.Model) bool { return imageModel(m.ID) != "" })
	}
	return &protocol.ModelList{
		Models:  models,
		OwnedBy: modelsOwner,
		From:    protocol.ModelListFromExtension,
	}
}

func fetchCodexModels(ctx context.Context, s settings, a account) ([]protocol.Model, bool) {
	u, err := url.Parse(baseURL(s, a) + "/models")
	if err != nil {
		return nil, false
	}
	q := u.Query()
	q.Set("client_version", s.clientVersion())
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	setIdentity(req.Header, nil, s, a)

	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, modelsMaxBody))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false
	}

	var payload struct {
		Models []struct {
			Slug             string   `json:"slug"`
			DisplayName      string   `json:"display_name"`
			Description      string   `json:"description"`
			ContextWindow    int      `json:"context_window"`
			InputModalities  []string `json:"input_modalities"`
			AvailableInPlans []string `json:"available_in_plans"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}

	plan := strings.ToLower(strings.TrimSpace(a.planType))
	var (
		models []protocol.Model
		seen   = make(map[string]bool)
	)
	for _, item := range payload.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" || seen[slug] {
			continue
		}
		if plan != "" && len(item.AvailableInPlans) > 0 {
			allowed := slices.ContainsFunc(item.AvailableInPlans, func(p string) bool {
				return strings.EqualFold(strings.TrimSpace(p), plan)
			})
			if !allowed {
				continue
			}
		}
		seen[slug] = true
		var modalities []string
		for _, m := range item.InputModalities {
			if m = strings.TrimSpace(m); m != "" {
				modalities = append(modalities, m)
			}
		}
		models = append(models, protocol.Model{
			ID:              slug,
			DisplayName:     strings.TrimSpace(item.DisplayName),
			Description:     strings.TrimSpace(item.Description),
			OwnedBy:         modelsOwner,
			ContextLength:   max(item.ContextWindow, 0),
			InputModalities: modalities,
		})
	}
	if len(models) == 0 {
		return nil, false
	}
	for _, im := range imageModels {
		if !seen[im.ID] {
			models = append(models, im)
		}
	}
	return models, true
}
