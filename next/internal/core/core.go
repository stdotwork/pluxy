// Package core holds core's live state: the published config snapshot, the extension
// generations it names (the extension the config runs from a manifest path and every enabled
// installed package), and the reload transaction that replaces them.
//
// A reload reads the config file, validates it, prepares what it needs (starting a new
// generation of each extension whose manifest or program changed) and only then publishes the
// new snapshot in one step. Each request keeps the snapshot and extension generations it
// started with. A reload that fails at any step leaves the active snapshot in place and
// records why. A replaced or removed extension generation drains: it finishes the requests
// that started on it, until its deadline, and then stops. Installing, enabling, disabling and
// removing a package are reloads with the package change applied (see packages.go).
//
// Accounts added by login live in one credential store for core's whole run; each published
// router routes over the config's accounts and those the store holds, for the extensions that
// run.
package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/extconfig"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Options configure a Core.
type Options struct {
	// ConfigPath is the config file.
	ConfigPath string
	// SafeMode runs core without starting any extension: requests fail, management works.
	SafeMode bool
	// Host tunes how extension programs are started.
	Host host.Options
	// Logs is core's logs folder, which core's log output also goes to while the config says
	// so. Nil: core uses the folder logs beside the config file, without writing to it.
	Logs *logfile.Files
	// Bundled is the index of the registry of packages this install ships with (absolute), or
	// empty for none. On the first start with a packages folder that has no record yet, every
	// package it lists is installed and enabled.
	Bundled string
}

// Revision describes one version of the config file core has seen.
type Revision struct {
	// Revision counts the config versions core has tried to load, from 1.
	Revision uint64 `json:"revision"`
	// Hash is the SHA-256 of the file's contents.
	Hash string `json:"hash,omitempty"`
	// At is when core read it (desired) or published it (active).
	At time.Time `json:"at"`
	// Error says why this version is not active, when it failed.
	Error string `json:"error,omitempty"`
}

// snapshot is one published config with everything built for it.
type snapshot struct {
	server.Generation
	revision uint64
	// exts are the extension generations that run, by extension ID; empty in safe mode.
	exts map[string]*host.Managed
	// pkgs is the record of installed packages the snapshot was prepared with.
	pkgs packages.State
	// failed holds the enabled packages that could not start, by ID: why.
	failed map[string]string
}

// Core is core's live state.
type Core struct {
	opts   Options
	state  *route.State
	creds  *cred.Manager // opened by the first load
	pkgDir string        // the packages folder, fixed for core's run

	settings  *extconfig.Store // extensions' settings, in the packages folder
	pagesPort atomic.Int64     // the pages listener's port, once main has it

	redactor *configdoc.Redactor // hides the config file's secrets from the management API

	checks *checks // each account's last check

	usage *usage.Store   // the usage record, kept across reloads
	logs  *logfile.Files // the logs folder

	// accounts serialises setting the accounts a router routes over: a change in the
	// credential store and a publish never interleave.
	accounts sync.Mutex

	reloading sync.Mutex // one reload at a time
	stopped   bool       // set by Stop under reloading: no reload after it
	cur       atomic.Pointer[snapshot]
	initial   packages.State // the record read at start, for the first load

	pkgOps sync.Mutex // one package change at a time

	mu       sync.Mutex
	desired  Revision
	active   Revision
	draining []*host.Managed
	retired  []packages.Installed // removed or replaced package versions whose files go once unused
}

// Start loads the config file and publishes it as revision 1. Unless in safe mode, it starts
// each extension's program to check it; the program then runs only while it is needed.
func Start(opts Options) (*Core, error) {
	c := &Core{opts: opts, state: route.NewState(), redactor: configdoc.NewRedactor(), checks: newChecks(), usage: usage.NewStore()}
	cfg, err := server.LoadConfig(opts.ConfigPath)
	if err != nil {
		return nil, c.scrubbed(err)
	}
	if c.logs = opts.Logs; c.logs == nil {
		c.logs = logfile.New(LogsDir(opts.ConfigPath))
	}
	c.pkgDir = cfg.Packages.Dir
	// No publisher's folder can be called .settings: IDs start with a letter or digit.
	c.settings = extconfig.NewStore(filepath.Join(c.pkgDir, ".settings"))
	if c.initial, err = c.loadPackages(cfg); err != nil {
		return nil, err
	}
	if err := c.reload(true, "start"); err != nil {
		c.checks.stop()
		if c.creds != nil {
			c.creds.Close()
		}
		return nil, err
	}
	c.creds.OnChange(c.accountsChanged)
	return c, nil
}

