// Command panelcore runs a real pluxy core with fake provider extensions, for the web panel's
// browser tests. Run it from the next folder: go run ./e2e/panelcore.
//
// Once core listens and test/beta is disabled, it prints one line of JSON on stdout,
// {"base":"http://127.0.0.1:port","key":"<management key>"}, and nothing else on stdout. It runs
// until stdin reaches EOF or it gets SIGINT or SIGTERM, then stops core and removes its files.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	platform     = runtime.GOOS + "/" + runtime.GOARCH
	startTimeout = 20 * time.Second
	stopTimeout  = 10 * time.Second
)

func main() {
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "panelcore:", err)
		os.Exit(1)
	}
}

// serve builds core and the test extension, starts core with them, prints its address and
// management key, and runs until stdin ends or a signal arrives. The deferred calls stop core
// and then remove the temporary folder, also when setup fails.
func serve() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, err := moduleRoot()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "pluxy-panelcore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	pluxy := filepath.Join(dir, "pluxy")
	testext := filepath.Join(dir, "testext")
	if err := buildBinary(root, pluxy, "./cmd/pluxy"); err != nil {
		return err
	}
	if err := buildBinary(root, testext, "./e2e/testext"); err != nil {
		return err
	}
	if _, err := writeLocal(dir, testext); err != nil {
		return err
	}
	index, err := writeRegistry(dir, testext)
	if err != nil {
		return err
	}
	mgmtKey, err := randomKey()
	if err != nil {
		return err
	}
	clientKey, err := randomKey()
	if err != nil {
		return err
	}
	cfgPath, err := writeConfig(dir, clientKey, mgmtKey)
	if err != nil {
		return err
	}

	c, err := startCore(pluxy, cfgPath, index)
	if err != nil {
		return err
	}
	defer c.stop()
	base, err := c.waitListening(ctx)
	if err != nil {
		return err
	}
	if err := disablePackage(base, mgmtKey, "test/beta"); err != nil {
		return err
	}
	out, err := json.Marshal(map[string]string{"base": base, "key": mgmtKey})
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(append(out, '\n')); err != nil {
		return err
	}

	stdinDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdinDone)
	}()
	select {
	case <-stdinDone:
	case <-ctx.Done():
	case <-c.done:
		return fmt.Errorf("core exited while running: %v", c.err)
	}
	return nil
}

// moduleRoot returns the folder of the next module, from the folder the command runs in.
func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		return "", errors.New("not inside the next module: run from its folder")
	}
	return filepath.Dir(mod), nil
}

// buildBinary compiles package pkg, relative to root, into out.
func buildBinary(root, out, pkg string) error {
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, pkg)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("building %s: %w", pkg, err)
	}
	return nil
}

// manifest is an extension's manifest, pluxy-extension.json.
type manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	ID              string            `json:"id"`
	Version         string            `json:"version"`
	Protocol        []int             `json:"protocol"`
	Programs        map[string]string `json:"programs"`
}

