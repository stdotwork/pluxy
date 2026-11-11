package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
)

// Fake values for the header tests. The gateway keys are secrets by their header's name.
const (
	gatewayKey   = "fake-gateway-key-e2e-7f3a"
	overrideType = "application/json; pluxy-e2e=override"
)

var placeholderRE = regexp.MustCompile(`pluxy-redacted-[0-9a-f]{24}`)

// headerUpstream is a fake provider that records each request's headers and answers with a
// 400 whose message repeats the X-Gateway-Key it got, as a gateway's error might.
type headerUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []headerRequest
}

type headerRequest struct {
	method, path string
	header       http.Header
}

func newHeaderUpstream(t *testing.T) *headerUpstream {
	u := &headerUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, headerRequest{r.Method, r.URL.Path, r.Header.Clone()})
		u.mu.Unlock()
		msg, _ := json.Marshal("fake upstream refused gateway key " + r.Header.Get("X-Gateway-Key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":`+string(msg)+`,"type":"invalid_request_error","code":"fake"}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

// posts returns the POST requests whose path starts with prefix and a slash.
func (u *headerUpstream) posts(prefix string) []headerRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []headerRequest
	for _, r := range u.seen {
		if r.method == http.MethodPost && strings.HasPrefix(r.path, prefix+"/") {
			out = append(out, r)
		}
	}
	return out
}

// pinned sends a chat request for model to account id and returns the reply's status and body.
func pinned(t *testing.T, base, id, model string) (int, string) {
	t.Helper()
	return pinnedWith(t, base, id, model, nil)
}

// pinnedWith is pinned with extra headers on the client's request.
func pinnedWith(t *testing.T, base, id, model string, extra map[string]string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluxy-Account", id)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The slice's check: every provider extension sends an account's headers on its upstream
// requests, and a configured header replaces the extension's own value for that name (each
// extension sets Content-Type itself). An account without headers sends neither.
func TestUpstreamHeadersEveryExtension(t *testing.T) {
	for _, ext := range proxyExtensions {
		t.Run(strings.TrimPrefix(ext.id, "pluxy/"), func(t *testing.T) {
			t.Parallel()
			up := newHeaderUpstream(t)
			account := func(id string, headers map[string]string) map[string]any {
				a := map[string]any{"id": id, "models": []string{"m-headers"}, "settings": map[string]any{"base_url": up.URL + "/" + id},
					"credential": ext.credential}
				if headers != nil {
					a["headers"] = headers
				}
				return a
			}
			base := runCore(t, map[string]any{
				"listen":      "127.0.0.1:0",
				"client_keys": []string{clientKey},
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, ext.manifest),
					"accounts": []any{
						account("with", map[string]string{"X-Gateway-Key": gatewayKey, "content-type": overrideType}),
						account("without", nil),
					},
				},
			})
			for _, id := range []string{"with", "without"} {
				pinned(t, base, id, "m-headers")
			}
			with, without := up.posts("/with"), up.posts("/without")
			if len(with) == 0 || len(without) == 0 {
				t.Fatalf("POSTs at the upstream: with %d, without %d; it saw %d requests", len(with), len(without), len(up.seen))
			}
			for _, r := range with {
				if got := r.header.Values("X-Gateway-Key"); len(got) != 1 || got[0] != gatewayKey {
					t.Errorf("with: %s got X-Gateway-Key %q, want %q", r.path, got, gatewayKey)
				}
				if got := r.header.Values("Content-Type"); len(got) != 1 || got[0] != overrideType {
					t.Errorf("with: %s got Content-Type %q, want only the configured %q", r.path, got, overrideType)
				}
			}
			for _, r := range without {
				if got := r.header.Values("X-Gateway-Key"); len(got) != 0 {
					t.Errorf("without: %s got X-Gateway-Key %q", r.path, got)
				}
				// The extension's own value, which the other account's header replaced.
				if ct := r.header.Get("Content-Type"); ct == "" || ct == overrideType {
					t.Errorf("without: %s got Content-Type %q, want the extension's own", r.path, ct)
				}
			}
		})
	}
}

