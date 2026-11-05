package vertex

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSaveTokenToFile_OwnerOnlyAndReplacesExisting checks that credentials are written
// owner-only, that an existing world-readable file is replaced completely, and that the
// JSON bytes are unchanged. The expected bytes were captured from the previous os.Create
// based implementation.
func TestSaveTokenToFile_OwnerOnlyAndReplacesExisting(t *testing.T) {
	const want = "{\n  \"email\": \"e@x\",\n  \"location\": \"us-central1\",\n  \"prefix\": \"p\",\n  \"project_id\": \"p1\",\n  \"service_account\": {\n    \"client_email\": \"a\\u003cb\\u003e@x\",\n    \"project_id\": \"p1\"\n  },\n  \"type\": \"vertex\",\n  \"weight\": 5\n}\n"

	newStorage := func() *VertexCredentialStorage {
		s := &VertexCredentialStorage{ServiceAccount: map[string]any{"project_id": "p1", "client_email": "a<b>@x"}, ProjectID: "p1", Email: "e@x", Location: "us-central1"}
		s.SetMetadata(map[string]any{"prefix": "p", "weight": 5})
		return s
	}

	dir := t.TempDir()
	if errChmod := os.Chmod(dir, 0o755); errChmod != nil {
		t.Fatalf("chmod dir: %v", errChmod)
	}
	path := filepath.Join(dir, "cred.json")

	check := func(t *testing.T) {
		t.Helper()
		got, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("read saved file: %v", errRead)
		}
		if string(got) != want {
			t.Errorf("saved bytes = %q, want %q", got, want)
		}
		info, errStat := os.Stat(path)
		if errStat != nil {
			t.Fatalf("stat saved file: %v", errStat)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("saved file mode = %v, want -rw-------", info.Mode().Perm())
		}
		entries, errDir := os.ReadDir(dir)
		if errDir != nil {
			t.Fatalf("read dir: %v", errDir)
		}
		if len(entries) != 1 {
			t.Errorf("directory has %d entries after save, want only the credential file", len(entries))
		}
	}

	t.Run("new file", func(t *testing.T) {
		if errSave := newStorage().SaveTokenToFile(path); errSave != nil {
			t.Fatalf("SaveTokenToFile: %v", errSave)
		}
		check(t)
	})

	t.Run("replaces longer world-readable file", func(t *testing.T) {
		old := bytes.Repeat([]byte("old-secret-content "), 200)
		if errWrite := os.WriteFile(path, old, 0o644); errWrite != nil {
			t.Fatalf("write old file: %v", errWrite)
		}
		if errChmod := os.Chmod(path, 0o644); errChmod != nil {
			t.Fatalf("chmod old file: %v", errChmod)
		}
		if errSave := newStorage().SaveTokenToFile(path); errSave != nil {
			t.Fatalf("SaveTokenToFile: %v", errSave)
		}
		check(t)
	})
}