// writeLocal puts the test extension in the local folder, with the manifest the config runs from
// a path, and returns the manifest's path.
func writeLocal(dir, testext string) (string, error) {
	local := filepath.Join(dir, "local")
	if err := os.Mkdir(local, 0o755); err != nil {
		return "", err
	}
	prog, err := os.ReadFile(testext)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(local, "testext"), prog, 0o755); err != nil {
		return "", err
	}
	m, err := json.Marshal(manifest{
		ID: "test/local", Version: "0.1.0", Protocol: []int{0},
		Programs: map[string]string{platform: "testext"},
	})
	if err != nil {
		return "", err
	}
	path := filepath.Join(local, "pluxy-extension.json")
	if err := os.WriteFile(path, m, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// writeRegistry writes the archives of test/alpha and test/beta and the registry index that
// offers them, and returns the index's path.
func writeRegistry(dir, testext string) (string, error) {
	prog, err := os.ReadFile(testext)
	if err != nil {
		return "", err
	}
	reg := filepath.Join(dir, "registry")
	if err := os.Mkdir(reg, 0o755); err != nil {
		return "", err
	}
	pkgs := map[string]any{}
	for _, p := range []struct{ id, version, file string }{
		{"test/alpha", "0.2.0", "alpha.tar.gz"},
		{"test/beta", "1.0.0", "beta.tar.gz"},
	} {
		archive, err := packageArchive(p.id, p.version, prog)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(reg, p.file), archive, 0o644); err != nil {
			return "", err
		}
		sum := sha256.Sum256(archive)
		pkgs[p.id] = map[string]any{"releases": []any{map[string]any{
			"version": p.version, "protocol": []int{0},
			"artifacts": map[string]any{platform: map[string]any{
				"url": p.file, "sha256": hex.EncodeToString(sum[:]), "size": len(archive),
			}},
		}}}
	}
	index, err := json.Marshal(map[string]any{"registry_version": 0, "packages": pkgs})
	if err != nil {
		return "", err
	}
	path := filepath.Join(reg, "index.json")
	if err := os.WriteFile(path, index, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// packageArchive packs the test extension as a release archive of package id, laid out as the
// Codex archive in e2e/packages_test.go is.
func packageArchive(id, version string, prog []byte) ([]byte, error) {
	m, err := json.Marshal(manifest{
		ID: id, Version: version, Protocol: []int{0},
		Programs: map[string]string{platform: "bin/testext"},
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		mode int64
		body []byte
	}{{"pluxy-extension.json", 0o644, m}, {"bin/", 0o755, nil}, {"bin/testext", 0o755, prog}} {
		h := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(f.name, "/") {
			h.Typeflag = tar.TypeDir
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.body); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// randomKey returns 32 random bytes, hex-encoded.
func randomKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// writeConfig writes core's config file with the local extension's account and the two accounts
// of test/alpha, and returns its path.
func writeConfig(dir, clientKey, mgmtKey string) (string, error) {
	cfg := map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  mgmtKey,
		"credentials_dir": filepath.Join(dir, "credentials"),
		"packages":        map[string]any{"dir": filepath.Join(dir, "packages")},
		"extension": map[string]any{
			"manifest": filepath.Join(dir, "local", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "local-1", "models": []string{"fake-model"},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-upstream-key"},
			}},
		},
		"extensions": map[string]any{"test/alpha": map[string]any{"accounts": []any{
			map[string]any{
				"id": "alpha-1", "models": []string{"alpha-model"},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-alpha-key"},
			},
			map[string]any{
				"id": "alpha-2", "models": []string{"alpha-model"},
				"credential": map[string]any{"kind": "api_key", "api_key": "fake-alpha-key-2"},
			},
		}}},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// core is a running pluxy core.
type core struct {
	cmd       *exec.Cmd
	listening chan string   // receives the address from core's listening line
	done      chan struct{} // closed once core has exited and its stderr is drained
	err       error         // core's exit status; valid once done is closed
}

// startCore runs core with the config at cfgPath and the registry index at index as its bundled
// packages. Core's stderr goes to this program's stderr, each line prefixed "core: ".
func startCore(bin, cfgPath, index string) (*core, error) {
	cmd := exec.Command(bin, "-config", cfgPath, "-bundled", index, "-watch-interval", "0")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &core{cmd: cmd, listening: make(chan string, 1), done: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(os.Stderr, "core: "+line)
			if addr, ok := strings.CutPrefix(line, "pluxy: listening on "); ok {
				select {
				case c.listening <- addr:
				default:
				}
			}
		}
		_, _ = io.Copy(io.Discard, stderr) // a line too long to scan: keep draining so core never blocks
		c.err = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

// waitListening returns the base URL core logs in its listening line, which already carries the
// http:// scheme. It fails if core exits first or does not log the line within startTimeout.
func (c *core) waitListening(ctx context.Context) (string, error) {
	timer := time.NewTimer(startTimeout)
	defer timer.Stop()
	select {
	case base := <-c.listening:
		return base, nil
	case <-c.done:
		return "", fmt.Errorf("core exited before listening: %v", c.err)
	case <-timer.C:
		return "", fmt.Errorf("core did not report its listening address within %s", startTimeout)
	case <-ctx.Done():
		return "", errors.New("interrupted before core listened")
	}
}

// stop asks core to shut down and kills it if it has not exited within stopTimeout.
func (c *core) stop() {
	_ = c.cmd.Process.Signal(os.Interrupt)
	timer := time.NewTimer(stopTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
	case <-timer.C:
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}

// disablePackage disables installed package id through core's management API.
func disablePackage(base, key, id string) error {
	req, err := http.NewRequest(http.MethodPost, base+"/v0/management/packages/"+id+"/disable", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	client := &http.Client{Timeout: stopTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("disable %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("disable %s: status %d: %s", id, resp.StatusCode, body)
	}
	return nil
}
