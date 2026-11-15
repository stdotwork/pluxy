package e2e

import (
	"bufio"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestStopRightAfterListening sends SIGTERM the moment core says where it listens, as a program
// that starts core and may stop it at once does (the desktop app). Core must already handle it
// then: it drains and exits 0, never killed by the signal. Repeated, since the gap it closes is
// short: before the fix, about two runs in three were killed.
func TestStopRightAfterListening(t *testing.T) {
	for i := range 20 {
		path := filepath.Join(t.TempDir(), "config.json")
		writeConfig(t, path, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
		})
		cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-watch-interval", "0")
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(stderr)
		said := false
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "pluxy: listening on ") {
				said = true
				break
			}
		}
		if said {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		} else {
			_ = cmd.Process.Kill()
		}
		go io.Copy(io.Discard, stderr)
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		select {
		case err := <-exited:
			if !said {
				t.Fatalf("run %d: core never said where it listens (%v)", i, err)
			}
			if err != nil {
				t.Fatalf("run %d: core stopped right after it said where it listens: %v, want a clean exit", i, err)
			}
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Fatalf("run %d: core did not stop", i)
		}
	}
}
