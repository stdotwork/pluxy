package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

const managementKey = "management-key-e2e"

// startOAuthCore runs core with the example OAuth provider, no configured accounts and the
// management API on, and returns its base URL and credentials folder.
func startOAuthCore(t *testing.T) (base, credDir string) {
	t.Helper()
	credDir = filepath.Join(t.TempDir(), "credentials")
	base = runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": credDir,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	return base, credDir
}

// manage sends a management request and decodes the JSON reply into out (when not nil).
func manage(t *testing.T, base, method, path string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, base+"/v0/management"+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return resp.StatusCode
}

type loginReply struct {
	Login struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Prompt *struct {
			Kind     string `json:"kind"`
			UserCode string `json:"user_code"`
		} `json:"prompt"`
		Error *struct{ Code, Message string } `json:"error"`
	} `json:"login"`
}

// deviceLogin adds account id by device login, approving the code at the fake provider.
func deviceLogin(t *testing.T, base string, srv *oauthtest.Server, id string) {
	t.Helper()
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{
		"id": id, "method": "device", "settings": map[string]string{"base_url": srv.URL},
	}, &l); code != http.StatusAccepted || l.Login.Prompt == nil || l.Login.Prompt.UserCode == "" {
		t.Fatalf("starting the login: status %d, %+v", code, l.Login)
	}
	srv.ApproveDevice(l.Login.Prompt.UserCode)
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

type accountList struct {
	Accounts []struct {
		ID, Source, Kind, State string
		Version                 uint64
	} `json:"accounts"`
}

// The slice's check: an account added by device login through the management API serves
// requests; when its access token is about to expire, many concurrent requests share one
// refresh call (the fake provider rotates refresh tokens and revokes the grant on any reuse),
// and all of them are served with the renewed token.
func TestLoginAndConcurrentRefresh(t *testing.T) {
	srv := oauthtest.New(t)
	// The first token expires in a second, inside core's renewal lead; renewed ones last.
	srv.AccessTTL = func(n int) time.Duration {
		if n == 1 {
			return time.Second
		}
		return time.Hour
	}
	base, credDir := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")

	var list accountList
	manage(t, base, "GET", "/accounts", nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].ID != "work" || list.Accounts[0].Source != "login" ||
		list.Accounts[0].Kind != "oauth" || list.Accounts[0].State != "active" {
		t.Fatalf("accounts = %+v", list.Accounts)
	}
	st, err := os.Stat(filepath.Join(credDir, "work.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credential file: %v, mode %v; want 0600", err, st.Mode().Perm())
	}

	release := srv.BlockRefresh()
	const n = 20
	var wg sync.WaitGroup
	status := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := post(t, context.Background(), base, clientKey, plainBody)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			status[i] = resp.StatusCode
		}()
	}
	select {
	case <-srv.RefreshStarted():
	case <-time.After(10 * time.Second):
		t.Fatal("no refresh reached the provider")
	}
	time.Sleep(300 * time.Millisecond) // every request is now waiting on the one refresh
	release()
	wg.Wait()
	for i, s := range status {
		if s != http.StatusOK {
			t.Errorf("request %d: status %d", i, s)
		}
	}
	if c := srv.RefreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
	tokens := srv.ChatTokens()
	if len(tokens) != n {
		t.Fatalf("%d chat requests reached the provider, want %d", len(tokens), n)
	}
	for _, tok := range tokens {
		if tok != tokens[0] {
			t.Fatalf("requests used different tokens: %v", tokens)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(credDir, "work.json"))
	if !strings.Contains(string(raw), tokens[0]) {
		t.Error("the renewed token is not the one stored")
	}
	// The management API never shows a secret.
	req, _ := http.NewRequest("GET", base+"/v0/management/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+managementKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), tokens[0]) {
		t.Errorf("account list shows the access token: %s", body)
	}
}

// A refresh whose account is removed while the refresh is running is refused: the result is
// not written, and the removed account does not come back.
func TestRefreshOfRemovedAccountIsRefused(t *testing.T) {
	srv := oauthtest.New(t)
	base, credDir := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")

	release := srv.BlockRefresh()
	done := make(chan int, 1)
	go func() {
		var out map[string]any
		done <- manage(t, base, "POST", "/accounts/work/refresh", nil, &out)
	}()
	select {
	case <-srv.RefreshStarted():
	case <-time.After(10 * time.Second):
		t.Fatal("no refresh reached the provider")
	}
	if code := manage(t, base, "DELETE", "/accounts/work", nil, nil); code != http.StatusNoContent {
		t.Fatalf("removing: status %d", code)
	}
	release()
	if code := <-done; code == http.StatusOK {
		t.Error("the refresh of a removed account succeeded")
	}
	if _, err := os.Stat(filepath.Join(credDir, "work.json")); !os.IsNotExist(err) {
		t.Errorf("the removed account's file is back: %v", err)
	}
	var list accountList
	manage(t, base, "GET", "/accounts", nil, &list)
	if len(list.Accounts) != 0 {
		t.Errorf("accounts = %+v, want none", list.Accounts)
	}
}

// When the upstream refuses an access token that has not expired (revoked early), core renews
// the credential once and serves the request on the same account with the new token.
func TestRefusedTokenIsRenewedAndRetried(t *testing.T) {
	srv := oauthtest.New(t)
	base, _ := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")
	postOK(t, base, plainBody)
	first := srv.ChatTokens()[0]
	srv.RevokeAccess(first)
	postOK(t, base, plainBody)
	tokens := srv.ChatTokens()
	if len(tokens) != 3 || tokens[1] != first || tokens[2] == first {
		t.Errorf("chat tokens = %v; want the first token twice (served, then refused), then a new one", tokens)
	}
	if c := srv.RefreshCalls(); c != 1 {
		t.Errorf("refresh calls = %d, want 1", c)
	}
}
