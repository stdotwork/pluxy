package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

const (
	setupFileFormat    = 0
	setupFileName      = "setup.json"
	setupProbeTimeout  = 4 * time.Second
	maxSetupProbeBytes = 64 << 10
	maxClientConfig    = 2 << 20
)

// setupPickOrder matches "The setup picks" order: providers then clients.
var setupPickOrder = []string{
	"pluxy/codex",
	"pluxy/claude",
	"pluxy/openai-compatible",
	"pluxy/gemini",
	"pluxy/xai",
	"pluxy/kimi",
	"pluxy/claude-code",
	"pluxy/codex-setup",
	"pluxy/opencode",
	"pluxy/pi-setup",
	"pluxy/omp-setup",
}

// StateDir returns core's state folder beside configPath.
func StateDir(configPath string) string {
	dir := filepath.Dir(configPath)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, "state")
}

type setupStateFile struct {
	Format    int                   `json:"format"`
	FirstRun  bool                  `json:"first_run"`
	ScannedAt *time.Time            `json:"scanned_at,omitempty"`
	Found     management.SetupFound `json:"found"`
}

// setupManager keeps the kept scan and first-run flag in memory and in state/setup.json, and
// runs probes in the background.
type setupManager struct {
	statePath string
	coreInfo  func() (listen string, tlsEnable bool)
	timeout   time.Duration

	mu        sync.Mutex
	firstRun  bool
	scanning  bool
	rescan    bool
	scannedAt *time.Time
	found     management.SetupFound

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newSetupManager(statePath string, coreInfo func() (string, bool)) *setupManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &setupManager{
		statePath: statePath,
		coreInfo:  coreInfo,
		timeout:   setupProbeTimeout,
		firstRun:  true,
		found: management.SetupFound{
			Clients: []management.SetupClient{},
			SignIns: []management.SetupSignIn{},
			Imports: []management.SetupImport{},
		},
		ctx:    ctx,
		cancel: cancel,
	}
	m.load()
	return m
}

func (m *setupManager) load() {
	b, err := os.ReadFile(m.statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		log.Printf("setup: reading %s: %v", m.statePath, err)
		return
	}
	var f setupStateFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("setup: parsing %s: %v", m.statePath, err)
		return
	}
	if f.Format != setupFileFormat {
		log.Printf("setup: %s has format %d; ignoring", m.statePath, f.Format)
		return
	}
	m.firstRun = f.FirstRun
	m.scannedAt = f.ScannedAt
	m.found = normalizeFound(f.Found)
}

func normalizeFound(f management.SetupFound) management.SetupFound {
	if f.Clients == nil {
		f.Clients = []management.SetupClient{}
	}
	if f.SignIns == nil {
		f.SignIns = []management.SetupSignIn{}
	}
	if f.Imports == nil {
		f.Imports = []management.SetupImport{}
	}
	return f
}

func (m *setupManager) saveLocked() error {
	dir := filepath.Dir(m.statePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data := setupStateFile{
		Format:    setupFileFormat,
		FirstRun:  m.firstRun,
		ScannedAt: m.scannedAt,
		Found:     normalizeFound(m.found),
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".setup-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.statePath)
}

func (m *setupManager) isFirstRun() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firstRun
}

func (m *setupManager) markDone() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.firstRun = false
	return m.saveLocked()
}

func (m *setupManager) triggerScan() {
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	if m.scanning {
		m.rescan = true
		m.mu.Unlock()
		return
	}
	m.scanning = true
	m.wg.Add(1)
	m.mu.Unlock()
	go m.runLoop()
}

