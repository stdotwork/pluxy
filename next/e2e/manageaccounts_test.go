package e2e

import (
	"io"
	"net/http"
	"path/filepath"
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
		{"settings": map[string]string{"base_url": "https://elsewhere.example"}},
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
