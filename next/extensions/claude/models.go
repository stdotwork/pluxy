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
	"regexp"
	"strconv"
	"strings"
	"sync"
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
var modelsJSON []byte

func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	if models, ok := fetchModels(ctx, call); ok {
		return &protocol.ModelList{
			OwnedBy: "anthropic",
			From:    protocol.ModelListFromAccount,
			Models:  models,
		}, nil
	}
	return builtinModels()
}

func builtinModels() (*protocol.ModelList, error) {
	var list protocol.ModelList
	if err := json.Unmarshal(modelsJSON, &list); err != nil {
		return nil, err
	}
	list.From = protocol.ModelListFromExtension
	return &list, nil
}

func fetchModels(ctx context.Context, call sdk.ModelsCall) ([]protocol.Model, bool) {
	fail := func(reason string) ([]protocol.Model, bool) {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, false
		}
		modelReadFailed(call.Account.ID, call.Account.Credential, reason)
		return nil, false
	}
	if authorize(make(http.Header), call.Account.Credential) != nil {
		return fail("the account has no usable credential")
	}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return fail("the account's settings are invalid")
	}
	base, err := url.Parse(s.BaseURL + "/v1/models")
	if err != nil {
		return fail("the base URL is invalid")
	}

	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	var (
		models  []protocol.Model
		seen    = make(map[string]bool)
		afterID string
	)
	for range modelsMaxPages {
		u := *base
		q := u.Query()
		q.Set("limit", strconv.Itoa(modelsPageSize))
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return fail("the request could not be built")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Anthropic-Version", anthropicVersion)
		if err := authorize(req.Header, call.Account.Credential); err != nil {
			return fail("the account has no usable credential")
		}

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
			Data []struct {
				ID             string `json:"id"`
				DisplayName    string `json:"display_name"`
				CreatedAt      string `json:"created_at"`
				MaxInputTokens int    `json:"max_input_tokens"`
				MaxTokens      int    `json:"max_tokens"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return fail("the reply was not valid JSON")
		}
		for _, item := range page.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			var created int64
			if item.CreatedAt != "" {
				if t, err := time.Parse(time.RFC3339Nano, item.CreatedAt); err == nil {
					created = t.Unix()
				}
			}
			models = append(models, protocol.Model{
				ID:                  id,
				DisplayName:         strings.TrimSpace(item.DisplayName),
				Created:             created,
				OwnedBy:             "anthropic",
				ContextLength:       max(item.MaxInputTokens, 0),
				MaxCompletionTokens: max(item.MaxTokens, 0),
			})
		}
		if !page.HasMore {
			break
		}
		next := strings.TrimSpace(page.LastID)
		if next == "" && len(page.Data) > 0 {
			next = strings.TrimSpace(page.Data[len(page.Data)-1].ID)
		}
		if next == "" || next == afterID {
			break
		}
		afterID = next
	}
	if len(models) == 0 {
		return fail("the list holds no models")
	}
	modelReadOK(call.Account.ID)
	return models, true
}

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

var dated = regexp.MustCompile(`-\d{8}$`)

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
