package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

// proxyUpstream is a fake provider that answers every request with a 400 and records, for each,
// its path and the address it came from, so a test can tell which proxy (if any) opened it.
type proxyUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []proxyUpstreamRequest
}

type proxyUpstreamRequest struct {
	method, path, from string
}

func newProxyUpstream(t *testing.T) *proxyUpstream {
	u := &proxyUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, proxyUpstreamRequest{r.Method, r.URL.Path, r.RemoteAddr})
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"fake upstream","type":"invalid_request_error","code":"fake"}}`)
	}))
	t.Cleanup(u.Close)
	return u
}

// under returns the requests whose path starts with prefix.
func (u *proxyUpstream) under(prefix string) []proxyUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []proxyUpstreamRequest
	for _, r := range u.seen {
		if strings.HasPrefix(r.path, prefix+"/") {
			out = append(out, r)
		}
	}
	return out
}

// port returns the port the server listens on.
func port(t *testing.T, rawURL string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// proxyExtensions are the provider extensions, with what an account of each needs besides its
// base_url.
var proxyExtensions = []struct {
	id, manifest string
	credential   map[string]any
}{
	{"pluxy/openai-compatible", "pluxy-extension.json", map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/claude", filepath.Join("claude", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/codex", filepath.Join("codex", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/gemini", filepath.Join("gemini", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/kimi", filepath.Join("kimi", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/xai", filepath.Join("xai", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/devin", filepath.Join("devin", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/meta", filepath.Join("meta", "pluxy-extension.json"), map[string]any{"kind": "api_key", "api_key": upstreamKey}},
	{"pluxy/antigravity", filepath.Join("antigravity", "pluxy-extension.json"),
		map[string]any{"kind": "oauth", "access_token": "ya29.e2e-access", "data": map[string]any{"project_id": "e2e-project"}}},
}

// The slice's check: every provider extension sends an account's upstream traffic through the
// proxy core gives it. The fake upstream's host name, upstream.test, resolves only at the fake
// proxies, so reaching it at all takes the proxy; each upstream connection is also matched to
// the proxy that opened it. One core per extension has three accounts on one upstream, told
// apart by their base_url's path: one inherits the config's proxy_url (an HTTP proxy with a
// password), one has its own (SOCKS5, with and without a password), one is direct and goes to
// 127.0.0.1 without either proxy. Each takes one pinned request.
func TestOutboundProxyEveryExtension(t *testing.T) {
	for i, ext := range proxyExtensions {
		t.Run(strings.TrimPrefix(ext.id, "pluxy/"), func(t *testing.T) {
			t.Parallel()
			up := newProxyUpstream(t)
			httpProxy := newHTTPProxy(t, true)
			socksScheme, socksAuth := "socks5", true
			if i%2 == 1 {
				socksScheme, socksAuth = "socks5h", false
			}
			socks := newSOCKS5Proxy(t, socksAuth)
			via := "http://" + net.JoinHostPort(proxiedHost, port(t, up.URL))
			account := func(id, base, proxy string) map[string]any {
				a := map[string]any{"id": id, "models": []string{"m-proxy"}, "settings": map[string]any{"base_url": base},
					"credential": ext.credential}
				if proxy != "" {
					a["proxy_url"] = proxy
				}
				return a
			}
			base := runCore(t, map[string]any{
				"listen":      "127.0.0.1:0",
				"client_keys": []string{clientKey},
				"proxy_url":   httpProxy.URL("http"),
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, ext.manifest),
					"accounts": []any{
						account("inherits", via+"/inherits", ""),
						account("own", via+"/own", socks.URL(socksScheme)),
						account("direct", up.URL+"/direct", "direct"),
					},
				},
			})
			for _, id := range []string{"inherits", "own", "direct"} {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
					strings.NewReader(`{"model":"m-proxy","messages":[{"role":"user","content":"hi"}]}`))
				req.Header.Set("Authorization", "Bearer "+clientKey)
				req.Header.Set("X-Pluxy-Account", id)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				cancel()
			}

			for _, c := range []struct {
				account string
				through *fakeProxy
				target  string // what the proxy saw, when through is set
			}{
				{"inherits", httpProxy, "POST " + via + "/inherits/"},
				{"own", socks, "SOCKS " + net.JoinHostPort(proxiedHost, port(t, up.URL))},
				{"direct", nil, ""},
			} {
				reqs := up.under("/" + c.account)
				if !hasPost(reqs) {
					t.Errorf("account %s: no request reached the upstream; it saw %v", c.account, reqs)
					continue
				}
				for _, r := range reqs {
					switch {
					case c.through != nil && !c.through.opened(r.from):
						t.Errorf("account %s: %s %s reached the upstream from %s, a connection its proxy did not open", c.account, r.method, r.path, r.from)
					case c.through == nil && (httpProxy.opened(r.from) || socks.opened(r.from)):
						t.Errorf("account %s: %s %s went through a proxy, though the account is direct", c.account, r.method, r.path)
					}
				}
				if c.through == nil {
					continue
				}
				seen, creds := c.through.seen()
				found := false
				for i, s := range seen {
					if strings.HasPrefix(s, c.target) {
						found = true
						if c.through.auth && creds[i] != proxyUser+":"+proxyPassword {
							t.Errorf("account %s: the proxy got %q with credentials %q", c.account, s, creds[i])
						}
					}
				}
				if !found {
					t.Errorf("account %s: the proxy saw no %q; it saw %v", c.account, c.target, seen)
				}
			}
			if seen, _ := httpProxy.seen(); hasAny(seen, "/direct/") {
				t.Errorf("the direct account's traffic reached the HTTP proxy: %v", seen)
			}
		})
	}
}

func hasPost(reqs []proxyUpstreamRequest) bool {
	for _, r := range reqs {
		if r.method == http.MethodPost {
			return true
		}
	}
	return false
}

func hasAny(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// A request to an https:// upstream goes through an HTTP proxy as a CONNECT tunnel to the
// upstream's host name. The tunnel leads to a server whose certificate the extension does not
// trust, so the request fails after the CONNECT: what counts is that the proxy got it.
func TestOutboundProxyTunnelsHTTPS(t *testing.T) {
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(tlsUp.Close)
	p := newHTTPProxy(t, true)
	target := net.JoinHostPort(proxiedHost, port(t, strings.Replace(tlsUp.URL, "https://", "http://", 1)))
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"proxy_url":   p.URL("http"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "tls", "settings": map[string]any{"base_url": "https://" + target + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
		},
	})
	resp := post(t, context.Background(), base, clientKey, `{"model":"m-proxy","messages":[{"role":"user","content":"hi"}]}`)
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	seen, creds := p.seen()
	for i, s := range seen {
		if s == "CONNECT "+target && creds[i] == proxyUser+":"+proxyPassword {
			return
		}
	}
	t.Fatalf("the proxy saw no CONNECT %s with the proxy's credentials: %v", target, seen)
}

// A Responses WebSocket session's upstream socket is opened through the proxy: its handshake
// reaches the upstream only by the proxy's host name.
func TestOutboundProxyWebSocket(t *testing.T) {
	for _, scheme := range []string{"http", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			u := newWSUpstream(t)
			p := newHTTPProxy(t, true)
			if scheme != "http" {
				p = newSOCKS5Proxy(t, true)
			}
			via := "http://" + net.JoinHostPort(proxiedHost, port(t, u.URL))
			base := runCore(t, map[string]any{
				"listen":      "127.0.0.1:0",
				"client_keys": []string{clientKey},
				"proxy_url":   p.URL(scheme),
				"extension": map[string]any{
					"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
					"accounts": []any{map[string]any{"id": "ws", "models": []string{"gpt-test"},
						"settings":   map[string]any{"base_url": via + "/v1", "websockets": true},
						"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws"}}},
				},
			})
			c := dialResponses(t, base)
			if m := wsTurn(t, c, wsCreate("hello", "")); m["type"] != "response.completed" {
				t.Fatalf("turn ended with %v", m)
			}
			if n := len(u.connections()); n != 1 {
				t.Fatalf("%d upstream sockets, want 1", n)
			}
			want := "GET " + via + "/v1/responses"
			if scheme != "http" {
				want = "SOCKS " + strings.TrimPrefix(via, "http://")
			}
			if seen, _ := p.seen(); !hasAny(seen, want) {
				t.Fatalf("the proxy saw no %q: %v", want, seen)
			}
		})
	}
}

// A new account's sign-in and its token renewals go through the config's proxy: the fake
// login server is reachable only by the proxy's host name, so the device login, its polling and
// the refresh succeed only through it.
func TestOutboundProxyLoginAndRefresh(t *testing.T) {
	t.Setenv("PLUXY_TEST_LOGIN_HOSTS", "1")
	srv := codextest.New(t)
	p := newSOCKS5Proxy(t, true)
	via := "http://" + net.JoinHostPort(proxiedHost, port(t, srv.URL))
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"proxy_url":       p.URL("socks5"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	var l loginReply
	settings := map[string]any{"base_url": via + "/backend-api/codex", "auth_base_url": via}
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "chatgpt", "method": "device", "settings": settings}, &l); code != http.StatusAccepted ||
		l.Login.Prompt == nil || l.Login.Prompt.UserCode == "" {
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
	seen, creds := p.seen()
	if len(seen) == 0 {
		t.Fatal("the proxy saw nothing")
	}
	for i, s := range seen {
		if creds[i] != proxyUser+":"+proxyPassword {
			t.Errorf("the proxy got %q with credentials %q", s, creds[i])
		}
	}
}

// The proxy's password is a secret: it is not in the config or accounts the management API
// shows, nor in core's log, the request logs or a client's error, also when the proxy is down.
// A settings change that sends back the proxy as shown keeps the password.
func TestOutboundProxyPasswordStaysSecret(t *testing.T) {
	up := newProxyUpstream(t)
	httpProxy := newHTTPProxy(t, true)
	socks := newSOCKS5Proxy(t, true)
	dead := "socks5://" + proxyUser + ":" + proxyPassword + "@" + strings.TrimPrefix(closedPort(t), "http://")
	via := "http://" + net.JoinHostPort(proxiedHost, port(t, up.URL))
	key := func(id, path, proxy string) map[string]any {
		return map[string]any{"id": id, "proxy_url": proxy, "settings": map[string]any{"base_url": via + path},
			"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}
	}
	base, path, logs := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"proxy_url":      httpProxy.URL("http"),
		"logs":           map[string]any{"request_log": true},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{key("own", "/own", socks.URL("socks5")), key("dead", "/dead", dead), key("inherits", "/inherits", "")},
		},
	})
	// ask sends a chat request pinned to account id and returns the status and the reply.
	ask := func(id string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
			strings.NewReader(`{"model":"m-proxy","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer "+clientKey)
		req.Header.Set("X-Pluxy-Account", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	var bodies []string
	for _, id := range []string{"own", "dead", "inherits"} {
		status, body := ask(id)
		bodies = append(bodies, body)
		if id == "dead" && status < 400 {
			t.Errorf("a request through a dead proxy: status %d", status)
		}
	}
	if len(up.under("/dead")) != 0 || len(up.under("/own")) == 0 || len(up.under("/inherits")) == 0 {
		t.Errorf("upstream requests: dead %d (want 0), own %d, inherits %d",
			len(up.under("/dead")), len(up.under("/own")), len(up.under("/inherits")))
	}

	read := func(path string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v0/management"+path, nil)
		req.Header.Set("Authorization", "Bearer "+managementKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	accounts, settings := read("/accounts"), read("/settings")
	shown := map[string]string{"GET /config": read("/config"), "GET /accounts": accounts, "GET /settings": settings}
	if !strings.Contains(accounts, `"proxy_url":"socks5://redacted@`+socks.addr+`"`) {
		t.Errorf("GET /accounts does not show the account's proxy redacted: %s", accounts)
	}
	if !strings.Contains(settings, `"proxy-url":"http://redacted@`+httpProxy.addr+`"`) {
		t.Errorf("GET /settings does not show the proxy redacted: %s", settings)
	}

	// Sending back the settings' proxy as shown keeps the password: the next request through it
	// still authenticates.
	var patched json.RawMessage
	if code := manage(t, base, "PATCH", "/settings", map[string]any{"proxy-url": "http://redacted@" + httpProxy.addr}, &patched); code != http.StatusOK {
		t.Fatalf("PATCH /settings: status %d", code)
	}
	shown["PATCH /settings"] = string(patched)
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), httpProxy.URL("http")) {
		t.Errorf("the config file lost the proxy's password: %s", b)
	}
	before, _ := httpProxy.seen()
	status, body := ask("inherits")
	bodies = append(bodies, body)
	requests, creds := httpProxy.seen()
	if !strings.Contains(body, "fake upstream") || len(requests) <= len(before) || creds[len(creds)-1] != proxyUser+":"+proxyPassword {
		t.Errorf("after the same-address PATCH a request got status %d, %q; the proxy saw %d new requests, last with %q",
			status, body, len(requests)-len(before), creds[len(creds)-1])
	}

	// Sending back the user-less address of another proxy does not carry the first one's user name
	// and password to it: the setting becomes what was sent, and the second proxy never sees them.
	other := newHTTPProxy(t, false)
	patched = nil
	if code := manage(t, base, "PATCH", "/settings", map[string]any{"proxy-url": "http://redacted@" + other.addr}, &patched); code != http.StatusOK {
		t.Fatalf("PATCH /settings to another proxy: status %d", code)
	}
	shown["PATCH /settings to another proxy"] = string(patched)
	settings = read("/settings")
	shown["GET /settings after"] = settings
	if !strings.Contains(settings, `"proxy-url":"http://redacted@`+other.addr+`"`) {
		t.Errorf("GET /settings after the PATCH does not show the other proxy: %s", settings)
	}
	var stored struct {
		ProxyURL string `json:"proxy_url"`
	}
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &stored) != nil || stored.ProxyURL != "http://redacted@"+other.addr {
		t.Errorf("the config file's proxy_url after the PATCH is %q, want the address that was sent without the first proxy's credentials (%v)", stored.ProxyURL, err)
	}
	first, _ := httpProxy.seen()
	status, body = ask("inherits")
	bodies = append(bodies, body)
	requests, creds = other.seen()
	if !strings.Contains(body, "fake upstream") || len(requests) == 0 {
		t.Errorf("a request through the other proxy got status %d, %q; the proxy saw %d requests", status, body, len(requests))
	}
	for _, c := range creds {
		if strings.Contains(c, proxyUser) || strings.Contains(c, proxyPassword) {
			t.Errorf("the other proxy got the first proxy's credentials: %q", c)
		}
	}
	if again, _ := httpProxy.seen(); len(again) != len(first) {
		t.Errorf("the first proxy still got %d requests after the setting named another", len(again)-len(first))
	}

	shown["GET /config after"] = read("/config")
	for i, b := range bodies {
		shown["reply "+itoa(i)] = b
	}
	logs.mu.Lock()
	shown["core's log"] = strings.Join(logs.lines, "\n")
	logs.mu.Unlock()
	files := 0
	_ = filepath.WalkDir(filepath.Join(filepath.Dir(path), "logs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			shown[filepath.Base(p)] = string(b)
			files++
		}
		return nil
	})
	if files == 0 {
		t.Error("the logs folder holds no request log to check")
	}
	for where, s := range shown {
		for _, secret := range []string{proxyUser, proxyPassword} {
			if strings.Contains(s, secret) {
				t.Errorf("%s holds the proxy's user name or password %q:\n%s", where, secret, s)
			}
		}
	}
	if !strings.Contains(strings.Join(bodies, "\n"), "fake upstream") {
		t.Errorf("no reply came from the upstream: %v", bodies)
	}
}

// A proxy_url that is not a proxy URL fails the load with a message that names the setting and
// not the password.
func TestOutboundProxyRefusesOtherSchemes(t *testing.T) {
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"proxy_url":      "ftp://" + proxyUser + ":" + proxyPassword + "@127.0.0.1:21",
	}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "-config", path).CombinedOutput()
	out := string(raw)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || strings.Contains(out, "listening on") {
		t.Fatalf("core did not refuse an ftp proxy (%v):\n%s", err, out)
	}
	if !strings.Contains(out, "proxy_url") || !strings.Contains(out, "(not a valid proxy URL)") || !strings.Contains(out, "must use http://") || strings.Contains(out, proxyPassword) || strings.Contains(out, proxyUser) {
		t.Fatalf("core's refusal does not name the setting, or holds the password:\n%s", out)
	}
}