// configSecrets returns the redactor of every secret the config holds: the client keys, the
// management key and the accounts' credentials.
func configSecrets(cfg *server.Config) *protocol.Redactor {
	cs := []*protocol.Credential{{Kind: protocol.CredentialAPIKey, APIKey: cfg.ManagementKey}}
	for _, k := range cfg.ClientKeys {
		cs = append(cs, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: k})
	}
	for _, a := range cfg.Accounts() {
		cs = append(cs, a.Credential)
	}
	return protocol.NewRedactor(cs...)
}

// LogsDir returns the logs folder of the config file at configPath: logs beside it.
func LogsDir(configPath string) string {
	dir := filepath.Dir(configPath)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, "logs")
}

// Config returns the active config.
func (c *Core) Config() *server.Config { return c.cur.Load().Config }

// Acquire returns the current generation for one request; release ends the request's hold on
// it, so a replaced extension generation can finish draining.
func (c *Core) Acquire() (*server.Generation, func()) {
	s, release := c.acquire()
	return &s.Generation, release
}

// acquire returns the current snapshot with a lease on each of its extension generations. A
// lease only keeps a replaced generation from finishing its drain; the request calls the one
// its account needs.
func (c *Core) acquire() (*snapshot, func()) {
	for {
		s := c.cur.Load()
		held := make([]*host.Managed, 0, len(s.exts))
		ok := true
		for _, g := range s.exts {
			if !g.Acquire() {
				ok = false
				break
			}
			held = append(held, g)
		}
		release := func() {
			for _, g := range held {
				g.Release()
			}
		}
		if ok {
			return s, release
		}
		release()
		if c.cur.Load() == s {
			// Stopped and not replaced: core is shutting down. The request fails in the
			// extension call.
			return s, func() {}
		}
	}
}

// lease returns the current generation of extension id with a lease on it, or nil when it does
// not run.
func (c *Core) lease(id string) (*host.Managed, func()) {
	for {
		s := c.cur.Load()
		if s == nil || s.exts[id] == nil {
			return nil, nil
		}
		g := s.exts[id]
		if g.Acquire() {
			return g, g.Release
		}
		if c.cur.Load() == s {
			return nil, nil
		}
	}
}

// Reload reloads the config file now, even if it has not changed, and reports why it failed.
// why is logged.
func (c *Core) Reload(why string) error { return c.reload(true, why) }

// reload runs one reload transaction. Unless force is set, a file whose contents match the
// last version seen, or that is empty (an editor in the middle of saving), is skipped.
func (c *Core) reload(force bool, why string) error {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	return c.transact(force, why, nil, nil, nil)
}

