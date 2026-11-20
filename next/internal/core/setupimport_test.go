package core

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

	"github.com/stdotwork/pluxy/next/management"
)

// importCall sends POST /setup/import with body to c and returns the status and the reply.
func importCall(t *testing.T, c *Core, key string, body any) (int, management.ImportReport, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/setup/import", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	c.Management().ServeHTTP(rec, req)
	var rep management.ImportReport
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	return rec.Code, rep, rec.Body.String()
}

// folderState is every file name and content under dir.
func folderState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// A dry run reports the import and writes nothing; the import then writes the config's
// settings and accounts into core's config and the auth folder's accounts into its store, and
// reports the same items.
func TestSetupImport(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`
	writeTestFile(t, cfgPath, cfg)
	cpa := t.TempDir()
	auths := filepath.Join(cpa, "auths")
	writeTestFile(t, filepath.Join(auths, "claude-a.json"), `{"type": "claude", "access_token": "fake-claude"}`)
	writeTestFile(t, filepath.Join(auths, "antigravity-a.json"), `{"type": "antigravity", "access_token": "fake-ag"}`)
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), `
port: 8317
api-keys: [fake-cpa-client-key]
remote-management: {secret-key: "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234"}
auth-dir: auths
debug: on
claude-api-key:
  - {api-key: fake-claude-key}
`)
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	creds := c.Config().CredentialsDir
	before, credsBefore := folderState(t, dir), folderState(t, creds)

	req := management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: true}
	code, rep, body := importCall(t, c, "fake-mgmt-key", req)
	if code != http.StatusOK || !rep.DryRun {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if after := folderState(t, dir); !mapsEqual(before, after) {
		t.Errorf("the dry run changed core's folder")
	}
	if after := folderState(t, creds); !mapsEqual(credsBefore, after) {
		t.Errorf("the dry run changed the credential store")
	}
	names := func(rows []management.ImportRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, strings.TrimSpace(r.What+" "+r.Name))
		}
		return out
	}
	want := map[string][]string{
		"brought":     {"api-keys", "auth-dir", "claude-api-key[0]", "auth file claude-a.json"},
		"changed":     {"server.host", "remote-management.secret-key", "port"},
		"not_brought": {"debug", "auth file antigravity-a.json"},
	}
	check := func(rep management.ImportReport) {
		t.Helper()
		for list, rows := range map[string][]management.ImportRow{"brought": rep.Brought, "changed": rep.Changed, "not_brought": rep.NotBrought} {
			if got := names(rows); !sameSet(got, want[list]) {
				t.Errorf("%s: %v, want %v", list, got, want[list])
			}
		}
		if len(rep.Needs) != 1 || rep.Needs[0].Pick != "pluxy/claude" || rep.Needs[0].Why != "2 accounts need it" {
			t.Errorf("needs %+v", rep.Needs)
		}
	}
	check(rep)
	if strings.Contains(body, "$2a$") || strings.Contains(body, "fake-claude") || strings.Contains(body, "fake-cpa-client-key") {
		t.Errorf("the report holds a secret: %s", body)
	}

	code, rep, body = importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: req.Config})
	if code != http.StatusOK || rep.DryRun {
		t.Fatalf("import: %d %s", code, body)
	}
	check(rep)
	got := c.Config()
	i := slices.IndexFunc(rep.Changed, func(r management.ImportRow) bool { return r.What == "remote-management.secret-key" })
	if key := rep.Changed[i].To; len(key) < 20 || got.ManagementKey != key {
		t.Errorf("the new management key %q is not core's (%q)", key, got.ManagementKey)
	}
	if !slices.Contains(got.ClientKeys, "fake-cpa-client-key") || !slices.Contains(got.ClientKeys, "fake-client-key-1") {
		t.Errorf("client keys %v", got.ClientKeys)
	}
	if got.Listen != "127.0.0.1:0" {
		t.Errorf("listen changed to %s", got.Listen)
	}
	ids := map[string]bool{}
	for _, a := range got.Accounts() {
		ids[a.ID] = true
	}
	for _, r := range c.creds.Records() {
		ids[r.ID] = true
	}
	if !ids["claude-api-key-1"] || !ids["claude-a"] || ids["antigravity-a"] {
		t.Errorf("accounts %v", ids)
	}

	// Importing again brings no account twice: each is named as one core already has.
	req.DryRun = false
	code, rep, body = importCall(t, c, got.ManagementKey, req)
	if code != http.StatusOK {
		t.Fatalf("second import: %d %s", code, body)
	}
	for _, it := range []string{"claude-api-key[0]", "auth file claude-a.json"} {
		if !slices.Contains(names(rep.NotBrought), it) {
			t.Errorf("second import: %s is not under not_brought: %s", it, body)
		}
	}
}

// In a real or dry import into a running core, every listen row says core keeps listening on its
// own address, with the file's reach in from and core's in to: the row of a file with no
// server.host too, which today's core listened on every interface without.
func TestSetupImportKeepsCoreListen(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"0.0.0.0:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "port: 8317\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	want := c.Config().Listen
	for _, dry := range []bool{true, false} {
		code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: dry})
		if code != http.StatusOK {
			t.Fatalf("dry %v: %d %s", dry, code, body)
		}
		rows := map[string]management.ImportRow{}
		for _, r := range rep.Changed {
			rows[r.What] = r
		}
		for _, what := range []string{"server.host", "port"} {
			r, ok := rows[what]
			if !ok {
				t.Fatalf("dry %v: %s is not under changed: %s", dry, what, body)
			}
			// The file's reach: every interface (no host) on 8317.
			if !strings.Contains(r.From, "every interface") && r.From != "0.0.0.0:8317" {
				t.Errorf("dry %v: %s from %q", dry, what, r.From)
			}
			if r.To != want {
				t.Errorf("dry %v: %s to %q, want core's %q: %s", dry, what, r.To, want, body)
			}
			if !strings.Contains(r.Why, "core keeps listening on "+want) {
				t.Errorf("dry %v: %s why %q", dry, what, r.Why)
			}
			if !strings.Contains(r.KeepWith, `"listen": "0.0.0.0:8317"`) {
				t.Errorf("dry %v: %s keep_with %q", dry, what, r.KeepWith)
			}
		}
	}
}

// A path that is not absolute, a missing file and a file that is not a config are refused, and
// the refusal holds none of the file's secrets.
func TestSetupImportRefusals(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	bad := filepath.Join(dir, "bad.yaml")
	writeTestFile(t, bad, "routing: {cooldown: {quota-response-mode: fake-secret-mode}}\nremote-management: {secret-key: fake-secret-mode}\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for _, cs := range []struct {
		req  management.SetupImportRequest
		code int
	}{
		{management.SetupImportRequest{Config: "config.yaml"}, http.StatusBadRequest},
		{management.SetupImportRequest{Config: filepath.Join(dir, "missing.yaml")}, http.StatusBadRequest},
		{management.SetupImportRequest{Config: bad, DryRun: true}, http.StatusUnprocessableEntity},
	} {
		code, _, body := importCall(t, c, "fake-mgmt-key", cs.req)
		if code != cs.code || strings.Contains(body, "fake-secret-mode") {
			t.Errorf("%+v: %d %s, want %d without the secret", cs.req, code, body, cs.code)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(s string) bool { return !slices.Contains(b, s) })
}