func (m *setupManager) runLoop() {
	defer m.wg.Done()
	for {
		if m.ctx.Err() != nil {
			m.mu.Lock()
			m.scanning = false
			m.rescan = false
			m.mu.Unlock()
			return
		}
		listen, tlsEnable := m.coreInfo()
		found := scanMachine(m.ctx, listen, tlsEnable, m.timeout)
		if m.ctx.Err() != nil {
			m.mu.Lock()
			m.scanning = false
			m.rescan = false
			m.mu.Unlock()
			return
		}
		now := time.Now().UTC()
		m.mu.Lock()
		// Re-read coreInfo in case SetListenAddr ran while the probes were running.
		curListen, curTLS := m.coreInfo()
		if curListen != "" && curListen != listen {
			found = refreshCoreState(found, curListen, curTLS)
		}
		m.found = found
		m.scannedAt = &now
		if err := m.saveLocked(); err != nil {
			log.Printf("setup: saving %s: %v", m.statePath, err)
		}
		again := m.rescan
		m.rescan = false
		if !again {
			m.scanning = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func (m *setupManager) updateCore(listen string, tlsEnable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scannedAt == nil {
		m.found.Core = setupCoreInfo(listen)
		return
	}
	m.found = refreshCoreState(m.found, listen, tlsEnable)
	_ = m.saveLocked()
}

func (m *setupManager) snapshot() (firstRun, scanning bool, scannedAt *time.Time, found management.SetupFound) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var at *time.Time
	if m.scannedAt != nil {
		t := *m.scannedAt
		at = &t
	}
	f := normalizeFound(m.found)
	f.Clients = slices.Clone(f.Clients)
	f.SignIns = slices.Clone(f.SignIns)
	f.Imports = slices.Clone(f.Imports)
	return m.firstRun, m.scanning, at, f
}

func (m *setupManager) stop() {
	m.cancel()
	m.wg.Wait()
}

// SetListenAddr records the actual listen address once the listener is bound (when config
// listen used port 0) and updates setup's core view.
func (c *Core) SetListenAddr(addr string) {
	if addr == "" {
		return
	}
	c.listenAddr.Store(addr)
	if c.setup != nil {
		listen, tlsEnable := c.setupCoreTarget()
		c.setup.updateCore(listen, tlsEnable)
	}
}

func (c *Core) setupCoreTarget() (string, bool) {
	cfg := c.Config()
	listen := cfg.Listen
	if bound, ok := c.listenAddr.Load().(string); ok && bound != "" {
		if chost, cport, err := net.SplitHostPort(listen); err == nil && (cport == "" || cport == "0") {
			if _, bport, err := net.SplitHostPort(bound); err == nil && bport != "" && bport != "0" {
				listen = net.JoinHostPort(chost, bport)
			}
		} else if listen == "" {
			listen = bound
		}
	}
	return listen, cfg.TLS.Enable
}

// Setup returns the kept scan, first-run flag, and ordered picks.
func (c *Core) Setup(ctx context.Context) management.Setup {
	firstRun, scanning, scannedAt, found := c.setup.snapshot()
	listen, _ := c.setupCoreTarget()
	if listen != "" {
		found.Core = setupCoreInfo(listen)
	}
	regs := c.Registries(ctx)
	installed := c.cur.Load().pkgs
	return management.Setup{
		FirstRun:  firstRun,
		Scanning:  scanning,
		ScannedAt: scannedAt,
		Found:     found,
		Picks:     buildSetupPicks(regs, installed, found),
	}
}

// StartSetupScan triggers a background scan and returns the current Setup with scanning: true.
func (c *Core) StartSetupScan(ctx context.Context) management.Setup {
	c.setup.triggerScan()
	s := c.Setup(ctx)
	s.Scanning = true
	return s
}

// ApplySetup installs and enables each requested pick without undoing prior picks on failure.
func (c *Core) ApplySetup(ctx context.Context, picks []string) management.SetupApplyReply {
	results := make([]management.SetupApplyResult, 0, len(picks))
	for _, id := range picks {
		v, err := c.Install(ctx, InstallRequest{ID: id, Enable: true})
		if err != nil {
			res := management.SetupApplyResult{
				ID:     id,
				Status: management.SetupApplyError,
				Error:  err.Error(),
			}
			if v.ID != "" {
				res.Package = &v
			}
			results = append(results, res)
			continue
		}
		results = append(results, management.SetupApplyResult{
			ID:      id,
			Status:  management.SetupApplyDone,
			Package: &v,
		})
	}
	return management.SetupApplyReply{Results: results}
}

// DoneSetup ends first run and persists it to state/setup.json.
func (c *Core) DoneSetup() (management.SetupDoneReply, error) {
	if err := c.setup.markDone(); err != nil {
		return management.SetupDoneReply{}, err
	}
	return management.SetupDoneReply{FirstRun: false}, nil
}

func (c *Core) handleSetup(m *server.Management) {
	m.Handle("GET /setup", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.Setup(r.Context()))
	})
	m.Handle("POST /setup/scan", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, c.StartSetupScan(r.Context()))
	})
	m.Handle("POST /setup/apply", func(w http.ResponseWriter, r *http.Request) {
		var req management.SetupApplyRequest
		if !readStrictJSON(w, r, &req) {
			return
		}
		if len(req.Picks) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"code": "invalid_request", "message": "picks must name at least one package"},
			})
			return
		}
		writeJSON(w, http.StatusOK, c.ApplySetup(r.Context(), req.Picks))
	})
	m.Handle("POST /setup/done", func(w http.ResponseWriter, r *http.Request) {
		reply, err := c.DoneSetup()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"code": "internal", "message": err.Error()},
			})
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
}