// transact runs one reload transaction with c.reloading held. change, when set, is applied to
// a copy of the package record before preparing; the record is saved only when everything is
// prepared, just before publishing. Packages in strict that fail to start fail the
// transaction; other enabled packages that fail are skipped and shown as failed.
//
// edit, when set, makes the transaction a change to the config file (see WriteConfig): it gets
// the file's contents and returns the new contents, which are prepared and, only when
// everything is prepared, written to the file just before publishing. An edit that fails or
// is refused leaves the file and the desired revision as they were.
func (c *Core) transact(force bool, why string, change func(packages.State), strict map[string]bool, edit func([]byte) ([]byte, error)) error {
	if c.stopped {
		return errors.New("core is stopping")
	}

	b, err := os.ReadFile(c.opts.ConfigPath)
	var orig []byte // the file's contents an edit started from
	if edit != nil {
		if err != nil {
			return fmt.Errorf("reading the config file: %w", err)
		}
		orig = b
		if b, err = edit(b); err != nil {
			return err
		}
		force = true
	}
	if err == nil && len(bytes.TrimSpace(b)) == 0 {
		if !force {
			return nil
		}
		err = errors.New("config file is empty")
	}
	if err != nil && !force && errors.Is(err, os.ErrNotExist) {
		return nil // replaced by rename in the middle of a save; the next look sees it
	}
	var hash string
	if err == nil {
		sum := sha256.Sum256(b)
		hash = "sha256:" + hex.EncodeToString(sum[:])
	}

	prev := c.cur.Load()
	c.mu.Lock()
	if !force && hash == c.desired.Hash {
		c.mu.Unlock()
		return nil
	}
	rev := c.desired.Revision + 1
	seen := c.desired
	c.desired = Revision{Revision: rev, Hash: hash, At: time.Now()}
	c.mu.Unlock()

	pkgs := c.initial
	if prev != nil {
		pkgs = prev.pkgs
	}
	if change != nil {
		pkgs = pkgs.Clone()
		change(pkgs)
	}
	var next *snapshot
	if err == nil {
		next, err = c.prepare(rev, b, prev, pkgs, strict)
	}
	if err == nil && change != nil {
		if err = packages.SaveState(c.pkgDir, pkgs); err != nil {
			err = fmt.Errorf("saving the package record: %w", err)
			c.discard(next, prev)
		}
	}
	if err == nil && edit != nil {
		// Another program may have written the file while the change was prepared; its
		// version must not be lost.
		if now, rerr := os.ReadFile(c.opts.ConfigPath); rerr != nil || !bytes.Equal(now, orig) {
			err = &editError{http.StatusConflict, "conflict", "the config file changed while the change was prepared; try again"}
		} else if err = writeFileAtomic(c.opts.ConfigPath, b); err != nil {
			err = fmt.Errorf("%w: %w", errWriteConfig, err)
		}
		if err != nil {
			c.discard(next, prev)
		}
	}
	if err != nil && edit != nil {
		// The edited version was never on disk: core still wants the file as it is.
		c.mu.Lock()
		c.desired = seen
		c.mu.Unlock()
		log.Printf("config change (%s) refused; the config file and the active revision stay: %s", why, c.scrub(err.Error(), orig, b))
		return err
	}
	if err != nil {
		// The message may quote the file (a value core refused), so its secrets are taken out
		// before it is logged, shown in status or returned.
		err = c.scrubbed(err, b)
		c.mu.Lock()
		c.desired.Error = err.Error()
		c.mu.Unlock()
		if prev != nil {
			log.Printf("config revision %d (%s) not applied; revision %d stays active: %v", rev, why, prev.revision, err)
		}
		return err
	}
	c.publish(next, prev, hash)
	log.Printf("config revision %d active (%s)", rev, why)
	for _, n := range next.Config.Notices {
		log.Print(n)
	}
	c.reportPendingImport(next.Config)
	return nil
}

// reportPendingImport logs how many credential files of today's auth folder (a config.yaml's
// oauth.auth-dir) are not imported yet. Core never imports them while it runs: see
// legacy.Import.
func (c *Core) reportPendingImport(cfg *server.Config) {
	if cfg.AuthDir == "" {
		return
	}
	known := map[string]bool{}
	for _, r := range c.creds.Records() {
		known[strings.ToLower(r.ID)] = true
	}
	for _, a := range cfg.Accounts() {
		known[strings.ToLower(a.ID)] = true
	}
	n, err := legacy.Pending(cfg.AuthDir, func(id string) bool { return known[strings.ToLower(id)] })
	switch {
	case err != nil:
		log.Printf("config.yaml: oauth.auth-dir: %v", err)
	case n > 0:
		log.Printf("config.yaml: oauth.auth-dir %s holds %d credential files not imported yet; to import them, stop core and run: pluxy -config %s -import-auth %s", cfg.AuthDir, n, c.opts.ConfigPath, cfg.AuthDir)
	}
}

// fixed refuses a config that changes a setting that cannot change while core runs.
func (c *Core) fixed(cfg *server.Config, prev *snapshot) error {
	if prev != nil && cfg.Listen != prev.Config.Listen {
		return fmt.Errorf("config: listen cannot change while core runs (%q to %q); restart core to apply it", prev.Config.Listen, cfg.Listen)
	}
	if prev != nil && cfg.ExtensionPages.Listen != prev.Config.ExtensionPages.Listen {
		return fmt.Errorf("config: extension_pages.listen cannot change while core runs (%q to %q); restart core to apply it", prev.Config.ExtensionPages.Listen, cfg.ExtensionPages.Listen)
	}
	if prev != nil && cfg.TLS != prev.Config.TLS {
		return errors.New("config: tls cannot change while core runs; restart core to apply it (a renewed certificate in the same files is read without a restart)")
	}
	if prev != nil && cfg.CredentialsDir != prev.Config.CredentialsDir {
		return fmt.Errorf("config: credentials_dir cannot change while core runs (%q to %q); restart core to apply it", prev.Config.CredentialsDir, cfg.CredentialsDir)
	}
	if cfg.Packages.Dir != c.pkgDir {
		return fmt.Errorf("config: packages.dir cannot change while core runs (%q to %q); restart core to apply it", c.pkgDir, cfg.Packages.Dir)
	}
	return nil
}

