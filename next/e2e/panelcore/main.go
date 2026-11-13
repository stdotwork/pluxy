// Command panelcore runs a real pluxy core with fake provider extensions, for the web panel's
// browser tests and the desktop app's tests. Run it from the next folder: go run ./e2e/panelcore.
//
// test/alpha brings a benign extension page and a settings schema, and test/hostile a page that
// tries to escape its sandbox; their files are in ui/e2e/fixtures. With -probe, test/probe too:
// the page the desktop app's native test shows (fixtures/probe).
//
// Once core listens and test/beta is disabled, it prints one line of JSON on stdout,
// {"base":"http://127.0.0.1:port","key":"<management key>","client_key":"<client key>",
// "config":"<core's config file>"}, and nothing else on stdout. The config file holds core's
// current management key, also after a rotation. Core writes its log to files (logs.to_file). It runs until stdin reaches
// EOF or it gets SIGINT or SIGTERM, then stops core and removes its files.
//
// With -tls, core's API is reached through an https proxy whose certificate comes from a new
// authority: base is https://127.0.0.1:port, and "ca" names the authority's PEM file.
//
// With -prepare <folder>, it starts nothing: it builds core into <folder>/pluxy and writes the
// test packages as core's bundled packages, <folder>/bundled/index.json (where core looks for
// them beside its program), prints {"core":"<folder>/pluxy"} and exits. The folder is the
// caller's to remove.
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
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
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
	prepare := flag.String("prepare", "", "only build core and its bundled test packages into this folder, then exit")
	withTLS := flag.Bool("tls", false, "reach core's API through an https proxy with a certificate from a new authority")
	probe := flag.Bool("probe", false, "also offer test/probe, the desktop app's native test page")
	flag.Parse()
	if *probe {
		testPackages = append(testPackages, probePackage)
	}
	var err error
	if *prepare != "" {
		err = prepareOnly(*prepare)
	} else {
		err = serve(*withTLS)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "panelcore:", err)
		os.Exit(1)
	}
}

// prepareOnly builds core into dir/pluxy and writes the test packages as its bundled packages.
func prepareOnly(dir string) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pluxy := filepath.Join(dir, "pluxy")
	testext := filepath.Join(dir, "testext")
	if err := buildBinary(root, pluxy, "./cmd/pluxy"); err != nil {
		return err
	}
	if err := buildBinary(root, testext, "./e2e/testext"); err != nil {
		return err
	}
	if _, err := writeRegistry(dir, "bundled", testext, filepath.Join(root, "ui")); err != nil {
		return err
	}
	out, err := json.Marshal(map[string]string{"core": pluxy})
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(out, '\n'))
	return err
}

// serve builds core and the test extension, starts core with them, prints its address and
// management key, and runs until stdin ends or a signal arrives. The deferred calls stop core
// and then remove the temporary folder, also when setup fails.
func serve(withTLS bool) error {
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
	index, err := writeRegistry(dir, "registry", testext, filepath.Join(root, "ui"))
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
	cfgPath, err := writeConfig(dir, clientKey, mgmtKey, withTLS)
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
	ready := map[string]string{"base": base, "key": mgmtKey, "client_key": clientKey, "config": cfgPath}
	if withTLS {
		tlsBase, caFile, closeTLS, err := serveTLS(dir, base)
		if err != nil {
			return err
		}
		defer closeTLS()
		ready["base"], ready["ca"] = tlsBase, caFile
	}
	out, err := json.Marshal(ready)
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
	UI              any               `json:"ui,omitempty"`
	Config          any               `json:"config,omitempty"`
}

// pkg is a test package: its manifest's extra parts and the fixture folder of its pages.
type pkg struct {
	id, version, file string
	ui, config        any
	fixture           string // folder under ui/e2e/fixtures, copied into the package's ui folder
}