// buildSetupPicks maps registry packages with tier "pick" to SetupPick in plan order.
func buildSetupPicks(regs []management.Registry, installed packages.State, found management.SetupFound) []management.SetupPick {
	byID := map[string]management.RegistryPackage{}
	var order []string
	for _, reg := range regs {
		for _, p := range reg.Packages {
			if p.Tier != packages.TierPick {
				continue
			}
			if _, seen := byID[p.ID]; !seen {
				byID[p.ID] = p
				order = append(order, p.ID)
			}
		}
	}
	rank := map[string]int{}
	for i, id := range setupPickOrder {
		rank[id] = i
	}
	slices.SortFunc(order, func(a, b string) int {
		ra, oka := rank[a]
		rb, okb := rank[b]
		switch {
		case oka && okb:
			return cmp.Compare(ra, rb)
		case oka:
			return -1
		case okb:
			return 1
		}
		// Providers before clients, then by ID.
		ka, kb := byID[a].Kind, byID[b].Kind
		if ka != kb {
			if ka == packages.KindProvider {
				return -1
			}
			if kb == packages.KindProvider {
				return 1
			}
		}
		return strings.Compare(a, b)
	})

	clientInstalled := map[string]bool{}
	for _, c := range found.Clients {
		if c.Installed {
			clientInstalled[c.ID] = true
		}
	}
	signInProviders := map[string]bool{}
	for _, s := range found.SignIns {
		signInProviders[s.Provider] = true
	}

	picks := make([]management.SetupPick, 0, len(order))
	for _, id := range order {
		pkg := byID[id]
		_, isInstalled := installed[id]
		suggested, because := suggestPick(id, clientInstalled, signInProviders)
		picks = append(picks, management.SetupPick{
			ID:        pkg.ID,
			Kind:      pkg.Kind,
			Title:     pkg.Title,
			Summary:   pkg.Summary,
			Installed: isInstalled,
			Suggested: suggested,
			Because:   because,
		})
	}
	return picks
}

func suggestPick(id string, clients, signIns map[string]bool) (bool, string) {
	switch id {
	case "pluxy/claude":
		switch {
		case clients["claude-code"] && signIns["pluxy/claude"]:
			return true, "Claude Code is installed and signed in on this machine"
		case clients["claude-code"]:
			return true, "Claude Code is installed on this machine"
		case signIns["pluxy/claude"]:
			return true, "Claude Code is signed in on this machine"
		}
	case "pluxy/claude-code":
		if clients["claude-code"] {
			return true, "Claude Code is installed on this machine"
		}
	case "pluxy/codex":
		switch {
		case clients["codex"] && signIns["pluxy/codex"]:
			return true, "Codex CLI is installed and Codex is signed in on this machine"
		case clients["codex"]:
			return true, "Codex CLI is installed on this machine"
		case signIns["pluxy/codex"]:
			return true, "Codex is signed in on this machine"
		}
	case "pluxy/codex-setup":
		if clients["codex"] {
			return true, "Codex CLI is installed on this machine"
		}
	case "pluxy/opencode":
		if clients["opencode"] {
			return true, "OpenCode is installed on this machine"
		}
	case "pluxy/pi-setup":
		if clients["pi"] {
			return true, "Pi is installed on this machine"
		}
	case "pluxy/omp-setup":
		if clients["omp"] {
			return true, "Oh My Pi is installed on this machine"
		}
	}
	return false, ""
}

func userHomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func setupCoreInfo(listen string) management.SetupCore {
	return management.SetupCore{
		Listen:               listen,
		ReachableFromNetwork: isReachableFromNetwork(listen),
	}
}

func isReachableFromNetwork(listen string) bool {
	if listen == "" {
		return false
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return !ip.IsLoopback()
	}
	return true
}

