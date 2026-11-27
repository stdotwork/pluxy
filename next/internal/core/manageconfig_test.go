package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicKeepsPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			// WriteFile is subject to the umask; set the mode the test means to check.
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := writeFileAtomic(path, []byte("new contents")); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "new contents" {
				t.Fatalf("contents = %q, %v", b, err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != mode {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), mode)
			}
			if names := dirNames(t, dir); len(names) != 1 || names[0] != "config.json" {
				t.Errorf("folder holds %v, want only config.json", names)
			}
		})
	}
}

func TestWriteFileAtomicWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir, linkDir := filepath.Join(dir, "real"), filepath.Join(dir, "links")
	for _, d := range []string{realDir, linkDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(realDir, "config.json")
	link := filepath.Join(linkDir, "config.json")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := writeFileAtomic(link, []byte("through the link")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Errorf("link points to %q, %v; want %q", dest, err, target)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "through the link" {
		t.Errorf("target holds %q, %v", b, err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("target mode = %v, %v; want 0640", fi.Mode().Perm(), err)
	}
	// No temp file beside the target or beside the link.
	if names := dirNames(t, realDir); len(names) != 1 {
		t.Errorf("target folder holds %v", names)
	}
	if names := dirNames(t, linkDir); len(names) != 1 {
		t.Errorf("link folder holds %v", names)
	}
}

func TestWriteFileAtomicFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "missing.json"), []byte("x")); err == nil {
		t.Error("writing a file that is not there succeeded")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("folder holds %v, want nothing", names)
	}
}

// A write that fails after the temp file exists (here the final rename, because the target is
// a folder) removes the temp file.
func TestWriteFileAtomicRemovesTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("replacing a folder with a file succeeded")
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("folder holds %v, want only config.json", names)
	}
}

// safeCore starts core in safe mode (no extensions) on a JSON config holding cfg.
func safeCore(t *testing.T, cfg string) (*Core, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c, path
}

const safeConfig = `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-management-key"}` + "\n"

func TestWriteConfigKeepsAnotherProgramsWrite(t *testing.T) {
	c, path := safeCore(t, safeConfig)
	other := strings.Replace(safeConfig, "fake-client-key-1", "written-by-an-editor", 1)
	before := c.Status()
	err := c.WriteConfig("test", func(cur []byte) ([]byte, error) {
		// Another program writes the file after core read it, while the change is prepared.
		if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
			t.Fatal(err)
		}
		return append(cur, ' '), nil
	})
	var ee *editError
	if !errors.As(err, &ee) || ee.status != http.StatusConflict {
		t.Fatalf("WriteConfig = %v, want a conflict", err)
	}
	if got, _ := os.ReadFile(path); string(got) != other {
		t.Errorf("the other program's write was lost: %s", got)
	}
	if after := c.Status(); after.Active != before.Active || after.Desired != before.Desired {
		t.Errorf("revisions changed: %+v, were %+v", after, before)
	}
}

func TestRefusedChangeLogsNoSecret(t *testing.T) {
	c, _ := safeCore(t, safeConfig)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	// A strategy core refuses, whose value is the client key: the refusal names the value.
	err := c.WriteConfig("test", func(cur []byte) ([]byte, error) {
		return []byte(strings.Replace(string(cur), `"management_key"`, `"routing": {"strategy": "fake-client-key-1"}, "management_key"`, 1)), nil
	})
	if err == nil || !strings.Contains(err.Error(), "fake-client-key-1") {
		t.Fatalf("WriteConfig = %v, want a refusal that names the strategy", err)
	}
	if !strings.Contains(buf.String(), "refused") || strings.Contains(buf.String(), "fake-client-key-1") {
		t.Errorf("log: %s", buf.String())
	}
}

