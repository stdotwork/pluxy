package core

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

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

// setupPickOrder is the order setup shows its picks in: providers, most asked for first, then
// the client tools.
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

type setupStateFile struct {
	Format    int                   `json:"format"`
	FirstRun  bool                  `json:"first_run"`
	ScannedAt *time.Time            `json:"scanned_at,omitempty"`
	Found     management.SetupFound `json:"found"`
	// Imported is what core kept of each import, by importKey of the config. It is kept apart from
	// Found, which every scan replaces.
	Imported map[string]management.SetupLastImport `json:"imported,omitempty"`
}

// setupManager keeps the kept scan and the first-run flag in memory and in state/setup.json,
// and runs the scan in the background.
type setupManager struct {
	statePath  string
	coreTarget func() (listen string, tlsEnable bool)
	timeout    time.Duration

	mu        sync.Mutex
	stopped   bool // stop has run: no scan starts any more
	firstRun  bool
	scanning  bool
	rescan    bool
	scannedAt *time.Time // nil until the first scan ends: there is no kept scan
	found     management.SetupFound
	imported  map[string]management.SetupLastImport
	// statesFor is the address the kept client states were read against (coreAddress of the
	// listen address and tlsEnable); empty when it is not known, as when the kept scan came
	// from another run.
	statesFor string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// newSetupManager reads the kept scan at statePath. With no state/setup.json, setup is on its
// first run unless core already has packages or accounts (an install from before setup existed).
func newSetupManager(statePath string, coreTarget func() (string, bool), inUse bool) *setupManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &setupManager{
		statePath:  statePath,
		coreTarget: coreTarget,
		timeout:    setupProbeTimeout,
		firstRun:   !inUse,
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
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(bytes.TrimSpace(b)) == 0 {
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
	if f.Found.Clients != nil {
		m.found.Clients = f.Found.Clients
	}
	if f.Found.SignIns != nil {
		m.found.SignIns = f.Found.SignIns
	}
	if f.Found.Imports != nil {
		m.found.Imports = f.Found.Imports
	}
	m.found.Core = f.Found.Core
	m.imported = f.Imported
}

// saveLocked writes state/setup.json, made with mode 0600 the first time.
func (m *setupManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(setupStateFile{
		Format:    setupFileFormat,
		FirstRun:  m.firstRun,
		ScannedAt: m.scannedAt,
		Found:     m.found,
		Imported:  m.imported,
	}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(m.statePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = f.Close()
	} else if errors.Is(err, fs.ErrExist) {
		err = nil
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(m.statePath, append(b, '\n'))
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

// listening is told the address core listens on once it does. A first run with no kept scan
// starts its first scan; otherwise the kept scan's client states are read again against the
// address core answers at, which a restart may have changed (a config listening on port 0, or
// tls.enable, which changes the scheme every config naming core's address holds).
func (m *setupManager) listening(listen string, tlsEnable bool) {
	m.mu.Lock()
	if m.scannedAt == nil {
		first := m.firstRun
		m.mu.Unlock()
		if first {
			m.triggerScan()
		}
		return
	}
	defer m.mu.Unlock()
	statesFor := coreAddress(listen, tlsEnable)
	if m.found.Core.Listen == listen && m.statesFor == statesFor {
		return
	}
	m.statesFor = statesFor
	refreshed := refreshCoreState(m.found, listen, tlsEnable)
	if slices.Equal(refreshed.Clients, m.found.Clients) && refreshed.Core == m.found.Core {
		return
	}
	m.found = refreshed
	if err := m.saveLocked(); err != nil {
		log.Printf("error: setup: saving %s: %v", m.statePath, err)
	}
}

func (m *setupManager) triggerScan() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	if m.scanning {
		m.rescan = true
		return
	}
	m.scanning = true
	m.wg.Add(1)
	go m.runLoop()
}

func (m *setupManager) runLoop() {
	defer m.wg.Done()
	for {
		listen, tlsEnable := m.coreTarget()
		found := scanMachine(m.ctx, filepath.Dir(m.statePath), listen, tlsEnable, m.timeout)
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.scanning, m.rescan = false, false
			m.mu.Unlock()
			return
		}
		now := time.Now().UTC()
		m.found = found
		m.scannedAt = &now
		m.statesFor = coreAddress(listen, tlsEnable)
		if err := m.saveLocked(); err != nil {
			log.Printf("error: setup: saving %s: %v", m.statePath, err)
		}
		if !m.rescan {
			m.scanning = false
			m.mu.Unlock()
			return
		}
		m.rescan = false
		m.mu.Unlock()
	}
}

func (m *setupManager) snapshot() (firstRun, scanning bool, scannedAt *time.Time, found management.SetupFound) {
	m.mu.Lock()
	firstRun, scanning = m.firstRun, m.scanning
	var at *time.Time
	if m.scannedAt != nil {
		t := *m.scannedAt
		at = &t
	}
	f := m.found
	f.Clients = slices.Clone(f.Clients)
	f.SignIns = slices.Clone(f.SignIns)
	f.Imports = slices.Clone(f.Imports)
	imported := maps.Clone(m.imported)
	m.mu.Unlock()
	// importKey reads the file system, so it runs outside the lock, as the scan does.
	for i, r := range f.Imports {
		if rec, ok := imported[importKey(r.Config)]; ok {
			f.Imports[i].LastImport = &rec
		}
	}
	return firstRun, scanning, at, f
}

// importKey names a config file for the record of its import: the home folder expanded and
// symbolic links resolved, or, when the file cannot be resolved, the cleaned path. The scan's row
// spells the file with ~, an import by an absolute path, so both sides go through it.
func importKey(config string) string {
	p, err := server.ExpandHome(config)
	if err != nil {
		p = config
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// recordImport keeps that an import ran from config, and saves it with the rest of the state.
func (m *setupManager) recordImport(config string, rec management.SetupLastImport) error {
	key := importKey(config)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.imported == nil {
		m.imported = map[string]management.SetupLastImport{}
	}
	m.imported[key] = rec
	return m.saveLocked()
}

func (m *setupManager) stop() {
	m.mu.Lock()
	m.stopped = true
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()
}

// SetListenAddr records the address the listener is bound to (the port core picked when the
// config listens on port 0), and tells setup core is listening.
func (c *Core) SetListenAddr(addr string) {
	if addr == "" {
		return
	}
	c.listenAddr.Store(addr)
	if c.setup != nil {
		c.setup.listening(c.setupCoreTarget())
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
		FirstRun:   firstRun,
		Scanning:   scanning,
		ScannedAt:  scannedAt,
		Found:      found,
		Picks:      buildSetupPicks(regs, installed, found),
		Registries: len(regs),
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
			writeError(w, http.StatusBadRequest, "invalid_request", "picks must name at least one package")
			return
		}
		writeJSON(w, http.StatusOK, c.ApplySetup(r.Context(), req.Picks))
	})
	m.Handle("POST /setup/done", func(w http.ResponseWriter, r *http.Request) {
		reply, err := c.DoneSetup()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
	m.Handle("GET /setup/clients/{id}/paste", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := ClientSnippetFor(id, "", ""); !ok {
			writeError(w, http.StatusNotFound, "not_found",
				fmt.Sprintf("no settings to paste for client tool %q (supported: %s)", id, strings.Join(ClientSnippetTools, ", ")))
			return
		}
		key := "<client-key>"
		if keyID := r.URL.Query().Get("key_id"); keyID != "" {
			keys := c.Config().ClientKeys
			i := slices.IndexFunc(keys, func(k string) bool { return c.keyView(k).ID == keyID })
			if i < 0 {
				writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no client key with id %q", keyID))
				return
			}
			v := c.keyView(keys[i])
			key = fmt.Sprintf("<client-key:%s (%s)>", v.ID, v.Masked)
		}
		listen, tlsEnable := c.setupCoreTarget()
		baseURL, err := server.LocalURL(listen, tlsEnable)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "core's own address is not known: "+err.Error())
			return
		}
		// A caller on another machine uses the address it reached core by, which is the one its
		// own programs reach core at; core's listen address names this machine instead.
		if !server.FromThisMachine(r) {
			baseURL, err = server.ReachedURL(r)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
				return
			}
		}
		snip, _ := ClientSnippetFor(id, baseURL, key)
		reply := management.ClientPaste{Tool: snip.Tool, Name: snip.Name, BaseURL: baseURL, Key: key, Note: snip.Note}
		for _, f := range snip.Files {
			reply.Files = append(reply.Files, management.ClientPasteFile{Path: f.Path, Content: f.Content})
		}
		writeJSON(w, http.StatusOK, reply)
	})
}

// buildSetupPicks lists the registries' packages of tier "pick" in setupPickOrder, others after.
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

	clientInstalled := map[string]string{}
	for _, c := range found.Clients {
		if c.Installed {
			clientInstalled[c.ID] = c.State
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

// setupSuggestions says which pick each find points to, and why: a client tool installed on
// the machine (by its SetupClient ID) or a sign-in found there (by its provider). A pick is
// suggested by its first row that holds.
var setupSuggestions = []struct {
	pick, client, signIn, because string
	// setsUp marks a pick that writes the client tool's config: not suggested while that
	// config is one its writer refuses.
	setsUp bool
}{
	{pick: "pluxy/claude", client: "claude-code", signIn: "pluxy/claude", because: "Claude Code is installed and signed in on this computer"},
	{pick: "pluxy/claude", client: "claude-code", because: "Claude Code is installed on this computer"},
	{pick: "pluxy/claude", signIn: "pluxy/claude", because: "Claude Code is signed in on this computer"},
	{pick: "pluxy/claude-code", client: "claude-code", because: "Claude Code is installed on this computer", setsUp: true},
	{pick: "pluxy/codex", client: "codex", signIn: "pluxy/codex", because: "Codex CLI is installed and Codex is signed in on this computer"},
	{pick: "pluxy/codex", client: "codex", because: "Codex CLI is installed on this computer"},
	{pick: "pluxy/codex", signIn: "pluxy/codex", because: "Codex is signed in on this computer"},
	{pick: "pluxy/codex-setup", client: "codex", because: "Codex CLI is installed on this computer", setsUp: true},
	{pick: "pluxy/opencode", client: "opencode", because: "OpenCode is installed on this computer", setsUp: true},
	{pick: "pluxy/pi-setup", client: "pi", because: "Pi is installed on this computer", setsUp: true},
	{pick: "pluxy/omp-setup", client: "omp", because: "Oh My Pi is installed on this computer", setsUp: true},
}

// suggestPick says whether pick id is suggested and why. clients holds the client tools found
// installed, with their config state; signIns the providers with a sign-in.
func suggestPick(id string, clients map[string]string, signIns map[string]bool) (bool, string) {
	for _, r := range setupSuggestions {
		state, installed := clients[r.client]
		if r.pick != id || r.client != "" && !installed || r.signIn != "" && !signIns[r.signIn] {
			continue
		}
		if r.setsUp && state == management.ClientStateRefused {
			continue
		}
		return true, r.because
	}
	return false, ""
}

// userHomeDir is the folder the scan looks in, the one server.ExpandHome expands a row's ~ with, so
// the import reads the file the row names. On Windows that is USERPROFILE, even where a shell such
// as Git Bash sets HOME to another folder.
func userHomeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func setupCoreInfo(listen string) management.SetupCore {
	return management.SetupCore{
		Listen:               listen,
		ReachableFromNetwork: isReachableFromNetwork(listen),
	}
}

// isReachableFromNetwork reports whether a core listening on listen takes connections from
// other machines: any host but localhost or a loopback address, every interface included.
func isReachableFromNetwork(listen string) bool {
	if listen == "" {
		return false
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	return !server.IsLoopbackHost(strings.TrimSpace(host))
}

// coreAddress is the address a program on this machine reaches core at, as the client tools'
// config files name it (server.LocalURL); empty when core's address is not known.
func coreAddress(listen string, tlsEnable bool) string {
	u, err := server.LocalURL(listen, tlsEnable)
	if err != nil {
		return ""
	}
	return u
}

// refreshCoreState reads each client tool's config again against the core listening on listen.
func refreshCoreState(found management.SetupFound, listen string, tlsEnable bool) management.SetupFound {
	found.Core = setupCoreInfo(listen)
	home := userHomeDir()
	coreURL := coreAddress(listen, tlsEnable)
	found.Clients = slices.Clone(found.Clients)
	for i := range found.Clients {
		c := &found.Clients[i]
		if spec, ok := clientSpecByID(c.ID); ok {
			c.Config, c.State = spec.inspect(home, coreURL)
		}
	}
	return found
}

func clientSpecByID(id string) (clientProbeSpec, bool) {
	i := slices.IndexFunc(clientSpecs, func(s clientProbeSpec) bool { return s.id == id })
	if i < 0 {
		return clientProbeSpec{}, false
	}
	return clientSpecs[i], true
}

type clientProbeSpec struct {
	id      string
	name    string
	program string
	paths   func(home string) []string
	// apps are application folders (a macOS bundle) that count as the tool being installed when
	// none of paths is found; nil for a tool with none.
	apps func(home string) []string
	// noVersion marks a tool core never runs: a desktop app starts its window, so the scan only
	// looks for it.
	noVersion bool
	inspect   func(home, coreURL string) (cfg string, state string)
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
	{
		id:        "claude-desktop",
		name:      "Claude Desktop",
		program:   "claude-desktop",
		paths:     func(string) []string { return nil },
		apps:      func(home string) []string { return claudeDesktopApps(runtime.GOOS, home) },
		noVersion: true,
		inspect:   unreadConfig(claudeDesktopConfigDisplay(runtime.GOOS)),
	},
	{
		id:      "grok-build",
		name:    "Grok Build",
		program: "grok",
		paths:   func(home string) []string { return grokPaths(runtime.GOOS, home) },
		inspect: unreadConfig("~/.grok/config.toml"),
	},
	{
		id:      "hermes",
		name:    "Hermes Agent",
		program: "hermes",
		paths:   func(home string) []string { return hermesPaths(runtime.GOOS, home, os.Getenv("LOCALAPPDATA")) },
		inspect: unreadConfig(hermesConfigDisplay(runtime.GOOS)),
	},
}

// The three tools below are found where the desktop app finds them (the recipe of each tool's
// native program in next/app-tools: claude-desktop, grok-setup and hermes-setup).

// claudeDesktopApps are where Claude Desktop's application bundle is on macOS: the system's
// applications folder, then the home folder's. On Linux it is the program `claude-desktop` on
// the PATH. On Windows no program or app ID is known for its installer, so nothing is looked for.
func claudeDesktopApps(goos, home string) []string {
	if goos != "darwin" || home == "" {
		return nil
	}
	return []string{
		filepath.Join("/Applications", "Claude.app"),
		filepath.Join(home, "Applications", "Claude.app"),
	}
}

// claudeDesktopConfigDisplay is where Claude Desktop keeps its third-party configuration file.
func claudeDesktopConfigDisplay(goos string) string {
	switch goos {
	case "darwin":
		return "~/Library/Application Support/Claude-3p/claude_desktop_config.json"
	case "windows":
		return `%LOCALAPPDATA%\Claude-3p\claude_desktop_config.json`
	}
	return "~/.config/Claude-3p/claude_desktop_config.json"
}

// grokPaths is the copy Grok Build's install script puts in ~/.grok/bin.
func grokPaths(goos, home string) []string {
	if home == "" {
		return nil
	}
	name := "grok"
	if goos == "windows" {
		name += ".exe"
	}
	return []string{filepath.Join(home, ".grok", "bin", name)}
}

// hermesPaths is the copy Hermes Agent's installer puts in ~/.local/bin, or in the bin folder of
// its home on Windows: `hermes` in %LOCALAPPDATA% (stripped of blanks, and only a full path), or
// in AppData\Local in the home folder when that is not set or not a full path.
func hermesPaths(goos, home, localAppData string) []string {
	if goos != "windows" {
		if home == "" {
			return nil
		}
		return []string{filepath.Join(home, ".local", "bin", "hermes")}
	}
	local := strings.TrimSpace(localAppData)
	if !filepath.IsAbs(local) {
		if home == "" {
			return nil
		}
		local = filepath.Join(home, "AppData", "Local")
	}
	return []string{filepath.Join(local, "hermes", "bin", "hermes.exe")}
}

// hermesConfigDisplay is where Hermes Agent keeps its settings with no profile chosen.
func hermesConfigDisplay(goos string) string {
	if goos == "windows" {
		return `%LOCALAPPDATA%\hermes\config.yaml`
	}
	return "~/.hermes/config.yaml"
}

// unreadConfig is the inspect of a tool whose settings core does not read: it names the file and
// says unchecked, which claims nothing about the file. The desktop app's page for the tool
// reads the file with the tool's own writer.
func unreadConfig(display string) func(home, coreURL string) (string, string) {
	return func(string, string) (string, string) { return display, stateUnchecked }
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// scanMachine looks for each client tool, sign-in and earlier setup on the machine. Version
// probes run in a fresh folder below scratch (core's state folder), removed after each.
func scanMachine(ctx context.Context, scratch, listen string, tlsEnable bool, timeout time.Duration) management.SetupFound {
	home := userHomeDir()
	coreURL := coreAddress(listen, tlsEnable)
	clients := make([]management.SetupClient, len(clientSpecs))

	var wg sync.WaitGroup
	for i, spec := range clientSpecs {
		wg.Go(func() {
			var installed bool
			var version string
			if prog := findProgram(spec.program, spec.paths(home)); prog != "" {
				installed = true
				if !spec.noVersion {
					version = probeVersion(ctx, prog, scratch, timeout)
				}
			} else if spec.apps != nil {
				installed = slices.ContainsFunc(spec.apps(home), isDir)
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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

// probeVersion runs program --version and reads a version from what it prints. The program
// runs in a fresh folder below scratch that is its home, config, data, state, cache and temporary
// folder, removed afterwards, so a tool that writes there on start leaves nothing in the
// person's own folders. A tool that cannot run there (a version manager's shim looking for its
// own folders) reports no version.
func probeVersion(ctx context.Context, program, scratch string, timeout time.Duration) string {
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		log.Printf("setup: %v", err)
		return ""
	}
	dir, err := os.MkdirTemp(scratch, ".probe-")
	if err != nil {
		log.Printf("setup: %v", err)
		return ""
	}
	defer os.RemoveAll(dir)

	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(pctx, program, "--version")
	cmd.Dir = dir
	cmd.Env = probeEnv(os.Environ(), dir)
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

// probeEnv is env with every folder a tool keeps its files in pointed at dir: the home folder
// (HOME, and USERPROFILE, APPDATA and LOCALAPPDATA on Windows), each XDG_*_HOME, CODEX_HOME and
// the temporary folder.
func probeEnv(env []string, dir string) []string {
	set := []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_DATA_HOME",
		"XDG_STATE_HOME", "XDG_CACHE_HOME", "CODEX_HOME", "TMPDIR", "TMP", "TEMP"}
	out := make([]string, 0, len(env)+len(set)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(k) // Windows names are not case-sensitive
		if slices.Contains(set, k) || strings.HasPrefix(k, "XDG_") && strings.HasSuffix(k, "_HOME") {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range set {
		out = append(out, k+"="+dir)
	}
	return append(out, "MISE_OFFLINE=1")
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
			label:    "Codex is signed in on this computer",
		},
		{
			path:     filepath.Join(home, ".claude", ".credentials.json"),
			provider: "pluxy/claude",
			from:     "claude-code",
			label:    "Claude Code is signed in on this computer",
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
	if b, err := readClientFile(cfgPath); err == nil {
		if res, err := legacy.Translate(b); err == nil && res.AuthDir != "" {
			if dir, err := server.ExpandHome(res.AuthDir); err == nil {
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(cpaDir, dir)
				}
				authDir = dir
			}
		}
	}
	accounts := 0
	if files, err := legacy.AuthFiles(authDir); err == nil {
		accounts = len(files)
	}
	out = append(out, management.SetupImport{
		Kind:     "cliproxyapi",
		Config:   "~/.cli-proxy-api/config.yaml",
		AuthDir:  shortenHome(authDir, home),
		Accounts: accounts,
	})
	return out
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
