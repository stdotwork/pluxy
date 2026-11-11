//go:build unix

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The test binary doubles as the fake extensions below. Core clears an extension's environment,
// so the fake reads its behaviour from the extension ID core passes it.
func TestMain(m *testing.M) {
	if mode, ok := strings.CutPrefix(os.Getenv(protocol.EnvExtensionID), "hosttest/"); ok {
		fakeExtension(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeExtension misbehaves during startup. It writes its process ID to the file "pid" in its
// working folder (the package folder) so the test can check it was reaped.
func fakeExtension(mode string) {
	if mode == "echo-credential" {
		echoCredential()
		return
	}
	if mode == "session-bad-frame" {
		sessionBadFrame(1)
		return
	}
	if mode == "session-bad-frame-14" {
		sessionBadFrame(14)
		return
	}
	if mode == "echo-proxy" {
		echoProxy()
		return
	}
	if mode == "echo-secrets" {
		echoSecrets()
		return
	}
	if mode == "refresh-odd-reply" {
		refreshOddReply()
		return
	}
	if mode != "sleep-child" { // the child shares its parent's folder
		_ = os.WriteFile("pid", []byte(strconv.Itoa(os.Getpid())), 0o644)
		_ = os.WriteFile("env", []byte(strings.Join(os.Environ(), "\n")), 0o644)
	}
	switch mode {
	case "close-stdout":
		// Close stdout without a handshake and stay alive.
		os.Stdout.Close()
	case "endless-line":
		// A handshake line that never ends.
		chunk := []byte(strings.Repeat("x", 64<<10))
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				break
			}
		}
	case "no-describe":
		// Handshake, then never answer describe.
		socket := os.Getenv(protocol.EnvSocket)
		ln, err := net.Listen("unix", socket)
		if err != nil {
			os.Exit(1)
		}
		fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
			}
		}()
	case "serve", "slow-exit", "session":
		// A working extension; slow-exit takes half a second to exit once asked to stop;
		// session also holds sessions (fakeSession).
		ext := sdk.Extension{ID: os.Getenv(protocol.EnvExtensionID), Version: "0.0.0",
			Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
				if err := out.Start(llm.ResponseInfo{ID: "r", Model: call.Request.Model}); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}}
		if mode == "session" {
			ext.Session = openFakeSession
		}
		err := sdk.Serve(ext)
		if mode == "slow-exit" {
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	case "child-holds-stderr":
		// Start a child that keeps our stderr open after we are killed, then stay silent.
		self, _ := os.Executable()
		child := exec.Command(self)
		child.Env = []string{protocol.EnvExtensionID + "=hosttest/sleep-child"}
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
		_ = os.WriteFile("child.pid", []byte(strconv.Itoa(child.Process.Pid)), 0o644)
	}
	time.Sleep(time.Hour)
}

func TestStartFailuresStayUnderTheDeadline(t *testing.T) {
	const startTimeout = 2 * time.Second
	// Killing a failed start and collecting its exit takes milliseconds, or up to waitDelay when
	// a child of the extension holds its output open. The rest is slack for a loaded machine.
	budget := startTimeout + waitDelay + 2*time.Second
	for _, tc := range []struct {
		mode    string
		wantErr string
	}{
		{"close-stdout", "closed its stdout"},
		{"endless-line", "longer than"},
		{"no-describe", "describe"},
		{"child-holds-stderr", "no handshake"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := fakePackage(t, tc.mode)
			if tc.mode == "child-holds-stderr" {
				t.Cleanup(func() { killFromFile(filepath.Join(dir, "child.pid")) })
			}
			type result struct {
				ext *Extension
				err error
			}
			done := make(chan result, 1)
			start := time.Now()
			go func() {
				// A long stop timeout: a failed start must not wait for a graceful stop.
				ext, err := Start(filepath.Join(dir, ManifestFile), Options{StartTimeout: startTimeout, StopTimeout: 30 * time.Second, Log: io.Discard})
				done <- result{ext, err}
			}()
			var res result
			select {
			case res = <-done:
			case <-time.After(budget):
				killFromFile(filepath.Join(dir, "pid"))
				t.Fatalf("Start had not returned %s after it began, with a %s start timeout", budget, startTimeout)
			}
			if res.err == nil {
				res.ext.Stop()
				t.Fatal("Start succeeded, want an error")
			}
			if !strings.Contains(res.err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", res.err, tc.wantErr)
			}
			t.Logf("Start failed after %s: %v", time.Since(start).Round(time.Millisecond), res.err)
			pid := readPID(t, filepath.Join(dir, "pid"))
			// Signal 0 reaches a zombie too, so ESRCH means the process was killed and reaped.
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("after Start failed, process %d still exists or was not reaped (signal 0: %v)", pid, err)
			}
		})
	}
}

// fakePackage writes a package folder whose program is this test binary.
func fakePackage(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, "ext")); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"manifest_version": 0,
		"id":               "hosttest/" + mode,
		"version":          "0.0.0",
		"protocol":         []int{protocol.Version},
		"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "ext"},
	})
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake extension wrote no pid file: %v", err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func killFromFile(path string) {
	if b, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(string(b)); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// echoCredential is an extension that writes the credentials it gets to stderr and stdout, as a
