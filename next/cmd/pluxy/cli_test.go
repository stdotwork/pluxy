package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/management"
)

// laterCommands are the management calls whose commands later changes add (the README's
// Resources tables name each one). The list only shrinks: a call added to core needs a command
// with it.
var laterCommands = []string{
	// pluxy config validate <file>, pluxy config apply <file>
	"POST /config/validate", "PUT /config",
	// pluxy settings set <name> <value>
	"PATCH /settings",
	// pluxy key rotate-management
	"POST /management-key/rotate",
	// pluxy logs clear, pluxy logs errors [name], pluxy logs request <id>
	"DELETE /logs", "GET /request-error-logs", "GET /request-error-logs/{name}", "GET /request-log-by-id/{id}",
	// pluxy ext config <id> [name=value]...
	"GET /extensions/{publisher}/{name}/config", "PATCH /extensions/{publisher}/{name}/config",
	// pluxy account methods
	"GET /login-methods",
	// pluxy account add <provider>: one sign-in from start to end
	"POST /accounts", "GET /logins/{id}", "POST /logins/{id}/input", "DELETE /logins/{id}",
	// pluxy account login <id>
	"POST /accounts/{id}/login",
	// pluxy account remove <id>
	"DELETE /accounts/{id}",
	// pluxy account refresh <id>
	"POST /accounts/{id}/refresh",
	// pluxy account import <file>
	"POST /accounts/import", "POST /vertex/import",
}

// noCommand are the management calls that get no command: they serve the panel's own pages.
var noCommand = []string{
	"GET /ui", // the extensions' pages and where the panel loads them from
}

// Every call core's management API serves has a pluxy command, or is listed above as one a
// later change adds or one that gets none; and every command calls routes core serves.
func TestEveryRouteHasACommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-management-key"}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := core.Start(core.Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	routes := c.Management().Routes()
	if len(routes) < 40 {
		t.Fatalf("core serves %d management calls, fewer than today's 43: the route list is not read whole", len(routes))
	}
	covered := map[string]string{}
	for _, cmd := range commands {
		for _, r := range cmd.routes {
			covered[r] = cmd.words
			if !slices.Contains(routes, r) {
				t.Errorf("pluxy %s calls %s, which core does not serve", cmd.words, r)
			}
		}
	}
	for _, r := range routes {
		_, has := covered[r]
		later, none := slices.Contains(laterCommands, r), slices.Contains(noCommand, r)
		switch {
		case !has && !later && !none:
			t.Errorf("core serves %s and no pluxy command calls it: add a command, or list it in laterCommands", r)
		case has && later:
			t.Errorf("pluxy %s calls %s: take it out of laterCommands", covered[r], r)
		}
	}
	for _, r := range append(slices.Clone(laterCommands), noCommand...) {
		if !slices.Contains(routes, r) {
			t.Errorf("%s is listed here but core does not serve it", r)
		}
	}
}

// chain set reads each step as model[@provider][:account,account].
func TestParseStep(t *testing.T) {
	for _, c := range []struct {
		in, model, provider string
		accounts            []string
		bad                 bool
	}{
		{in: "gpt-6.1-sol", model: "gpt-6.1-sol"},
		{in: "claude-opus-5-5@pluxy/claude", model: "claude-opus-5-5", provider: "pluxy/claude"},
		{in: "claude-opus-5-5@pluxy/claude:a,b", model: "claude-opus-5-5", provider: "pluxy/claude", accounts: []string{"a", "b"}},
		{in: "claude-opus-5-5:a", model: "claude-opus-5-5", accounts: []string{"a"}},
		{in: "llama3:8b@", model: "llama3:8b"},
		{in: "claude-opus-4-7@20260416", model: "claude-opus-4-7@20260416"},
		{in: "claude-opus-4-7@20260416:a", model: "claude-opus-4-7@20260416", accounts: []string{"a"}},
		{in: "claude-opus-4-7@20260416@pluxy/claude:a", model: "claude-opus-4-7@20260416", provider: "pluxy/claude", accounts: []string{"a"}},
		{in: "llama3:8b@pluxy/ollama", model: "llama3:8b", provider: "pluxy/ollama"},
		{in: "@pluxy/claude", bad: true},
		{in: "opus:", bad: true},
		{in: "opus:a,,b", bad: true},
	} {
		s, err := parseStep(c.in)
		switch {
		case c.bad && err == nil:
			t.Errorf("parseStep(%q) = %+v, want an error", c.in, s)
		case !c.bad && (err != nil || s.Model != c.model || s.Provider != c.provider || !slices.Equal(s.Accounts, c.accounts)):
			t.Errorf("parseStep(%q) = %+v, %v; want %s, %q, %v", c.in, s, err, c.model, c.provider, c.accounts)
		}
	}
}

