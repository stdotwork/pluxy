package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	// pluxy ext config <id> [name=value]...
	"GET /extensions/{publisher}/{name}/config", "PATCH /extensions/{publisher}/{name}/config",
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
		case "POST /v0/management/client-keys":
			w.Write([]byte(`{"key":{"id":"k2","masked":"sk******cd"},"value":"sk-new-key"}` + "\n"))
		case "GET /v0/management/status":
			// A newer core whose version field changed type.
			w.Write([]byte(`{"version":5,"active":{"revision":1}}` + "\n"))
		case "GET /v0/management/config":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"error":{"code":"invalid_config","message":"the config file does not parse, so it cannot be shown with its secrets hidden: bad"}}`))
		case "GET /v0/management/settings":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"error":{"code":"invalid_config","message":"the settings do not parse"}}`))
		case "PUT /v0/management/themes/t3/f":
			// A newer core that counts a theme's bytes differently.
			w.Write([]byte(`{"id":"t3/f","bytes":"12"}` + "\n"))
		case "PATCH /v0/management/accounts/a%2Fb", "PATCH /v0/management/accounts/a/b":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if r.URL.EscapedPath() != "/v0/management/accounts/a%2Fb" || body["disabled"] != true {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte(`{"account":{"id":"a/b","source":"login","state":"active","disabled":true,"refreshable":false}}`))
		case "GET /v0/management/route":
			w.Write([]byte(`{"model":"m","order":[{"id":"soon","account":"acc_1","provider":"p","model":"m","rank":{"reset":"2026-11-21T10:00:00Z","window_seconds":604800}},` +
				`{"id":"unread","account":"acc_2","provider":"p","model":"m","rank":{}},` +
				`{"id":"spent","account":"acc_3","provider":"p","model":"m","rank":{"reset":"2026-11-20T10:00:00Z","window_seconds":604800,"full_until":"2026-11-20T08:00:00Z"}}],"left_out":[]}`))
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
	// key add prints the new key on stdout and says on stderr that it is not shown again; --json
	// prints the reply alone.
	if out, errOut, code := run("key", "add"); code != 0 || out != "sk-new-key\n" || errOut != "Save this key now: pluxy will not print it again.\n" {
		t.Fatalf("key add: code=%d out=%q err=%q", code, out, errOut)
	}
	if out, errOut, code := run("key", "add", "--json"); code != 0 || errOut != "" || !strings.HasPrefix(out, `{"key":`) {
		t.Fatalf("key add --json: code=%d out=%q err=%q", code, out, errOut)
	}
	if out, _, code := run("key", "list", "--json"); code != 0 || out != `{"keys":[{"id":"k1","masked":"sk******ab"}]}`+"\n" {
		t.Errorf("key list --json: %d %q, want the body unchanged", code, out)
	}
	// --json prints the reply body unchanged even when a field's type changed in a newer core.
	if out, errOut, code := run("status", "--json"); code != 0 || out != `{"version":5,"active":{"revision":1}}`+"\n" || errOut != "" {
		t.Errorf("status --json with a changed field type: %d %q %q, want the body unchanged", code, out, errOut)
	}
	// A 422 on a command that changes nothing did not refuse a change.
	if _, errOut, code := run("config", "show"); code != 1 || !strings.Contains(errOut, "does not parse") {
		t.Errorf("config show on a config that does not parse: %d %q, want 1 and the reason", code, errOut)
	}
	// So does any other command that only reads, with no mark of its own.
	if _, errOut, code := run("settings"); code != 1 || !strings.Contains(errOut, "do not parse") {
		t.Errorf("settings on settings that do not parse: %d %q, want 1 and the reason", code, errOut)
	}
	// A one-part theme ID is a usage error, not a path with an empty part.
	for _, args := range [][]string{{"theme", "show", "default"}, {"theme", "remove", "default"}} {
		if _, errOut, code := run(args...); code != 2 || !strings.Contains(errOut, "publisher/name") {
			t.Errorf("%q: exit %d %q, want 2 and how to write the ID", args, code, errOut)
		}
	}
	// theme add --json prints the reply body unchanged too.
	themeFile := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(themeFile, []byte(`{"name":"F"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := run("theme", "add", themeFile, "--json"); code != 0 || out != `{"id":"t3/f","bytes":"12"}`+"\n" || errOut != "" {
		t.Errorf("theme add --json with a changed field type: %d %q %q, want the body unchanged", code, out, errOut)
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
	// Under soonest-reset each account shows the reset it was ranked by.
	out, _, code := run("route", "m")
	soon := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04") + " (7d window)"
	spent := "used up until " + time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC).Local().Format(time.DateTime)
	if lines := strings.Split(out, "\n"); code != 0 || len(lines) < 4 || !strings.Contains(lines[0], "RESET") ||
		!strings.Contains(lines[1], soon) || !strings.Contains(lines[2], "none known") || !strings.Contains(lines[3], spent) {
		t.Errorf("route m: %d %q, want each account's reset", code, out)
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

// The Imports table says which setup core has brought across and when, and "no" for one it has not.
func TestCLISetupImportsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"first_run":false,"scanning":false,"scanned_at":"2026-11-19T12:00:00Z","found":{"clients":[],"sign_ins":[],"imports":[{"kind":"cliproxyapi","config":"~/.cli-proxy-api/config.yaml","auth_dir":"~/.cli-proxy-api","accounts":3,"last_import":{"at":"2026-11-20T08:30:05Z","brought":4,"changed":2,"not_brought":2}},{"kind":"cliproxyapi","config":"~/other/config.yaml","auth_dir":"~/other","accounts":1}],"core":{"listen":"127.0.0.1:8317","reachable_from_network":false}},"picks":[]}`))
	}))
	defer srv.Close()
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "mgmt-secret"}[k]
	}
	var out, errOut bytes.Buffer
	if code := runCLI([]string{"setup"}, &out, &errOut, env); code != 0 {
		t.Fatalf("setup: code=%d err=%q", code, errOut.String())
	}
	want := "Imports:\n" +
		"KIND         CONFIG                        AUTH DIR          ACCOUNTS  BROUGHT ACROSS\n" +
		"cliproxyapi  ~/.cli-proxy-api/config.yaml  ~/.cli-proxy-api  3         2026-11-20T08:30:05Z\n" +
		"cliproxyapi  ~/other/config.yaml           ~/other           1         no\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("setup printed:\n%s\nwant it to hold:\n%s", out.String(), want)
	}
}

