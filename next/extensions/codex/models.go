package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
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
	modelsOwner = "openai"
	// codexClientVersion is the default Codex CLI version in the User-Agent and the ChatGPT
	// backend model list's client_version; older or missing versions omit newer models.
	codexClientVersion = "0.159.0"
	modelsTimeout      = 10 * time.Second
	modelsMaxBody      = 8 << 20
)

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

func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	builtin := builtinModels(call.Account.Credential)
	if a, err := accountOf(call.Account.Credential); err == nil && a.subscription {
		if models, ok := fetchCodexModels(ctx, call, s, a, builtin.Models); ok {
			return &protocol.ModelList{
				Models:  models,
				OwnedBy: modelsOwner,
				From:    protocol.ModelListFromAccount,
			}, nil
		}
	}
	return builtin, nil
}

func fillFromBuiltin(m protocol.Model, list []protocol.Model) protocol.Model {
	i := slices.IndexFunc(list, func(b protocol.Model) bool { return b.ID == m.ID })
	if i < 0 {
		return m
	}
	b := list[i]
	m.DisplayName = cmp.Or(m.DisplayName, b.DisplayName)
	m.Description = cmp.Or(m.Description, b.Description)
	m.Version = cmp.Or(m.Version, b.Version)
	m.Created = cmp.Or(m.Created, b.Created)
	m.ContextLength = cmp.Or(m.ContextLength, b.ContextLength)
	m.MaxCompletionTokens = cmp.Or(m.MaxCompletionTokens, b.MaxCompletionTokens)
	if len(m.InputModalities) == 0 {
		m.InputModalities = slices.Clone(b.InputModalities)
	}
	if len(m.OutputModalities) == 0 {
		m.OutputModalities = slices.Clone(b.OutputModalities)
	}
	if m.Capabilities == nil && b.Capabilities != nil {
		c := *b.Capabilities
		c.ReasoningLevels = slices.Clone(c.ReasoningLevels)
		m.Capabilities = &c
	}
	return m
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

func fetchCodexModels(ctx context.Context, call sdk.ModelsCall, s settings, a account, builtin []protocol.Model) ([]protocol.Model, bool) {
	fail := func(reason string) ([]protocol.Model, bool) {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, false
		}
		modelReadFailed(call.Account.ID, call.Account.Credential, reason)
		return nil, false
	}
	u, err := url.Parse(baseURL(s, a) + "/models")
	if err != nil {
		return fail("the base URL is invalid")
	}
	q := u.Query()
	q.Set("client_version", s.clientVersion())
	u.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fail("the request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	setIdentity(req.Header, nil, s, a)

	resp, err := client.Do(req)
	if err != nil {
		return fail(requestFailure(err))
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, modelsMaxBody))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if readErr != nil {
		return fail("reading the reply failed")
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
		return fail("the reply was not valid JSON")
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
		models = append(models, fillFromBuiltin(protocol.Model{
			ID:              slug,
			DisplayName:     strings.TrimSpace(item.DisplayName),
			Description:     strings.TrimSpace(item.Description),
			OwnedBy:         modelsOwner,
			ContextLength:   max(item.ContextWindow, 0),
			InputModalities: modalities,
		}, builtin))
	}
	if len(models) == 0 {
		return fail("the list holds no model for the account")
	}
	modelReadOK(call.Account.ID)
	for _, im := range imageModels {
		if !seen[im.ID] {
			models = append(models, im)
		}
	}
	return models, true
}
