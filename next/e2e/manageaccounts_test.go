package e2e

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

type cooldownView struct {
	Scope, Model, Kind string
	Until              time.Time
	Error              *struct{ Code, Message string }
}

type accountView struct {
	ID, Source, State string
	Disabled          bool
	Priority          int
	Weight            *int
	DisableCooling    *bool `json:"disable_cooling"`
	Prefix            string
	ExcludedModels    []string   `json:"excluded_models"`
	MaxConcurrency    int        `json:"max_concurrency"`
	RequestRetry      *int       `json:"request_retry"`
	CooldownUntil     *time.Time `json:"cooldown_until"`
	Cooldowns         []cooldownView
	LastError         *struct{ Code, Message string } `json:"last_error"`
	LastErrorAt       *time.Time                      `json:"last_error_at"`
	Settings          json.RawMessage
	SettingsInexact   bool   `json:"settings_inexact"`
	ProxyURL          string `json:"proxy_url"`
	Headers           map[string]string
}

// startAccountsCore runs core with the example OAuth provider and two accounts in its config
// whose tokens the fake provider answers with a reply ("fine") and with a 429 ("limited").
func startAccountsCore(t *testing.T, srv *oauthtest.Server) string {
	t.Helper()
	settings := map[string]string{"base_url": srv.URL}
	return runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{"id": "fine", "settings": settings, "models": []any{"m1", map[string]any{"name": "m2", "alias": "two"}},
					"priority": 2, "credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("text")}},
				map[string]any{"id": "limited", "settings": settings, "models": []string{"m1"},
					"credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("rate_limited")}},
			},
		},
	})
}

func getAccount(t *testing.T, base, id string) accountView {
	t.Helper()
	var list struct{ Accounts []accountView }
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
		t.Fatalf("GET /accounts: %d", code)
	}
	for _, a := range list.Accounts {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no account %q in %+v", id, list.Accounts)
	return accountView{}
}

// pinnedChat sends one chat request that must use account id and returns its status.
func pinnedChat(t *testing.T, base, id string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"m1","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluxy-Account", id)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// The account list shows an account's cooldowns, when the whole account is blocked until and
// the failure that blocked it; POST /accounts/{id}/cooldown/reset clears them, and the account
// serves again at once.
func TestManageAccountCooldowns(t *testing.T) {
	srv := oauthtest.New(t)
	base := startAccountsCore(t, srv)

	if code := pinnedChat(t, base, "limited"); code != http.StatusTooManyRequests {
		t.Fatalf("request on the limited account: %d, want 429", code)
	}
	a := getAccount(t, base, "limited")
	if len(a.Cooldowns) != 1 || a.Cooldowns[0].Scope != "credential" || a.Cooldowns[0].Kind != "rate_limit" ||
		a.Cooldowns[0].Error == nil || a.Cooldowns[0].Error.Code != "rate_limit" {
		t.Fatalf("cooldowns = %+v, want one credential rate limit", a.Cooldowns)
	}
	// The fake provider says Retry-After: 20.
	if a.CooldownUntil == nil || time.Until(*a.CooldownUntil) < 10*time.Second || time.Until(*a.CooldownUntil) > 21*time.Second {
		t.Fatalf("cooldown_until = %v, want about 20s from now", a.CooldownUntil)
	}
	if a.LastError == nil || a.LastError.Code != "rate_limit" || a.LastErrorAt == nil {
		t.Fatalf("last error = %+v at %v", a.LastError, a.LastErrorAt)
	}
	if f := getAccount(t, base, "fine"); len(f.Cooldowns) != 0 || f.LastError != nil || f.Priority != 2 {
		t.Fatalf("the other account = %+v, want no cooldowns and priority 2", f)
	}
	// While blocked, a pinned request does not reach the upstream.
	before := len(srv.ChatTokens())
	if code := pinnedChat(t, base, "limited"); code != http.StatusTooManyRequests || len(srv.ChatTokens()) != before {
		t.Fatalf("request while cooling: %d, upstream calls %d -> %d", code, before, len(srv.ChatTokens()))
	}

	var reset struct {
		Cleared int
		Account accountView
	}
	if code := manage(t, base, "POST", "/accounts/limited/cooldown/reset", nil, &reset); code != http.StatusOK {
		t.Fatalf("reset: %d", code)
	}
	if reset.Cleared != 1 || len(reset.Account.Cooldowns) != 0 || reset.Account.CooldownUntil != nil || reset.Account.LastError != nil {
		t.Fatalf("reset reply = %+v", reset)
	}
	if a := getAccount(t, base, "limited"); len(a.Cooldowns) != 0 || a.LastError != nil {
		t.Fatalf("after reset: %+v", a)
	}
	// The next request reaches the upstream again (which refuses it again).
	if code := pinnedChat(t, base, "limited"); code != http.StatusTooManyRequests || len(srv.ChatTokens()) != before+1 {
		t.Fatalf("after reset: %d, upstream calls %d -> %d, want one more", code, before, len(srv.ChatTokens()))
	}
	if code := manage(t, base, "POST", "/accounts/nobody/cooldown/reset", nil, nil); code != http.StatusNotFound {
		t.Fatalf("reset of an unknown account: %d, want 404", code)
	}
}