// Setup subcommands (TTY vs non-TTY, --yes, --pick, partial failure) and client show.
func TestCLISetupAndClientShow(t *testing.T) {
	var applied [][]string
	var doneCalled bool
	// The paste route's requests (path and query), so a test can tell the CLI asked core for the
	// snippet rather than building it itself.
	var pasteMu sync.Mutex
	var pasteCalls []string
	takePasteCalls := func() []string {
		pasteMu.Lock()
		defer pasteMu.Unlock()
		calls := pasteCalls
		pasteCalls = nil
		return calls
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid management key"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/paste") {
			pasteMu.Lock()
			pasteCalls = append(pasteCalls, r.Method+" "+r.URL.RequestURI())
			pasteMu.Unlock()
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/setup":
			w.Write([]byte(`{"first_run":true,"scanning":false,"scanned_at":"2026-11-19T12:00:00Z","found":{"clients":[{"id":"claude-code","name":"Claude Code","installed":true,"version":"2.4.1","config":"~/.claude/settings.json","state":"not-configured"}],"sign_ins":[],"imports":[],"core":{"listen":"127.0.0.1:8317","reachable_from_network":false}},"picks":[{"id":"pluxy/claude","kind":"provider","title":"Claude","summary":"Claude","installed":false,"suggested":true,"because":"Claude Code is installed"},{"id":"pluxy/openai-compatible","kind":"provider","title":"OpenAI","summary":"OpenAI","installed":false,"suggested":false}]}`))
		case "POST /v0/management/setup/scan":
			w.Write([]byte(`{"first_run":true,"scanning":true,"found":{"clients":[],"sign_ins":[],"imports":[],"core":{"listen":"127.0.0.1:8317","reachable_from_network":false}},"picks":[]}`))
		case "POST /v0/management/setup/apply":
			var req struct {
				Picks []string `json:"picks"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			applied = append(applied, req.Picks)
			if slices.Contains(req.Picks, "bad/pkg") {
				w.Write([]byte(`{"results":[{"id":"pluxy/claude","status":"done","package":{"id":"pluxy/claude","version":"0.1.0","registry":"bundled","url":"","sha256":"","fingerprint":"","enabled":true,"installed_at":"2026-11-19T12:00:00Z","state":"active"}},{"id":"bad/pkg","status":"error","error":"not found"}]}`))
				return
			}
			w.Write([]byte(`{"results":[{"id":"pluxy/claude","status":"done","package":{"id":"pluxy/claude","version":"0.1.0","registry":"bundled","url":"","sha256":"","fingerprint":"","enabled":true,"installed_at":"2026-11-19T12:00:00Z","state":"active"}}]}`))
		case "POST /v0/management/setup/done":
			doneCalled = true
			w.Write([]byte(`{"first_run":false}`))
		case "GET /v0/management/client-keys":
			w.Write([]byte(`{"keys":[{"id":"k1","masked":"sk******ab"}]}`))
		case "GET /v0/management/setup/clients/claude-code/paste", "GET /v0/management/setup/clients/codex/paste",
			"GET /v0/management/setup/clients/opencode/paste", "GET /v0/management/setup/clients/pi/paste",
			"GET /v0/management/setup/clients/omp/paste":
			key := "<client-key>"
			switch r.URL.Query().Get("key_id") {
			case "":
			case "k1":
				key = "<client-key:k1 (sk******ab)>"
			default:
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":"not_found","message":"no client key with that id"}}`))
				return
			}
			tool := strings.Split(strings.TrimPrefix(r.URL.Path, "/v0/management/setup/clients/"), "/")[0]
			snip, _ := core.ClientSnippetFor(tool, "http://"+r.Host, key)
			reply := management.ClientPaste{Tool: snip.Tool, Name: snip.Name, BaseURL: "http://" + r.Host, Key: key, Note: snip.Note}
			for _, f := range snip.Files {
				reply.Files = append(reply.Files, management.ClientPasteFile{Path: f.Path, Content: f.Content})
			}
			json.NewEncoder(w).Encode(reply)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"nope"}}`))
		}
	}))
	defer srv.Close()

	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "mgmt-secret"}[k]
	}
	runWith := func(stdin string, tty bool, args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCLIWithInput(args, strings.NewReader(stdin), &out, &errOut, env, tty)
		return out.String(), errOut.String(), code
	}

	// Non-terminal stdin does not prompt or apply.
	if out, errOut, code := runWith("", false, "setup"); code != 0 || errOut != "Next: pluxy setup apply --yes installs what core suggests (pluxy/claude).\n" || !strings.Contains(out, "pluxy/claude") || len(applied) != 0 {
		t.Fatalf("setup non-tty: code=%d out=%q err=%q applied=%v", code, out, errOut, applied)
	}
	// Terminal answering n does not apply.
	if _, errOut, code := runWith("n\n", true, "setup"); code != 0 || !strings.Contains(errOut, "Apply suggested picks") || len(applied) != 0 {
		t.Fatalf("setup tty n: code=%d err=%q applied=%v", code, errOut, applied)
	}
	// Terminal answering y applies suggested picks.
	if out, _, code := runWith("y\n", true, "setup"); code != 0 || !strings.Contains(out, "done") || len(applied) != 1 || !slices.Equal(applied[0], []string{"pluxy/claude"}) {
		t.Fatalf("setup tty y: code=%d out=%q applied=%v", code, out, applied)
	}
	// --yes applies without terminal.
	applied = nil
	if _, _, code := runWith("", false, "setup", "--yes"); code != 0 || len(applied) != 1 || !slices.Equal(applied[0], []string{"pluxy/claude"}) {
		t.Fatalf("setup --yes: code=%d applied=%v", code, applied)
	}
	// setup scan --json prints body unchanged.
	if out, _, code := runWith("", false, "setup", "scan", "--json"); code != 0 || !strings.Contains(out, `"scanning":true`) {
		t.Fatalf("setup scan --json: %d %q", code, out)
	}
	// setup --yes --json prints the apply reply alone.
	applied = nil
	if out, _, code := runWith("", false, "setup", "--yes", "--json"); code != 0 || len(applied) != 1 || !json.Valid([]byte(out)) || !strings.HasPrefix(out, `{"results":`) {
		t.Fatalf("setup --yes --json: code=%d out=%q applied=%v", code, out, applied)
	}
	// setup apply with one failing pick exits 1, prints the reply and, on stderr, an error object.
	if out, errOut, code := runWith("", false, "setup", "apply", "--pick", "pluxy/claude", "--pick", "bad/pkg", "--json"); code != 1 || !strings.Contains(out, `"bad/pkg"`) || !strings.HasPrefix(errOut, `{"error":{"code":"failed"`) {
		t.Fatalf("setup apply partial failure: code=%d out=%q err=%q", code, out, errOut)
	}
	// setup apply with no picks, no terminal and no --yes is a usage error; with --json its
	// error is an object.
	applied = nil
	if _, errOut, code := runWith("", false, "setup", "apply"); code != 2 || len(applied) != 0 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("setup apply without a terminal: code=%d err=%q applied=%v", code, errOut, applied)
	}
	if out, errOut, code := runWith("", false, "setup", "apply", "--json"); code != 2 || out != "" || !strings.HasPrefix(errOut, `{"error":{"code":"invalid_request"`) {
		t.Fatalf("setup apply --json without a terminal: code=%d out=%q err=%q", code, out, errOut)
	}
	// An API error with --json is the API's own object.
	if _, errOut, code := runWith("", false, "logs", "--json"); code != 5 || errOut != "{\"error\":{\"code\":\"not_found\",\"message\":\"nope\"}}\n" {
		t.Fatalf("logs --json against a 404: code=%d err=%q", code, errOut)
	}
	// setup done.
	if out, _, code := runWith("", false, "setup", "done"); code != 0 || !doneCalled || !strings.Contains(out, "complete") {
		t.Fatalf("setup done: code=%d doneCalled=%v out=%q", code, doneCalled, out)
	}

	// client show for all 5 tools, never printing the management key.
	const placeholderHint = "Replace <client-key> with a client key: core's config file holds them, and \"pluxy key add\" makes another.\n"
	const modelHint = "Replace <model-id> with the name of a model your accounts serve: \"pluxy model list\" lists them.\n"
	for _, tool := range []string{"claude-code", "codex", "opencode", "pi", "omp"} {
		want := placeholderHint
		if tool == "opencode" || tool == "pi" || tool == "omp" {
			want += modelHint
		}
		out, errOut, code := runWith("", false, "client", "show", tool)
		if code != 0 || errOut != want || !strings.Contains(out, srv.URL) || strings.Contains(out, "mgmt-secret") || strings.Contains(out, "Replace") {
			t.Errorf("client show %s: code=%d err=%q out=%q", tool, code, errOut, out)
		}
		// The snippet came from core: one call to the tool's paste route, naming no key.
		if got, want := takePasteCalls(), []string{"GET /v0/management/setup/clients/" + tool + "/paste"}; !slices.Equal(got, want) {
			t.Errorf("client show %s called the paste route as %q, want %q", tool, got, want)
		}
	}
	// Pi's files are JSON with no comment line in them.
	out, errOut, _ := runWith("", false, "client", "show", "pi", "--json")
	if got, want := takePasteCalls(), []string{"GET /v0/management/setup/clients/pi/paste"}; !slices.Equal(got, want) {
		t.Errorf("client show pi --json called the paste route as %q, want %q", got, want)
	}
	if errOut != "" {
		t.Errorf("client show pi --json: stderr=%q, want none", errOut)
	}
	var pi struct {
		Files []struct{ Path, Content string }
	}
	if err := json.Unmarshal([]byte(out), &pi); err != nil || len(pi.Files) != 2 {
		t.Fatalf("client show pi --json: %v: %s", err, out)
	}
	for _, f := range pi.Files {
		if !json.Valid([]byte(f.Content)) || strings.Contains(f.Content, "#") {
			t.Errorf("client show pi: %s is not plain JSON: %q", f.Path, f.Content)
		}
	}
	// --key-id valid vs unknown.
	const storedHint = "Replace <client-key:k1 (sk******ab)> with that key's full value from core's config file: pluxy never prints a stored key.\n"
	if out, errOut, code := runWith("", false, "client", "show", "claude-code", "--key-id", "k1"); code != 0 || errOut != storedHint || !strings.Contains(out, "k1") || !strings.Contains(out, "sk******ab") || strings.Contains(out, "mgmt-secret") {
		t.Errorf("client show --key-id k1: code=%d err=%q out=%q", code, errOut, out)
	}
	if got, want := takePasteCalls(), []string{"GET /v0/management/setup/clients/claude-code/paste?key_id=k1"}; !slices.Equal(got, want) {
		t.Errorf("client show --key-id k1 called the paste route as %q, want %q", got, want)
	}
	if out, errOut, code := runWith("", false, "client", "show", "claude-code", "--key-id", "k1", "--json"); code != 0 || errOut != "" || !json.Valid([]byte(out)) {
		t.Errorf("client show --key-id k1 --json: code=%d err=%q out=%q", code, errOut, out)
	}
	if _, _, code := runWith("", false, "client", "show", "claude-code", "--key-id", "missing"); code != 5 {
		t.Errorf("client show --key-id missing: code=%d, want 5", code)
	}
	if _, _, code := runWith("", false, "client", "show", "unknown-tool"); code != 2 {
		t.Errorf("client show unknown: code=%d, want 2", code)
	}
}

// After pluxy setup and pluxy setup apply, one line on standard error names the next step for the
// state core is in; once first-run setup has ended, and with --json, there is none.
func TestCLISetupNextStep(t *testing.T) {
	const (
		claude  = `{"id":"pluxy/claude","kind":"provider","title":"Claude","summary":"Claude","installed":%v,"suggested":%v}`
		client  = `{"id":"claude-code","name":"Claude Code","installed":true,"version":"2.4.1","config":"~/.claude/settings.json","state":"%s"}`
		absent  = `{"id":"codex","name":"Codex CLI","installed":false,"config":"~/.codex/config.toml","state":"not-configured"}`
		account = `{"id":"a1","public_id":"p1","source":"login","extension":"pluxy/claude","state":"ok"}`
		method  = `{"extension":"%s","methods":[{"method":"api_key","label":"API key"}]}`
	)
	type state struct {
		firstRun, scanning bool
		clients, picks     string
		accounts, methods  string
	}
	var cur, afterApply state
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v0/management"))
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/setup":
			fmt.Fprintf(w, `{"first_run":%v,"scanning":%v,"scanned_at":"2026-11-19T12:00:00Z","found":{"clients":[%s],"sign_ins":[],"imports":[],"core":{"listen":"127.0.0.1:8317","reachable_from_network":false}},"picks":[%s]}`,
				cur.firstRun, cur.scanning, cur.clients, cur.picks)
		case "GET /v0/management/accounts":
			fmt.Fprintf(w, `{"accounts":[%s]}`, cur.accounts)
		case "GET /v0/management/login-methods":
			fmt.Fprintf(w, `{"extensions":[%s]}`, cur.methods)
		case "POST /v0/management/setup/apply":
			cur = afterApply
			w.Write([]byte(`{"results":[{"id":"pluxy/claude","status":"done","package":{"id":"pluxy/claude","version":"0.1.0","enabled":true,"state":"active"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"nope"}}`))
		}
	}))
	defer srv.Close()
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	run := func(stdin string, tty bool, args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		calls = nil
		code := runCLIWithInput(args, strings.NewReader(stdin), &out, &errOut, env, tty)
		return out.String(), errOut.String(), code
	}

	suggested := state{firstRun: true, clients: fmt.Sprintf(client, "not-configured"), picks: fmt.Sprintf(claude, false, true)}
	noAccount := state{firstRun: true, clients: fmt.Sprintf(client, "not-configured"), picks: fmt.Sprintf(claude, true, true), methods: fmt.Sprintf(method, "pluxy/claude")}
	withAccount := noAccount
	withAccount.accounts = account
	const (
		applyLine   = "Next: pluxy setup apply --yes installs what core suggests (pluxy/claude).\n"
		accountLine = "Next: add an account with pluxy account add pluxy/claude (pluxy account methods lists the ways to sign in).\n"
		clientLine  = "Next: pluxy client show claude-code prints what to paste into Claude Code's config; then pluxy setup done ends first-run setup.\n"
	)
	for _, c := range []struct {
		name string
		s    state
		want string
	}{
		{"suggested picks not installed", suggested, applyLine},
		{"still scanning", state{firstRun: true, scanning: true, picks: fmt.Sprintf(claude, false, false)},
			"Next: core is still looking at this machine: run pluxy setup again in a moment.\n"},
		{"providers offered, none suggested", state{firstRun: true, picks: fmt.Sprintf(claude, false, false)},
			"Next: install a provider with pluxy setup apply --pick <id>, an ID from the picks pluxy setup lists.\n"},
		{"no provider offered or running", state{firstRun: true},
			"Next: no provider is running, so no account can be added yet: pluxy ext list shows what is installed, and pluxy ext available what can be installed.\n"},
		{"a provider installed and not running", state{firstRun: true, picks: fmt.Sprintf(claude, true, true)},
			"Next: no provider is running, so no account can be added yet: pluxy ext list shows what is installed, and pluxy ext available what can be installed.\n"},
		{"a provider running, no account", noAccount, accountLine},
		{"two providers running, no account", state{firstRun: true, picks: fmt.Sprintf(claude, true, true), methods: fmt.Sprintf(method, "pluxy/claude") + "," + fmt.Sprintf(method, "pluxy/codex")},
			"Next: add an account with pluxy account add <provider> (pluxy account methods lists the providers and their ways to sign in).\n"},
		{"an account, a client to point at core", withAccount, clientLine},
		{"an account, the found client configured", state{firstRun: true, clients: fmt.Sprintf(client, "configured") + "," + absent, picks: fmt.Sprintf(claude, true, true), accounts: account},
			"Next: pluxy setup done ends first-run setup.\n"},
		{"setup ended", state{firstRun: false, clients: fmt.Sprintf(client, "not-configured"), picks: fmt.Sprintf(claude, false, true)}, ""},
	} {
		cur = c.s
		out, errOut, code := run("", false, "setup")
		if code != 0 || errOut != c.want || strings.Contains(out, "Next:") {
			t.Errorf("setup, %s: code=%d stderr=%q, want %q; stdout=%q", c.name, code, errOut, c.want, out)
		}
		// --json prints the reply alone and asks core for nothing more.
		cur = c.s
		out, errOut, code = run("", false, "setup", "--json")
		if code != 0 || errOut != "" || !json.Valid([]byte(out)) || !slices.Equal(calls, []string{"GET /setup"}) {
			t.Errorf("setup --json, %s: code=%d stderr=%q calls=%v out=%q", c.name, code, errOut, calls, out)
		}
	}

	// The step named after picks are applied is the one for the state they leave.
	for _, c := range []struct {
		name  string
		stdin string
		tty   bool
		args  []string
	}{
		{"setup --yes", "", false, []string{"setup", "--yes"}},
		{"setup on a terminal, answering y", "y\n", true, []string{"setup"}},
		{"setup apply --yes", "", false, []string{"setup", "apply", "--yes"}},
		{"setup apply --pick", "", false, []string{"setup", "apply", "--pick", "pluxy/claude"}},
		{"setup apply on a terminal, answering y", "y\n", true, []string{"setup", "apply"}},
	} {
		cur, afterApply = suggested, noAccount
		out, errOut, code := run(c.stdin, c.tty, c.args...)
		if code != 0 || !strings.HasSuffix(errOut, accountLine) || strings.Count(errOut, "Next:") != 1 || !strings.Contains(out, "pluxy/claude  done") {
			t.Errorf("%s: code=%d stderr=%q, want it to end %q; stdout=%q", c.name, code, errOut, accountLine, out)
		}
	}
	// Declining leaves the picks to apply, and the line says how.
	for _, args := range [][]string{{"setup"}, {"setup", "apply"}} {
		cur = suggested
		if _, errOut, code := run("n\n", true, args...); code != 0 || !strings.HasSuffix(errOut, applyLine) || slices.Contains(calls, "POST /setup/apply") {
			t.Errorf("%v on a terminal, answering n: code=%d stderr=%q calls=%v", args, code, errOut, calls)
		}
	}
	// Nothing suggested is left to apply: the line is the one for the state core is in.
	cur = withAccount
	if out, errOut, code := run("", false, "setup", "apply", "--yes"); code != 0 || out != "no suggested picks to apply\n" || errOut != clientLine {
		t.Errorf("setup apply --yes with nothing to apply: code=%d out=%q stderr=%q", code, out, errOut)
	}
	// With --json an apply prints its reply alone.
	cur, afterApply = suggested, noAccount
	if out, errOut, code := run("", false, "setup", "apply", "--yes", "--json"); code != 0 || errOut != "" || !strings.HasPrefix(out, `{"results":`) {
		t.Errorf("setup apply --yes --json: code=%d out=%q stderr=%q", code, out, errOut)
	}
	// A core that does not answer the calls the line needs gets no line, and no error.
	cur = state{firstRun: true}
	old := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/accounts") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		old.ServeHTTP(w, r)
	})
	if _, errOut, code := run("", false, "setup"); code != 0 || errOut != "" {
		t.Errorf("setup against a core without GET /accounts: code=%d stderr=%q, want no line", code, errOut)
	}
}

// In the human Clients table of pluxy setup, a client that is not installed and not-configured
// shows "-" in the STATE column; installed clients and non-installed clients with any other state
// still show their state, and --json output is byte-for-byte unchanged.
func TestCLISetupClientsTable(t *testing.T) {
	const body = `{"first_run":false,"scanning":false,"scanned_at":"2026-11-19T12:00:00Z","found":{"clients":[` +
		`{"id":"codex","name":"Codex CLI","installed":false,"config":"~/.codex/config.toml","state":"not-configured"},` +
		`{"id":"claude-code","name":"Claude Code","installed":true,"version":"2.4.1","config":"~/.claude/settings.json","state":"not-configured"},` +
		`{"id":"opencode","name":"OpenCode","installed":false,"config":"~/.config/opencode/opencode.json","state":"configured"},` +
		`{"id":"pi","name":"Pi","installed":false,"config":"~/.pi/agent/models.json","state":"refused"}` +
		`],"sign_ins":[],"imports":[],"core":{"listen":"127.0.0.1:8317","reachable_from_network":false}},"picks":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/setup" {
			w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":"not_found","message":"nope"}}`))
	}))
	defer srv.Close()
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCLIWithInput(args, strings.NewReader(""), &out, &errOut, env, false)
		return out.String(), errOut.String(), code
	}

	out, errOut, code := run("setup")
	if code != 0 || errOut != "" {
		t.Fatalf("setup: code=%d stderr=%q stdout=%q", code, errOut, out)
	}
	wantRows := [][]string{
		{"CLIENT", "INSTALLED", "VERSION", "STATE", "CONFIG"},
		{"codex", "no", "-", "-", "~/.codex/config.toml"},
		{"claude-code", "yes", "2.4.1", "not-configured", "~/.claude/settings.json"},
		{"opencode", "no", "-", "configured", "~/.config/opencode/opencode.json"},
		{"pi", "no", "-", "refused", "~/.pi/agent/models.json"},
	}
	_, after, ok := strings.Cut(out, "\nClients:\n")
	if !ok {
		t.Fatalf("setup output missing Clients section:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(after, "\n"), "\n")
	if len(lines) != len(wantRows) {
		t.Fatalf("Clients table has %d rows, want %d:\n%s", len(lines), len(wantRows), after)
	}
	for i, want := range wantRows {
		if got := strings.Fields(lines[i]); !slices.Equal(got, want) {
			t.Errorf("Clients row %d = %v, want %v (line: %q)", i, got, want, lines[i])
		}
	}

	jsonOut, jsonErr, jsonCode := run("setup", "--json")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("setup --json: code=%d stderr=%q", jsonCode, jsonErr)
	}
	if jsonOut != body {
		t.Errorf("setup --json changed:\ngot:  %q\nwant: %q", jsonOut, body)
	}
}

// pluxy import cpa says what core could not read in core's own words, with no error code after
// them; the reply, its code and the exit code stay as they are.
func TestCLIImportCPAUnreadable(t *testing.T) {
	const body = `{"error":{"code":"invalid_request","message":"there is no file at /srv/cpa/config.yaml"}}`
	status := http.StatusBadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /v0/management/setup/import" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
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
	if out, errOut, code := run("import", "cpa", "/srv/cpa/config.yaml"); code != 1 || out != "" || errOut != "pluxy: there is no file at /srv/cpa/config.yaml\n" {
		t.Errorf("import cpa: code=%d out=%q stderr=%q", code, out, errOut)
	}
	if out, errOut, code := run("import", "cpa", "/srv/cpa/config.yaml", "--json"); code != 1 || out != "" || errOut != body+"\n" {
		t.Errorf("import cpa --json: code=%d out=%q stderr=%q, want the reply's body", code, out, errOut)
	}
	// Every other refusal keeps its code.
	status = http.StatusUnprocessableEntity
	if _, errOut, code := run("import", "cpa", "/srv/cpa/config.yaml"); code != 6 || errOut != "pluxy: there is no file at /srv/cpa/config.yaml (invalid_request)\n" {
		t.Errorf("import cpa refused with 422: code=%d stderr=%q", code, errOut)
	}
}

// pluxy status names the build as core's own first line does.
func TestStatusBuildLine(t *testing.T) {
	for _, c := range []struct{ version, commit, built, want string }{
		{"1.2.3", "0123abc", "2026-11-10T08:00:00Z", "pluxy 1.2.3 (0123abc, 2026-11-10T08:00:00Z)\n"},
		{"dev", "unknown", "unknown", "pluxy dev (no commit or build date recorded)\n"},
	} {
		var out bytes.Buffer
		printStatus(&invocation{stdout: &out}, management.Status{Version: c.version, Commit: c.commit, Built: c.built})
		if line, _, _ := strings.Cut(out.String(), "\n"); line+"\n" != c.want {
			t.Errorf("status of %s (%s, %s) starts %q, want %q", c.version, c.commit, c.built, line, c.want)
		}
	}
}

// While first-run setup has not ended, core's start names where to go on: the panel when this
// build serves one, and the command with the flags it needs to reach this core.
func TestSetupStartLine(t *testing.T) {
	for _, c := range []struct {
		panel      bool
		url        string
		configPath string
		listen     string
		want       string
	}{
		{true, "http://127.0.0.1:8317", "config.json", "127.0.0.1:8317",
			"first-run setup is not finished. Next: open http://127.0.0.1:8317/panel/ in a browser and sign in with the management key, or run pluxy setup"},
		{false, "http://127.0.0.1:8317", "config.json", "127.0.0.1:8317",
			"first-run setup is not finished. Next: run pluxy setup"},
		{true, "https://127.0.0.1:9000", "/etc/pluxy/config.json", "0.0.0.0:9000",
			"first-run setup is not finished. Next: open https://127.0.0.1:9000/panel/ in a browser and sign in with the management key, or run pluxy setup --config /etc/pluxy/config.json"},
		// A config listening on port 0 does not say where core is, so the command is given the address.
		{false, "http://127.0.0.1:40123", "/tmp/x/config.json", "127.0.0.1:0",
			"first-run setup is not finished. Next: run pluxy setup --config /tmp/x/config.json --url http://127.0.0.1:40123"},
		// A path with a space stays one argument when the command is pasted.
		{false, "http://127.0.0.1:8317", "/tmp/my files/config.json", "127.0.0.1:8317",
			`first-run setup is not finished. Next: run pluxy setup --config "/tmp/my files/config.json"`},
	} {
		if got := setupStartLine(c.panel, c.url, c.configPath, c.listen); got != c.want {
			t.Errorf("setupStartLine(%v, %q, %q, %q) =\n%q, want\n%q", c.panel, c.url, c.configPath, c.listen, got, c.want)
		}
	}
}

// A setup line says when no scan has run, rather than claiming one finished.
func TestPrintSetupScanLine(t *testing.T) {
	var out bytes.Buffer
	x := &invocation{stdout: &out}
	for _, tc := range []struct {
		name string
		s    management.Setup
		want string
	}{
		{"no scan yet", management.Setup{}, "scan: not scanned"},
		{"scanning", management.Setup{Scanning: true}, "scan: scanning"},
	} {
		out.Reset()
		printSetup(x, tc.s)
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: %q, want %q", tc.name, out.String(), tc.want)
		}
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

func TestUsageEventsFailedFilter(t *testing.T) {
	const (
		failedEv = `{"seq":1,"timestamp":"2026-11-19T12:00:00Z","endpoint":"/v1/chat/completions","model":"gpt-failed","account":"acct-1","key_id":"k1","failed":true,"status":502,"error_code":"upstream_error","latency_ms":40,"tokens":{"total_tokens":0}}`
		okEv     = `{"seq":2,"timestamp":"2026-11-19T12:01:00Z","endpoint":"/v1/chat/completions","model":"gpt-ok","account":"acct-1","key_id":"k1","failed":false,"status":200,"latency_ms":50,"tokens":{"total_tokens":15}}`
	)
	var seenQueries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v0/management/usage/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		seenQueries = append(seenQueries, q)
		var events string
		switch q.Get("failed") {
		case "":
			events = failedEv + "," + okEv
		case "true":
			events = failedEv
		case "false":
			events = okEv
		default:
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"enabled":true,"events":[` + events + `],"next":2,"more":false,"lost":false}` + "\n"))
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

	// Without --failed: both failed and successful events are listed.
	out, errOut, code := run("usage", "events")
	if code != 0 || !strings.Contains(out, "gpt-failed") || !strings.Contains(out, "gpt-ok") {
		t.Fatalf("pluxy usage events: code=%d out=%q err=%q", code, out, errOut)
	}
	if seenQueries[0].Has("failed") {
		t.Errorf("without --failed, query had failed=%q", seenQueries[0].Get("failed"))
	}

	// With --failed true: only failed events are listed.
	out, errOut, code = run("usage", "events", "--failed", "true")
	if code != 0 || !strings.Contains(out, "gpt-failed") || strings.Contains(out, "gpt-ok") {
		t.Fatalf("pluxy usage events --failed true: code=%d out=%q err=%q", code, out, errOut)
	}
	if got := seenQueries[1].Get("failed"); got != "true" {
		t.Errorf("with --failed true, query failed=%q, want true", got)
	}

	// With --failed false: only successful events are listed.
	out, errOut, code = run("usage", "events", "--failed", "false")
	if code != 0 || !strings.Contains(out, "gpt-ok") || strings.Contains(out, "gpt-failed") {
		t.Fatalf("pluxy usage events --failed false: code=%d out=%q err=%q", code, out, errOut)
	}
	if got := seenQueries[2].Get("failed"); got != "false" {
		t.Errorf("with --failed false, query failed=%q, want false", got)
	}

	// Help text documents --failed with the same wording as usage export.
	_, helpOut, code := run("usage", "events", "--help")
	if code != 0 || !strings.Contains(helpOut, "-failed") || !strings.Contains(helpOut, "only failed requests (true) or only successful ones (false)") {
		t.Errorf("pluxy usage events --help: code=%d err=%q", code, helpOut)
	}
}

// theme add keeps the ID alone on standard output, so it can be piped, and says on standard error
// that the theme is not on, unless it is or core does not say.
func TestThemeAddSaysItIsNotOn(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	list := func(w http.ResponseWriter) { w.Write([]byte(`{"current":"","themes":[]}`)) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodGet {
			list(w)
			return
		}
		w.Write([]byte(`{"id":"t3/f","name":"F","bytes":12}` + "\n"))
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(file, []byte(`{"name":"F"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, string, int, []string) {
		mu.Lock()
		requests = nil
		mu.Unlock()
		var out, errOut bytes.Buffer
		code := runCLI(append(args, "--url", srv.URL, "--key", "k"), &out, &errOut, func(string) string { return "" })
		mu.Lock()
		defer mu.Unlock()
		return out.String(), errOut.String(), code, slices.Clone(requests)
	}
	const notOn = "Stored but not turned on. To turn it on, run: pluxy theme set t3/f\n"
	for _, c := range []struct {
		name    string
		list    func(http.ResponseWriter)
		wantErr string
	}{
		{"nothing selected", func(w http.ResponseWriter) { w.Write([]byte(`{"current":"","themes":[]}`)) }, notOn},
		{"another selected", func(w http.ResponseWriter) { w.Write([]byte(`{"current":"pluxy/paper","themes":[]}`)) }, notOn},
		{"already selected", func(w http.ResponseWriter) { w.Write([]byte(`{"current":"t3/f","themes":[]}`)) }, ""},
		{"a field of a newer core", func(w http.ResponseWriter) { w.Write([]byte(`{"current":"t3/f","themes":7}`)) }, ""},
		{"list fails", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, ""},
		{"list is not JSON", func(w http.ResponseWriter) { w.Write([]byte("not json")) }, ""},
	} {
		mu.Lock()
		list = c.list
		mu.Unlock()
		out, errOut, code, _ := run("theme", "add", file)
		if code != 0 || out != "t3/f\n" || errOut != c.wantErr {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want 0, the ID alone, %q", c.name, code, out, errOut, c.wantErr)
		}
	}
	// --json prints core's reply and nothing else, and asks core nothing more.
	out, errOut, code, sent := run("theme", "add", file, "--json")
	if code != 0 || out != `{"id":"t3/f","name":"F","bytes":12}`+"\n" || errOut != "" || len(sent) != 1 {
		t.Errorf("--json: exit %d, stdout %q, stderr %q, requests %q", code, out, errOut, sent)
	}

	// A bad ID is refused here, in plain words, with no request.
	const shape = `write it as publisher/name, such as my/paper, with each part 1 to 64 characters from a-z, 0-9 and -`
	for id, want := range map[string]string{
		"Bad Id":                        `theme ID "Bad Id" is not valid: ` + shape,
		"nopublisher":                   `theme ID "nopublisher" is not valid: ` + shape,
		"a/b/c":                         `theme ID "a/b/c" is not valid: ` + shape,
		"my/" + strings.Repeat("a", 65): `theme ID "my/` + strings.Repeat("a", 65) + `" is not valid: ` + shape,
		"current/paper":                 `theme ID "current/paper" is not valid: "current" cannot be the publisher, so use another, such as my/paper`,
	} {
		out, errOut, code, sent := run("theme", "add", file, "--id", id)
		if code != exitUsage || out != "" || errOut != "pluxy: "+want+"\n" || len(sent) != 0 {
			t.Errorf("--id %q: exit %d, stdout %q, stderr %q, requests %q; want exit 2 and %q", id, code, out, errOut, sent, want)
		}
	}
	// So is an ID worked out from a file whose name leaves nothing to use.
	noName := filepath.Join(t.TempDir(), ".json")
	if err := os.WriteFile(noName, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code, sent := run("theme", "add", noName); code != exitUsage || !strings.Contains(errOut, shape) || len(sent) != 0 {
		t.Errorf("a file named .json: exit %d, stderr %q, requests %q", code, errOut, sent)
	}
}

// A command group's --help and -h list its commands on standard output, for every group; a bare
// group and an unknown subcommand stay usage errors.
func TestGroupHelp(t *testing.T) {
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCLI(args, &out, &errOut, func(string) string { return "" })
		return out.String(), errOut.String(), code
	}
	groups := map[string]bool{}
	for _, c := range commands {
		if word, _, ok := strings.Cut(c.words, " "); ok && !slices.ContainsFunc(commands, func(o command) bool { return o.words == word }) {
			groups[word] = true
		}
	}
	if !groups["theme"] || len(groups) < 3 {
		t.Fatalf("groups = %v", groups)
	}
	for group := range groups {
		for _, flag := range []string{"--help", "-h", "-help", "--h"} {
			out, errOut, code := run(group, flag)
			if code != exitOK || errOut != "" || !strings.HasPrefix(out, "usage: pluxy "+group+" <command> [flags]\n") {
				t.Errorf("%s %s: exit %d, stdout %q, stderr %q", group, flag, code, out, errOut)
			}
			for _, c := range commands {
				if strings.HasPrefix(c.words, group+" ") && !strings.Contains(out, c.words) {
					t.Errorf("%s %s does not list %q", group, flag, c.words)
				}
			}
		}
		if out, errOut, code := run(group); code != exitUsage || out != "" || !strings.Contains(errOut, group+" needs a command") {
			t.Errorf("bare %s: exit %d, stdout %q, stderr %q", group, code, out, errOut)
		}
		if out, errOut, code := run(group, "frobnicate", "--help"); code != exitUsage || out != "" || !strings.Contains(errOut, "unknown "+group+" command") {
			t.Errorf("%s frobnicate --help: exit %d, stdout %q, stderr %q", group, code, out, errOut)
		}
	}
}

func TestResolveThemeID(t *testing.T) {
	tests := []struct {
		name     string
		flagID   string
		path     string
		raw      string
		expected string
	}{
		{"flag wins", "acme/custom", "/tmp/Foo.json", `{"id":"other/id"}`, "acme/custom"},
		{"pluxy file id", "", "/tmp/Foo.json", `{"theme_version":0,"id":"acme/from-file"}`, "acme/from-file"},
		{"pluxy file invalid id", "", "/tmp/Foo.json", `{"theme_version":0,"id":"ok-id","name":"X Y"}`, "t3/foo"},
		{"T3 file id without name", "", "/tmp/Foo.json", `{"id":"ok-id","name":""}`, "t3/ok-id"},
		{"T3 file name", "", "/tmp/Foo.json", `{"name":"Nightfall"}`, "t3/nightfall"},
		{"T3 file name trimmed", "", "/tmp/Foo.json", `{"name":"  My Theme!! "}`, "t3/my-theme"},
		{"T3 file long name", "", "/tmp/Foo.json", `{"name":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`, "t3/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"T3 file hashed name", "", "/tmp/Foo.json", `{"name":"日本語"}`, "t3/theme-nbamha"},
		{"T3 file hashed UTF-16 name", "", "/tmp/Foo.json", `{"name":"😀"}`, "t3/theme-1kdnhdk"},
		{"T3 file invalid id", "", "/tmp/Foo.json", `{"id":"Bad_ID","name":"X Y"}`, "t3/x-y"},
		{"T3 file publisher id", "", "/tmp/Foo.json", `{"id":"acme/from-file","name":"X Y"}`, "t3/x-y"},
		{"T3 file no id or name", "", "/tmp/My Theme_v2.JSON", `{}`, "t3/my-theme-v2"},
		{"T3 file empty name", "", "/tmp/Fallback Name.json", `{"name":""}`, "t3/fallback-name"},
		{"non-ascii filename", "", "/tmp/Café.json", `{}`, "t3/caf-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveThemeID(tt.flagID, tt.path, []byte(tt.raw)); got != tt.expected {
				t.Errorf("resolveThemeID(%q, %q, %s) = %q, want %q", tt.flagID, tt.path, tt.raw, got, tt.expected)
			}
		})
	}
	if got := themeIDFromName(""); got != "custom-theme" {
		t.Errorf("themeIDFromName(%q) = %q, want %q", "", got, "custom-theme")
	}
}

func TestCLIExtAvailableNoRegistry(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/registries" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()

	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCLI(args, &out, &errOut, env)
		return out.String(), errOut.String(), code
	}

	// With no registries, ext available explains why and names what to run next.
	body = `{"registries":[]}`
	out, errOut, code := run("ext", "available")
	if code != exitOK || errOut != "" {
		t.Fatalf("ext available (no registries): exit=%d stderr=%q stdout=%q", code, errOut, out)
	}
	for _, want := range []string{"This build has no extension registry.", "pluxy-pack", "-bundled"} {
		if !strings.Contains(out, want) {
			t.Errorf("ext available (no registries) stdout = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "REGISTRY") {
		t.Errorf("ext available (no registries) printed table header: %q", out)
	}

	// --json still prints the API reply unchanged.
	out, errOut, code = run("ext", "available", "--json")
	if code != exitOK || errOut != "" || strings.TrimSpace(out) != `{"registries":[]}` {
		t.Errorf("ext available --json: exit=%d stderr=%q stdout=%q", code, errOut, out)
	}

	// With a registry, ext available prints the table and not the no-registry message.
	body = `{"registries":[{"name":"bundled","url":"/tmp/bundled/index.json","packages":[{"id":"pluxy/claude","title":"Claude","kind":"provider","tier":"pick","releases":[{"version":"0.1.0","protocol":[0],"platforms":["linux/amd64"]}]}]}]}`
	out, errOut, code = run("ext", "available")
	if code != exitOK || errOut != "" {
		t.Fatalf("ext available (with registry): exit=%d stderr=%q stdout=%q", code, errOut, out)
	}
	if !strings.Contains(out, "pluxy/claude") || strings.Contains(out, "no extension registry") {
		t.Errorf("ext available (with registry) stdout = %q", out)
	}
}