func TestRefusedSettingsReplyShowsNoFileSecret(t *testing.T) {
	// Safe mode does not read the manifest, so the account's credential stays only in the file.
	c, _ := safeCore(t, `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-management-key",
		"extension": {"manifest": "missing.json", "accounts": [{"id": "a", "credential": {"kind": "api_key", "api_key": "fake-provider-secret"}}]}}`+"\n")
	req := httptest.NewRequest(http.MethodPatch, ManagementPrefix+"settings", strings.NewReader(`{"routing": {"strategy": "fake-provider-secret"}}`))
	req.Header.Set("Authorization", "Bearer fake-management-key")
	req.RemoteAddr = "127.0.0.1:1234" // management answers only this machine
	rec := httptest.NewRecorder()
	c.Management().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "pluxy-redacted-") || strings.Contains(rec.Body.String(), "fake-provider-secret") {
		t.Errorf("reply %d: %s", rec.Code, rec.Body.String())
	}
}

// leakyConfig refuses to load with a message that quotes the strategy, whose value is the
// account's API key.
const leakyConfig = `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-management-key",
	"routing": {"strategy": "fake-provider-secret"},
	"extension": {"manifest": "missing.json", "accounts": [{"id": "a", "credential": {"kind": "api_key", "api_key": "fake-provider-secret"}}]}}` + "\n"