// PATCH /accounts/{id} changes a login account's routing fields along with disabled, checks them
// as the config's are checked and changes nothing when one is bad; null sets one back.
func TestManageAccountFields(t *testing.T) {
	srv := oauthtest.New(t)
	base, _ := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")

	var reply struct{ Account accountView }
	if code := manage(t, base, "PATCH", "/accounts/work", map[string]any{
		"priority": 3, "weight": 2, "prefix": "w", "excluded_models": []string{"x-*"}, "max_concurrency": 4, "disabled": true,
		"disable_cooling": true, "request_retry": 0,
	}, &reply); code != http.StatusOK {
		t.Fatalf("PATCH: %d", code)
	}
	a := reply.Account
	if a.Priority != 3 || a.Weight == nil || *a.Weight != 2 || a.Prefix != "w" || !slices.Equal(a.ExcludedModels, []string{"x-*"}) ||
		a.MaxConcurrency != 4 || !a.Disabled || a.DisableCooling == nil || !*a.DisableCooling || a.RequestRetry == nil || *a.RequestRetry != 0 {
		t.Fatalf("after PATCH: %+v", a)
	}
	if a := getAccount(t, base, "work"); a.Priority != 3 || a.Prefix != "w" || !a.Disabled || a.RequestRetry == nil || *a.RequestRetry != 0 {
		t.Fatalf("listed after PATCH: %+v", a)
	}

	for _, bad := range []map[string]any{
		{"prefix": "a/b", "priority": 9},
		{"max_concurrency": -1},
		{"request_retry": -1},
		{"request_retry": "2"},
		{"priority": "high"},
		{"disabled": nil},
		{},
	} {
		if code := manage(t, base, "PATCH", "/accounts/work", bad, nil); code != http.StatusBadRequest {
			t.Errorf("PATCH %v: %d, want 400", bad, code)
		}
	}
	if a := getAccount(t, base, "work"); a.Priority != 3 || a.Prefix != "w" {
		t.Fatalf("a refused PATCH changed the account: %+v", a)
	}

	var cleared struct{ Account accountView }
	if code := manage(t, base, "PATCH", "/accounts/work", map[string]any{"weight": nil, "prefix": nil, "disable_cooling": nil, "request_retry": nil, "disabled": false}, &cleared); code != http.StatusOK {
		t.Fatalf("PATCH with nulls: %d", code)
	}
	if a := cleared.Account; a.Weight != nil || a.Prefix != "" || a.DisableCooling != nil || a.RequestRetry != nil || a.Disabled || a.Priority != 3 {
		t.Fatalf("after nulls: %+v", a)
	}
}

