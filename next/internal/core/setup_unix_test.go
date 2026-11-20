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
)

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