// chain list writes each step so that chain set reads back the same step.
func TestStepTextRoundTrips(t *testing.T) {
	for _, model := range []string{"gpt-6.1-sol", "llama3:8b", "claude-opus-4-7@20260416", "x@pluxy/x", "x@pluxy/x:y", "x@", "a:b@c"} {
		for _, provider := range []string{"", "pluxy/claude"} {
			for _, accounts := range [][]string{nil, {"a"}, {"a", "b"}} {
				s := management.ChainStep{Model: model, Provider: provider, Accounts: accounts}
				got, err := parseStep(s.String())
				if err != nil || got.Model != s.Model || got.Provider != s.Provider || !slices.Equal(got.Accounts, s.Accounts) {
					t.Errorf("parseStep(%q) = %q %q %q, %v; want %q %q %q", s.String(), got.Model, got.Provider, got.Accounts, err, s.Model, s.Provider, s.Accounts)
				}
			}
		}
	}
	// A step that reads back as written gets no escape.
	for s, want := range map[*management.ChainStep]string{
		{Model: "gpt-6.1-sol"}:                         "gpt-6.1-sol",
		{Model: "llama3:8b", Accounts: []string{"a"}}:  "llama3:8b:a",
		{Model: "claude-opus-4-7@20260416"}:            "claude-opus-4-7@20260416",
		{Model: "llama3:8b"}:                           "llama3:8b@",
		{Model: "x@pluxy/x", Accounts: []string{"a"}}:  "x@pluxy/x@:a",
		{Model: "llama3:8b", Provider: "pluxy/ollama"}: "llama3:8b@pluxy/ollama",
	} {
		if got := s.String(); got != want {
			t.Errorf("%q %q %q written as %q, want %q", s.Model, s.Provider, s.Accounts, got, want)
		}
	}
}

