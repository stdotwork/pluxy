package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// modelsJSON is the Antigravity model catalog of today's core, the antigravity section of
// core/internal/registry/models/models.json. Cloud Code names the models an account has; this
// catalog describes them.
//
//go:embed models.json
var modelsJSON []byte

var catalog = func() []protocol.Model {
	var m []protocol.Model
	if err := json.Unmarshal(modelsJSON, &m); err != nil {
		panic("antigravity: models.json: " + err.Error())
	}
	return m
}()

// Limits of a model list reply, as in today's core (core/sdk/auth/antigravity_models.go).
const (
	maxModelsReply = 8 << 20
	maxModelIDs    = 1024
	maxModelIDLen  = 256
)

// lastModels keeps the last list fetched for each account, which stands in when a later fetch
// fails, as the last good catalog does in today's core (core/sdk/cliproxy/antigravity_models.go).
// A list not used for lastModelsTTL is dropped, as today's core drops one unused for twice its
// three-hour refresh interval, so removed accounts and old settings do not pile up.
var (
	lastMu     sync.Mutex
	lastModels = map[string]keptList{} // by account key
	modelsNow  = time.Now
)

const lastModelsTTL = 6 * time.Hour

type keptList struct {
	models []protocol.Model
	used   time.Time
}

// listModels lists the models an account has. As today's core does (fetchAntigravityModelCapabilityHints
// in core/sdk/cliproxy/antigravity_models.go and the probe in core/sdk/auth/antigravity_models.go), it
// asks Cloud Code which models the account has (a single request, never a generation) and lists the
// catalog models Cloud Code names, matching IDs without case or surrounding spaces. When the request
// fails, or the reply names no models object, it lists the last list it fetched for the account, or
// none: today's core registers no models for such an account either.
func listModels(ctx context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	list := &protocol.ModelList{OwnedBy: "antigravity"}
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c == nil || c.AccessToken == "" {
		return list, nil
	}
	key := strings.Join([]string{call.Account.ID, readData(c).ProjectID, s.BaseURL, s.UserAgent}, "\x00")
	ids, ok := fetchModelIDs(ctx, s, c.AccessToken, readData(c).ProjectID)
	lastMu.Lock()
	defer lastMu.Unlock()
	now := modelsNow()
	for k, kept := range lastModels {
		if now.Sub(kept.used) > lastModelsTTL {
			delete(lastModels, k)
		}
	}
	if !ok {
		if kept, found := lastModels[key]; found {
			list.Models = kept.models
			lastModels[key] = keptList{models: kept.models, used: now}
		}
		return list, nil
	}
	for _, m := range catalog {
		if ids[m.ID] {
			list.Models = append(list.Models, m)
		}
	}
	lastModels[key] = keptList{models: list.Models, used: now}
	return list, nil
}

// fetchModelIDs asks Cloud Code for the account's models and returns their IDs, lower-cased and
// trimmed. ok is false when the request failed or its reply is not a model list.
func fetchModelIDs(ctx context.Context, s settings, token, project string) (ids map[string]bool, ok bool) {
	body, _ := json.Marshal(map[string]string{"project": project})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", s.UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsReply+1))
	if err != nil || len(raw) > maxModelsReply {
		return nil, false
	}
	var reply struct {
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Models == nil || len(reply.Models) > maxModelIDs {
		return nil, false
	}
	ids = make(map[string]bool, len(reply.Models))
	for id := range reply.Models {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" && len(id) <= maxModelIDLen {
			ids[id] = true
		}
	}
	return ids, true
}