func TestFailedReloadLogsNoSecret(t *testing.T) {
	c, path := safeCore(t, safeConfig)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	if err := os.WriteFile(path, []byte(leakyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := c.Reload("test")
	if err == nil || strings.Contains(err.Error(), "fake-provider-secret") || !strings.Contains(err.Error(), "pluxy-redacted-") {
		t.Fatalf("Reload = %v, want a refusal without the secret", err)
	}
	if !strings.Contains(buf.String(), "not applied") || strings.Contains(buf.String(), "fake-provider-secret") {
		t.Errorf("log: %s", buf.String())
	}
	if s := c.Status(); s.Desired.Error == "" || strings.Contains(s.Desired.Error, "fake-provider-secret") {
		t.Errorf("status desired.error = %q", s.Desired.Error)
	}
}

func TestFailedStartShowsNoSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(leakyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(Options{ConfigPath: path, SafeMode: true})
	if err == nil || strings.Contains(err.Error(), "fake-provider-secret") || !strings.Contains(err.Error(), "pluxy-redacted-") {
		t.Fatalf("Start = %v, want a refusal without the secret", err)
	}
}

func TestScrubConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(leakyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := server.LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "fake-provider-secret") {
		t.Fatalf("LoadConfig = %v, want a refusal that quotes the value", err)
	}
	if got := ScrubConfigError(path, err); strings.Contains(got.Error(), "fake-provider-secret") || !errors.Is(got, err) {
		t.Errorf("ScrubConfigError = %v", got)
	}
}

// A copy of a settings secret's placeholder whose secret the sent settings no longer hold as it
// was is refused with the copy's line, which PATCH /accounts/{id} shows (server.ErrSecretCopy).
func TestAccountRedactorRefusesACopyWithItsLine(t *testing.T) {
	const key = "sk-real-secret-value-1234"
	r := accountRedactor{configdoc.NewRedactor()}
	cur := "{\n  \"api_key\": \"" + key + "\",\n  \"note\": \"" + key + "\"\n}\n"
	sent := "{\n  \"api_key\": \"brand-new-key-999999\",\n  \"note\": \"" + r.Placeholder(key) + "\"\n}\n"
	_, err := r.RestoreSettings([]byte(sent), []byte(cur))
	if !errors.Is(err, server.ErrSecretCopy) || !strings.HasPrefix(err.Error(), "settings: line 3: a redacted value is a copy of a secret") || strings.Contains(err.Error(), key) {
		t.Errorf("RestoreSettings = %v, want server.ErrSecretCopy naming line 3", err)
	}
}

// The Config page saves the listen address and the remote-management switch as settings. A new
// listen address is written to the file and served on after a restart; until then core keeps its
// address and every other change still applies, and the settings say what waits.
func TestSettingsSaveListenForARestartAndKeepApplyingTheRest(t *testing.T) {
	h, path := startManagementCore(t, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	get := func() management.Settings {
		t.Helper()
		rec := manageCall(t, h, http.MethodGet, "/settings", "")
		var s management.Settings
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil {
			t.Fatalf("GET /settings = %d %s", rec.Code, rec.Body)
		}
		return s
	}
	if s := get(); s.Listen != "127.0.0.1:0" || s.ListenAfterRestart != "" || s.ManagementAllowRemote || !s.FromThisComputer {
		t.Fatalf("settings at the start = %+v", s)
	}

	if rec := manageCall(t, h, http.MethodPatch, "/settings", `{"listen": "127.0.0.1:18417"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH listen = %d %s", rec.Code, rec.Body)
	}
	if s := get(); s.Listen != "127.0.0.1:0" || s.ListenAfterRestart != "127.0.0.1:18417" {
		t.Errorf("after saving a listen address: listen %q, after restart %q", s.Listen, s.ListenAfterRestart)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"listen":"127.0.0.1:18417"`) {
		t.Errorf("the file does not hold the address:\n%s", b)
	}

	// The rest still applies while the address waits: the file is not refused as a whole.
	if rec := manageCall(t, h, http.MethodPatch, "/settings", `{"management_allow_remote": true}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH management_allow_remote with an address waiting = %d %s", rec.Code, rec.Body)
	}
	s := get()
	if !s.ManagementAllowRemote || s.Listen != "127.0.0.1:0" || s.ListenAfterRestart != "127.0.0.1:18417" {
		t.Errorf("settings = %+v", s)
	}
	var status management.Status
	if rec := manageCall(t, h, http.MethodGet, "/status", ""); json.Unmarshal(rec.Body.Bytes(), &status) != nil || status.Desired.Revision != status.Active.Revision || status.Desired.Error != "" {
		t.Errorf("status = %+v, want the newest file in use", status)
	}

	// Setting the address core serves on again leaves nothing waiting.
	if rec := manageCall(t, h, http.MethodPatch, "/settings", `{"listen": "127.0.0.1:0"}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH the active address = %d %s", rec.Code, rec.Body)
	}
	if s := get(); s.ListenAfterRestart != "" {
		t.Errorf("after restart = %q, want none", s.ListenAfterRestart)
	}

	// An address that is no address, or that this computer does not have, is refused: core would
	// not start on it, and nothing is written.
	before, _ := os.ReadFile(path)
	for name, body := range map[string]string{
		"no port":                                `{"listen": "127.0.0.1"}`,
		"a port too large":                       `{"listen": "127.0.0.1:70000"}`,
		"an address this computer does not have": `{"listen": "192.0.2.77:8400"}`,
	} {
		if rec := manageCall(t, h, http.MethodPatch, "/settings", body); rec.Code != http.StatusBadRequest || errorCodeOf(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("a refused address changed the file:\n%s", after)
	}
}

// A call from another computer is told so, which is how a page knows that turning remote
// management off would shut it out.
func TestSettingsSayWhenTheCallIsFromAnotherComputer(t *testing.T) {
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"mgmt-key","management_allow_remote":true}`)
	req := httptest.NewRequest(http.MethodGet, management.Prefix+"/settings", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer mgmt-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var s management.Settings
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.FromThisComputer || !s.ManagementAllowRemote {
		t.Errorf("GET /settings from another computer = %d %s", rec.Code, rec.Body)
	}
}

// The Settings file tab: a new listen address in the text is no mistake. The check passes, the
// save goes through with the rest of the edit applied, and the address waits for the restart.
func TestConfigDocumentWithANewListenAddressSavesAndWaits(t *testing.T) {
	h, path := startManagementCore(t, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	content := `{"listen":"127.0.0.1:18417","client_keys":["fake-client-key-1"],"management_key":"mgmt-key","management_allow_remote":true}`
	body, _ := json.Marshal(map[string]string{"content": content})
	rec := manageCall(t, h, http.MethodPost, "/config/validate", string(body))
	var checked struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &checked) != nil || !checked.Valid {
		t.Fatalf("validate = %d %s", rec.Code, rec.Body)
	}
	if rec := manageCall(t, h, http.MethodPut, "/config", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("PUT /config = %d %s", rec.Code, rec.Body)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "127.0.0.1:18417") {
		t.Errorf("file = %s", b)
	}
	var s management.Settings
	rec = manageCall(t, h, http.MethodGet, "/settings", "")
	if json.Unmarshal(rec.Body.Bytes(), &s) != nil || s.Listen != "127.0.0.1:0" || s.ListenAfterRestart != "127.0.0.1:18417" || !s.ManagementAllowRemote {
		t.Errorf("settings = %+v", s)
	}
}