// A Responses WebSocket session's handshake carries the account's headers, over the Codex
// extension's own OpenAI-Beta.
func TestUpstreamHeadersWebSocket(t *testing.T) {
	u := newWSUpstream(t)
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "ws", "models": []string{"gpt-test"},
				"headers":    map[string]string{"X-Gateway-Key": gatewayKey, "OpenAI-Beta": "pluxy-e2e-override"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws"}}},
		},
	})
	c := dialResponses(t, base)
	if m := wsTurn(t, c, wsCreate("hello", "")); m["type"] != "response.completed" {
		t.Fatalf("turn ended with %v", m)
	}
	conns := u.connections()
	if len(conns) != 1 {
		t.Fatalf("%d upstream sockets, want 1", len(conns))
	}
	h := conns[0].header
	if got := h.Values("X-Gateway-Key"); len(got) != 1 || got[0] != gatewayKey {
		t.Errorf("handshake X-Gateway-Key %q, want %q", got, gatewayKey)
	}
	if got := h.Values("OpenAI-Beta"); len(got) != 1 || got[0] != "pluxy-e2e-override" {
		t.Errorf("handshake OpenAI-Beta %q, want only the configured value", got)
	}
}

// A new account's sign-in, its check and its token renewal carry the headers the request to
// add it named: every request the fake login server gets, through a recording front, has them.
func TestUpstreamHeadersLoginAndRefresh(t *testing.T) {
	srv := codextest.New(t)
	target, _ := url.Parse(srv.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	var mu sync.Mutex
	var seen []string
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" X-Gateway-Key="+r.Header.Get("X-Gateway-Key"))
		mu.Unlock()
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	var l loginReply
	settings := map[string]any{"base_url": front.URL + "/backend-api/codex", "auth_base_url": front.URL}
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "chatgpt", "method": "device", "settings": settings,
		"headers": map[string]string{"X-Gateway-Key": gatewayKey}}, &l); code != http.StatusAccepted || l.Login.Prompt == nil || l.Login.Prompt.UserCode == "" {
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
	if code := manage(t, base, "POST", "/accounts/chatgpt/refresh", nil, nil); code != http.StatusOK {
		t.Fatalf("refresh: status %d", code)
	}
	if n := srv.RefreshCalls(); n != 1 {
		t.Fatalf("refresh calls at the login server = %d, want 1", n)
	}
	mu.Lock()
	defer mu.Unlock()
	// At least the device code request, a token poll and the refresh.
	if len(seen) < 3 {
		t.Fatalf("the login server got %d requests: %v", len(seen), seen)
	}
	for _, s := range seen {
		if !strings.HasSuffix(s, " X-Gateway-Key="+gatewayKey) {
			t.Errorf("a request without the account's header: %s", s)
		}
	}
}

// A header core sets from the request itself fails the load, with a message that names the
// setting and the header, never the value; in a config.yaml too.
func TestUpstreamHeadersRefusedNames(t *testing.T) {
	const value = "fake-refused-value-e2e"
	refuse := func(t *testing.T, path string, want ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "-config", path).CombinedOutput()
		out := string(raw)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || strings.Contains(out, "listening on") {
			t.Fatalf("core did not refuse the header (%v):\n%s", err, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("the refusal does not name %q:\n%s", w, out)
			}
		}
		if strings.Contains(out, value) {
			t.Errorf("the refusal holds the header's value:\n%s", out)
		}
	}
	for _, name := range []string{"Host", "content-length", "Transfer-Encoding", "CONNECTION"} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(map[string]any{
				"listen":      "127.0.0.1:0",
				"client_keys": []string{clientKey},
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, "pluxy-extension.json"),
					"accounts": []any{map[string]any{"id": "a", "headers": map[string]string{"X-Fine": "ok", name: value},
						"settings":   map[string]any{"base_url": "http://127.0.0.1:9/v1"},
						"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
				},
			})
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			refuse(t, path, "accounts[0].headers", "header "+name+" cannot be set")
		})
	}
	t.Run("config.yaml", func(t *testing.T) {
		yaml := `config-version: 8
server:
  host: "127.0.0.1"
  port: ` + strconv.Itoa(freePort(t)) + `
access:
  api-keys:
    - "` + clientKey + `"
api-keys:
  claude:
    - name: gw
      base-url: http://127.0.0.1:9
      headers:
        Host: ` + value + `
      keys:
        - api-key: ` + upstreamKey + `
`
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		refuse(t, path, "api-keys.claude key gw headers", "header Host cannot be set")
	})
}

