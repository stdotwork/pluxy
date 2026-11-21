package e2e

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCLIStdin runs the built pluxy program with args and stdin as its standard input, and the
// extra environment variables env.
func runCLIStdin(t *testing.T, env []string, stdin string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), args...)
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	cmd.Dir = t.TempDir() // no config.json here
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return cliResult{stdout.String(), stderr.String(), code}
}

// The pluxy command connects an account through an extension's sign-in, lists it, renews its
// credential, reads its quota and removes it: the account calls the command could not make.
func TestCLIAccountCommands(t *testing.T) {
	pkg := testPackageID(t, "test/account", "0.1.0")
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension":       map[string]any{"manifest": filepath.Join(pkg, "pluxy-extension.json"), "accounts": []any{}},
	})
	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}

	// The extension is started after core listens: wait for it to offer its sign-in.
	deadline := time.Now().Add(15 * time.Second)
	for {
		r := runCLI(t, env, "account", "methods")
		if r.code == 0 && strings.Contains(r.stdout, "test/account") && strings.Contains(r.stdout, "browser") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the extension never offered a sign-in: %s", r)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Add an account through the browser sign-in, giving the callback URL it asks for.
	const id = "e2e-account"
	r := runCLIStdin(t, env, "", "account", "add", "test/account", "--id", id, "--method", "browser",
		"--input", "redirect_url=https://localhost:1455/auth/callback?code=ok")
	if r.code != 0 || !strings.Contains(r.stdout, "account "+id+" added") ||
		!strings.Contains(r.stderr, "https://login.example.test/authorize") {
		t.Fatalf("account add: %s", r)
	}

	r = runCLI(t, env, "account", "list")
	if r.code != 0 || !strings.Contains(r.stdout, id) || !strings.Contains(r.stdout, "Test account") || !strings.Contains(r.stdout, "LABEL") {
		t.Fatalf("account list after adding: %s", r)
	}

	// The sign-in's credential is an OAuth one, which the test extension renews.
	if r := runCLI(t, env, "account", "refresh", id); r.code != 0 || !strings.Contains(r.stdout, "account "+id+" refreshed") {
		t.Fatalf("account refresh: %s", r)
	}

	// Its quota, with percents and times rather than the reply's raw fields.
	r = runCLI(t, env, "account", "quota", id)
	if r.code != 0 || !strings.Contains(r.stdout, "42%") || !strings.Contains(r.stdout, "Test plan") ||
		strings.Contains(r.stdout, "used_percent") {
		t.Fatalf("account quota: %s", r)
	}

	if r := runCLI(t, env, "account", "remove", id); r.code != 0 || !strings.Contains(r.stdout, "account "+id+" removed") {
		t.Fatalf("account remove: %s", r)
	}
	if r := runCLI(t, env, "account", "list"); r.code != 0 || strings.Contains(r.stdout, id) {
		t.Fatalf("account list after removing: %s", r)
	}
}