func coreClientURL(listen string, tlsEnable bool) string {
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ""
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() == nil {
			host = "::1"
		} else {
			host = "127.0.0.1"
		}
	}
	scheme := "http"
	if tlsEnable {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func refreshCoreState(found management.SetupFound, listen string, tlsEnable bool) management.SetupFound {
	found.Core = setupCoreInfo(listen)
	home := userHomeDir()
	coreURL := coreClientURL(listen, tlsEnable)
	if home == "" {
		return found
	}
	for i := range found.Clients {
		c := &found.Clients[i]
		switch c.ID {
		case "claude-code":
			c.Config, c.State = inspectClaudeCode(home, coreURL)
		case "codex":
			c.Config, c.State = inspectCodex(home, coreURL)
		case "opencode":
			c.Config, c.State = inspectOpenCode(home, coreURL)
		case "pi":
			c.Config, c.State = inspectPi(home, coreURL)
		case "omp":
			c.Config, c.State = inspectOMP(home, coreURL)
		}
	}
	return found
}

type clientProbeSpec struct {
	id      string
	name    string
	program string
	paths   func(home string) []string
	inspect func(home, coreURL string) (cfg string, state string)
}

var clientSpecs = []clientProbeSpec{
	{
		id:      "claude-code",
		name:    "Claude Code",
		program: "claude",
		paths: func(home string) []string {
			exe := exeName("claude")
			return []string{
				filepath.Join(home, ".local", "bin", exe),
				filepath.Join(home, ".claude", "local", exe),
			}
		},
		inspect: inspectClaudeCode,
	},
	{
		id:      "codex",
		name:    "Codex CLI",
		program: "codex",
		paths: func(home string) []string {
			return []string{filepath.Join(home, ".local", "bin", exeName("codex"))}
		},
		inspect: inspectCodex,
	},
	{
		id:      "opencode",
		name:    "OpenCode",
		program: "opencode",
		paths: func(home string) []string {
			return []string{filepath.Join(home, ".opencode", "bin", exeName("opencode"))}
		},
		inspect: inspectOpenCode,
	},
	{
		id:      "pi",
		name:    "Pi",
		program: "pi",
		paths: func(home string) []string {
			if runtime.GOOS == "windows" {
				return nil
			}
			return []string{
				filepath.Join(home, ".pi", "agent", "bin", "pi"),
				filepath.Join(home, ".local", "bin", "pi"),
			}
		},
		inspect: inspectPi,
	},
	{
		id:      "omp",
		name:    "Oh My Pi",
		program: "omp",
		paths: func(home string) []string {
			exe := exeName("omp")
			paths := []string{filepath.Join(home, ".bun", "bin", exe)}
			if runtime.GOOS != "windows" {
				paths = append(paths, filepath.Join(home, ".local", "bin", exe))
			} else if local := os.Getenv("LOCALAPPDATA"); local != "" && filepath.IsAbs(local) {
				paths = append(paths, filepath.Join(local, "omp", exe))
			}
			return paths
		},
		inspect: inspectOMP,
	},
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func scanMachine(ctx context.Context, listen string, tlsEnable bool, timeout time.Duration) management.SetupFound {
	home := userHomeDir()
	coreURL := coreClientURL(listen, tlsEnable)
	clients := make([]management.SetupClient, len(clientSpecs))

	var wg sync.WaitGroup
	for i, spec := range clientSpecs {
		wg.Go(func() {
			var installed bool
			var version string
			if prog := findProgram(spec.program, spec.paths(home)); prog != "" {
				installed = true
				version = probeVersion(ctx, prog, timeout)
			}
			cfg, state := spec.inspect(home, coreURL)
			clients[i] = management.SetupClient{
				ID:        spec.id,
				Name:      spec.name,
				Installed: installed,
				Version:   version,
				Config:    cfg,
				State:     state,
			}
		})
	}
	wg.Wait()

	return management.SetupFound{
		Clients: clients,
		SignIns: scanSignIns(home),
		Imports: scanImports(home),
		Core:    setupCoreInfo(listen),
	}
}

func findProgram(name string, knownPaths []string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, p := range knownPaths {
		if isExecutableFile(p) {
			return p
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

var versionPattern = regexp.MustCompile(`\bv?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)\b`)

func probeVersion(ctx context.Context, program string, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = setupProbeTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(pctx, program, "--version")
	cmd.Env = append(os.Environ(), "MISE_OFFLINE=1")
	cmd.WaitDelay = 200 * time.Millisecond
	configureProbeCmd(cmd)

	var out bytes.Buffer
	lw := &limitedBuffer{buf: &out, limit: maxSetupProbeBytes}
	cmd.Stdout = lw
	cmd.Stderr = lw
	if err := cmd.Run(); err != nil {
		return ""
	}
	return parseVersionOutput(out.String())
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	rem := l.limit - l.buf.Len()
	if rem > 0 {
		if len(p) > rem {
			_, _ = l.buf.Write(p[:rem])
		} else {
			_, _ = l.buf.Write(p)
		}
	}
	return len(p), nil
}

func parseVersionOutput(out string) string {
	if m := versionPattern.FindStringSubmatch(out); len(m) >= 2 {
		return m[1]
	}
	return ""
}

func readLimitedFile(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxClientConfig+1))
	if err != nil || len(b) > maxClientConfig {
		return nil, false
	}
	return b, true
}

func sameCoreTarget(gotURL, coreURL string, wantV1 bool) bool {
	if coreURL == "" || gotURL == "" {
		return false
	}
	want, err := url.Parse(coreURL)
	if err != nil || want.Host == "" {
		return false
	}
	got, err := url.Parse(strings.TrimSpace(gotURL))
	if err != nil || got.Host == "" {
		return false
	}
	if !strings.EqualFold(got.Scheme, want.Scheme) {
		return false
	}
	gotPath := strings.TrimSuffix(got.Path, "/")
	if wantV1 {
		if gotPath != "/v1" {
			return false
		}
	} else {
		if gotPath != "" {
			return false
		}
	}
	if urlPort(got) != urlPort(want) {
		return false
	}
	gHost, wHost := got.Hostname(), want.Hostname()
	if strings.EqualFold(gHost, wHost) {
		return true
	}
	return isLoopbackHost(wHost) && isLoopbackHost(gHost)
}

func urlPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// inspectClaudeCode checks ~/.claude/settings.json using the rules in
// next/app-tools/claude-code/src/settings.rs.
func inspectClaudeCode(home, coreURL string) (string, string) {
	const display = "~/.claude/settings.json"
	if home == "" {
		return display, management.ClientStateNotConfigured
	}
	b, ok := readLimitedFile(filepath.Join(home, ".claude", "settings.json"))
	if !ok || len(bytes.TrimSpace(b)) == 0 {
		return display, management.ClientStateNotConfigured
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		return display, management.ClientStateNotConfigured
	}
	if schema, ok := root["$schema"].(string); ok && schema != "https://json.schemastore.org/claude-code-settings.json" {
		return display, management.ClientStateNotConfigured
	}
	envRaw, ok := root["env"].(map[string]any)
	if !ok {
		return display, management.ClientStateNotConfigured
	}
	str := func(k string) (string, bool) {
		s, ok := envRaw[k].(string)
		return s, ok
	}
	baseURL, hasBase := str("ANTHROPIC_BASE_URL")
	if !hasBase {
		return display, management.ClientStateNotConfigured
	}
	tok, _ := str("ANTHROPIC_AUTH_TOKEN")
	disc, _ := str("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY")
	autoSrv, _ := str("CLAUDE_CODE_AUTO_MODE_SERVER")
	if tok != "" && disc == "1" && autoSrv == "0" && sameCoreTarget(baseURL, coreURL, false) {
		return display, management.ClientStateConfigured
	}
	return display, management.ClientStateNeedsUpdate
}

// inspectCodex checks ~/.codex/config.toml using a small TOML line reader and the rules in
// next/app-tools/codex/src/config.rs.
func inspectCodex(home, coreURL string) (string, string) {
	const display = "~/.codex/config.toml"
	if home == "" {
		return display, management.ClientStateNotConfigured
	}
	b, ok := readLimitedFile(filepath.Join(home, ".codex", "config.toml"))
	if !ok || len(bytes.TrimSpace(b)) == 0 {
		return display, management.ClientStateNotConfigured
	}
	parsed := parseCodexConfigTOML(b)
	if !parsed.hasBaseURL {
		return display, management.ClientStateNotConfigured
	}
	if parsed.bearerToken != "" &&
		parsed.modelProvider == "pluxy" &&
		parsed.hasModelCatalog &&
		(parsed.wireAPI == "" || parsed.wireAPI == "responses") &&
		sameCoreTarget(parsed.baseURL, coreURL, true) {
		return display, management.ClientStateConfigured
	}
	return display, management.ClientStateNeedsUpdate
}

type codexTOMLFields struct {
	modelProvider   string
	hasModelCatalog bool
	hasBaseURL      bool
	baseURL         string
	bearerToken     string
	wireAPI         string
}

// parseCodexConfigTOML reads only the keys inspectCodex needs without pulling in a TOML dependency.
func parseCodexConfigTOML(b []byte) codexTOMLFields {
	var out codexTOMLFields
	section := ""
	lines := strings.Split(string(b), "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			section = normalizeTOMLPath(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = normalizeTOMLPath(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		eq := indexUnquoted(line, '=')
		if eq < 0 {
			continue
		}
		k := normalizeTOMLPath(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if section != "" && k != "" {
			k = section + "." + k
		}
		switch k {
		case "model_provider":
			if s, ok := parseTOMLString(val); ok {
				out.modelProvider = s
			}
		case "model_catalog_json":
			if val != "" {
				out.hasModelCatalog = true
			}
		case "model_providers.pluxy.base_url":
			if s, ok := parseTOMLString(val); ok {
				out.hasBaseURL = true
				out.baseURL = s
			}
		case "model_providers.pluxy.experimental_bearer_token":
			if s, ok := parseTOMLString(val); ok {
				out.bearerToken = s
			}
		case "model_providers.pluxy.wire_api":
			if s, ok := parseTOMLString(val); ok {
				out.wireAPI = s
			}
		}
	}
	return out
}

func stripTOMLComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' {
			return line[:i]
		}
	}
	return line
}

func indexUnquoted(s string, target byte) int {
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == target {
			return i
		}
	}
	return -1
}