// Exit codes and output of the client subcommands against a fake core.
func TestCLIOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid management key"}}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/client-keys":
			w.Write([]byte(`{"keys":[{"id":"k1","masked":"sk******ab"}]}` + "\n"))
		case "PATCH /v0/management/accounts/a%2Fb", "PATCH /v0/management/accounts/a/b":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if r.URL.EscapedPath() != "/v0/management/accounts/a%2Fb" || body["disabled"] != true {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"account":{"id":"a/b","source":"login","state":"active","disabled":true,"refreshable":false}}`))
		case "GET /v0/management/accounts/a1/models":
			w.Write([]byte(`{"models":[{"id":"m1","names":["m1"],"owned_by":"anthropic","context_length":200000,"max_completion_tokens":64000},{"id":"m2","names":[],"excluded":true,"owned_by":"anthropic"}],"from":"account"}` + "\n"))
		case "GET /v0/management/accounts/a%2Fb/models", "GET /v0/management/accounts/a/b/models":
			if r.URL.EscapedPath() != "/v0/management/accounts/a%2Fb/models" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"models":[{"id":"m1","names":["m1"]}],"from":"extension"}` + "\n"))
		case "POST /v0/management/reload":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"desired":{"revision":2,"at":"2026-11-19T00:00:00Z","error":"bad listen"},"active":{"revision":1,"at":"2026-11-19T00:00:00Z"},"extensions":[]}`))
		case "POST /v0/management/models/refresh":
			var body struct {
				Account string `json:"account"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Account == "nobody" {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":"not_found","message":"no account \"nobody\""}}`))
				return
			}
			if body.Account == "kept" {
				w.Write([]byte(`{"models":[{"id":"m1","upstream":"m1","extension":"pluxy/openai-compatible","accounts":["kept"]}],"accounts":[{"account":"kept","models":1,"kept":true,"error":"boom"}]}` + "\n"))
				return
			}
			w.Write([]byte(`{"models":[{"id":"m1","upstream":"m1","extension":"pluxy/openai-compatible","accounts":["a"]}],"accounts":[{"account":"a","models":1}]}` + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"nothing here"}}`))
		}
	}))
	defer srv.Close()
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		env := func(k string) string {
			return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
		}
		code := runCLI(args, &out, &errOut, env)
		return out.String(), errOut.String(), code
	}
	if out, _, code := run("key", "list", "--json"); code != 0 || out != `{"keys":[{"id":"k1","masked":"sk******ab"}]}`+"\n" {
		t.Errorf("key list --json: %d %q, want the body unchanged", code, out)
	}
	if out, _, code := run("key", "list"); code != 0 || !strings.Contains(out, "k1") || !strings.Contains(out, "sk******ab") {
		t.Errorf("key list: %d %q", code, out)
	}
	// Flags after the argument, and an ID that needs escaping.
	if out, errOut, code := run("account", "disable", "a/b", "--json"); code != 0 || !strings.Contains(out, `"disabled":true`) {
		t.Errorf("account disable a/b --json: %d %q %q", code, out, errOut)
	}
	if _, errOut, code := run("reload"); code != 6 || !strings.Contains(errOut, "bad listen") {
		t.Errorf("refused reload: %d %q, want 6 and the reason", code, errOut)
	}
	if out, errOut, code := run("account", "quota", "x", "--json"); code != 5 || out != "" || !strings.Contains(errOut, `"not_found"`) {
		t.Errorf("not found --json: %d %q %q, want 5 and the error object on standard error", code, out, errOut)
	}
	if _, _, code := run("status", "--key", "wrong"); code != 4 {
		t.Errorf("wrong key: %d, want 4", code)
	}
	for _, args := range [][]string{{"account"}, {"account", "enable"}, {"account", "enable", "a", "b"}, {"nope"}, {"status", "--bogus"}} {
		if _, _, code := run(args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if out, _, code := run("model", "refresh", "--json"); code != 0 || !strings.Contains(out, `"models":1`) {
		t.Errorf("model refresh --json: %d %q", code, out)
	}
	if out, _, code := run("model", "refresh", "--account", "kept"); code != 0 || !strings.Contains(out, "kept previous list: boom") {
		t.Errorf("model refresh --account kept: %d %q", code, out)
	}
	if _, _, code := run("model", "refresh", "--account", "nobody"); code != 5 {
		t.Errorf("model refresh --account nobody: %d, want 5", code)
	}
	if out, errOut, code := run("account", "models", "a1"); code != 0 || !strings.Contains(out, "from: account") || !strings.Contains(out, "m1") || !strings.Contains(out, "200000") || !strings.Contains(out, "64000") || !strings.Contains(out, "m2") {
		t.Errorf("account models a1: %d %q %q", code, out, errOut)
	}
	if out, errOut, code := run("account", "models", "a/b", "--json"); code != 0 || out != `{"models":[{"id":"m1","names":["m1"]}],"from":"extension"}`+"\n" {
		t.Errorf("account models a/b --json: %d %q %q", code, out, errOut)
	}
	if out, _, code := run("help"); code != 0 || !strings.Contains(out, "account enable <id>") {
		t.Errorf("help: %d %q", code, out)
	}
}

// findCore reads the address and the management key from the config, on this machine when
// core listens on every interface; flags and the environment win over it.
func TestFindCore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"listen": "0.0.0.0:8499", "client_keys": ["fake-client-key-1"], "management_key": "from-config"}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := findCore(path, "", "")
	if err != nil || got.url != "http://127.0.0.1:8499" || got.key != "from-config" {
		t.Errorf("from the config: %+v %v", got, err)
	}
	if got, err := findCore(path, "", "given"); err != nil || got.key != "given" || got.url != "http://127.0.0.1:8499" {
		t.Errorf("with a key: %+v %v", got, err)
	}
	if got, err := findCore(filepath.Join(t.TempDir(), "none.json"), "http://h:1", "k"); err != nil || got.url != "http://h:1" {
		t.Errorf("with both and no config: %+v %v", got, err)
	}
	if _, err := findCore(filepath.Join(t.TempDir(), "none.json"), "", ""); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Errorf("no config and no address: %v", err)
	}
}

