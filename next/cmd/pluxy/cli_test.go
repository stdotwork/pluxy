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
	// pluxy account models <id>
	"GET /accounts/{id}/models",
	// pluxy account import <file>
	"POST /accounts/import", "POST /vertex/import",
	// pluxy usage events
	"GET /usage/events",
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
		case "POST /v0/management/reload":
			w.WriteHeader(http.StatusUnprocessableEntity)
			w.Write([]byte(`{"desired":{"revision":2,"at":"2026-11-19T00:00:00Z","error":"bad listen"},"active":{"revision":1,"at":"2026-11-19T00:00:00Z"},"extensions":[]}`))
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

// Setup subcommands (TTY vs non-TTY, --yes, --pick, partial failure) and client show.
func TestCLISetupAndClientShow(t *testing.T) {
	var applied [][]string
	var doneCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid management key"}}`))
			return
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
	if out, errOut, code := runWith("", false, "setup"); code != 0 || errOut != "" || !strings.Contains(out, "pluxy/claude") || len(applied) != 0 {
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
	for _, tool := range []string{"claude-code", "codex", "opencode", "pi", "omp"} {
		out, errOut, code := runWith("", false, "client", "show", tool)
		if code != 0 || errOut != "" || !strings.Contains(out, srv.URL) || strings.Contains(out, "mgmt-secret") {
			t.Errorf("client show %s: code=%d err=%q out=%q", tool, code, errOut, out)
		}
	}
	// Pi's files are JSON with no comment line in them.
	out, _, _ := runWith("", false, "client", "show", "pi", "--json")
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
	if out, _, code := runWith("", false, "client", "show", "claude-code", "--key-id", "k1"); code != 0 || !strings.Contains(out, "k1") || !strings.Contains(out, "sk******ab") || strings.Contains(out, "mgmt-secret") {
		t.Errorf("client show --key-id k1: code=%d out=%q", code, out)
	}
	if _, _, code := runWith("", false, "client", "show", "claude-code", "--key-id", "missing"); code != 5 {
		t.Errorf("client show --key-id missing: code=%d, want 5", code)
	}
	if _, _, code := runWith("", false, "client", "show", "unknown-tool"); code != 2 {
		t.Errorf("client show unknown: code=%d, want 2", code)
	}
}