func normalizeTOMLPath(s string) string {
	var parts []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '.') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '"' || s[i] == '\'' {
			q := s[i]
			i++
			start := i
			for i < len(s) && s[i] != q {
				if q == '"' && s[i] == '\\' && i+1 < len(s) {
					i++
				}
				i++
			}
			parts = append(parts, s[start:i])
			if i < len(s) {
				i++
			}
			continue
		}
		start := i
		for i < len(s) && s[i] != '.' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		parts = append(parts, s[start:i])
	}
	return strings.Join(parts, ".")
}

func parseTOMLString(val string) (string, bool) {
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		var s string
		if json.Unmarshal([]byte(val), &s) == nil {
			return s, true
		}
		return val[1 : len(val)-1], true
	}
	if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
		return val[1 : len(val)-1], true
	}
	return "", false
}

// inspectOpenCode checks ~/.config/opencode/opencode.jsonc (or opencode.json) using the rules
// in next/app-tools/opencode/src/config.rs.
func inspectOpenCode(home, coreURL string) (string, string) {
	display := "~/.config/opencode/opencode.jsonc"
	if home == "" {
		return display, management.ClientStateNotConfigured
	}
	dir := filepath.Join(home, ".config", "opencode")
	jsoncPath := filepath.Join(dir, "opencode.jsonc")
	jsonPath := filepath.Join(dir, "opencode.json")
	target := jsoncPath
	if _, err := os.Lstat(jsoncPath); err != nil {
		if _, err2 := os.Lstat(jsonPath); err2 == nil {
			target = jsonPath
			display = "~/.config/opencode/opencode.json"
		}
	}
	b, ok := readLimitedFile(target)
	if !ok || len(bytes.TrimSpace(b)) == 0 {
		return display, management.ClientStateNotConfigured
	}
	b = bytes.TrimPrefix(b, []byte("\xEF\xBB\xBF"))
	var root map[string]any
	if err := json.Unmarshal(cleanJSONC(b), &root); err != nil {
		return display, management.ClientStateNotConfigured
	}
	providers, _ := root["provider"].(map[string]any)
	pluxyProv, hasPluxy := providers["pluxy"].(map[string]any)
	if !hasPluxy {
		return display, management.ClientStateNotConfigured
	}
	v2Providers, _ := root["providers"].(map[string]any)
	_, hasV2 := v2Providers["pluxy"]
	npm, _ := pluxyProv["npm"].(string)
	options, _ := pluxyProv["options"].(map[string]any)
	baseURL, _ := options["baseURL"].(string)
	apiKey, _ := options["apiKey"].(string)
	models, _ := pluxyProv["models"].(map[string]any)

	var oursModel string
	switch m := root["model"].(type) {
	case string:
		oursModel, _ = strings.CutPrefix(m, "pluxy/")
	case map[string]any:
		if pid, _ := m["providerID"].(string); pid == "pluxy" {
			oursModel, _ = m["model"].(string)
		}
	}
	_, hasOursModel := models[oursModel]
	if !hasV2 && apiKey != "" && npm == "@ai-sdk/openai-compatible" &&
		oursModel != "" && hasOursModel && sameCoreTarget(baseURL, coreURL, true) {
		return display, management.ClientStateConfigured
	}
	return display, management.ClientStateNeedsUpdate
}

