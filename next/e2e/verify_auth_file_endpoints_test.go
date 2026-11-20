package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// verifyAuthFileCore is a core with the example OAuth extension, no configured accounts, and a
// credentials folder that stays in place when the core is stopped and started again.
type verifyAuthFileCore struct {
	t          *testing.T
	srv        *oauthtest.Server
	configPath string
	credDir    string
}

func newVerifyAuthFileCore(t *testing.T) *verifyAuthFileCore {
	t.Helper()
	dir := t.TempDir()
	c := &verifyAuthFileCore{
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

func (c *verifyAuthFileCore) start() (string, func()) {
	c.t.Helper()
	base, _, stop, err := c.tryStart()
	if err != nil {
		c.t.Fatal(err)
	}
	return base, stop
}

func (c *verifyAuthFileCore) tryStart() (string, *coreLog, func(), error) {
	c.t.Helper()
	argv := append([]string{"-config", c.configPath}, noWatch...)
	return startCoreStoppable(c.t, filepath.Join(binDir, "pluxy"), argv...)
}

// A status-only PATCH keeps the fields it does not name, and both the status and those fields
// survive a core restart: the management client does not have to send the account back whole.
func TestVerifyAuthFilePatchPreservesFieldsAcrossRestart(t *testing.T) {
	c := newVerifyAuthFileCore(t)
	base, stop := c.start()
	deviceLogin(t, base, c.srv, "work")

	var changed struct{ Account accountView }
	if code := manage(t, base, "PATCH", "/accounts/work", map[string]any{"priority": 3}, &changed); code != http.StatusOK {
		t.Fatalf("PATCH priority: %d", code)
	}
	if changed.Account.Priority != 3 || changed.Account.Disabled {
		t.Fatalf("after PATCH priority: %+v", changed.Account)
	}

	var disabled struct{ Account accountView }
	if code := manage(t, base, "PATCH", "/accounts/work", map[string]any{"disabled": true}, &disabled); code != http.StatusOK {
		t.Fatalf("PATCH disabled: %d", code)
	}
	if !disabled.Account.Disabled || disabled.Account.Priority != 3 {
		t.Fatalf("disabled-only PATCH lost a field: %+v", disabled.Account)
	}
	if a := getAccount(t, base, "work"); !a.Disabled || a.Priority != 3 {
		t.Fatalf("listed after the two PATCHes: %+v", a)
	}

	stop()
	base, _ = c.start()
	a := getAccount(t, base, "work")
	if !a.Disabled || a.Priority != 3 {
		t.Fatalf("after restart: disabled %v, priority %d; want true and 3", a.Disabled, a.Priority)
	}
}

// One malformed credential file does not make the account endpoints unavailable: core starts,
// lists the valid account, and still accepts PATCH and DELETE for it.
func TestVerifyAuthFileCorruptRecordKeepsAccountEndpointsAvailable(t *testing.T) {
	t.Skip("bug mg-auth-file-endpoint-bugs: one malformed credential file stops core from starting, so GET, PATCH and DELETE /accounts are unavailable")
	c := newVerifyAuthFileCore(t)
	base, stop := c.start()
	deviceLogin(t, base, c.srv, "work")
	stop()

	if err := os.WriteFile(filepath.Join(c.credDir, "broken.json"), []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	base, _, stop, err := c.tryStart()
	if err != nil {
		t.Fatalf("one malformed credential file stopped core, so GET, PATCH and DELETE /accounts are unavailable: %v", err)
	}
	defer stop()

	var list accountList
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
		t.Fatalf("GET /accounts beside a malformed credential file: %d", code)
	}
	if len(list.Accounts) != 1 || list.Accounts[0].ID != "work" {
		t.Fatalf("accounts beside a malformed credential file: %+v, want only work", list.Accounts)
	}

	var changed struct{ Account accountView }
	if code := manage(t, base, "PATCH", "/accounts/work", map[string]any{"disabled": true}, &changed); code != http.StatusOK {
		t.Fatalf("PATCH /accounts/work: %d", code)
	}
	if !changed.Account.Disabled {
		t.Fatalf("PATCH reply: %+v, want work disabled", changed.Account)
	}
	if code := manage(t, base, "DELETE", "/accounts/work", nil, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE /accounts/work: %d", code)
	}
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK || len(list.Accounts) != 0 {
		t.Fatalf("GET /accounts after DELETE: %d, %+v", code, list.Accounts)
	}
}
