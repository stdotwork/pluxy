package e2e

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestListenFD hands core a listening socket the test keeps, as the desktop app does in managed
// mode: core serves on it and says its address, core holds it, and the extension core starts
// does not (an extension holding it could accept calls meant for core, the app's key included).
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
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("socket:[%d]", st.Ino)
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         addr,
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "upstream-1", "models": []string{"gpt-4o"},
				"settings":   map[string]any{"base_url": "http://127.0.0.1:1/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-watch-interval", "0", "-listen-fd", "3")
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
	if runtime.GOOS == "linux" {
		pid := cmd.Process.Pid
		if !holds(pid, socket) {
			t.Fatalf("core (pid %d) does not hold %s", pid, socket)
		}
		kids := children(pid)
		if len(kids) == 0 {
			t.Fatal("core started no extension")
		}
		for _, k := range kids {
			if holds(k, socket) {
				t.Errorf("core's child %d holds the core's listening socket %s", k, socket)
			}
		}
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
}

// children returns the processes whose parent is pid (Linux).
func children(pid int) []int {
	var out []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		k, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// pid (comm) state ppid ...: comm may hold spaces and parentheses.
		f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+1:]))
		if len(f) > 1 && f[1] == strconv.Itoa(pid) {
			out = append(out, k)
		}
	}
	return out
}

// holds reports whether process pid has a descriptor open on target, such as "socket:[123]" (Linux).
func holds(pid int, target string) bool {
	dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if l, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil && l == target {
			return true
		}
	}
	return false
}

// TestListenFDStandardStream: core refuses a standard stream as its socket, and its error still
// reaches standard error.
func TestListenFDStandardStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	out, err := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-safe-mode", "-watch-interval", "0", "-listen-fd", "2").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "-listen-fd 2") {
		t.Fatalf("core did not refuse fd 2 with its error shown (%v):\n%s", err, out)
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