// cleanJSONC strips // and /* */ comments and trailing commas from JSONC input.
func cleanJSONC(in []byte) []byte {
	var noComments bytes.Buffer
	noComments.Grow(len(in))
	inString := false
	escaped := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			noComments.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			noComments.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(in) {
			if in[i+1] == '/' {
				i += 2
				for i < len(in) && in[i] != '\n' {
					i++
				}
				if i < len(in) {
					noComments.WriteByte('\n')
				}
				continue
			}
			if in[i+1] == '*' {
				i += 2
				for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		noComments.WriteByte(c)
	}

	b := noComments.Bytes()
	var out bytes.Buffer
	out.Grow(len(b))
	inString = false
	escaped = false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

// inspectPi checks ~/.pi/agent/settings.json and cliproxyapi.json using the rules in
// next/app-tools/pi-setup/src/pi.rs.
func inspectPi(home, coreURL string) (string, string) {
	const display = "~/.pi/agent/settings.json"
	if home == "" {
		return display, management.ClientStateNotConfigured
	}
	agentDir := filepath.Join(home, ".pi", "agent")
	sb, ok := readLimitedFile(filepath.Join(agentDir, "settings.json"))
	if !ok || len(bytes.TrimSpace(sb)) == 0 {
		return display, management.ClientStateNotConfigured
	}
	var st map[string]any
	if err := json.Unmarshal(sb, &st); err != nil {
		return display, management.ClientStateNotConfigured
	}
	pkgs, _ := st["packages"].([]any)
	registered := false
	for _, item := range pkgs {
		var src string
		switch v := item.(type) {
		case string:
			src = v
		case map[string]any:
			src, _ = v["source"].(string)
		}
		src = strings.TrimSuffix(strings.TrimSpace(src), "/")
		src = strings.TrimPrefix(src, "./")
		if src == "pluxy-cliproxyapi-provider" {
			registered = true
			break
		}
	}
	if !registered {
		return display, management.ClientStateNotConfigured
	}

	cb, ok := readLimitedFile(filepath.Join(agentDir, "cliproxyapi.json"))
	if !ok {
		return display, management.ClientStateNeedsUpdate
	}
	var cfg map[string]any
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return display, management.ClientStateNeedsUpdate
	}
	baseURL, _ := cfg["baseUrl"].(string)
	apiKey, _ := cfg["apiKey"].(string)
	providerID, _ := cfg["providerId"].(string)
	providerID = cmp.Or(strings.TrimSpace(providerID), "cliproxyapi")
	defProv, _ := st["defaultProvider"].(string)
	defModel, _ := st["defaultModel"].(string)
	_, markerErr := os.Stat(filepath.Join(agentDir, "pluxy-cliproxyapi-provider", ".pluxy-adapter.json"))
	if markerErr == nil &&
		apiKey != "" &&
		defProv == providerID &&
		defModel != "" &&
		sameCoreTarget(baseURL, coreURL, false) {
		return display, management.ClientStateConfigured
	}
	return display, management.ClientStateNeedsUpdate
}

// inspectOMP checks ~/.omp/agent/models.yml (or models.yaml) and config.yml (or config.yaml)
// using the rules in next/app-tools/omp-setup/src/{main.rs,omp.rs}.
func inspectOMP(home, coreURL string) (string, string) {
	display := "~/.omp/agent/models.yml"
	if home == "" {
		return display, management.ClientStateNotConfigured
	}
	agentDir := filepath.Join(home, ".omp", "agent")
	modelsName := "models.yml"
	if _, err := os.Stat(filepath.Join(agentDir, "models.yml")); err != nil {
		if _, err2 := os.Stat(filepath.Join(agentDir, "models.yaml")); err2 == nil {
			modelsName = "models.yaml"
		}
	}
	display = "~/.omp/agent/" + modelsName

	configName := "config.yml"
	if _, err := os.Stat(filepath.Join(agentDir, "config.yml")); err != nil {
		if _, err2 := os.Stat(filepath.Join(agentDir, "config.yaml")); err2 == nil {
			configName = "config.yaml"
		}
	}

	mb, mok := readLimitedFile(filepath.Join(agentDir, modelsName))
	cb, cok := readLimitedFile(filepath.Join(agentDir, configName))
	if !mok && !cok {
		return display, management.ClientStateNotConfigured
	}

	var modelsDoc, configDoc map[string]any
	if mok && len(bytes.TrimSpace(mb)) > 0 {
		if err := yaml.Unmarshal(mb, &modelsDoc); err != nil {
			return display, management.ClientStateNotConfigured
		}
	}
	if cok && len(bytes.TrimSpace(cb)) > 0 {
		if err := yaml.Unmarshal(cb, &configDoc); err != nil {
			return display, management.ClientStateNotConfigured
		}
	}

	providers, _ := modelsDoc["providers"].(map[string]any)
	pluxyProv, hasPluxy := providers["pluxy"].(map[string]any)
	roles, _ := configDoc["modelRoles"].(map[string]any)
	defRole, _ := roles["default"].(string)
	model, hasModel := strings.CutPrefix(defRole, "pluxy/")
	if !hasPluxy && (!hasModel || model == "") {
		return display, management.ClientStateNotConfigured
	}
	baseURL, _ := pluxyProv["baseUrl"].(string)
	apiKey, _ := pluxyProv["apiKey"].(string)
	api, _ := pluxyProv["api"].(string)
	discovery, _ := pluxyProv["discovery"].(map[string]any)
	discType, _ := discovery["type"].(string)
	if apiKey != "" &&
		(api == "openai-responses" || api == "openai-completions") &&
		discType == "openai-models-list" &&
		hasModel && model != "" &&
		sameCoreTarget(baseURL, coreURL, true) {
		return display, management.ClientStateConfigured
	}
	return display, management.ClientStateNeedsUpdate
}

func scanSignIns(home string) []management.SetupSignIn {
	out := []management.SetupSignIn{}
	if home == "" {
		return out
	}
	checks := []struct {
		path     string
		provider string
		from     string
		label    string
	}{
		{
			path:     filepath.Join(home, ".codex", "auth.json"),
			provider: "pluxy/codex",
			from:     "codex",
			label:    "Codex is signed in on this machine",
		},
		{
			path:     filepath.Join(home, ".claude", ".credentials.json"),
			provider: "pluxy/claude",
			from:     "claude-code",
			label:    "Claude Code is signed in on this machine",
		},
	}
	for _, c := range checks {
		info, err := os.Stat(c.path)
		if err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			out = append(out, management.SetupSignIn{
				Provider: c.provider,
				From:     c.from,
				Label:    c.label,
			})
		}
	}
	return out
}