// PATCH /accounts/{id} replaces a login account's settings. GET /accounts shows them with a
// placeholder in place of each secret, never the secret, and a placeholder sent back keeps the
// secret it stands for. An account whose settings change starts with fresh routing state; one
// sent its settings unchanged keeps it.
func TestManageAccountSettings(t *testing.T) {
	pkg := testPackage(t, "0.1.0")
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension":       map[string]any{"manifest": filepath.Join(pkg, "pluxy-extension.json"), "accounts": []any{}},
	})
	const secret = "sk-account-settings-e2e"
	// Every value below a key named like a secret is one too, however deep.
	hidden := []string{secret, "sk-nested-object-e2e", "sk-nested-list-e2e", "sk-other-e2e", "sk-first-e2e", "sk-second-e2e", "e2e-proxy-pass", "sk-header-e2e"}
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "keyed", "method": "settings",
		"input": map[string]string{"settings": `{"base_url":"https://one.example/v1","api_key":"` + secret +
			`","password":{"primary":"sk-nested-object-e2e"},"api_keys":[["sk-nested-list-e2e"]],"secret_key":"sk-other-e2e",` +
			`"client_keys":["sk-first-e2e","sk-second-e2e"]}`}}, &l); code != http.StatusAccepted {
		t.Fatalf("login: %d", code)
	}
	for deadline := time.Now().Add(10 * time.Second); l.Login.State == "pending" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login: %+v", l.Login)
	}

	placeholder := regexp.MustCompile(`^pluxy-redacted-[0-9a-f]{24}$`)
	// listed returns the account's settings as GET /accounts shows them, checking that no reply
	// of the management API holds the secret.
	listed := func(reply []byte) map[string]any {
		t.Helper()
		for _, h := range hidden {
			if strings.Contains(string(reply), h) {
				t.Fatalf("the management API showed the secret %s: %s", h, reply)
			}
		}
		var list struct{ Accounts []accountView }
		var raw json.RawMessage
		if code := manage(t, base, "GET", "/accounts", nil, &raw); code != http.StatusOK {
			t.Fatalf("GET /accounts: %d", code)
		}
		for _, h := range hidden {
			if strings.Contains(string(raw), h) {
				t.Fatalf("GET /accounts showed the secret %s: %s", h, raw)
			}
		}
		_ = json.Unmarshal(raw, &list)
		for _, a := range list.Accounts {
			if a.ID == "keyed" {
				var s map[string]any
				if err := json.Unmarshal(a.Settings, &s); err != nil {
					t.Fatalf("settings %s: %v", a.Settings, err)
				}
				return s
			}
		}
		t.Fatal("the account is not listed")
		return nil
	}
	// received returns the settings the extension receives with a request.
	received := func() map[string]any {
		t.Helper()
		var reply struct {
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(askOK(t, base, "account-settings")), &reply); err != nil || len(reply.Choices) != 1 {
			t.Fatalf("reply: %v", err)
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSuffix(reply.Choices[0].Message.Content, " done")), &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	patch := func(settings any) (int, []byte) {
		t.Helper()
		var raw json.RawMessage
		code := manage(t, base, "PATCH", "/accounts/keyed", map[string]any{"settings": settings}, &raw)
		return code, raw
	}
	cooldowns := func() int { t.Helper(); return len(getAccount(t, base, "keyed").Cooldowns) }

	s := listed(nil)
	key, _ := s["api_key"].(string)
	if !placeholder.MatchString(key) || s["base_url"] != "https://one.example/v1" {
		t.Fatalf("listed settings = %v, want base_url as set and api_key as a placeholder", s)
	}
	if code, body := ask(t, base, "rate-limited"); code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited model: %d %s", code, body)
	}
	if cooldowns() == 0 {
		t.Fatal("no cooldown after the rate-limited request")
	}

	// The settings sent back as listed: the secret stays and so does the routing state.
	code, reply := patch(s)
	if code != http.StatusOK {
		t.Fatalf("PATCH with the listed settings: %d %s", code, reply)
	}
	if got := listed(reply); got["api_key"] != key {
		t.Fatalf("after PATCH: %v, want the same placeholder", got)
	}
	if got := received(); got["api_key"] != secret || got["base_url"] != "https://one.example/v1" {
		t.Fatalf("the extension received %v after PATCH, want the secret kept", got)
	}
	if cooldowns() == 0 {
		t.Fatal("unchanged settings dropped the account's cooldown")
	}

	// A refused change changes nothing.
	for _, bad := range []struct {
		settings any
		code     int
	}{
		{"text", http.StatusBadRequest},
		{[]string{"a"}, http.StatusBadRequest},
		{map[string]any{"base_url": key, "api_key": key}, http.StatusBadRequest},                    // a placeholder where no secret goes
		{map[string]any{"api_key": "pluxy-redacted-000000000000000000000000"}, http.StatusConflict}, // matches no secret the account holds
		{map[string]any{"api_key": s["secret_key"], "secret_key": key}, http.StatusBadRequest},      // another secret's placeholder
	} {
		if code, reply := patch(bad.settings); code != bad.code {
			t.Errorf("PATCH settings %v: %d %s, want %d", bad.settings, code, reply, bad.code)
		}
	}
	if got := received(); got["api_key"] != secret || got["secret_key"] != "sk-other-e2e" || got["base_url"] != "https://one.example/v1" || cooldowns() == 0 {
		t.Fatalf("a refused PATCH changed the account: %v", got)
	}

	// Within a list, placeholders may move: the secrets move with them.
	order, _ := s["client_keys"].([]any)
	if len(order) != 2 {
		t.Fatalf("client_keys listed as %v", s["client_keys"])
	}
	moved := maps.Clone(s)
	moved["client_keys"] = []any{order[1], order[0]}
	if code, reply := patch(moved); code != http.StatusOK {
		t.Fatalf("PATCH with the list reordered: %d %s", code, reply)
	}
	if got := received(); !reflect.DeepEqual(got["client_keys"], []any{"sk-second-e2e", "sk-first-e2e"}) || got["api_key"] != secret {
		t.Fatalf("the extension received %v, want client_keys reordered", got)
	}

	// Changed settings, with the secret kept by its placeholder: the account starts fresh.
	if code, reply := patch(map[string]any{"base_url": "https://two.example/v1", "api_key": key, "websockets": true}); code != http.StatusOK {
		t.Fatalf("PATCH with new settings: %d %s", code, reply)
	} else if got := listed(reply); got["base_url"] != "https://two.example/v1" || got["api_key"] != key || got["websockets"] != true {
		t.Fatalf("after PATCH: %v", got)
	}
	if got := received(); got["api_key"] != secret || got["base_url"] != "https://two.example/v1" {
		t.Fatalf("the extension received %v, want the new base_url and the kept secret", got)
	}
	if n := cooldowns(); n != 0 {
		t.Fatalf("%d cooldowns after the settings changed, want none", n)
	}

	// Settings holding a number that reading it as a float64 changes are marked, so a panel that
	// reads numbers that way does not write them back changed. A secret's number is never shown.
	for _, c := range []struct {
		n       string
		inexact bool
	}{
		{"0.10000000000000001", true}, {"1e-400", true}, {"1e400", true}, {"9007199254740993", true},
		{"0.5", false}, {"0.1", false}, {"1.0", false}, {"-0", false}, {"2e3", false}, {"9007199254740994", false}, {"1e300", false},
	} {
		if code, reply := patch(map[string]any{"api_key": key, "n": json.RawMessage(c.n), "password": json.RawMessage("2e-400")}); code != http.StatusOK {
			t.Fatalf("PATCH with n %s: %d %s", c.n, code, reply)
		}
		if a := getAccount(t, base, "keyed"); len(a.Settings) == 0 || a.SettingsInexact != c.inexact {
			t.Errorf("n %s: settings_inexact = %v, want %v; settings %s", c.n, a.SettingsInexact, c.inexact, a.Settings)
		}
	}

	// Settings and the account's proxy_url in one PATCH: both change, the proxy's password never
	// shows, and the settings' placeholder keeps its secret. (That the proxy shown and sent back
	// keeps its password is TestPatchProxyURLRoundTrip's.)
	both := func(body map[string]any) accountView {
		t.Helper()
		var raw json.RawMessage
		if code := manage(t, base, "PATCH", "/accounts/keyed", body, &raw); code != http.StatusOK {
			t.Fatalf("PATCH %v: %d %s", body, code, raw)
		}
		listed(raw)
		return getAccount(t, base, "keyed")
	}
	a := both(map[string]any{"settings": map[string]any{"api_key": key, "proxied": true}, "proxy_url": "http://e2e-user:e2e-proxy-pass@127.0.0.1:9"})
	if !strings.Contains(a.ProxyURL, "redacted") || !strings.Contains(string(a.Settings), `"proxied":true`) {
		t.Fatalf("after PATCH of settings and proxy_url: proxy_url %q, settings %s", a.ProxyURL, a.Settings)
	}
	shown := a.ProxyURL
	a = both(map[string]any{"settings": map[string]any{"api_key": key, "proxied": false}, "proxy_url": shown})
	if a.ProxyURL != shown || !strings.Contains(string(a.Settings), `"proxied":false`) || !strings.Contains(string(a.Settings), key) {
		t.Fatalf("after PATCH with the shown proxy_url and placeholder: proxy_url %q, settings %s", a.ProxyURL, a.Settings)
	}
	if a = both(map[string]any{"proxy_url": nil}); a.ProxyURL != "" {
		t.Fatalf("proxy_url after null: %q", a.ProxyURL)
	}
	if got := received(); got["api_key"] != secret || got["proxied"] != false {
		t.Fatalf("the extension received %v, want the kept secret and proxied false", got)
	}

	// Headers and settings in one PATCH, each with its placeholders kept. A stale settings
	// placeholder is a 409 and a stale header placeholder a 400, and neither changes anything.
	a = both(map[string]any{"settings": map[string]any{"api_key": key, "proxied": true}, "headers": map[string]string{"X-Api-Key": "sk-header-e2e", "X-Plain": "one"}})
	hk := a.Headers["X-Api-Key"]
	if !placeholder.MatchString(hk) || a.Headers["X-Plain"] != "one" || !strings.Contains(string(a.Settings), `"proxied":true`) {
		t.Fatalf("after PATCH of headers and settings: headers %v, settings %s", a.Headers, a.Settings)
	}
	stale := "pluxy-redacted-000000000000000000000000"
	for _, bad := range []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"settings": map[string]any{"api_key": stale}, "headers": map[string]string{"X-Api-Key": hk, "X-Plain": "two"}}, http.StatusConflict},
		{map[string]any{"settings": map[string]any{"api_key": key, "proxied": false}, "headers": map[string]string{"X-Api-Key": stale}}, http.StatusBadRequest},
	} {
		var raw json.RawMessage
		if code := manage(t, base, "PATCH", "/accounts/keyed", bad.body, &raw); code != bad.code {
			t.Errorf("PATCH %v: %d %s, want %d", bad.body, code, raw, bad.code)
		}
	}
	if now := getAccount(t, base, "keyed"); now.Headers["X-Plain"] != "one" || !strings.Contains(string(now.Settings), `"proxied":true`) {
		t.Fatalf("a refused PATCH changed the account: headers %v, settings %s", now.Headers, now.Settings)
	}
	a = both(map[string]any{"settings": map[string]any{"api_key": key, "proxied": false}, "headers": a.Headers})
	if a.Headers["X-Api-Key"] != hk || a.Headers["X-Plain"] != "one" || !strings.Contains(string(a.Settings), `"proxied":false`) {
		t.Fatalf("after PATCH with the shown headers and settings: headers %v, settings %s", a.Headers, a.Settings)
	}
	// The test extension takes no headers, so it is asked once they are gone.
	if a = both(map[string]any{"headers": nil}); len(a.Headers) != 0 {
		t.Fatalf("headers after null: %v", a.Headers)
	}
	if got := received(); got["api_key"] != secret || got["proxied"] != false {
		t.Fatalf("the extension received %v, want the kept secret", got)
	}

	// A new secret replaces the old one; null removes the settings.
	if code, reply := patch(map[string]any{"api_key": "sk-new"}); code != http.StatusOK || strings.Contains(string(reply), "sk-new") {
		t.Fatalf("PATCH with a new secret: %d %s", code, reply)
	}
	if got := received(); got["api_key"] != "sk-new" || got["base_url"] != nil {
		t.Fatalf("the extension received %v, want only the new secret", got)
	}
	if code, reply := patch(nil); code != http.StatusOK {
		t.Fatalf("PATCH settings null: %d %s", code, reply)
	}
	if a := getAccount(t, base, "keyed"); len(a.Settings) != 0 {
		t.Fatalf("settings after null: %s", a.Settings)
	}
}

