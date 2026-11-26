package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// A login account's ban is removed with the account: after DELETE and a core stop,
// routing.json holds no ban for it.
func TestRemovedLoginAccountBanEnds(t *testing.T) {
	pkg := testPackageID(t, "test/ban", "1.0.0")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(dir, "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	bin := filepath.Join(binDir, "pluxy")
	base, _, stop, err := startCoreStoppable(t, bin, append([]string{"-config", path}, noWatch...)...)
	if err != nil {
		t.Fatal(err)
	}

	var l loginReply
	if code := manage(t, base, http.MethodPost, "/accounts", map[string]any{
		"id": "login-a", "method": "api_key", "input": map[string]string{"api_key": "sk-login-a"},
	}, &l); code != http.StatusAccepted {
		t.Fatalf("login: %d", code)
	}
	for deadline := time.Now().Add(10 * time.Second); l.Login.State == "pending" && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		manage(t, base, http.MethodGet, "/logins/"+l.Login.ID, nil, &l)
	}
	if l.Login.State != "done" {
		t.Fatalf("login: %+v", l.Login)
	}
	if code, body := ask(t, base, "account-closed"); code != http.StatusForbidden {
		t.Fatalf("account-closed request: %d %s, want 403", code, body)
	}
	if h := accountHealth(t, base, "login-a"); h.State != mgmt.HealthBanned {
		t.Fatalf("login-a after account_closed: %+v, want banned", h)
	}
	if code := manage(t, base, http.MethodDelete, "/accounts/login-a", nil, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE login-a: %d, want 204", code)
	}
	stop()

	assertNoBan(t, filepath.Join(dir, "state", "routing.json"), "login-a")
}

// A config account's ban is removed when the account leaves the config: after the reload and
// a core stop, routing.json holds no ban for it.
func TestRemovedConfigAccountBanEnds(t *testing.T) {
	pkg := testPackageID(t, "test/ban", "1.0.0")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "config-a"}},
		},
	}
	writeConfig(t, path, cfg)
	bin := filepath.Join(binDir, "pluxy")
	base, _, stop, err := startCoreStoppable(t, bin, append([]string{"-config", path}, noWatch...)...)
	if err != nil {
		t.Fatal(err)
	}

	if code, body := ask(t, base, "account-closed"); code != http.StatusForbidden {
		t.Fatalf("account-closed request: %d %s, want 403", code, body)
	}
	if h := accountHealth(t, base, "config-a"); h.State != mgmt.HealthBanned {
		t.Fatalf("config-a after account_closed: %+v, want banned", h)
	}

	cfg["extension"].(map[string]any)["accounts"] = []any{}
	writeConfig(t, path, cfg)
	if code, s := reload(t, base); code != http.StatusOK || s.Active.Error != "" {
		t.Fatalf("reload without config-a: %d %+v", code, s)
	}
	stop()

	assertNoBan(t, filepath.Join(dir, "state", "routing.json"), "config-a")
}

// assertNoBan reads routing.json after core stopped and fails when it still holds a ban for id.
func assertNoBan(t *testing.T, path, id string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc struct {
		Bans []struct {
			Account string `json:"account"`
		} `json:"bans"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	for _, ban := range doc.Bans {
		if ban.Account == id {
			t.Fatalf("%s still holds a ban for %s: %s", path, id, b)
		}
	}
}
