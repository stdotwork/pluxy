package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// Where a redacted secret may go back. GET /config shows each secret as a placeholder, so a
// management client can edit the file without reading the secrets, and PUT /config and PATCH
// /accounts put the real values back. A client that cannot read a secret must not be able to
// make core send it to an upstream of the client's choosing: each placeholder goes back only
// into its own secret's place.

// Fake keys of the two config accounts and of the login account.
const (
	placeKeyA     = "sk-place-account-a-e2e"
	placeKeyB     = "sk-place-account-b-e2e"
	placeKeyLogin = "sk-place-login-e2e"
	placeModel    = "m-place"
)

// keyUpstream is a fake OpenAI-compatible upstream that records the headers of every request it
// gets. It lists one model and answers chat requests with a short stream.
type keyUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []keyRequest
}

type keyRequest struct {
	method, path string
	header       http.Header
}

func newKeyUpstream(t *testing.T) *keyUpstream {
	t.Helper()
	u := &keyUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, keyRequest{r.Method, r.URL.Path, r.Header.Clone()})
		u.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"`+placeModel+`","object":"model"}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "text/event-stream")
			const head = `"id":"chatcmpl-place","object":"chat.completion.chunk","created":1700000000,"model":"` + placeModel + `"`
			for _, c := range []string{
				`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`,
				`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`[DONE]`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *keyUpstream) requests() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.seen)
}

// carrying returns, for each request u got with a header whose value holds secret, that header's
// name and the request's method and path.
func (u *keyUpstream) carrying(secret string) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for _, r := range u.seen {
		for _, name := range slices.Sorted(maps.Keys(r.header)) {
			if slices.ContainsFunc(r.header[name], func(v string) bool { return strings.Contains(v, secret) }) {
				out = append(out, name+" of "+r.method+" "+r.path)
			}
		}
	}
	return out
}

// placeCore is a running core with two config accounts of the openai-compatible extension, "a"
// and "b", each with its own key and its own upstream, and a third upstream, c, no account
// points at.
type placeCore struct {
	*mcore
	a, b, c *keyUpstream
	creds   string // the credentials folder (JSON layout only)
}

func startPlacement(t *testing.T, isYAML bool) *placeCore {
	t.Helper()
	p := &placeCore{a: newKeyUpstream(t), b: newKeyUpstream(t), c: newKeyUpstream(t)}
	manifest := filepath.Join(binDir, "pluxy-extension.json")
	dir := t.TempDir()
	p.mcore = &mcore{yaml: isYAML}
	var content []byte
	if isYAML {
		p.path = filepath.Join(dir, "config.yaml")
		var b strings.Builder
		b.WriteString("server:\n  host: 127.0.0.1\n  port: " + strconv.Itoa(freePort(t)) + "\n")
		fmt.Fprintf(&b, "access:\n  api-keys:\n    - %q\n", clientKey)
		fmt.Fprintf(&b, "management:\n  secret-key: %q\n", managementKey)
		fmt.Fprintf(&b, "extension:\n  manifest: %q\n  accounts:\n", manifest)
		for _, a := range []struct{ id, url, key string }{{"a", p.a.URL, placeKeyA}, {"b", p.b.URL, placeKeyB}} {
			fmt.Fprintf(&b, "    - id: %s\n      settings:\n        base_url: %q\n      credential:\n        kind: api_key\n        api_key: %q\n", a.id, a.url+"/v1", a.key)
		}
		content = []byte(b.String())
	} else {
		p.path = filepath.Join(dir, "config.json")
		p.creds = filepath.Join(dir, "credentials")
		account := func(id, url, key string) map[string]any {
			return map[string]any{"id": id, "settings": map[string]any{"base_url": url + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": key}}
		}
		cfg := map[string]any{
			"listen":          "127.0.0.1:0",
			"client_keys":     []string{clientKey},
			"management_key":  managementKey,
			"credentials_dir": p.creds,
			"extension": map[string]any{
				"manifest": manifest,
				"accounts": []any{account("a", p.a.URL, placeKeyA), account("b", p.b.URL, placeKeyB)},
			},
		}
		content, _ = json.MarshalIndent(cfg, "", "  ")
		content = append(content, '\n')
	}
	if err := os.WriteFile(p.path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	p.base, _ = runCoreFile(t, p.path, noWatch...)
	return p
}

// placeMarks are the placeholders a redacted config document holds for each secret.
type placeMarks struct{ management, client, a, b string }

func (p *placeCore) marks(t *testing.T, content string) placeMarks {
	t.Helper()
	var doc map[string]any
	var err error
	if p.yaml {
		err = yaml.Unmarshal([]byte(content), &doc)
	} else {
		err = json.Unmarshal([]byte(content), &doc)
	}
	if err != nil {
		t.Fatalf("redacted config: %v\n%s", err, content)
	}
	str := func(v any) string {
		t.Helper()
		s, _ := v.(string)
		if !placeholderText.MatchString(s) {
			t.Fatalf("%v is not a placeholder in\n%s", v, content)
		}
		return s
	}
	ext, _ := doc["extension"].(map[string]any)
	accounts, _ := ext["accounts"].([]any)
	if len(accounts) != 2 {
		t.Fatalf("accounts %v in\n%s", accounts, content)
	}
	key := func(i int) string {
		a, _ := accounts[i].(map[string]any)
		c, _ := a["credential"].(map[string]any)
		return str(c["api_key"])
	}
	var m placeMarks
	if p.yaml {
		mg, _ := doc["management"].(map[string]any)
		ac, _ := doc["access"].(map[string]any)
		keys, _ := ac["api-keys"].([]any)
		m.management, m.client = str(mg["secret-key"]), str(keys[0])
	} else {
		keys, _ := doc["client_keys"].([]any)
		m.management, m.client = str(doc["management_key"]), str(keys[0])
	}
	m.a, m.b = key(0), key(1)
	if len(map[string]bool{m.management: true, m.client: true, m.a: true, m.b: true}) != 4 {
		t.Fatalf("the four secrets do not have four placeholders: %+v", m)
	}
	return m
}

// editJSON returns a JSON config document with edit applied to it.
func editJSON(t *testing.T, content string, edit func(cfg map[string]any)) string {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(content), &cfg); err != nil {
		t.Fatal(err)
	}
	edit(cfg)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// configAccount returns the account with this id in a decoded JSON config.
func configAccount(t *testing.T, cfg map[string]any, id string) map[string]any {
	t.Helper()
	ext, _ := cfg["extension"].(map[string]any)
	accounts, _ := ext["accounts"].([]any)
	for _, x := range accounts {
		if a, _ := x.(map[string]any); a["id"] == id {
			return a
		}
	}
	t.Fatalf("no account %q in %v", id, cfg)
	return nil
}

// putRefused sends content as the new config and fails the test unless core refuses it with a
// 4xx and leaves the file and revisions as they were. It reports whether core refused it.
func (p *placeCore) putRefused(t *testing.T, what, content, hash string) bool {
	t.Helper()
	before := p.snap(t)
	code, body := p.call(t, http.MethodPut, "/config", map[string]string{"content": content, "base_hash": hash})
	if code < 400 || code > 499 {
		t.Errorf("PUT /config with %s: status %d, want a 4xx refusal: %s", what, code, body)
		return false
	}
	t.Logf("PUT /config with %s: refused with %d: %s", what, code, body)
	p.wantSame(t, before, "PUT /config with "+what)
	return true
}

// putOK sends content as the new config and fails the test unless core takes it.
func (p *placeCore) putOK(t *testing.T, what, content, hash string) {
	t.Helper()
	p.expect(t, http.StatusOK, http.MethodPut, "/config", map[string]string{"content": content, "base_hash": hash})
	if f := p.file(t); strings.Contains(f, "pluxy-redacted-") {
		t.Errorf("PUT /config with %s wrote a placeholder to the file:\n%s", what, f)
	}
}

// testAccount runs POST /accounts/{id}/test, which sends the account one small request upstream.
func testAccount(t *testing.T, base, id string) {
	t.Helper()
	var reply struct {
		Result struct {
			OK    bool
			Error *struct{ Code, Message string }
		}
	}
	if code := manage(t, base, "POST", "/accounts/"+id+"/test", map[string]string{"model": placeModel}, &reply); code != http.StatusOK {
		t.Fatalf("POST /accounts/%s/test: status %d", id, code)
	}
	t.Logf("POST /accounts/%s/test: ok %v, error %+v", id, reply.Result.OK, reply.Result.Error)
}

// wantNotSent fails the test if any request u got carries secret in a header.
func wantNotSent(t *testing.T, u *keyUpstream, upstream, secret, what string) {
	t.Helper()
	if names := u.carrying(secret); len(names) > 0 {
		t.Errorf("%s: %s's upstream received %s in %v", what, upstream, secret, names)
	}
}

// wantNotStored fails the test if a file in the credentials folder, where core keeps the accounts
// added by login, holds secret. The login account's own key must be there, so the check looked at
// the stored account.
func wantNotStored(t *testing.T, dir, secret, what string) {
	t.Helper()
	var all strings.Builder
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		all.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all.String(), placeKeyLogin) {
		t.Fatalf("%s: the credentials folder %s does not hold the login account's key, so the check saw no stored account", what, dir)
	}
	if strings.Contains(all.String(), secret) {
		t.Errorf("%s: the login account's stored record holds %s", what, secret)
	}
}

// wantReached fails the test unless u got a request, so a check that u got no secret saw one.
func wantReached(t *testing.T, u *keyUpstream, upstream string) {
	t.Helper()
	if u.requests() == 0 {
		t.Errorf("%s's upstream got no request, so the checks on what it received saw nothing", upstream)
	}
}

// Case 1: the management key's placeholder in account b's api_key.
func TestConfigPlacementManagementKeyAsAccountKey(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			p := startPlacement(t, f.yaml)
			r := p.config(t)
			m := p.marks(t, r.Content)
			p.putRefused(t, "the management key's placeholder in account b's api_key", replaceOnce(t, r.Content, m.b, m.management), r.Hash)
			testAccount(t, p.base, "b")
			wantReached(t, p.b, "account b")
			wantNotSent(t, p.b, "account b", managementKey, "the management key's placeholder in b's api_key")
		})
	}
}

// Case 2: account a's api_key placeholder in account b's api_key.
func TestConfigPlacementOtherAccountKey(t *testing.T) {
	p := startPlacement(t, false)
	r := p.config(t)
	m := p.marks(t, r.Content)
	p.putRefused(t, "account a's placeholder in account b's api_key", replaceOnce(t, r.Content, m.b, m.a), r.Hash)
	testAccount(t, p.base, "b")
	wantReached(t, p.b, "account b")
	wantNotSent(t, p.b, "account b", placeKeyA, "account a's placeholder in b's api_key")
}

// Case 3: account b keeps its own key's placeholder, but its base_url moves to upstream c. Known
// limit, documented in next/README.md: the placeholder is in its own secret's place, so it goes
// back, and core then sends b's key to c. Nothing in the file ties a key to an address, and
// refusing a kept key whenever an account's address changes is a decision of its own, not yet
// made. This test records today's behaviour: c gets b's key.
func TestConfigPlacementMovedBaseURL(t *testing.T) {
	p := startPlacement(t, false)
	r := p.config(t)
	moved := editJSON(t, r.Content, func(cfg map[string]any) {
		configAccount(t, cfg, "b")["settings"] = map[string]any{"base_url": p.c.URL + "/v1"}
	})
	p.putOK(t, "b's base_url moved to a new upstream, its key's placeholder kept", moved, r.Hash)
	testAccount(t, p.base, "b")
	if len(p.c.carrying(placeKeyB)) == 0 {
		t.Error("b's base_url moved, its key's placeholder kept: the new upstream did not receive b's key; if core now refuses or drops the kept key, update this test and the README's note on the limit")
	}
}

// Case 4: the management key's or a client key's placeholder as a secret header of account b.
func TestConfigPlacementSecretInHeader(t *testing.T) {
	for _, c := range []struct {
		name, header, secret string
		mark                 func(placeMarks) string
	}{
		{"management key as X-Api-Key", "X-Api-Key", managementKey, func(m placeMarks) string { return m.management }},
		{"client key as Authorization", "Authorization", clientKey, func(m placeMarks) string { return m.client }},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := startPlacement(t, false)
			r := p.config(t)
			ph := c.mark(p.marks(t, r.Content))
			edited := editJSON(t, r.Content, func(cfg map[string]any) {
				configAccount(t, cfg, "b")["headers"] = map[string]any{c.header: ph}
			})
			p.putRefused(t, "the "+c.name+" header of account b", edited, r.Hash)
			testAccount(t, p.base, "b")
			wantReached(t, p.b, "account b")
			wantNotSent(t, p.b, "account b", c.secret, "a placeholder in b's "+c.header+" header")
		})
	}
}

// Case 4, by YAML aliases: core's loader reads an alias as its anchor, so a secret is read where
// each alias of it is. Every place core reads a secret from after the write must be one where the
// file has that secret now: account a's key aliased into account b's api_key, and the management
// key aliased into a header of account b, are refused, and b's upstream never gets either.
func TestConfigPlacementYAMLAlias(t *testing.T) {
	for _, c := range []struct {
		name, secret string
		edit         func(t *testing.T, content string, m placeMarks) string
	}{
		{"account a's key aliased into account b's api_key", placeKeyA, func(t *testing.T, content string, m placeMarks) string {
			return replaceOnce(t, replaceOnce(t, content, m.a, "&ka "+m.a), m.b, "*ka")
		}},
		{"the management key aliased into a header of account b", managementKey, func(t *testing.T, content string, m placeMarks) string {
			s := replaceOnce(t, content, m.management, "&mk "+m.management)
			return replaceOnce(t, s, "    - id: b\n", "    - id: b\n      headers:\n        X-Api-Key: *mk\n")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := startPlacement(t, true)
			r := p.config(t)
			p.putRefused(t, c.name, c.edit(t, r.Content, p.marks(t, r.Content)), r.Hash)
			testAccount(t, p.base, "b")
			wantReached(t, p.b, "account b")
			wantNotSent(t, p.b, "account b", c.secret, c.name)
		})
	}
}

// startPlacementLogin is startPlacement with a third account, "login", added through the
// openai-compatible extension's api-key login, its upstream returned.
func startPlacementLogin(t *testing.T) (*placeCore, *keyUpstream) {
	t.Helper()
	p := startPlacement(t, false)
	l := newKeyUpstream(t)
	var reply loginReply
	if code := manage(t, p.base, "POST", "/accounts", map[string]any{"id": "login", "method": "api_key",
		"input": map[string]string{"api_key": placeKeyLogin, "base_url": l.URL + "/v1"}}, &reply); code != http.StatusAccepted {
		t.Fatalf("starting the api_key login: %d %+v", code, reply.Login)
	}
	for deadline := time.Now().Add(10 * time.Second); reply.Login.State == "pending" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		manage(t, p.base, "GET", "/logins/"+reply.Login.ID, nil, &reply)
	}
	if reply.Login.State != "done" {
		t.Fatalf("api_key login: %+v", reply.Login)
	}
	return p, l
}

// loginSettings returns the login account's settings as GET /accounts shows them.
func loginSettings(t *testing.T, base string) map[string]any {
	t.Helper()
	var s map[string]any
	if a := getAccount(t, base, "login"); len(a.Settings) > 0 {
		if err := json.Unmarshal(a.Settings, &s); err != nil {
			t.Fatalf("settings %s: %v", a.Settings, err)
		}
	}
	return s
}

// Case 5: a login account's settings and headers, by PATCH /accounts/{id}.
func TestConfigPlacementLoginAccount(t *testing.T) {
	type patchCase struct {
		name   string
		body   func(m placeMarks, set map[string]any) map[string]any
		secret string
		// header: the placement is in a header, which core sends upstream. Otherwise it is in
		// settings, and the openai-compatible extension sends no setting but its base_url, so
		// an upstream would not get the secret either way: the stored account is checked instead.
		header bool
	}
	withSetting := func(set map[string]any, k, v string) map[string]any {
		out := map[string]any{}
		for n, x := range set {
			out[n] = x
		}
		out[k] = v
		return out
	}
	for _, c := range []patchCase{
		{"another account's key in its settings", func(m placeMarks, set map[string]any) map[string]any {
			return map[string]any{"settings": withSetting(set, "api_key", m.a)}
		}, placeKeyA, false},
		{"the management key in its settings", func(m placeMarks, set map[string]any) map[string]any {
			return map[string]any{"settings": withSetting(set, "api_key", m.management)}
		}, managementKey, false},
		{"the management key in its X-Api-Key header", func(m placeMarks, _ map[string]any) map[string]any {
			return map[string]any{"headers": map[string]string{"X-Api-Key": m.management}}
		}, managementKey, true},
		{"another account's key in its Authorization header", func(m placeMarks, _ map[string]any) map[string]any {
			return map[string]any{"headers": map[string]string{"Authorization": m.a}}
		}, placeKeyA, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, l := startPlacementLogin(t)
			m := p.marks(t, p.config(t).Content)
			body := c.body(m, loginSettings(t, p.base))
			var raw json.RawMessage
			code := manage(t, p.base, "PATCH", "/accounts/login", body, &raw)
			if code < 400 || code > 499 {
				t.Errorf("PATCH /accounts/login with %s: status %d, want a 4xx refusal: %s", c.name, code, raw)
			} else {
				t.Logf("PATCH /accounts/login with %s: refused with %d: %s", c.name, code, raw)
			}
			if !c.header {
				wantNotStored(t, p.creds, c.secret, c.name)
				return
			}
			testAccount(t, p.base, "login")
			wantReached(t, l, "the login account")
			wantNotSent(t, l, "the login account", c.secret, c.name)
		})
	}
	// Its base_url moved to upstream c, its key kept: the same known limit as
	// TestConfigPlacementMovedBaseURL, recorded as today's behaviour: c gets the login's key.
	t.Run("a new base_url, its key kept", func(t *testing.T) {
		p, _ := startPlacementLogin(t)
		set := withSetting(loginSettings(t, p.base), "base_url", p.c.URL+"/v1")
		var raw json.RawMessage
		if code := manage(t, p.base, "PATCH", "/accounts/login", map[string]any{"settings": set}, &raw); code != http.StatusOK {
			t.Fatalf("PATCH /accounts/login with base_url moved to a new upstream: status %d: %s", code, raw)
		}
		testAccount(t, p.base, "login")
		if len(p.c.carrying(placeKeyLogin)) == 0 {
			t.Error("the login account's base_url moved: the new upstream did not receive its key; if core now refuses or drops the kept key, update this test and the README's note on the limit")
		}
	})
}

// Case 6, controls: edits that keep each placeholder in its own secret's place are taken, and
// each account goes on sending its own key, and only its own, to its own upstream.
func TestConfigPlacementControls(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(t *testing.T, cfg map[string]any)
		bID   string
		check func(t *testing.T, file string)
	}{
		{"the accounts reordered", func(t *testing.T, cfg map[string]any) {
			ext := cfg["extension"].(map[string]any)
			accounts := ext["accounts"].([]any)
			ext["accounts"] = []any{accounts[1], accounts[0]}
		}, "b", func(t *testing.T, file string) {
			if strings.Index(file, `"id": "b"`) > strings.Index(file, `"id": "a"`) {
				t.Errorf("the file does not have b first:\n%s", file)
			}
		}},
		{"an account renamed", func(t *testing.T, cfg map[string]any) {
			configAccount(t, cfg, "b")["id"] = "b-renamed"
		}, "b-renamed", nil},
		{"a non-secret field changed", func(t *testing.T, cfg map[string]any) {
			configAccount(t, cfg, "a")["priority"] = 5
		}, "b", func(t *testing.T, file string) {
			if !strings.Contains(file, `"priority": 5`) {
				t.Errorf("the file lacks the new priority:\n%s", file)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := startPlacement(t, false)
			r := p.config(t)
			p.putOK(t, c.name, editJSON(t, r.Content, func(cfg map[string]any) { c.edit(t, cfg) }), r.Hash)
			file := p.file(t)
			for _, s := range []string{managementKey, clientKey, placeKeyA, placeKeyB} {
				if strings.Count(file, s) != 1 {
					t.Errorf("secret %q is in the file %d times, want once:\n%s", s, strings.Count(file, s), file)
				}
			}
			if c.check != nil {
				c.check(t, file)
			}
			for _, a := range []struct {
				id         string
				u          *keyUpstream
				own, other string
			}{{"a", p.a, placeKeyA, placeKeyB}, {c.bID, p.b, placeKeyB, placeKeyA}} {
				testAccount(t, p.base, a.id)
				if len(a.u.carrying(a.own)) == 0 {
					t.Errorf("account %s's upstream did not receive its own key", a.id)
				}
				for _, s := range []string{a.other, managementKey, clientKey} {
					wantNotSent(t, a.u, "account "+a.id, s, c.name)
				}
			}
		})
	}
}
