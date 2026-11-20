package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// An extension's model_alias and excluded_models apply to its accounts added by a sign-in, and
// not to those added with an API key, as today's oauth-model-alias and oauth-excluded-models
// skip API keys; the model list shows each account's names.
func TestLoginDefaultsSkipAPIKeys(t *testing.T) {
	t.Parallel()
	srv := oauthtest.New(t)
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"routing":         map[string]any{"force_model_prefix": true},
		"extension": map[string]any{
			"manifest":        filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts":        []any{},
			"model_alias":     []any{map[string]any{"name": "m1", "alias": "one"}},
			"excluded_models": []string{"x-*"},
		},
	})
	settings := map[string]string{"base_url": srv.URL}
	models := []string{"m1", "x-2"}

	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": "sub", "method": "device", "settings": settings, "models": models, "prefix": "s",
	}, &l); code != http.StatusAccepted || l.Login.Prompt == nil {
		t.Fatalf("starting the device login: status %d, %+v", code, l.Login)
	}
	srv.ApproveDevice(l.Login.Prompt.UserCode)
	waitLogin(t, base, l)

	l = loginReply{}
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": "key", "method": "api_key", "input": map[string]string{"api_key": srv.IssueToken("text")},
		"settings": settings, "models": models, "prefix": "k",
	}, &l); code != http.StatusAccepted {
		t.Fatalf("starting the API key login: status %d, %+v", code, l.Login)
	}
	waitLogin(t, base, l)

	want := []string{"k/m1", "k/x-2", "s/one"}
	var got []string
	deadline := time.Now().Add(10 * time.Second)
	for {
		got = listModels(t, base)
		if slices.Equal(got, want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !slices.Equal(got, want) {
		t.Errorf("models = %v, want %v", got, want)
	}
}

// GET /v0/management/models lists the models /v1/models lists, in the same order, each with its
// extension, its upstream name and the IDs of the accounts that serve it, and no credential.
func TestManagementModels(t *testing.T) {
	const secret = "sk-models-secret-key"
	account := func(id string, models ...any) map[string]any {
		a := map[string]any{
			"id": id, "models": models,
			"settings":   map[string]any{"base_url": "http://127.0.0.1:1/v1"},
			"credential": map[string]any{"kind": "api_key", "api_key": secret},
		}
		if id == "acct-a" {
			a["prefix"] = "p"
		}
		return a
	}
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{
				account("acct-a", "m1"),
				account("acct-b", "m1", "m2", map[string]any{"name": "m3", "alias": "three"}),
			},
		},
	})

	code, raw := management(t, http.MethodGet, base, "/v0/management/models")
	if code != http.StatusOK {
		t.Fatalf("GET /v0/management/models: %d %s", code, raw)
	}
	if strings.Contains(string(raw), secret) {
		t.Errorf("the reply holds an account's credential: %s", raw)
	}
	var got struct {
		Models []struct {
			ID        string   `json:"id"`
			Upstream  string   `json:"upstream"`
			Extension string   `json:"extension"`
			Accounts  []string `json:"accounts"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("reply %s: %v", raw, err)
	}
	type row struct {
		ID, Upstream, Extension string
		Accounts                []string
	}
	const ext = "pluxy/openai-compatible"
	want := []row{
		{"m1", "m1", ext, []string{"acct-a", "acct-b"}},
		{"m2", "m2", ext, []string{"acct-b"}},
		{"p/m1", "m1", ext, []string{"acct-a"}},
		{"three", "m3", ext, []string{"acct-b"}},
	}
	var rows []row
	var ids []string
	for _, m := range got.Models {
		rows = append(rows, row{m.ID, m.Upstream, m.Extension, m.Accounts})
		ids = append(ids, m.ID)
	}
	if !slices.EqualFunc(rows, want, func(a, b row) bool {
		return a.ID == b.ID && a.Upstream == b.Upstream && a.Extension == b.Extension && slices.Equal(a.Accounts, b.Accounts)
	}) {
		t.Errorf("models = %+v, want %+v", rows, want)
	}
	if served := listModels(t, base); !slices.Equal(ids, served) {
		t.Errorf("management lists %v, GET /v1/models lists %v", ids, served)
	}
}

// waitLogin waits for the login l started to finish, and fails unless it is done.
func waitLogin(t *testing.T, base string, l loginReply) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for l.Login.State == "pending" {
		if time.Now().After(deadline) {
			t.Fatal("the login did not finish")
		}
		time.Sleep(100 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login ended %s: %+v", l.Login.State, l.Login.Error)
	}
}

// listModels returns the IDs GET /v1/models lists.
func listModels(t *testing.T, base string) []string {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+clientKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// Worst-case Model lists: an OpenAI-compatible upstream adds a model; after
// POST /v0/management/models/refresh it is in /v1/models and served with no restart; when the
// upstream list call then returns 500 or an empty list, the model stays listed and served. An
// account whose config names models falls back to them when its upstream list cannot be read.
func TestModelListRefreshAndKeepLastGood(t *testing.T) {
	var (
		mu     sync.Mutex
		models = []string{"m-one"}
		status = http.StatusOK
		empty  = false
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+upstreamKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			mu.Lock()
			st, em := status, empty
			cur := append([]string(nil), models...)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if st != http.StatusOK {
				w.WriteHeader(st)
				io.WriteString(w, `{"error":{"message":"boom","type":"server_error"}}`)
				return
			}
			data := []map[string]any{}
			if !em {
				for _, id := range cur {
					data = append(data, map[string]any{"id": id, "object": "model", "owned_by": "test-upstream"})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, req.Model)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "upstream-1",
					"settings":   map[string]any{"base_url": up.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
				},
				map[string]any{
					"id":         "cfg-1",
					"models":     []string{"cfg-model"},
					"settings":   map[string]any{"base_url": "http://127.0.0.1:1/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
				},
			},
		},
	})

	wantInitial := []string{"cfg-model", "m-one"}
	if got := listModels(t, base); !slices.Equal(got, wantInitial) {
		t.Fatalf("initial /v1/models = %v, want %v", got, wantInitial)
	}

	// Upstream adds a model; refresh picks it up without a restart.
	mu.Lock()
	models = []string{"m-one", "m-two"}
	mu.Unlock()
	if code := manage(t, base, "POST", "/models/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("POST /models/refresh: status %d", code)
	}
	wantRefreshed := []string{"cfg-model", "m-one", "m-two"}
	if got := listModels(t, base); !slices.Equal(got, wantRefreshed) {
		t.Fatalf("after refresh /v1/models = %v, want %v", got, wantRefreshed)
	}
	resp := post(t, t.Context(), base, clientKey, `{"model":"m-two","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions for m-two after refresh: status %d", resp.StatusCode)
	}

	// Upstream list call fails (500): model stays listed and served.
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	if code := manage(t, base, "POST", "/models/refresh", map[string]string{"account": "upstream-1"}, nil); code != http.StatusOK {
		t.Fatalf("POST /models/refresh (500 upstream): status %d", code)
	}
	if got := listModels(t, base); !slices.Equal(got, wantRefreshed) {
		t.Fatalf("after 500 refresh /v1/models = %v, want %v", got, wantRefreshed)
	}
	resp = post(t, t.Context(), base, clientKey, `{"model":"m-two","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/chat/completions for m-two after 500 refresh: status %d", resp.StatusCode)
	}

	// Upstream returns an empty list: last good list stays.
	mu.Lock()
	status = http.StatusOK
	empty = true
	mu.Unlock()
	if code := manage(t, base, "POST", "/models/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("POST /models/refresh (empty upstream): status %d", code)
	}
	if got := listModels(t, base); !slices.Equal(got, wantRefreshed) {
		t.Fatalf("after empty refresh /v1/models = %v, want %v", got, wantRefreshed)
	}

	// Unknown account returns 404.
	if code := manage(t, base, "POST", "/models/refresh", map[string]string{"account": "nobody"}, nil); code != http.StatusNotFound {
		t.Fatalf("POST /models/refresh unknown account = %d, want 404", code)
	}
}

func TestClaudeAccountLiveModelList(t *testing.T) {
	t.Parallel()
	const liveKey = "sk-ant-api-e2e-live"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Api-Key") != liveKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Errorf("Anthropic-Version = %q, want 2023-06-01", r.Header.Get("Anthropic-Version"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"data": [
				{
					"type": "model",
					"id": "claude-e2e-live-1",
					"display_name": "Claude E2E Live 1",
					"created_at": "2026-11-01T00:00:00Z",
					"max_input_tokens": 400000,
					"max_tokens": 32000
				}
			],
			"has_more": false
		}`)
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "live-claude",
					"settings":   map[string]any{"base_url": up.URL},
					"credential": map[string]any{"kind": "api_key", "api_key": liveKey},
				},
			},
		},
	})

	code, raw := management(t, http.MethodGet, base, "/v0/management/accounts/live-claude/models")
	if code != http.StatusOK {
		t.Fatalf("GET /v0/management/accounts/live-claude/models: %d %s", code, raw)
	}
	var got struct {
		From   string `json:"from"`
		Models []struct {
			ID                  string `json:"id"`
			DisplayName         string `json:"display_name"`
			ContextLength       int    `json:"context_length"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode reply %s: %v", raw, err)
	}
	if got.From != "account" {
		t.Errorf("from = %q, want account (body: %s)", got.From, raw)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "claude-e2e-live-1" ||
		got.Models[0].ContextLength != 400000 || got.Models[0].MaxCompletionTokens != 32000 {
		t.Fatalf("models = %+v, want claude-e2e-live-1 with limits", got.Models)
	}
	if listed := listModels(t, base); !slices.Equal(listed, []string{"claude-e2e-live-1"}) {
		t.Errorf("/v1/models = %v, want [claude-e2e-live-1]", listed)
	}
}