// extensionSpec is one extension a snapshot runs.
type extensionSpec struct {
	manifest string
	cfg      server.ExtensionConfig
	pkg      *packages.Installed // nil for the config's extension
}

// prepare validates the config and builds everything the snapshot needs, without changing
// what is live. A new extension generation it starts is stopped again if prepare fails. An
// enabled package that cannot be prepared fails prepare when it is in strict or runs in prev;
// otherwise it is left out and recorded in failed.
func (c *Core) prepare(rev uint64, b []byte, prev *snapshot, pkgs packages.State, strict map[string]bool) (*snapshot, error) {
	cfg, err := server.ParseConfig(b, c.opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if err := c.fixed(cfg, prev); err != nil {
		return nil, err
	}

	next := &snapshot{revision: rev, exts: map[string]*host.Managed{}, pkgs: pkgs, failed: map[string]string{}}
	want := map[string]extensionSpec{}
	if cfg.Extension.Manifest != "" {
		m, err := host.LoadManifest(cfg.Extension.Manifest)
		if err != nil && !c.opts.SafeMode {
			return nil, err
		}
		if err == nil {
			cfg.Extension.ID = m.ID
			want[m.ID] = extensionSpec{manifest: cfg.Extension.Manifest, cfg: cfg.Extension}
		}
	}
	for _, in := range pkgs.Sorted() {
		if !in.Enabled {
			continue
		}
		if _, dup := want[in.ID]; dup {
			err := fmt.Errorf("extension %s already runs from the config's extension.manifest; remove one of the two", in.ID)
			if strict[in.ID] {
				return nil, err
			}
			next.failed[in.ID] = err.Error()
			continue
		}
		x := cfg.Extensions[in.ID]
		x.ID = in.ID
		want[in.ID] = extensionSpec{manifest: in.Manifest(c.pkgDir), cfg: x, pkg: &in}
	}

	if !c.opts.SafeMode {
		for _, id := range slices.Sorted(maps.Keys(want)) {
			spec := want[id]
			g, err := c.prepareExtension(id, spec, rev, prev)
			switch {
			case err == nil:
				next.exts[id] = g
			case spec.pkg != nil && !strict[id] && (prev == nil || prev.exts[id] == nil):
				// It does not run now (core is starting, or it failed before): skip it rather than
				// refuse the reload. A package that runs now keeps running: the reload is refused.
				log.Printf("package %s %s not started: %v", id, spec.pkg.Version, err)
				next.failed[id] = err.Error()
			default:
				c.discard(next, prev)
				return nil, err
			}
		}
	}

	attempt := func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		if c.opts.SafeMode {
			return failed(safeModeEvent().Error)
		}
		g := next.exts[ext]
		if g == nil {
			return failed(&llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", ext)})
		}
		return g.Generate(ctx, a, req)
	}
	count := func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		if c.opts.SafeMode {
			return failed(safeModeEvent().Error)
		}
		g := next.exts[ext]
		if g == nil {
			return failed(&llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", ext)})
		}
		return g.CountTokens(ctx, a, req)
	}
	openSession := func(ctx context.Context, ext string, a protocol.Account, info protocol.SessionInfo) (server.Session, *llm.Error) {
		if c.opts.SafeMode {
			return nil, safeModeEvent().Error
		}
		g := next.exts[ext]
		if g == nil {
			return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", ext)}
		}
		s, e := g.OpenSession(ctx, a, info)
		if e != nil {
			return nil, e
		}
		return hostSession{s}, nil
	}
	next.Generation = server.Generation{Config: cfg, Attempt: attempt, Count: count, OpenSession: openSession, KeyID: c.redactor.ID}
	if cfg.Usage.On() {
		next.Usage = c.usage
	}
	next.RequestLog = &server.RequestLog{Dir: c.logs.Dir(), All: cfg.Logs.RequestLog, ErrorMaxFiles: cfg.Logs.ErrorLogs(),
		Redact: configSecrets(cfg), Trim: c.logs.Trim}
	router, err := route.New(cfg.Routing, c.configured(next), attempt, c.state)
	if err != nil {
		c.discard(next, prev)
		return nil, fmt.Errorf("config: %w", err)
	}
	if !c.opts.SafeMode {
		router.UseCatalog(catalog(next.exts))
	}
	next.Router = router
	if c.creds == nil { // the first load: nothing is live yet
		store, err := cred.Open(cred.FileBackend{Dir: cfg.CredentialsDir})
		if err != nil {
			c.discard(next, prev)
			return nil, err
		}
		c.creds = cred.NewManager(store, providers{c}, cred.Options{Reserved: c.reserved})
	}
	router.UseCredentials(c.creds)
	return next, nil
}

// prepareExtension returns the generation of extension id the snapshot runs: prev's when its
// code has not changed, else a new one, started and handshaken. An installed package's files
// must still have the fingerprint recorded when it was installed.
func (c *Core) prepareExtension(id string, spec extensionSpec, rev uint64, prev *snapshot) (*host.Managed, error) {
	fp, err := host.Fingerprint(spec.manifest)
	if err != nil {
		return nil, err
	}
	if spec.pkg != nil && fp != spec.pkg.Fingerprint {
		return nil, fmt.Errorf("package %s %s: its files changed since it was installed; install it again", id, spec.pkg.Version)
	}
	if prev != nil {
		if g := prev.exts[id]; g != nil && g.Path() == spec.manifest && g.Fingerprint() == fp && g.State() != host.StateQuarantined {
			return g, nil // same code: keep the running generation
		}
	}
	g, err := host.Prepare(spec.manifest, rev, policy(spec.cfg), c.opts.Host)
	if err != nil {
		return nil, err
	}
	if g.Manifest().ID != id {
		g.Stop()
		return nil, fmt.Errorf("the manifest at %s is for %s, not %s", spec.manifest, g.Manifest().ID, id)
	}
	if spec.pkg != nil && g.Fingerprint() != spec.pkg.Fingerprint {
		g.Stop()
		return nil, fmt.Errorf("package %s %s: its files changed while it started; install it again", id, spec.pkg.Version)
	}
	return g, nil
}

// discard stops the generations next started that prev does not run.
func (c *Core) discard(next, prev *snapshot) {
	if next == nil {
		return
	}
	for id, g := range next.exts {
		if prev == nil || prev.exts[id] != g {
			g.Stop()
		}
	}
}

// publish makes next the snapshot new requests get, carries routing state over, and drains the
// extension generations it replaces or no longer runs.
func (c *Core) publish(next, prev *snapshot, hash string) {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	accounts := c.routed(next)
	if err := next.Router.SetAccounts(accounts); err != nil {
		log.Printf("updating accounts: %v", err)
	}
	if prev != nil {
		c.state.Retain(prev.Router, next.Router, sameAccount)
	}
	for id, g := range next.exts {
		g.SetPolicy(policy(c.extensionConfig(next, id)))
	}
	u := next.Config.Usage
	c.usage.SetLimits(u.MaxEvents, time.Duration(u.MaxAgeMS)*time.Millisecond)
	if err := c.logs.Configure(next.Config.Logs.ToFile, next.Config.Logs.MaxTotalSizeMB); err != nil {
		log.Printf("logs: %v", err)
	}
	c.mu.Lock()
	c.cur.Store(next)
	c.active = Revision{Revision: next.revision, Hash: hash, At: time.Now()}
	c.desired.Error = ""
	var old []*host.Managed
	if prev != nil {
		for id, g := range prev.exts {
			if next.exts[id] != g {
				old = append(old, g)
			}
		}
	}
	c.draining = append(c.draining, old...)
	c.mu.Unlock()
	c.checkAccounts(next, accounts)
	for _, g := range old {
		g.Drain(func() {
			c.mu.Lock()
			if i := slices.Index(c.draining, g); i >= 0 {
				c.draining = slices.Delete(c.draining, i, i+1)
			}
			c.mu.Unlock()
			c.sweep()
		})
	}
	c.sweep()
}

// extensionConfig returns the config section of extension id in s.
func (c *Core) extensionConfig(s *snapshot, id string) server.ExtensionConfig {
	if s.Config.Extension.ID == id {
		return s.Config.Extension
	}
	return s.Config.Extensions[id]
}

// configured returns the config's accounts of the extensions s runs, each tagged with its
// extension; in safe mode, every config account (requests to them fail).
func (c *Core) configured(s *snapshot) []route.Account {
	var out []route.Account
	for _, a := range s.Config.Accounts() {
		if c.opts.SafeMode || s.exts[a.Extension] != nil {
			out = append(out, a)
		}
	}
	return out
}

// routed returns the accounts s routes over: the config's, then those the credential store
// holds, for the extensions s runs. A stored account whose ID the config also uses is left out.
func (c *Core) routed(s *snapshot) []route.Account {
	accounts := c.configured(s)
	all := s.Config.Accounts()
	kinds := map[string]string{}
	for _, r := range c.creds.Records() {
		kinds[r.ID] = r.Credential.Kind
	}
	for _, a := range c.creds.Accounts() {
		if s.exts[a.Extension] == nil {
			continue
		}
		if slices.ContainsFunc(all, func(x route.Account) bool { return x.ID == a.ID }) {
			log.Printf("account %q is both in the config and added by login; using the config's", a.ID)
			continue
		}
		accounts = append(accounts, c.extensionConfig(s, a.Extension).LoginDefaults(a, kinds[a.ID]))
	}
	return accounts
}

// accountsChanged gives the active router the credential store's accounts after they changed.
func (c *Core) accountsChanged() {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	s := c.cur.Load()
	accounts := c.routed(s)
	if err := s.Router.SetAccounts(accounts); err != nil {
		log.Printf("updating accounts: %v", err)
	}
	c.checkAccounts(s, accounts)
}

// reserved reports whether the active config sets an account with ID id, which a login may
// then not take.
func (c *Core) reserved(id string) bool {
	s := c.cur.Load()
	return s != nil && slices.ContainsFunc(s.Config.Accounts(), func(a route.Account) bool { return a.ID == id })
}

// sameAccount reports whether an account with one ID is the same upstream account in two
// routers: the same extension (by ID; a new version of it is the same provider) and the same
// settings. Its credential may change: a credential is the account's, not its identity.
func sameAccount(old, cur *route.Account) bool {
	return old.Extension == cur.Extension && sameJSON(old.Settings, cur.Settings)
}

// sameJSON compares two JSON values by meaning, not spelling: object key order and spacing do
// not matter. Numbers compare by their digits, so no two distinct numbers compare equal.
func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	decode := func(raw json.RawMessage) (any, bool) {
		if len(raw) == 0 {
			return nil, true
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		return v, d.Decode(&v) == nil
	}
	x, okx := decode(a)
	y, oky := decode(b)
	return okx && oky && reflect.DeepEqual(x, y)
}

// policy resolves an extension's run policy from its config section.
func policy(x server.ExtensionConfig) host.Policy {
	p := host.DefaultPolicy()
	ms := func(v int64) time.Duration { return time.Duration(v) * time.Millisecond }
	if x.IdleStopMS != nil {
		p.IdleStop = ms(*x.IdleStopMS)
	}
	if x.DrainTimeoutMS != nil {
		p.DrainTimeout = ms(*x.DrainTimeoutMS)
	}
	r := x.Restart
	if r.BackoffMS > 0 {
		p.RestartBackoff = ms(r.BackoffMS)
	}
	if r.MaxBackoffMS > 0 {
		p.RestartBackoffMax = ms(r.MaxBackoffMS)
	}
	if r.QuarantineAfter > 0 {
		p.QuarantineAfter = r.QuarantineAfter
	}
	if r.StableMS > 0 {
		p.StableAfter = ms(r.StableMS)
	}
	return p
}

// Stop stops every extension generation without waiting for requests.
func (c *Core) Stop() {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	c.stopped = true
	c.checks.stop()
	c.mu.Lock()
	gens := append([]*host.Managed(nil), c.draining...)
	c.mu.Unlock()
	if s := c.cur.Load(); s != nil {
		for _, g := range s.exts {
			gens = append(gens, g)
		}
	}
	var wg sync.WaitGroup
	for _, g := range gens {
		wg.Go(g.Stop)
	}
	wg.Wait()
	c.creds.Close()
}

// ExtensionStatus is a generation's status as management shows it.
type ExtensionStatus struct {
	host.Status
	// Current is true for a generation new requests go to.
	Current bool `json:"current"`
}

// Status is what management shows about core's live state.
type Status struct {
	SafeMode bool `json:"safe_mode"`
	// Desired is the newest config version core has seen; Active is the one serving.
	Desired    Revision          `json:"desired"`
	Active     Revision          `json:"active"`
	Extensions []ExtensionStatus `json:"extensions"`
}

// Status returns core's live state.
func (c *Core) Status() Status {
	c.mu.Lock() // publish switches the snapshot and the revisions under it
	s := Status{SafeMode: c.opts.SafeMode, Desired: c.desired, Active: c.active, Extensions: []ExtensionStatus{}}
	draining := append([]*host.Managed(nil), c.draining...)
	cur := c.cur.Load()
	c.mu.Unlock()
	if cur != nil {
		for _, id := range slices.Sorted(maps.Keys(cur.exts)) {
			s.Extensions = append(s.Extensions, ExtensionStatus{Status: cur.exts[id].Status(), Current: true})
		}
	}
	for _, g := range draining {
		s.Extensions = append(s.Extensions, ExtensionStatus{Status: g.Status()})
	}
	return s
}

// failedStream is a reply that is one error.
type failedStream struct{ ev llm.Event }

func failed(e *llm.Error) route.Stream {
	return &failedStream{llm.Event{Type: llm.EventError, Error: e}}
}

func (s *failedStream) Next() llm.Event { return s.ev }
func (s *failedStream) Close()          {}

// hostSession is a host session as the client API handler uses it.
type hostSession struct{ *host.Session }

func (s hostSession) Turn(ctx context.Context, a protocol.Account, req llm.Request) server.Stream {
	return s.Session.Turn(ctx, a, req)
}

// providers is the credential manager's view of the extensions that run.
type providers struct{ c *Core }

func (p providers) Running() []string {
	s := p.c.cur.Load()
	if s == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(s.exts))
}