// GET /login-methods lists each running extension that signs in, with its methods and the
// inputs each asks for.
func TestManageLoginMethods(t *testing.T) {
	base, _ := startOAuthCore(t)
	var reply struct {
		Extensions []struct {
			Extension string
			Methods   []struct {
				Method, Kind, Label string
				Inputs              []struct {
					Name, Label string
					Secret      bool
				}
			}
		}
	}
	if code := manage(t, base, "GET", "/login-methods", nil, &reply); code != http.StatusOK {
		t.Fatalf("GET /login-methods: %d", code)
	}
	if len(reply.Extensions) != 1 || reply.Extensions[0].Extension != "example/oauth" {
		t.Fatalf("extensions = %+v", reply.Extensions)
	}
	var methods []string
	for _, m := range reply.Extensions[0].Methods {
		methods = append(methods, m.Method+":"+m.Kind)
		if m.Method == "api_key" && (len(m.Inputs) != 1 || m.Inputs[0].Name != "api_key" || !m.Inputs[0].Secret) {
			t.Errorf("api_key inputs = %+v", m.Inputs)
		}
	}
	if !slices.Equal(methods, []string{"device:device", "browser:browser", "api_key:api_key"}) {
		t.Fatalf("methods = %v", methods)
	}
}

// GET /accounts/{id}/models lists the models an account serves with the names clients use for
// them; POST /accounts/{id}/test sends the account one small request straight to its extension
// and reports the outcome, leaving routing state alone.
func TestManageAccountModelsAndTest(t *testing.T) {
	srv := oauthtest.New(t)
	base := startAccountsCore(t, srv)

	var models struct {
		Models []struct {
			ID       string
			Names    []string
			Excluded bool
		}
	}
	if code := manage(t, base, "GET", "/accounts/fine/models", nil, &models); code != http.StatusOK {
		t.Fatalf("GET models: %d", code)
	}
	if len(models.Models) != 2 || models.Models[0].ID != "m1" || !slices.Equal(models.Models[0].Names, []string{"m1"}) ||
		models.Models[1].ID != "m2" || !slices.Equal(models.Models[1].Names, []string{"two"}) {
		t.Fatalf("models = %+v", models.Models)
	}
	if code := manage(t, base, "GET", "/accounts/nobody/models", nil, nil); code != http.StatusNotFound {
		t.Fatalf("models of an unknown account: %d, want 404", code)
	}

	type testReply struct {
		Result struct {
			OK        bool
			Model     string
			LatencyMS int64 `json:"latency_ms"`
			Reply     string
			Error     *struct{ Code, Message string }
		}
	}
	var ok testReply
	if code := manage(t, base, "POST", "/accounts/fine/test", nil, &ok); code != http.StatusOK {
		t.Fatalf("test: %d", code)
	}
	if !ok.Result.OK || ok.Result.Model != "m1" || ok.Result.Reply == "" || ok.Result.Error != nil {
		t.Fatalf("test of the working account = %+v", ok.Result)
	}
	var named testReply
	if code := manage(t, base, "POST", "/accounts/fine/test", map[string]string{"model": "m2"}, &named); code != http.StatusOK || named.Result.Model != "m2" || !named.Result.OK {
		t.Fatalf("test with a model: %d %+v", code, named.Result)
	}

	var failed testReply
	if code := manage(t, base, "POST", "/accounts/limited/test", nil, &failed); code != http.StatusOK {
		t.Fatalf("test: %d", code)
	}
	if failed.Result.OK || failed.Result.Error == nil || failed.Result.Error.Code != "rate_limit" {
		t.Fatalf("test of the limited account = %+v", failed.Result)
	}
	if a := getAccount(t, base, "limited"); len(a.Cooldowns) != 0 || a.LastError != nil {
		t.Fatalf("a test changed routing state: %+v", a)
	}
	if code := manage(t, base, "POST", "/accounts/nobody/test", nil, nil); code != http.StatusNotFound {
		t.Fatalf("test of an unknown account: %d, want 404", code)
	}
}