func TestUsageCLI(t *testing.T) {
	var seenQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seenQueries = append(seenQueries, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/v0/management/usage":
			w.Write([]byte(`{"enabled":true,"total_requests":42,"success_count":40,"failure_count":2,"success_rate":0.95,"average_latency_ms":120,"tokens":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500},"by":"account","breakdown":[{"name":"acct-1","provider":"p/openai","total_requests":30,"success_count":29,"failure_count":1,"tokens":{"input_tokens":700,"output_tokens":300,"total_tokens":1000}}],"models":[{"name":"gpt-4o","total_requests":42,"success_count":40,"failure_count":2,"tokens":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500}}],"providers":[],"accounts":[],"keys":[],"bucket_seconds":86400,"series":[]}` + "\n"))
		case "/v0/management/usage/events":
			w.Write([]byte(`{"enabled":true,"events":[{"seq":9,"timestamp":"2026-11-19T12:00:00Z","endpoint":"/v1/chat/completions","model":"gpt-4o","account":"acct-1","key_id":"k1","failed":false,"status":200,"latency_ms":50,"tokens":{"total_tokens":15}}],"next":9,"more":false,"lost":false}` + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		env := func(k string) string {
			return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
		}
		code := runCLI(args, &out, &errOut, env)
		return out.String(), errOut.String(), code
	}

	out, errOut, code := run("usage", "--since", "30d", "--by", "account", "--client-key", "k1")
	if code != 0 || !strings.Contains(out, "acct-1") || !strings.Contains(out, "ACCOUNT") {
		t.Fatalf("pluxy usage --since 30d --by account: code=%d out=%q err=%q", code, out, errOut)
	}
	outJSON, errOut, code := run("usage", "--since", "30d", "--by", "account", "--json")
	if code != 0 || !strings.Contains(outJSON, `"by":"account"`) || !strings.Contains(outJSON, `"acct-1"`) {
		t.Fatalf("pluxy usage --json: code=%d out=%q err=%q", code, outJSON, errOut)
	}
	outEv, errOut, code := run("usage", "events", "--before", "--limit", "5", "--client-key", "k1")
	if code != 0 || !strings.Contains(outEv, "gpt-4o") || !strings.Contains(outEv, "acct-1") {
		t.Fatalf("pluxy usage events --before: code=%d out=%q err=%q", code, outEv, errOut)
	}
	// Verify query parameters passed to API.
	if !strings.Contains(seenQueries[0], "since=30d") || !strings.Contains(seenQueries[0], "by=account") || !strings.Contains(seenQueries[0], "key=k1") {
		t.Errorf("usage query = %q, want since=30d, by=account, key=k1", seenQueries[0])
	}
	if !strings.Contains(seenQueries[2], "before=0") || !strings.Contains(seenQueries[2], "limit=5") || !strings.Contains(seenQueries[2], "key=k1") {
		t.Errorf("usage events query = %q, want before=0, limit=5, key=k1", seenQueries[2])
	}
}

func TestResolveThemeID(t *testing.T) {
	if got := resolveThemeID("acme/custom", "/tmp/Foo.json", []byte(`{"id":"other/id"}`)); got != "acme/custom" {
		t.Errorf("flag wins: got %q", got)
	}
	if got := resolveThemeID("", "/tmp/Foo.json", []byte(`{"id":"acme/from-file"}`)); got != "acme/from-file" {
		t.Errorf("valid file id: got %q", got)
	}
	if got := resolveThemeID("", "/tmp/My Theme_v2.JSON", []byte(`{"id":"not-two-parts"}`)); got != "t3/my-theme-v2" {
		t.Errorf("fallback from filename: got %q", got)
	}
	if got := resolveThemeID("", "/tmp/Café.json", []byte(`{"name":"No ID"}`)); got != "t3/caf-" {
		t.Errorf("non-ascii filename: got %q", got)
	}
}
