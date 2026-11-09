package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// An extension's model_alias and excluded_models apply to its accounts added by a sign-in, and
// not to those added with an API key, as today's oauth-model-alias and oauth-excluded-models
// skip API keys; the model list shows each account's names.
func TestLoginDefaultsSkipAPIKeys(t *testing.T) {
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