// GET /accounts/{id}/quota reads the account's plan and usage through its extension (the
// protocol's quota call); an account with no plan to report gets 422.
func TestManageAccountQuota(t *testing.T) {
	srv := oauthtest.New(t)
	settings := map[string]string{"base_url": srv.URL}
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{"id": "fine", "settings": settings, "credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("text")}},
				map[string]any{"id": "full", "settings": settings, "credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("rate_limited")}},
				map[string]any{"id": "key", "settings": settings, "credential": map[string]any{"kind": "api_key", "api_key": "k-e2e"}},
			},
		},
	})
	type quotaReply struct {
		Quota struct {
			Plan   string
			Limits []struct {
				ID, Label     string
				UsedPercent   *float64 `json:"used_percent"`
				ResetsAt      int64    `json:"resets_at"`
				WindowSeconds int64    `json:"window_seconds"`
			}
			Raw map[string]any
		}
		FetchedAt time.Time `json:"fetched_at"`
		Error     *struct{ Code, Message string }
	}
	for id, want := range map[string]float64{"fine": 25, "full": 100} {
		var q quotaReply
		if code := manage(t, base, "GET", "/accounts/"+id+"/quota", nil, &q); code != http.StatusOK {
			t.Fatalf("quota of %s: %d %+v", id, code, q.Error)
		}
		l := q.Quota.Limits
		if q.Quota.Plan != "test" || len(l) != 1 || l[0].ID != "window" || l[0].Label != "5-hour limit" || l[0].UsedPercent == nil ||
			*l[0].UsedPercent != want || l[0].ResetsAt != oauthtest.QuotaResetsAt || l[0].WindowSeconds != 18000 || q.Quota.Raw["plan"] != "test" || q.FetchedAt.IsZero() {
			t.Fatalf("quota of %s = %+v", id, q)
		}
	}
	var unsupported quotaReply
	if code := manage(t, base, "GET", "/accounts/key/quota", nil, &unsupported); code != http.StatusUnprocessableEntity || unsupported.Error == nil || unsupported.Error.Code != "unsupported" {
		t.Fatalf("quota of an API key account: %d %+v, want 422 unsupported", code, unsupported.Error)
	}
	if code := manage(t, base, "GET", "/accounts/nobody/quota", nil, nil); code != http.StatusNotFound {
		t.Fatalf("quota of an unknown account: %d, want 404", code)
	}
}