// careless provider's debug output would. A generate call without a credential writes the access
// token its last refresh returned.
func echoCredential() {
	var mu sync.Mutex
	var refreshed string
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-credential",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			mu.Lock()
			secret := refreshed
			mu.Unlock()
			if c := call.Account.Credential; c != nil {
				secret = c.APIKey + c.AccessToken
			}
			fmt.Fprintf(os.Stderr, "stderr holds %s\nstderr done %s\n", secret, call.Account.ID)
			fmt.Fprintf(os.Stdout, "stdout holds %s\nstdout done %s\n", secret, call.Account.ID)
			if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
				return err
			}
			return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
		},
		Refresh: func(ctx context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
			fmt.Fprintf(os.Stderr, "refreshing with %s\n", call.Account.Credential.RefreshToken)
			mu.Lock()
			refreshed = "new-access-token-5678"
			mu.Unlock()
			return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "new-access-token-5678", RefreshToken: "new-refresh-token-5678"}, nil
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// What an extension writes to stderr and stdout is logged without the credentials core gave it
// or got back from it, including one a refresh replaced.
func TestExtensionOutputIsRedacted(t *testing.T) {
	const (
		apiKey     = "sk-log-secret-1234"
		oldRefresh = "old-refresh-token-1234"
		newAccess  = "new-access-token-5678"
	)
	log := &syncBuffer{}
	ext, err := Start(filepath.Join(fakePackage(t, "echo-credential"), ManifestFile), Options{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	ctx := context.Background()
	generate := func(a protocol.Account) {
		s := ext.Generate(ctx, a, llm.Request{Model: "m"})
		defer s.Close()
		for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
		}
		log.waitFor(t, "stderr done "+a.ID, "stdout done "+a.ID)
	}
	generate(protocol.Account{ID: "key", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: apiKey}})
	if _, e := ext.Refresh(ctx, protocol.Account{ID: "oauth", Credential: &protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: "old-access-token-1234", RefreshToken: oldRefresh}}); e != nil {
		t.Fatal(e)
	}
	generate(protocol.Account{ID: "oauth"})
	out := log.String()
	for _, secret := range []string{apiKey, oldRefresh, newAccess} {
		if strings.Contains(out, secret) {
			t.Errorf("the log holds %q:\n%s", secret, out)
		}
	}
	if n := strings.Count(out, protocol.Redacted); n != 5 {
		t.Errorf("want 5 redacted credentials in the log, got %d:\n%s", n, out)
	}
}

// syncBuffer is a log both of an extension's output streams write to.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor waits until the log holds every line in lines: the output streams are read on their
// own, so a line can arrive after the call that wrote it has finished.
func (b *syncBuffer) waitFor(t *testing.T, lines ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, missing := b.String(), ""
		for _, l := range lines {
			if !strings.Contains(out, l) {
				missing = l
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the log never got %q:\n%s", missing, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A line too long to log is left out rather than logged in parts: a credential split between
// two parts would not be recognised by the redaction.
func TestLongOutputLineIsNotSplit(t *testing.T) {
	const secret = "sk-split-secret-1234"
	var out bytes.Buffer
	l := &lineLogger{w: &out, prefix: "[x] ", redact: func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }}
	// The first write passes the limit in the middle of the credential.
	half := len(secret) / 2
	for _, chunk := range []string{strings.Repeat("a", maxLogLine) + secret[:half], secret[half:] + "b\nshort " + secret + "\n"} {
		if _, err := l.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got := out.String()
	if strings.Contains(got, secret[:half]) || strings.Contains(got, secret[half:]) {
		t.Errorf("part of the credential was logged")
	}
	if want := "[x] short [redacted]\n"; !strings.HasSuffix(got, want) || strings.Count(got, "\n") != 2 {
		t.Errorf("log = %.200q..., want a note for the long line, then %q", got, want)
	}
	// A long line that ends within one write is left out too.
	out.Reset()
	if _, err := l.Write([]byte(strings.Repeat("x", maxLogLine+1) + "\n")); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "xxxx") || !strings.Contains(got, "not logged") {
		t.Errorf("log = %.200q..., want only a note", got)
	}
}

// Core clears an extension's environment, except for the variable that lets tests move an
// extension's login hosts, and only when core itself holds it.
func TestStartPassesOnlyTheTestLoginHostsVariable(t *testing.T) {
	t.Setenv("PLUXY_HOSTTEST_OTHER", "leaks")
	for _, tc := range []struct {
		value string
		want  bool
	}{{"1", true}, {"", false}} {
		t.Setenv(protocol.EnvTestLoginHosts, tc.value)
		dir := fakePackage(t, "serve")
		ext, err := Start(filepath.Join(dir, ManifestFile), Options{})
		if err != nil {
			t.Fatal(err)
		}
		ext.Stop()
		b, err := os.ReadFile(filepath.Join(dir, "env"))
		if err != nil {
			t.Fatalf("the fake extension wrote no env file: %v", err)
		}
		env := "\n" + string(b) + "\n"
		if got := strings.Contains(env, "\n"+protocol.EnvTestLoginHosts+"=1\n"); got != tc.want {
			t.Errorf("with %s=%q in core's environment, extension has it = %v, want %v:\n%s", protocol.EnvTestLoginHosts, tc.value, got, tc.want, b)
		}
		if strings.Contains(env, "PLUXY_HOSTTEST_OTHER") {
			t.Errorf("another variable of core's reached the extension:\n%s", b)
		}
	}
}
