package e2e

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// brokenCredentialCore is a core with the example OAuth extension, no configured accounts, and
// a credentials folder that stays in place when the core is stopped and started again.
type brokenCredentialCore struct {
	t          *testing.T
	srv        *oauthtest.Server
	configPath string
	credDir    string
}

func newBrokenCredentialCore(t *testing.T) *brokenCredentialCore {
	t.Helper()
	dir := t.TempDir()
	c := &brokenCredentialCore{
		t:          t,
		srv:        oauthtest.New(t),
		configPath: filepath.Join(dir, "config.json"),
		credDir:    filepath.Join(dir, "credentials"),
	}
	writeConfig(t, c.configPath, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": c.credDir,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	return c
}

func (c *brokenCredentialCore) start() (string, func()) {
	c.t.Helper()
	base, _, stop, err := c.tryStart()
	if err != nil {
		c.t.Fatalf("one malformed credential file stopped core, so no account answers: %v", err)
	}
	return base, stop
}

func (c *brokenCredentialCore) tryStart() (string, *coreLog, func(), error) {
	c.t.Helper()
	argv := append([]string{"-config", c.configPath}, noWatch...)
	return startCoreStoppable(c.t, filepath.Join(binDir, "pluxy"), argv...)
}

// brokenAccountView is the account fields this test reads back.
type brokenAccountView struct {
	ID        string
	Source    string
	Extension string
	State     string
	Health    struct {
		State string
		Why   string
	}
}

// One malformed credential file does not stop core: the good account still serves, the bad file
// is listed as a broken account naming the file and the error, and deleting it removes the file.
func TestBrokenCredentialFileDoesNotStopCore(t *testing.T) {
	c := newBrokenCredentialCore(t)
	base, stop := c.start()
	deviceLogin(t, base, c.srv, "work")
	stop()

	badPath := filepath.Join(c.credDir, "broken.json")
	if err := os.WriteFile(badPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _, stop, err := c.tryStart()
	if err != nil {
		t.Fatalf("one malformed credential file stopped core, so no account answers: %v", err)
	}
	defer stop()

	var list struct{ Accounts []brokenAccountView }
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
		t.Fatalf("GET /accounts beside a malformed credential file: %d", code)
	}
	if len(list.Accounts) != 2 {
		t.Fatalf("accounts beside a malformed credential file: %+v, want work and broken", list.Accounts)
	}
	byID := map[string]brokenAccountView{}
	for _, a := range list.Accounts {
		byID[a.ID] = a
	}
	if a := byID["work"]; a.Health.State != "ok" {
		t.Fatalf("the good account: %+v, want health ok", a)
	}
	a, ok := byID["broken"]
	if !ok {
		t.Fatalf("the broken account is not listed: %+v", list.Accounts)
	}
	if a.Health.State != "broken" {
		t.Errorf("the broken account's health is %q, want broken", a.Health.State)
	}
	if !strings.Contains(a.Health.Why, badPath) || !strings.Contains(a.Health.Why, "unexpected end of JSON input") {
		t.Errorf("the broken account's why is %q, want the file and the parse error", a.Health.Why)
	}
	if a.Extension != "" || a.State != "" || a.Source != "login" {
		t.Errorf("the broken account: %+v, want source login and nothing guessed", a)
	}

	// The good account still serves.
	resp := post(t, context.Background(), base, clientKey, plainBody)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a request with the good account beside a malformed credential file: %d", resp.StatusCode)
	}
	// The broken account is never routed to.
	var plan struct {
		Order []struct{ ID string }
	}
	if code := manage(t, base, "GET", "/route?model=gpt-test", nil, &plan); code != http.StatusOK || len(plan.Order) != 1 || plan.Order[0].ID != "work" {
		t.Errorf("route plan %+v (status %d), want work only", plan.Order, code)
	}

	// pluxy account list shows it.
	if r := runCLI(t, nil, "account", "list", "--url", base, "--key", managementKey); r.code != 0 ||
		!strings.Contains(r.stdout, "broken") || !strings.Contains(r.stdout, badPath) {
		t.Fatalf("pluxy account list: %s\nwant the broken account with its file", r)
	}
	// Deleting it removes the file.
	if code := manage(t, base, "DELETE", "/accounts/broken", nil, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE /accounts/broken: %d", code)
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatalf("after DELETE, the broken file: %v, want it gone", err)
	}

	// A file fixed or written again is read normally on a restart.
	stop()
	good, err := os.ReadFile(filepath.Join(c.credDir, "work.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.Replace(string(good), `"id": "work"`, `"id": "broken"`, 1)
	if fixed == string(good) {
		t.Fatalf("the record file has no id field to rename: %s", good)
	}
	if err := os.WriteFile(badPath, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	base, stop = c.start()
	defer stop()
	list.Accounts = nil
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
		t.Fatalf("GET /accounts after the file was fixed: %d", code)
	}
	for _, a := range list.Accounts {
		if a.ID == "broken" && a.Health.State == "ok" && a.Extension != "" {
			return
		}
	}
	t.Fatalf("after the file was fixed, accounts = %+v, want broken read as a normal account", list.Accounts)
}
