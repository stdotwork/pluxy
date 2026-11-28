//go:build !windows

package core

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestImportConfigAtAFIFOIsRefusedNotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no FIFO on this machine: %v", err)
	}

	read := make(chan error, 1)
	go func() {
		_, err := readImportConfig(path)
		read <- err
	}()
	select {
	case err := <-read:
		if err == nil || !strings.Contains(err.Error(), "is not a file") {
			t.Errorf("reading a config at a FIFO: %v, want a refusal as not a file", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening an import's config at a FIFO blocked: the import would hang")
	}
}