func (p providers) Provider(id string) cred.Provider {
	if s := p.c.cur.Load(); s == nil || s.exts[id] == nil {
		return nil
	}
	return provider{c: p.c, id: id}
}

// provider is one extension as the credential manager calls it: each call goes to the
// extension's generation that is current when it starts and holds a lease on it until it ends,
// so a login or refresh runs on one version from start to end.
type provider struct {
	c  *Core
	id string
}

func (p provider) Info() protocol.Info {
	if s := p.c.cur.Load(); s != nil && s.exts[p.id] != nil {
		return s.exts[p.id].Info()
	}
	return protocol.Info{ID: p.id}
}

func (p provider) Login(ctx context.Context, account protocol.Account, login protocol.Login) cred.LoginCall {
	g, release := p.c.lease(p.id)
	if g == nil {
		return &failedLogin{protocol.Reply{Event: unavailableEvent(p.id)}}
	}
	return &leasedLogin{LoginCall: g.Login(ctx, account, login), release: release}
}

func (p provider) Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error) {
	g, release := p.c.lease(p.id)
	if g == nil {
		return nil, unavailableEvent(p.id).Error
	}
	defer release()
	return g.Refresh(ctx, account)
}

func safeModeEvent() llm.Event {
	return llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeExtensionUnavailable, Message: "core is in safe mode: no extension runs"}}
}

func unavailableEvent(id string) llm.Event {
	return llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", id)}}
}

// leasedLogin is a login call that returns its lease on the extension generation when closed.
type leasedLogin struct {
	*host.LoginCall
	release func()
	once    sync.Once
}

func (l *leasedLogin) Close() {
	l.LoginCall.Close()
	l.once.Do(l.release)
}

// failedLogin is a login call that is one error.
type failedLogin struct{ final protocol.Reply }

func (l *failedLogin) Next() protocol.Reply         { return l.final }
func (l *failedLogin) Send(map[string]string) error { return errors.New("the login is not running") }
func (l *failedLogin) Close()                       {}
