package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// checkUpstream is a fake OpenAI-compatible upstream for account checks: its model list accepts
// one key, refuses the others, and holds the check of a third key until released.
type checkUpstream struct {
	*httptest.Server
	release chan struct{}
	mu      sync.Mutex
	lists   map[string]int // GET /v1/models by key
	posts   int            // generations
}

func newCheckUpstream(t *testing.T) *checkUpstream {
	u := &checkUpstream{release: make(chan struct{}), lists: map[string]int{}}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			u.mu.Lock()
			u.lists[key]++
			u.mu.Unlock()
			switch key {
			case "sk-good":
				io.WriteString(w, `{"object":"list","data":[{"id":"gpt-test","object":"model"},{"id":"gpt-other","object":"model"}]}`)
			case "sk-slow":
				select {
				case <-u.release:
				case <-r.Context().Done():
					return
				}
				io.WriteString(w, `{"object":"list","data":[]}`)
			default:
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			u.mu.Lock()
			u.posts++
			u.mu.Unlock()
			io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-test",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	t.Cleanup(func() {
		select {
		case <-u.release:
		default:
			close(u.release)
		}
	})
	return u
}

func (u *checkUpstream) counts() (map[string]int, int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	lists := map[string]int{}
	for k, v := range u.lists {
		lists[k] = v
	}
	return lists, u.posts
}

type accountChecks struct {
	Accounts []struct {
		ID    string `json:"id"`
		Check *struct {
			State  string `json:"state"`
			Detail string `json:"detail"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"check"`
	} `json:"accounts"`
}

// waitChecks polls the account listing until ok holds for the check states by account ID.
func waitChecks(t *testing.T, base string, ok func(map[string]string) bool) accountChecks {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var list accountChecks
		if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
			t.Fatalf("GET /accounts: %d", code)
		}
		states := map[string]string{}
		for _, a := range list.Accounts {
			if a.Check != nil {
				states[a.ID] = a.Check.State
			}
		}
		if ok(states) {
			return list
		}
		if time.Now().After(deadline) {
			t.Fatalf("account checks did not settle: %v", states)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStartupAccountCheck shows core checking each account when it starts, without generating
// and without holding up requests: a good key passes, a refused key fails, and a check that hangs
// leaves its account "checking" while core serves. A reload checks again only the account whose
// key changed.
func TestStartupAccountCheck(t *testing.T) {
	u := newCheckUpstream(t)
	account := func(id, key string) map[string]any {
		return map[string]any{
			"id":         id,
			"models":     []string{"gpt-test"},
			"settings":   map[string]any{"base_url": u.URL + "/v1"},
			"credential": map[string]any{"kind": "api_key", "api_key": key},
		}
	}
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{account("good", "sk-good"), account("bad", "sk-revoked"), account("slow", "sk-slow")},
		},
	}
	base, path, logs := runCoreArgs(t, cfg)

	list := waitChecks(t, base, func(s map[string]string) bool { return s["good"] == "ok" && s["bad"] == "failed" })
	for _, a := range list.Accounts {
		switch a.ID {
		case "good":
			if a.Check.Detail != "the upstream lists 2 models" {
				t.Errorf("good account's check detail = %q", a.Check.Detail)
			}
		case "bad":
			if a.Check.Error == nil || a.Check.Error.Code != "auth" || !strings.Contains(a.Check.Error.Message, "Incorrect API key") {
				t.Errorf("bad account's check error = %+v; want code auth with the upstream's message", a.Check.Error)
			}
		case "slow":
			if a.Check == nil || a.Check.State != "checking" {
				t.Errorf("slow account's check = %+v; want checking while the upstream holds it", a.Check)
			}
		}
	}
	all := logs.waitFor(t, `account "bad": check failed: auth`)
	if !strings.Contains(all, `account "good": check passed: the upstream lists 2 models`) {
		t.Errorf("core did not log the good account's check; log:\n%s", all)
	}
	if strings.Contains(all, "sk-revoked") || strings.Contains(all, "sk-good") {
		t.Errorf("core logged an upstream key; log:\n%s", all)
	}
	if _, posts := u.counts(); posts != 0 {
		t.Errorf("checks made %d generations upstream, want none", posts)
	}

	// Core serves while a check hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hello") {
		t.Fatalf("request while a check hangs: %d %s", resp.StatusCode, body)
	}
	close(u.release)
	waitChecks(t, base, func(s map[string]string) bool { return s["slow"] == "ok" })

	// A reload with the bad account's key fixed checks that account again, and only it.
	cfg["extension"].(map[string]any)["accounts"] = []any{account("good", "sk-good"), account("bad", "sk-good"), account("slow", "sk-slow")}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := manage(t, base, "POST", "/reload", nil, nil); code != http.StatusOK {
		t.Fatalf("POST /reload: %d", code)
	}
	waitChecks(t, base, func(s map[string]string) bool { return s["bad"] == "ok" })
	lists, _ := u.counts()
	if lists["sk-good"] != 2 || lists["sk-slow"] != 1 || lists["sk-revoked"] != 1 {
		t.Errorf("model list calls by key = %v; want sk-good 2 (good at start, bad after the reload), sk-slow 1, sk-revoked 1", lists)
	}
}

// TestLoginAccountCheck shows core checking an account a login adds, with the token the login
// got and without generating; renewing the token or reloading does not check it again.
func TestLoginAccountCheck(t *testing.T) {
	srv := oauthtest.New(t)
	base, _ := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")
	list := waitChecks(t, base, func(s map[string]string) bool { return s["work"] == "ok" })
	if d := list.Accounts[0].Check.Detail; d != "the provider accepted the token" {
		t.Errorf("check detail = %q", d)
	}
	checked := srv.CheckTokens()
	if len(checked) != 1 || checked[0] == "" {
		t.Fatalf("check calls = %d (%q), want one with the login's token", len(checked), checked)
	}
	if n := len(srv.ChatTokens()); n != 0 {
		t.Errorf("the check made %d generations, want none", n)
	}

	var out map[string]any
	if code := manage(t, base, "POST", "/accounts/work/refresh", nil, &out); code != http.StatusOK {
		t.Fatalf("POST /accounts/work/refresh: %d %v", code, out)
	}
	if code := manage(t, base, "POST", "/reload", nil, nil); code != http.StatusOK {
		t.Fatalf("POST /reload: %d", code)
	}
	postOK(t, base, plainBody)
	if n := len(srv.CheckTokens()); n != 1 {
		t.Errorf("check calls after a renewal and a reload = %d, want still 1", n)
	}
	waitChecks(t, base, func(s map[string]string) bool { return s["work"] == "ok" })
}
