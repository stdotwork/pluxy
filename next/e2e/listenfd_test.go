package e2e

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestListenFD hands core a listening socket the test keeps, as the desktop app does in managed
// mode: core serves on it and says its address, and once core has stopped no other program can
// bind that address while the test holds its copy.
func TestListenFD(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         addr,
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-safe-mode", "-watch-interval", "0", "-listen-fd", "3")
	cmd.ExtraFiles = []*os.File{f}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
		<-exited
	}()
	sc := bufio.NewScanner(stderr)
	said := ""
	for sc.Scan() {
		if a, ok := strings.CutPrefix(sc.Text(), "pluxy: listening on "); ok {
			said = a
			break
		}
	}
	go io.Copy(io.Discard, stderr)
	if said != "http://"+addr {
		t.Fatalf("core said it listens on %q, want http://%s", said, addr)
	}
	r, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("/healthz answered %d", r.StatusCode)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		exited <- nil
	case <-time.After(15 * time.Second):
		t.Fatal("core did not stop")
	}
	if other, err := net.Listen("tcp", addr); err == nil {
		other.Close()
		t.Fatal("another listener could bind the address while the test held the socket")
	}
}

// TestListenFDNotOpen: core refuses to start on a descriptor that is not a listening socket.
func TestListenFDNotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	out, err := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-safe-mode", "-watch-interval", "0", "-listen-fd", "9").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "-listen-fd 9") || strings.Contains(string(out), "listening on") {
		t.Fatalf("core started on a descriptor that is not open (%v):\n%s", err, out)
	}
}