// reauthDevice signs in to existing account id again by device login and returns how the login
// ended.
func reauthDevice(t *testing.T, base string, srv *oauthtest.Server, id string) loginReply {
	t.Helper()
	var l loginReply
	if code := manage(t, base, "POST", "/accounts/"+id+"/login", map[string]any{"method": "device"}, &l); code != http.StatusAccepted || l.Login.Prompt == nil {
		t.Fatalf("starting the new login: status %d, %+v", code, l.Login)
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
	return l
}

// A new login to an existing account that signs in as someone else is refused and leaves the
// account's credential as it was; one that signs in as the same user (letter case aside) replaces
// it, as today's core does.
func TestReloginIdentityCheck(t *testing.T) {
	srv := oauthtest.New(t)
	base, _ := startOAuthCore(t)
	deviceLogin(t, base, srv, "work")
	var list struct {
		Accounts []struct {
			ID      string
			Version uint64
			Label   string
		}
	}
	manage(t, base, "GET", "/accounts", nil, &list)
	version := list.Accounts[0].Version

	srv.UserID = "user-2"
	l := reauthDevice(t, base, srv, "work")
	if l.Login.State != "failed" || l.Login.Error == nil || l.Login.Error.Code != "invalid_request" || !strings.Contains(l.Login.Error.Message, "different account") {
		t.Fatalf("login as another user ended %s with %+v, want failed: different account", l.Login.State, l.Login.Error)
	}
	manage(t, base, "GET", "/accounts", nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].Version != version {
		t.Fatalf("the refused login changed the account: %+v (version was %d)", list.Accounts, version)
	}
	if code := pinnedChat(t, base, "work"); code != http.StatusOK {
		t.Fatalf("after the refused login the account answers %d", code)
	}

	srv.UserID, srv.Email = "user-1", "USER@example.test"
	if l := reauthDevice(t, base, srv, "work"); l.Login.State != "done" {
		t.Fatalf("login as the same user ended %s with %+v", l.Login.State, l.Login.Error)
	}
	manage(t, base, "GET", "/accounts", nil, &list)
	if list.Accounts[0].Version == version || list.Accounts[0].Label != "USER@example.test" {
		t.Fatalf("the accepted login did not replace the credential: %+v", list.Accounts)
	}
}
