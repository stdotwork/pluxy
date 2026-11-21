package main

import (
	"context"
	_ "embed"
	"encoding/json"
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
	// modelsTimeout stays under core's 15 s model-list read, so a hung provider still yields the built-in list.
	modelsTimeout  = 10 * time.Second
	modelsPageSize = 1000
	modelsMaxPages = 100
	modelsMaxBody  = 8 << 20
)

// geminiModelsJSON is the built-in fallback model list for a Gemini API-key account, and
// vertexModelsJSON is the list for a Vertex AI account (service account or express-mode key). Each
// holds the models today's core lists when an account's config names no models, with owned_by
// "google": core/sdk/cliproxy/service_models.go, case "gemini" takes registry.GetGeminiModels (the
// gemini section of core/internal/registry/models/models.json), and case "vertex" takes
// registry.GetGeminiVertexModels (the vertex section). To refresh the lists by hand, take those
// sections of models.json in https://github.com/router-for-me/models, the file the old core copied.
//
// Generated once from that catalog, with two differences. The imagen-* models are left out: their
// only generation method is predict, which this extension does not serve. A model the catalog
// gives no generation methods lists generateContent, as today's core shows it. Every name is
// models/<id>, as today's core shows it.
//
//go:embed models.json
var geminiModelsJSON []byte

//go:embed vertex-models.json
var vertexModelsJSON []byte

// listModels answers a models call. Vertex accounts return the built-in Vertex catalog (Vertex has
// no usable per-model list endpoint with token limits); Gemini API-key accounts query
// GET {base_url}/v1beta/models and fall back to the built-in Gemini catalog on failure or an empty
// response.
func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	if s.Vertex != nil {
		return builtinModels(vertexModelsJSON)
	}
	if models, ok := fetchGeminiModels(ctx, s.BaseURL, call.Account.Credential); ok {
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

func fetchGeminiModels(ctx context.Context, baseURL string, cred *protocol.Credential) ([]protocol.Model, bool) {
	if cred == nil || cred.Kind != protocol.CredentialAPIKey || cred.APIKey == "" {
		return nil, false
	}
	base, err := url.Parse(baseURL + "/v1beta/models")
	if err != nil {
		return nil, false
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
			return nil, false
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Goog-Api-Key", cred.APIKey)

		resp, err := client.Do(req)
		if err != nil {
			return nil, false
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, modelsMaxBody))
		resp.Body.Close()
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, false
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
			return nil, false
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
		return nil, false
	}
	return models, true
}
