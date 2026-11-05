package misc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMergeMetadata(t *testing.T) {
	source := map[string]any{
		"type":         "codex",
		"access_token": "token-123",
	}
	metadata := map[string]any{
		"disabled":   false,
		"email":      "test@example.com",
		"prefix":     "custom-prefix",
		"websockets": false,
		"note":       "custom note",
	}

	result, err := MergeMetadata(source, metadata)
	if err != nil {
		t.Fatalf("MergeMetadata() error = %v", err)
	}

	if result["type"] != "codex" {
		t.Errorf("type = %v, want codex", result["type"])
	}
	if result["access_token"] != "token-123" {
		t.Errorf("access_token = %v, want token-123", result["access_token"])
	}
	if result["disabled"] != false {
		t.Errorf("disabled = %v, want false", result["disabled"])
	}
	if result["email"] != "test@example.com" {
		t.Errorf("email = %v, want test@example.com", result["email"])
	}
	if result["prefix"] != "custom-prefix" {
		t.Errorf("prefix = %v, want custom-prefix", result["prefix"])
	}
	if result["websockets"] != false {
		t.Errorf("websockets = %v, want false", result["websockets"])
	}
	if result["note"] != "custom note" {
		t.Errorf("note = %v, want custom note", result["note"])
	}
}

func TestWriteFileAtomic_FailureKeepsOldFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions enforced for this user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cred.json")
	if errWrite := os.WriteFile(path, []byte("old"), 0o600); errWrite != nil {
		t.Fatalf("write old file: %v", errWrite)
	}
	// A read-only directory makes the temp file creation fail before anything is replaced.
	if errChmod := os.Chmod(dir, 0o500); errChmod != nil {
		t.Fatalf("chmod dir: %v", errChmod)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if errAtomic := WriteFileAtomic(path, []byte("new")); errAtomic == nil {
		t.Fatal("WriteFileAtomic into a read-only directory succeeded, want error")
	}
	got, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read old file: %v", errRead)
	}
	if string(got) != "old" {
		t.Errorf("file content after failed write = %q, want %q", got, "old")
	}
}
