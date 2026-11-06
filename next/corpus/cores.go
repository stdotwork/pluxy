package corpus

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Core is a running core under test.
type Core struct {
	// URL is the base URL clients use, for example http://127.0.0.1:43121.
	URL  string
	stop func()
	logs *logBuffer
}

// Stop ends the core and waits for it to exit.
func (c *Core) Stop() { c.stop() }

// Logs returns what the core wrote to stdout and stderr.
func (c *Core) Logs() string { return c.logs.String() }

// Provider tells the runner how to point each core at the fake upstream for one provider
// directory under cases/<format>/.
type Provider struct {
	// OldCore returns the old core's api-keys block for the provider, given the fake upstream
	// URL, the models the cases use and one upstream key per account.
	OldCore func(upstreamURL string, models, keys []string) map[string]any
	// NewCore returns the new core's extension name (a key of NewCoreExtensions) and its
	// accounts for the provider, one per key. models is empty when the accounts serve any
	// model.
	NewCore func(upstreamURL string, models, keys []string) (extension string, accounts []any)
}

// Providers maps a provider directory name to its setup. Later slices add theirs here.
var Providers = map[string]Provider{
	"openai-compatible": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"openai-compatibility": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
	// An upstream that speaks OpenAI Responses with an API key. The new core uses the
	// OpenAI-compatible extension with wire_api "responses". Today's core has no generic
	// Responses upstream; the closest is a Codex API key with a custom base URL, with the
	// Codex client identity (cloaking) turned off.
	"openai-responses": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k, "disable-codex-cloaking": true})
			}
			return map[string]any{"codex": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "wire_api": "responses"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
}

// NewCoreExtensions maps an extension name to its package, relative to the next module root.
var NewCoreExtensions = map[string]string{
	"openai-compatible": "./extensions/openai-compatible",
}

// BuildNewCore builds the new core and its extensions into dir. moduleRoot is the next/
// folder.
func BuildNewCore(moduleRoot, dir string) error {
	builds := map[string]string{"pluxy": "./cmd/pluxy"}
	for name, pkg := range NewCoreExtensions {
		builds[name] = pkg
	}
	for out, pkg := range builds {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, out), pkg)
		cmd.Dir = moduleRoot
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s: %v\n%s", pkg, err, b)
		}
		if out == "pluxy" {
			continue
		}
		manifest := map[string]any{
			"manifest_version": 0,
			"id":               "pluxy/" + out,
			"version":          "0.0.0-corpus",
			"protocol":         []int{0},
			"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: out},
		}
		b, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, out+".pluxy-extension.json"), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// BuildOldCore builds today's core from coreDir (the repository's core/ folder) to out. The
// old core's plugin host needs cgo.
func BuildOldCore(coreDir, out string) error {
	cmd := exec.Command("go", "build", "-o", out, "./cmd/server")
	cmd.Dir = coreDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building the old core: %v\n%s", err, b)
	}
	return nil
}

// StartNewCore starts the new core from binDir with the case's provider pointed at upstreamURL,
// one account per key.
func StartNewCore(binDir, workDir, provider, upstreamURL string, models, keys []string) (*Core, error) {
	p, ok := Providers[provider]
	if !ok || p.NewCore == nil {
		return nil, fmt.Errorf("no new-core setup for provider %q", provider)
	}
	ext, accounts := p.NewCore(upstreamURL, models, keys)
	cfg := map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{ClientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, ext+".pluxy-extension.json"),
			"accounts": accounts,
		},
	}
	path := filepath.Join(workDir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path)
	cmd.Dir = workDir
	cmd.Env = isolatedEnv(workDir)
	return startProcess(cmd, regexp.MustCompile(`pluxy: listening on (http://\S+)`), "")
}

// OldCoreSetup holds the inputs for one old-core run.
type OldCoreSetup struct {
	Binary      string
	WorkDir     string // empty temporary folder: config, auth folder and HOME go here
	Provider    string
	UpstreamURL string
	Models      []string
	Keys        []string // one upstream key per account
}

// StartOldCore starts today's core on a free local port with a generated config: the client
// key, one provider entry with one key per account pointed at the fake upstream, no extra retry
// rounds, no waits and no cooldowns, management and the bundled panel off, and the
// embedded model catalog. HOME, the auth folder and the working folder are all inside
// WorkDir, and outbound proxies point at a closed port so the old core's background version
// checks cannot reach the internet; the fake upstream is on loopback, which is never proxied.
func StartOldCore(s OldCoreSetup) (*Core, error) {
	p, ok := Providers[s.Provider]
	if !ok || p.OldCore == nil {
		return nil, fmt.Errorf("no old-core setup for provider %q", s.Provider)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	authDir := filepath.Join(s.WorkDir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"host":                 "127.0.0.1",
			"port":                 port,
			"response-compression": map[string]any{"enabled": false},
		},
		"management": map[string]any{"secret-key": "", "disable-control-panel": true, "disable-auto-update-panel": true},
		"access":     map[string]any{"api-keys": []string{ClientKey}},
		"routing": map[string]any{
			"retry":    map[string]any{"request-retry": 0, "max-retry-interval": 0},
			"cooldown": map[string]any{"disable-cooling": true},
		},
		"oauth":         map[string]any{"auth-dir": authDir},
		"api-keys":      p.OldCore(s.UpstreamURL, s.Models, s.Keys),
		"observability": map[string]any{"usage": map[string]any{"usage-statistics-enabled": false}},
		"plugins":       map[string]any{"enabled": false},
	}
	// YAML is a superset of JSON, so the old core reads this as its YAML config.
	path := filepath.Join(s.WorkDir, "config.yaml")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Binary, "-config", path, "-local-model")
	cmd.Dir = s.WorkDir
	cmd.Env = isolatedEnv(s.WorkDir)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return startProcess(cmd, regexp.MustCompile(`API server started successfully on: (\S+)`), "http://"+addr)
}

// isolatedEnv is the environment for a core under test: nothing from the caller's
// environment except PATH, and HOME inside the test's folder.
func isolatedEnv(dir string) []string {
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(home, 0o700)
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + dir,
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"NO_PROXY=127.0.0.1,localhost",
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startProcess starts cmd and waits until a line of its output matches ready. The first
// submatch is the base URL unless url is given.
func startProcess(cmd *exec.Cmd, ready *regexp.Regexp, url string) (*Core, error) {
	logs := &logBuffer{}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		pw.Close()
		close(exited)
	}()
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			logs.add(line)
			if m := ready.FindStringSubmatch(line); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-exited
			}
		})
	}
	select {
	case m := <-found:
		if url == "" {
			url = m
		}
		if !strings.HasPrefix(url, "http") {
			url = "http://" + url
		}
		if err := waitListening(url, exited); err != nil {
			stop()
			return nil, fmt.Errorf("%w\n%s", err, logs)
		}
		return &Core{URL: url, stop: stop, logs: logs}, nil
	case <-exited:
		return nil, fmt.Errorf("core exited before it was ready:\n%s", logs)
	case <-time.After(60 * time.Second):
		stop()
		return nil, fmt.Errorf("core was not ready after 60s:\n%s", logs)
	}
}

func waitListening(url string, exited <-chan struct{}) error {
	host := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("core exited")
		default:
		}
		c, err := net.DialTimeout("tcp", host, time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("nothing listening on %s", host)
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuffer) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
