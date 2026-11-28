package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	modelsTimeout  = 10 * time.Second
	modelsPageSize = 1000
	modelsMaxPages = 100
	modelsMaxBody  = 8 << 20
)

//go:embed models.json
var geminiModelsJSON []byte

//go:embed vertex-models.json
var vertexModelsJSON []byte

func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if s.Vertex != nil {
		return builtinModels(vertexModelsJSON)
	}
	if models, ok := fetchGeminiModels(ctx, call.Account.ID, s.BaseURL, call.Account.Credential); ok {
		return &protocol.ModelList{
			OwnedBy: "google",
			From:    protocol.ModelListFromAccount,
			Models:  models,
		}, nil
	}
	return builtinModels(geminiModelsJSON)
}

func builtinModels(raw []byte) (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	list.From = protocol.ModelListFromExtension
	return &list, nil
}

func fetchGeminiModels(ctx context.Context, accountID, baseURL string, cred *protocol.Credential) ([]protocol.Model, bool) {
	fail := func(reason string) ([]protocol.Model, bool) {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, false
		}
		modelReadFailed(accountID, cred, reason)
		return nil, false
	}
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return nil, false
	}
	base, err := url.Parse(baseURL + "/v1beta/models")
	if err != nil {
		return fail("the base URL is invalid")
	}

	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	var (
		models    []protocol.Model
		seen      = make(map[string]bool)
		pageToken string
	)
	for range modelsMaxPages {
		u := *base
		q := u.Query()
		q.Set("pageSize", strconv.Itoa(modelsPageSize))
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return fail("the request could not be built")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Goog-Api-Key", cred.APIKey)

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

		var page struct {
			Models []struct {
				Name                       string   `json:"name"`
				Version                    string   `json:"version"`
				DisplayName                string   `json:"displayName"`
				Description                string   `json:"description"`
				InputTokenLimit            int      `json:"inputTokenLimit"`
				OutputTokenLimit           int      `json:"outputTokenLimit"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fail("the reply was not valid JSON")
		}
		for _, item := range page.Models {
			rawName := strings.TrimSpace(item.Name)
			id := strings.TrimPrefix(rawName, "models/")
			if id == "" || seen[id] {
				continue
			}
			if len(item.SupportedGenerationMethods) > 0 && !slices.Contains(item.SupportedGenerationMethods, "generateContent") {
				continue
			}
			seen[id] = true
			name := rawName
			if !strings.HasPrefix(name, "models/") {
				name = "models/" + id
			}
			inLimit := max(item.InputTokenLimit, 0)
			outLimit := max(item.OutputTokenLimit, 0)
			models = append(models, protocol.Model{
				ID:                  id,
				Name:                name,
				DisplayName:         strings.TrimSpace(item.DisplayName),
				Description:         strings.TrimSpace(item.Description),
				Version:             strings.TrimSpace(item.Version),
				OwnedBy:             "google",
				ContextLength:       inLimit,
				MaxCompletionTokens: outLimit,
				InputTokenLimit:     inLimit,
				OutputTokenLimit:    outLimit,
				GenerationMethods:   item.SupportedGenerationMethods,
			})
		}
		next := strings.TrimSpace(page.NextPageToken)
		if next == "" || next == pageToken {
			break
		}
		pageToken = next
	}
	if len(models) == 0 {
		return fail("the list holds no models")
	}
	modelReadOK(accountID)
	return models, true
}
