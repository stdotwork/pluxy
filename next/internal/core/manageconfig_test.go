package core

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/server"
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