func scanImports(home string) []management.SetupImport {
	out := []management.SetupImport{}
	if home == "" {
		return out
	}
	cpaDir := filepath.Join(home, ".cli-proxy-api")
	cfgPath := filepath.Join(cpaDir, "config.yaml")
	info, err := os.Stat(cfgPath)
	if err != nil || !info.Mode().IsRegular() {
		return out
	}
	authDir := cpaDir
	displayAuthDir := "~/.cli-proxy-api"
	if b, ok := readLimitedFile(cfgPath); ok {
		if dir := readCPAAuthDir(b); dir != "" {
			resolved := expandHome(dir, home)
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(cpaDir, resolved)
			}
			authDir = resolved
			displayAuthDir = shortenHome(resolved, home)
		}
	}
	accounts := 0
	if files, err := legacy.AuthFiles(authDir); err == nil {
		accounts = len(files)
	}
	out = append(out, management.SetupImport{
		Kind:     "cliproxyapi",
		Config:   "~/.cli-proxy-api/config.yaml",
		AuthDir:  displayAuthDir,
		Accounts: accounts,
	})
	return out
}

func readCPAAuthDir(b []byte) string {
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return ""
	}
	if s, ok := doc["auth-dir"].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	if oauth, ok := doc["oauth"].(map[string]any); ok {
		if s, ok := oauth["auth-dir"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return filepath.Join(home, rest)
	}
	if rest, ok := strings.CutPrefix(path, `~\`); ok {
		return filepath.Join(home, rest)
	}
	return path
}

func shortenHome(path, home string) string {
	if path == home {
		return "~"
	}
	prefix := home + string(filepath.Separator)
	if rest, ok := strings.CutPrefix(path, prefix); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}
