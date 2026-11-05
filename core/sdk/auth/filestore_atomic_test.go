package auth

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func saveMetadataAuth(t *testing.T, baseDir, fileName string, metadata map[string]any) error {
	t.Helper()
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Metadata: metadata,
	})
	return err
}

func TestFileTokenStoreSaveRewriteTightensExistingFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are not enforced on Windows")
	}
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity-user.json")
	if err := os.WriteFile(path, []byte(`{"type":"antigravity","access_token":"old","disabled":false}`), 0o644); err != nil {
		t.Fatalf("write existing auth file: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod existing auth file: %v", err)
	}

	if err := saveMetadataAuth(t, baseDir, "antigravity-user.json", map[string]any{"type": "antigravity", "access_token": "new"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved auth file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("rewritten auth file mode = %o, want 600", got)
	}
}

func TestFileTokenStoreSaveRewriteReplacesContentFully(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity-user.json")
	// A long old body, so a rewrite that did not replace the whole file would leave a tail behind.
	old := `{"type":"antigravity","access_token":"old-token-with-a-very-long-value-0123456789012345678901234567890123456789","extra":"stale","disabled":false}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write existing auth file: %v", err)
	}

	if err := saveMetadataAuth(t, baseDir, "antigravity-user.json", map[string]any{"type": "antigravity", "access_token": "n"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved auth file: %v", err)
	}
	want := `{"access_token":"n","disabled":false,"type":"antigravity"}`
	if string(got) != want {
		t.Errorf("saved auth file = %s, want %s", got, want)
	}
	leftovers, err := filepath.Glob(filepath.Join(baseDir, "*.tmp.*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestFileTokenStoreSaveFailedRewriteKeepsOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not enforced on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity-user.json")
	old := `{"type":"antigravity","access_token":"old","disabled":false}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatalf("write existing auth file: %v", err)
	}
	// A read-only directory makes the temp file creation fail while the old file stays writable.
	if err := os.Chmod(baseDir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(baseDir, 0o700) })

	err := saveMetadataAuth(t, baseDir, "antigravity-user.json", map[string]any{"type": "antigravity", "access_token": "new"})
	if err == nil {
		t.Fatal("Save() error = nil, want error from read-only directory")
	}
	got, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read auth file: %v", errRead)
	}
	if string(got) != old {
		t.Errorf("auth file after failed save = %s, want unchanged %s", got, old)
	}
}
