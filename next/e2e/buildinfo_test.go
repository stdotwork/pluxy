package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// buildInfoVar is the package path the linker sets build info through (-X).
const buildInfoVar = "github.com/stdotwork/pluxy/next/internal/buildinfo."

// TestBuildInfoFromLinker builds core with its version, commit and build date set at link time,
// then checks that -version prints them without reading a config, that core logs them at start,
// and that management status carries them.
func TestBuildInfoFromLinker(t *testing.T) {
	const (
		version = "1.2.3-e2e"
		commit  = "0123456789abcdef0123456789abcdef01234567"
		built   = "2026-11-10T08:00:00Z"
	)
	bin := filepath.Join(t.TempDir(), "pluxy")
	ldflags := "-X " + buildInfoVar + "Version=" + version +
		" -X " + buildInfoVar + "Commit=" + commit +
		" -X " + buildInfoVar + "Date=" + built
	if out, err := exec.Command("go", "build", "-buildvcs=false", "-ldflags", ldflags, "-o", bin, "../cmd/pluxy").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	want := "pluxy " + version + " (" + commit + ", " + built + ")"

	// The config file named here does not exist: -version must exit 0 before it reads any config.
	out, err := exec.Command(bin, "-version", "-config", filepath.Join(t.TempDir(), "missing.json")).CombinedOutput()
	if err != nil || string(out) != want+"\n" {
		t.Fatalf("-version: %v\n%s\nwant exit 0 and exactly %q", err, out, want)
	}

	base, logged := startSafeCore(t, bin)
	if !slices.Contains(logged, "pluxy: "+want) {
		t.Errorf("core's log before it listened has no line %q:\n%s", "pluxy: "+want, strings.Join(logged, "\n"))
	}
	code, b := management(t, http.MethodGet, base, "/v0/management/status")
	if code != http.StatusOK {
		t.Fatalf("GET /v0/management/status: %d %s", code, b)
	}
	var s struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Built   string `json:"built"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("status %s: %v", b, err)
	}
	if s.Version != version || s.Commit != commit || s.Built != built {
		t.Errorf("status build info = %q, %q, %q; want %q, %q, %q", s.Version, s.Commit, s.Built, version, commit, built)
	}
}

// TestBuildInfoDefaults checks the build made without -ldflags, as TestMain makes it: -version
// still prints exactly one line with a version, a commit and a build date.
func TestBuildInfoDefaults(t *testing.T) {
	out, err := exec.Command(filepath.Join(binDir, "pluxy"), "-version").CombinedOutput()
	line, ok := strings.CutSuffix(string(out), "\n")
	if err != nil || !ok || !regexp.MustCompile(`^pluxy \S+ \(\S+, \S+\)$`).MatchString(line) {
		t.Errorf("-version: %v; output %q, want one line matching ^pluxy \\S+ \\(\\S+, \\S+\\)$", err, out)
	}
}

// startSafeCore runs the program at bin in safe mode, with no extensions and a management key,
// and returns its base URL and the lines it logged before it started listening.
func startSafeCore(t *testing.T, bin string) (string, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "-config", path, "-safe-mode", "-watch-interval", "0")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
	var logged []string
	sc := bufio.NewScanner(stderr)
	for sc.Scan() {
		line := sc.Text()
		t.Logf("core: %.4096s", line)
		logged = append(logged, line)
		if addr, ok := strings.CutPrefix(line, "pluxy: listening on "); ok {
			go io.Copy(io.Discard, stderr) // keep draining what core logs later
			return addr, logged
		}
	}
	t.Fatalf("core exited before listening:\n%s", strings.Join(logged, "\n"))
	return "", nil
}