var testPackages = []pkg{
	{
		id: "test/alpha", version: "0.2.0", file: "alpha.tar.gz", fixture: "alpha",
		ui: map[string]any{
			"version": 0, "dir": "ui", "permissions": []string{"config.read", "config.write"},
			"pages": []any{map[string]any{"id": "usage", "title": "Alpha usage", "short": "Alpha", "path": "index.html"}},
		},
		// Literal JSON, so the fields keep their order, as the form shows them.
		config: map[string]any{"version": 0, "schema": json.RawMessage(`{
			"type": "object",
			"properties": {
				"greeting": {"type": "string", "title": "Greeting", "maxLength": 40, "default": "hello"},
				"interval": {"type": "integer", "title": "Interval (seconds)", "minimum": 1, "maximum": 3600, "default": 60},
				"unit": {"type": "string", "title": "Unit", "enum": ["tokens", "requests"], "default": "tokens"},
				"detailed": {"type": "boolean", "title": "Detailed view", "default": false},
				"token": {"type": "string", "title": "Export token", "writeOnly": true}
			},
			"required": ["greeting"]
		}`)},
	},
	{id: "test/beta", version: "1.0.0", file: "beta.tar.gz"},
	{
		id: "test/hostile", version: "0.1.0", file: "hostile.tar.gz", fixture: "hostile",
		ui: map[string]any{
			"version": 0, "dir": "ui", "permissions": []string{"config.read"},
			"pages": []any{map[string]any{"id": "hostile", "title": "Hostile", "short": "Evil", "path": "index.html"}},
		},
		config: map[string]any{"version": 0, "schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"canary": map[string]any{"type": "string", "title": "Canary"},
				"panel":  map[string]any{"type": "string", "title": "Panel"},
			},
		}},
	},
}

// probePackage is the page the desktop app's native test shows: it tries to reach the app's
// commands and writes what happened into its own report setting, the one way out it has.
var probePackage = pkg{
	id: "test/probe", version: "0.1.0", file: "probe.tar.gz", fixture: "probe",
	ui: map[string]any{
		"version": 0, "dir": "ui", "permissions": []string{"config.read", "config.write"},
		"pages": []any{map[string]any{"id": "probe", "title": "Probe", "short": "Probe", "path": "index.html"}},
	},
	config: map[string]any{"version": 0, "schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"report": map[string]any{"type": "string", "title": "Report", "maxLength": 4096},
		},
	}},
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

// writeRegistry writes the archives of the test packages and the registry index that offers
// them into dir/folder, and returns the index's path. uiRoot is next/ui: the fixtures and the
// page library.
func writeRegistry(dir, folder, testext, uiRoot string) (string, error) {
	prog, err := os.ReadFile(testext)
	if err != nil {
		return "", err
	}
	reg := filepath.Join(dir, folder)
	if err := os.Mkdir(reg, 0o755); err != nil {
		return "", err
	}
	pkgs := map[string]any{}
	for _, p := range testPackages {
		ui, err := pageFiles(uiRoot, p.fixture)
		if err != nil {
			return "", err
		}
		archive, err := packageArchive(p, prog, ui)
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

// pageFiles returns the files of fixture's pages, with the page library beside them, by name
// under the package's ui folder; none for no fixture.
func pageFiles(uiRoot, fixture string) (map[string][]byte, error) {
	if fixture == "" {
		return nil, nil
	}
	files := map[string][]byte{}
	dir := filepath.Join(uiRoot, "e2e", "fixtures", fixture)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			if files[e.Name()], err = os.ReadFile(filepath.Join(dir, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	if files["pluxy-page.js"], err = os.ReadFile(filepath.Join(uiRoot, "extension-pages", "pluxy-page.js")); err != nil {
		return nil, err
	}
	return files, nil
}

// packageArchive packs the test extension as a release archive of package p, laid out as the
// Codex archive in e2e/packages_test.go is, with ui holding its pages' files.
func packageArchive(p pkg, prog []byte, ui map[string][]byte) ([]byte, error) {
	m, err := json.Marshal(manifest{
		ID: p.id, Version: p.version, Protocol: []int{0},
		Programs: map[string]string{platform: "bin/testext"},
		UI:       p.ui, Config: p.config,
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	type entry struct {
		name string
		mode int64
		body []byte
	}
	entries := []entry{{"pluxy-extension.json", 0o644, m}, {"bin/", 0o755, nil}, {"bin/testext", 0o755, prog}}
	if len(ui) > 0 {
		entries = append(entries, entry{"ui/", 0o755, nil})
		for _, name := range slices.Sorted(maps.Keys(ui)) {
			entries = append(entries, entry{"ui/" + name, 0o644, ui[name]})
		}
	}
	for _, f := range entries {
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
// writeConfig writes core's config. remote: core stands for a remote core behind an https
// proxy, whose forwarded calls the management API answers only with management_allow_remote.
func writeConfig(dir, clientKey, mgmtKey string, remote bool) (string, error) {
	cfg := map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  mgmtKey,
		"credentials_dir": filepath.Join(dir, "credentials"),
		"logs":            map[string]any{"to_file": true},
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
	if remote {
		cfg["management_allow_remote"] = true
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
