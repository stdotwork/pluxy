//go:build !windows

package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/management"
)

// A client's config at a FIFO is refused, not opened for reading: reading one waits for a
// writer, which would hold the scan, and core's stop, until one comes.
func TestClientConfigAtAFIFOIsRefusedNotRead(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("PATH", t.TempDir())
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("no FIFO on this machine: %v", err)
	}

	states := make(chan string, 1)
	go func() {
		_, state := inspectClaudeCode(home, "http://127.0.0.1:8317")
		states <- state
	}()
	select {
	case state := <-states:
		if state != management.ClientStateRefused {
			t.Errorf("state %q for a config at a FIFO, want refused", state)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a client's config at a FIFO blocked: the scan would hang")
	}

	scanned := make(chan struct{})
	go func() {
		scanMachine(context.Background(), t.TempDir(), "127.0.0.1:8317", false, time.Second)
		close(scanned)
	}()
	select {
	case <-scanned:
	case <-time.After(10 * time.Second):
		t.Fatal("a scan with a client's config at a FIFO did not finish")
	}
}

// A probe that does not answer is cut at the 4 s limit, and a child it started goes with it.
func TestProbeTimeoutEndsTheProbesChildren(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	hang := filepath.Join(dir, "hang")
	writeTestFile(t, hang, "#!/bin/sh\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n")
	os.Chmod(hang, 0o755)

	start := time.Now()
	if ver := probeVersion(context.Background(), hang, t.TempDir(), setupProbeTimeout); ver != "" {
		t.Errorf("version %q from a probe that did not answer", ver)
	}
	if took := time.Since(start); took < setupProbeTimeout || took > setupProbeTimeout+2*time.Second {
		t.Errorf("the probe took %v, want the %v limit", took, setupProbeTimeout)
	}
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the probe's child %d still runs after the probe was cut", pid)
		}
	}
}