// Headers from today's config.yaml (a provider group's, which its keys inherit unless a key
// sets its own, and an openai-compatibility group's) and from today's auth files arrive at the
// upstream. A secret header's value is a placeholder in GET /config and GET /accounts (the same
// one in both), a placeholder sent back in PATCH /accounts keeps the stored value, and the value
// is in no reply, log or request log, also when the upstream's error repeats it.
func TestUpstreamHeadersImportedAndSecret(t *testing.T) {
	const (
		claudeKey = "fake-claude-gateway-e2e-11"
		groupKey  = "fake-group-token-e2e-22"
		kimiKey   = "fake-kimi-gateway-e2e-33"
		kimi2Key  = "fake-kimi-two-gateway-e2e-44"
		// What the client's requests send as X-Client-Header: kimi-1's "X-Copy": "$X-Client-Header"
		// must not copy it.
		clientHeader = "fake-client-header-e2e-5d1c"
	)
	up := newHeaderUpstream(t)
	kimi := kimitest.New(t)
	kimiAccess, kimiRefresh := kimi.Grant()
	kimi2Access, kimi2Refresh := kimi.Grant()
	work := t.TempDir()
	authDir := filepath.Join(work, "old-auths")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file, _ := json.Marshal(map[string]any{"type": "kimi", "access_token": kimiAccess, "refresh_token": kimiRefresh,
		"token_type": "Bearer", "device_id": "device-e2e", "base_url": kimi.URL + "/coding",
		"expired": time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"headers": map[string]any{"X-Gateway-Key": kimiKey, "X-Copy": "$X-Client-Header", "X-Number": 7}})
	if err := os.WriteFile(filepath.Join(authDir, "kimi-1.json"), file, 0o600); err != nil {
		t.Fatal(err)
	}
	// A second stored account with its own secret header: PATCH of one may not take the other's.
	file2, _ := json.Marshal(map[string]any{"type": "kimi", "access_token": kimi2Access, "refresh_token": kimi2Refresh,
		"token_type": "Bearer", "device_id": "device-e2e-2", "base_url": kimi.URL + "/coding",
		"expired": time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339),
		"headers": map[string]any{"X-Gateway-Key": kimi2Key}})
	if err := os.WriteFile(filepath.Join(authDir, "kimi-2.json"), file2, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := `config-version: 8
server:
  host: "127.0.0.1"
  port: ` + strconv.Itoa(freePort(t)) + `
management:
  secret-key: "` + managementKey + `"
access:
  api-keys:
    - "` + clientKey + `"
oauth:
  auth-dir: "` + authDir + `"
observability:
  logs:
    request-log: true
api-keys:
  claude:
    - name: gw
      base-url: ` + up.URL + `/claude
      headers:
        X-Gateway-Key: ` + claudeKey + `
        X-Title: visible-title
      models:
        - name: m-headers
      keys:
        - api-key: ` + upstreamKey + `
        - api-key: sk-upstream-e2e-2
          headers: {}
  openai-compatibility:
    - name: compat
      base-url: ` + up.URL + `/compat/v1
      headers:
        X-Group-Token: ` + groupKey + `
      models:
        - name: m-compat
      keys:
        - api-key: ` + upstreamKey + `
`
	path := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-import-auth", authDir).CombinedOutput()
	t.Logf("import:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"imported kimi-1.json as account kimi-1 (pluxy/kimi)", "imported kimi-2.json as account kimi-2 (pluxy/kimi)", "headers.X-Copy (a value taken from the client's request)"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("import output lacks %q", want)
		}
	}
	bundled := bundledRegistry(t, map[string]string{
		"pluxy/openai-compatible": "openai-compatible",
		"pluxy/claude":            filepath.Join("claude", "claude"),
		"pluxy/kimi":              filepath.Join("kimi", "kimi"),
	})
	base, logs := runCoreFile(t, path, "-bundled", bundled)
	logs.waitFor(t, "config revision 1 active")

	var replies []string
	for _, r := range []struct{ id, model string }{{"gw-1", "m-headers"}, {"gw-2", "m-headers"}, {"compat", "m-compat"}, {"kimi-1", "kimi-k2.8"}} {
		_, body := pinnedWith(t, base, r.id, r.model, map[string]string{"X-Client-Header": clientHeader})
		replies = append(replies, body)
	}
	check := func(what string, got []string, want string) {
		t.Helper()
		if want == "" && len(got) != 0 || want != "" && (len(got) != 1 || got[0] != want) {
			t.Errorf("%s: %q, want %q", what, got, want)
		}
	}
	// The two claude keys share the group's base-url: told apart by their API key.
	var gw1, gw2 []headerRequest
	for _, r := range up.posts("/claude") {
		if strings.Contains(r.header.Get("X-Api-Key")+r.header.Get("Authorization"), "sk-upstream-e2e-2") {
			gw2 = append(gw2, r)
		} else {
			gw1 = append(gw1, r)
		}
	}
	compat := up.posts("/compat")
	if len(gw1) == 0 || len(gw2) == 0 || len(compat) == 0 {
		t.Fatalf("POSTs at the upstream: gw-1 %d, gw-2 %d, compat %d", len(gw1), len(gw2), len(compat))
	}
	for _, r := range gw1 {
		check("gw-1 X-Gateway-Key", r.header.Values("X-Gateway-Key"), claudeKey)
		check("gw-1 X-Title", r.header.Values("X-Title"), "visible-title")
	}
	// The key that sets headers: {} sends none of its group's.
	for _, r := range gw2 {
		check("gw-2 X-Gateway-Key", r.header.Values("X-Gateway-Key"), "")
		check("gw-2 X-Title", r.header.Values("X-Title"), "")
	}
	for _, r := range compat {
		check("compat X-Group-Token", r.header.Values("X-Group-Token"), groupKey)
	}
	// The requests of one Kimi account, told apart by its token.
	kimiRequests := func(access string) []kimitest.Request {
		var out []kimitest.Request
		for _, r := range kimi.Requests() {
			if r.Header.Get("Authorization") == "Bearer "+access {
				out = append(out, r)
			}
		}
		return out
	}
	kreqs := kimiRequests(kimiAccess)
	if len(kreqs) == 0 {
		t.Fatal("fake Kimi saw no request")
	}
	for _, r := range kreqs {
		check("kimi X-Gateway-Key", r.Header.Values("X-Gateway-Key"), kimiKey)
		check("kimi X-Copy", r.Header.Values("X-Copy"), "")
		check("kimi X-Number", r.Header.Values("X-Number"), "")
	}
	before := len(kreqs)

	// Management reads show placeholders, the same for one value in both.
	var cfgView struct{ Content string }
	if code := manage(t, base, "GET", "/config", nil, &cfgView); code != http.StatusOK {
		t.Fatalf("GET /config: %d", code)
	}
	type account struct {
		ID      string            `json:"id"`
		Headers map[string]string `json:"headers"`
	}
	accounts := func() map[string]account {
		var v struct{ Accounts []account }
		if code := manage(t, base, "GET", "/accounts", nil, &v); code != http.StatusOK {
			t.Fatalf("GET /accounts: %d", code)
		}
		m := map[string]account{}
		for _, a := range v.Accounts {
			m[a.ID] = a
		}
		return m
	}
	list := accounts()
	shown := list["gw-1"].Headers["X-Gateway-Key"]
	if !placeholderRE.MatchString(shown) || !strings.Contains(cfgView.Content, "X-Gateway-Key: "+shown) {
		t.Errorf("gw-1's X-Gateway-Key shows as %q, not the placeholder GET /config holds:\n%s", shown, cfgView.Content)
	}
	if list["gw-1"].Headers["X-Title"] != "visible-title" || list["gw-2"].Headers != nil {
		t.Errorf("gw-1 headers %v, gw-2 headers %v", list["gw-1"].Headers, list["gw-2"].Headers)
	}
	kimiShown := list["kimi-1"].Headers["X-Gateway-Key"]
	if !placeholderRE.MatchString(kimiShown) || !placeholderRE.MatchString(list["compat"].Headers["X-Group-Token"]) {
		t.Errorf("kimi-1 headers %v, compat headers %v: want placeholders", list["kimi-1"].Headers, list["compat"].Headers)
	}

	// The placeholder sent back keeps the stored value; one that stands for nothing is refused.
	var patched struct{ Account account }
	if code := manage(t, base, "PATCH", "/accounts/kimi-1", map[string]any{"headers": map[string]string{"X-Gateway-Key": kimiShown, "X-Added": "added-value"}}, &patched); code != http.StatusOK {
		t.Fatalf("PATCH headers: status %d", code)
	}
	if patched.Account.Headers["X-Gateway-Key"] != kimiShown || patched.Account.Headers["X-Added"] != "added-value" {
		t.Errorf("PATCH replied headers %v", patched.Account.Headers)
	}
	if code := manage(t, base, "PATCH", "/accounts/kimi-1", map[string]any{"headers": map[string]string{"X-Gateway-Key": "pluxy-redacted-000000000000000000000000"}}, nil); code != http.StatusBadRequest {
		t.Errorf("PATCH with a placeholder that stands for nothing: status %d, want 400", code)
	}
	// A placeholder of another account's value is no placeholder of this one: refused, and the
	// account keeps its own headers.
	if code := manage(t, base, "PATCH", "/accounts/kimi-2", map[string]any{"headers": map[string]string{"X-Gateway-Key": kimiShown}}, nil); code != http.StatusBadRequest {
		t.Errorf("PATCH kimi-2 with kimi-1's placeholder: status %d, want 400", code)
	}
	kimi2Shown := list["kimi-2"].Headers["X-Gateway-Key"]
	if now := accounts()["kimi-2"].Headers; !placeholderRE.MatchString(kimi2Shown) || kimi2Shown == kimiShown || now["X-Gateway-Key"] != kimi2Shown || len(now) != 1 {
		t.Errorf("kimi-2 headers before %q, after the refused PATCH %v; want its own placeholder, unchanged", kimi2Shown, now)
	}
	if _, body := pinned(t, base, "kimi-2", "kimi-k2.8"); body == "" {
		t.Error("kimi-2 got no reply")
	}
	k2 := kimiRequests(kimi2Access)
	if len(k2) == 0 {
		t.Fatal("fake Kimi saw no request for kimi-2")
	}
	for _, r := range k2 {
		check("kimi-2 X-Gateway-Key after the refused PATCH", r.Header.Values("X-Gateway-Key"), kimi2Key)
	}
	_, body := pinned(t, base, "kimi-1", "kimi-k2.8")
	replies = append(replies, body)
	kreqs = kimiRequests(kimiAccess)
	if len(kreqs) <= before {
		t.Fatal("fake Kimi saw no request after the PATCH")
	}
	last := kreqs[len(kreqs)-1]
	check("kimi X-Gateway-Key after PATCH", last.Header.Values("X-Gateway-Key"), kimiKey)
	check("kimi X-Added after PATCH", last.Header.Values("X-Added"), "added-value")

	// No secret value anywhere it could show.
	b, _ := json.Marshal(accounts())
	where := map[string]string{"GET /config": cfgView.Content, "GET /accounts": string(b), "replies": strings.Join(replies, "\n")}
	if !strings.Contains(where["replies"], "fake upstream refused gateway key") {
		t.Errorf("no reply came from the upstream: %v", replies)
	}
	where["core's log"] = logs.waitFor(t, "")
	files := 0
	_ = filepath.WalkDir(filepath.Join(work, "logs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			where[filepath.Base(p)] = string(b)
			files++
		}
		return nil
	})
	if files == 0 {
		t.Error("the logs folder holds no request log to check")
	}
	for w, s := range where {
		for _, secret := range []string{claudeKey, groupKey, kimiKey, kimi2Key} {
			if strings.Contains(s, secret) {
				t.Errorf("%s holds %q:\n%s", w, secret, s)
			}
		}
	}
}
