package e2e

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Each newly offered API-key method creates a usable account, refuses a bad key with the
// upstream's reason, and keeps keys out of management replies and logs.
func TestProviderAPIKeyLogin(t *testing.T) {
	for _, provider := range []string{"openai-compatible", "xai", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			var settings any
			input := map[string]string{}
			var key, model, reason string
			var verify func()
			switch provider {
			case "openai-compatible":
				key = "made-up-compatible-key"
				model = "gpt-test"
				var calls, models atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
					if r.URL.Path == "/v1/models" && r.Method == "GET" {
						models.Add(1)
						if tok != key {
							w.WriteHeader(401)
							json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Incorrect API key provided: " + tok}})
							return
						}
						json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
						return
					}
					if r.URL.Path != "/v1/chat/completions" || tok != key {
						http.NotFound(w, r)
						return
					}
					calls.Add(1)
					kimitest.JSONText(w, "Hello")
				}))
				defer srv.Close()
				input["base_url"] = srv.URL + "/v1/"
				reason = "Incorrect API key provided"
				verify = func() {
					if calls.Load() != 2 || models.Load() < 3 {
						t.Errorf("upstream: %d chats, %d model reads; want 2 chats and at least 3 model reads (two successful logins and one refusal)", calls.Load(), models.Load())
					}
				}
			case "xai":
				srv := xaitest.New(t)
				key, settings, model, reason = srv.Key(), srv.Settings(), "grok-4.7", "Incorrect API key provided"
				verify = func() {
					if len(srv.Requests()) != 1 || srv.ModelRequests() < 2 {
						t.Errorf("upstream: %d calls, %d model reads", len(srv.Requests()), srv.ModelRequests())
					}
				}
			case "kimi":
				srv := kimitest.New(t)
				key, _ = srv.Grant()
				settings, model, reason = srv.Settings(), "kimi-k2.8", "Invalid Authentication"
				verify = func() {
					if len(srv.Requests()) != 1 {
						t.Errorf("upstream: %d calls", len(srv.Requests()))
					}
				}
			}
			manifest := filepath.Join(binDir, provider, "pluxy-extension.json")
			if provider == "openai-compatible" {
				manifest = filepath.Join(binDir, "pluxy-extension.json")
			}
			base, configPath, logs := runCoreArgs(t, map[string]any{
				"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
				"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
				"extension":       map[string]any{"manifest": manifest, "accounts": []any{}},
			})
			var methods struct {
				Extensions []struct {
					Extension string
					Methods   []protocol.LoginMethod
				}
			}
			var raw json.RawMessage
			code := manage(t, base, "GET", "/login-methods", nil, &raw)
			// Check discovery independently of account creation: the method was absent before this change.
			if code != 200 {
				t.Fatalf("login methods: %d", code)
			}
			// The response is keyed as extensions by the management API.
			if err := json.Unmarshal(raw, &methods); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, ext := range methods.Extensions {
				for _, m := range ext.Methods {
					if m.Method == "api_key" {
						found = true
						secret := false
						for _, in := range m.Inputs {
							if in.Name == "api_key" {
								secret = in.Secret
							}
						}
						if !secret {
							t.Error("API-key input is not secret")
						}
					}
				}
			}
			if !found {
				t.Error("GET /login-methods has no api_key method")
			}
			secretFree := func(raw []byte) {
				t.Helper()
				if strings.Contains(string(raw), key) || strings.Contains(string(raw), "made-up-rejected-key") {
					t.Fatal("management reply leaked a key")
				}
			}
			login := func(path, id, k string) loginReply {
				t.Helper()
				in := map[string]string{"api_key": "  " + k + "  "}
				if path == "/accounts" {
					for n, v := range input {
						in[n] = v
					}
				}
				body := map[string]any{"method": "api_key", "input": in}
				if path == "/accounts" {
					body["id"] = id
					body["settings"] = settings
				}
				var raw json.RawMessage
				code := manage(t, base, "POST", path, body, &raw)
				secretFree(raw)
				if code != 202 {
					t.Fatalf("starting api_key login: %d %s", code, raw)
				}
				var l loginReply
				json.Unmarshal(raw, &l)
				deadline := time.Now().Add(10 * time.Second)
				for l.Login.State == "pending" && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
					manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &raw)
					secretFree(raw)
					json.Unmarshal(raw, &l)
				}
				return l
			}
			l := login("/accounts", "good", key)
			if l.Login.State != "done" {
				t.Fatalf("valid key login: %+v", l.Login)
			}
			if body := askOK(t, base, model); !strings.Contains(body, "Hello") {
				t.Fatalf("chat: %s", body)
			}
			if provider == "openai-compatible" {
				l = login("/accounts/good/login", "", key)
				if l.Login.State != "done" {
					t.Fatalf("re-login with key alone: %+v", l.Login)
				}
				askOK(t, base, model)
			}
			l = login("/accounts", "bad", "made-up-rejected-key")
			if l.Login.State != "failed" || l.Login.Error == nil || !strings.Contains(l.Login.Error.Message, reason) {
				t.Fatalf("bad key login: %+v", l.Login)
			}
			manage(t, base, "GET", "/accounts", nil, &raw)
			secretFree(raw)
			var accounts struct{ Accounts []accountView }
			json.Unmarshal(raw, &accounts)
			if len(accounts.Accounts) != 1 || accounts.Accounts[0].ID != "good" {
				t.Fatalf("accounts: %s", raw)
			}
			// The credential store is outside this config folder. Nothing written beside the
			// config, including config.json or any config.yaml, may contain the account's key.
			files := 0
			err := filepath.WalkDir(filepath.Dir(configPath), func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files++
				secretFree(raw)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if files == 0 {
				t.Fatal("no config files inspected")
			}
			verify()
			logs.mu.Lock()
			all := strings.Join(logs.lines, "\n")
			logs.mu.Unlock()
			secretFree([]byte(all))
		})
	}
}
