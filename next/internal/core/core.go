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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/buildinfo"
	"github.com/stdotwork/pluxy/next/internal/catalogue"
	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/extconfig"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/ignite"
	"github.com/stdotwork/pluxy/next/internal/logfile"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

type Options struct {
	ConfigPath string
	SafeMode   bool
	Host       host.Options
	Logs       *logfile.Files
	Bundled    string
}

type Revision = management.Revision

type snapshot struct {
	server.Generation
	revision uint64
	exts     map[string]*host.Managed
	pkgs     packages.State
	failed   map[string]string
	pprof    *pprofListener
}

type Core struct {
	opts           Options
	state          *route.State
	creds          *cred.Manager
	pkgDir         string
	registryClient *http.Client

	settings   *extconfig.Store
	pagesPort  atomic.Int64
	listenAddr atomic.Value
	setup      *setupManager

	redactor *configdoc.Redactor
	keyIDs   *configdoc.Redactor
	requests *server.Requests

	checks *checks

	ignition *ignite.Planner

	routing *routing

	usage        *usage.Store
	logs         *logfile.Files
	themes       *themeStore
	catalogue    *catalogue.Catalogue
	catalogueNow func() time.Time

	staged stagedKey
	keyNow func() time.Time

	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
	bgMu     sync.Mutex
	bgClosed bool

	accounts sync.Mutex

	reloading sync.Mutex
	stopped   bool
	cur       atomic.Pointer[snapshot]
	initial   packages.State

	pkgOps sync.Mutex

	mu       sync.Mutex
	desired  Revision
	active   Revision
	draining []*host.Managed
	retired  []packages.Installed
}

func Start(opts Options) (*Core, error) {
	opts.Host.SettingsSecrets = configdoc.SettingsSecrets
	bgCtx, bgCancel := context.WithCancel(context.Background())
	c := &Core{opts: opts, state: route.NewState(), redactor: configdoc.NewRedactor(), requests: server.NewRequests(), checks: newChecks(), ignition: ignite.NewPlanner(),
		bgCtx: bgCtx, bgCancel: bgCancel, catalogueNow: time.Now, keyNow: time.Now}
	c.loadPublicKey()
	c.opts.Host.ProxyURL = func() string {
		if s := c.cur.Load(); s != nil {
			return s.Config.ProxyURL
		}
		return ""
	}
	c.opts.Host.Timeouts = func() *protocol.Timeouts {
		if s := c.cur.Load(); s != nil {
			return s.Config.Routing.Timeouts
		}
		return nil
	}
	c.opts.Host.Payload = func() *payload.Rules {
		if s := c.cur.Load(); s != nil {
			return s.Config.Payload
		}
		return nil
	}
	cfg, err := server.LoadConfig(opts.ConfigPath)
	if err != nil {
		bgCancel()
		return nil, &ConfigError{c.scrubbed(err)}
	}
	keyIDs, err := configdoc.LoadKeyID(filepath.Join(StateDir(opts.ConfigPath), "key-id.key"))
	if err != nil {
		bgCancel()
		return nil, c.scrubbed(err)
	}
	c.keyIDs = keyIDs
	u := cfg.Usage
	c.usage, err = usage.Open(usage.Options{
		Dir:       UsageDir(opts.ConfigPath),
		MaxEvents: u.MaxEvents,
		MaxAge:    time.Duration(u.MaxAgeMS) * time.Millisecond,
		KeepDays:  u.KeepDays,
	})
	if err != nil {
		bgCancel()
		return nil, c.scrubbed(err)
	}
	if c.logs = opts.Logs; c.logs == nil {
		c.logs = logfile.New(LogsDir(opts.ConfigPath))
	}
	c.themes = newThemeStore(filepath.Join(StateDir(opts.ConfigPath), "themes"))
	c.catalogue = catalogue.New(catalogue.Options{
		StatePath: filepath.Join(StateDir(opts.ConfigPath), "catalogue.json"),
	})
	c.pkgDir = cfg.Packages.Dir
	c.settings = extconfig.NewStore(filepath.Join(c.pkgDir, ".settings"))
	if c.initial, err = c.loadPackages(); err != nil {
		_ = c.usage.Close()
		bgCancel()
		return nil, err
	}
	c.initRouting()
	if err := c.reload(true, "start"); err != nil {
		bgCancel()
		c.checks.stop()
		if c.creds != nil {
			c.creds.Close()
		}
		_ = c.usage.Close()
		return nil, err
	}
	c.creds.OnChange(c.accountsChanged)
	snap := c.cur.Load()
	inUse := len(snap.pkgs) > 0 || len(snap.Config.Accounts()) > 0 || len(c.creds.Records()) > 0 || len(c.creds.Broken()) > 0
	c.setup = newSetupManager(filepath.Join(StateDir(opts.ConfigPath), setupFileName), c.setupCoreTarget, inUse)
	c.creds.OnCredentialChange(c.credentialChanged)
	c.startModelRefresh()
	c.startRouting()
	return c, nil
}

func configSecrets(cfg *server.Config) *protocol.Redactor {
	cs := []*protocol.Credential{{Kind: protocol.CredentialAPIKey, APIKey: cfg.ManagementKey}}
	for _, k := range cfg.ClientKeys {
		cs = append(cs, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: k})
	}
	for _, f := range proxyurl.Password(cfg.ProxyURL) {
		cs = append(cs, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: f})
	}
	for _, a := range cfg.Accounts() {
		cs = append(cs, a.Credential)
		for _, f := range proxyurl.Password(a.ProxyURL) {
			cs = append(cs, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: f})
		}
		for _, v := range upheader.Forms(a.Headers) {
			cs = append(cs, &protocol.Credential{Kind: "header", APIKey: v})
		}
		for _, s := range configdoc.SettingsSecrets(a.Settings) {
			cs = append(cs, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: s})
		}
	}
	return protocol.NewRedactor(cs...)
}

func configDir(configPath string) string {
	dir := filepath.Dir(configPath)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return dir
}

func LogsDir(configPath string) string {
	return filepath.Join(configDir(configPath), "logs")
}

func UsageDir(configPath string) string {
	return filepath.Join(configDir(configPath), "usage")
}

func StateDir(configPath string) string {
	return filepath.Join(configDir(configPath), "state")
}

func (c *Core) Config() *server.Config { return c.cur.Load().Config }

func (c *Core) Acquire() (*server.Generation, func()) {
	s, release := c.acquire()
	return &s.Generation, release
}

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
			return s, func() {}
		}
	}
}

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

func (c *Core) Reload(why string) error { return c.reload(true, why) }

func (c *Core) reload(force bool, why string) error {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	return c.transact(force, why, nil, nil, nil)
}

func (c *Core) transact(force bool, why string, change func(packages.State), strict map[string]bool, edit func([]byte) ([]byte, error)) error {
	if c.stopped {
		return errors.New("core is stopping")
	}

	b, err := os.ReadFile(c.opts.ConfigPath)
	var orig []byte
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
		return nil
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
		c.mu.Lock()
		c.desired = seen
		c.mu.Unlock()
		log.Printf("error: config change (%s) refused; the config file and the active revision stay: %s", why, c.scrub(err.Error(), orig, b))
		return err
	}
	if err != nil {
		err = c.scrubbed(err, b)
		c.mu.Lock()
		c.desired.Error = err.Error()
		c.mu.Unlock()
		if prev != nil {
			log.Printf("error: config revision %d (%s) not applied; revision %d stays active: %v", rev, why, prev.revision, err)
		}
		return err
	}
	c.publish(next, prev, hash)
	log.Printf("config revision %d active (%s)", rev, why)
	for _, n := range next.Config.Notices {
		log.Print(n)
	}
	return nil
}

func keepListen(cfg *server.Config, prev *snapshot) {
	if prev == nil || cfg.Listen == prev.Config.Listen {
		return
	}
	log.Printf("config: listen %q waits for a restart; core keeps serving on %q", cfg.Listen, prev.Config.Listen)
	followed := func(listen, pages string) bool {
		host, _, err := net.SplitHostPort(listen)
		return err == nil && pages == net.JoinHostPort(host, "0")
	}
	if followed(cfg.Listen, cfg.ExtensionPages.Listen) && followed(prev.Config.Listen, prev.Config.ExtensionPages.Listen) {
		cfg.ExtensionPages.Listen = prev.Config.ExtensionPages.Listen
	}
	cfg.Listen = prev.Config.Listen
}

func (c *Core) fixed(cfg *server.Config, prev *snapshot) error {
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

type extensionSpec struct {
	manifest string
	cfg      server.ExtensionConfig
	pkg      *packages.Installed
}

func (c *Core) prepare(rev uint64, b []byte, prev *snapshot, pkgs packages.State, strict map[string]bool) (*snapshot, error) {
	cfg, err := server.ParseConfig(b, c.opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	keepListen(cfg, prev)
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
	endpoint := func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		if c.opts.SafeMode {
			return failed(safeModeEvent().Error)
		}
		g := next.exts[ext]
		if g == nil {
			return failed(&llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", ext)})
		}
		return g.Endpoint(ctx, a, req)
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
	next.Generation = server.Generation{Config: cfg, Attempt: attempt, Count: count, Endpoint: endpoint, OpenSession: openSession, KeyID: c.keyID, Requests: c.requests}
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
	if c.catalogue != nil {
		router.UseCatalogue(c.catalogue.LookupFor)
	}
	if !c.opts.SafeMode {
		router.UseCatalog(catalog(next.exts))
	}
	next.Router = router
	if c.creds == nil {
		store, err := cred.Open(cred.FileBackend{Dir: cfg.CredentialsDir})
		if err != nil {
			c.discard(next, prev)
			return nil, err
		}
		c.creds = cred.NewManager(store, providers{c}, cred.Options{Reserved: c.reserved, PublicID: c.state.PublicID, Taken: c.takenByConfig,
			OnReauth: func(id string) { c.state.Unban(id) }, OnRemove: func(id string) { c.state.Unban(id); c.state.ForgetQuota(id) }})
	}
	router.UseCredentials(c.creds)
	router.UseReadings(readings{c})
	pp, err := openPprof(cfg, prev)
	if err != nil {
		c.discard(next, prev)
		return nil, err
	}
	next.pprof = pp
	return next, nil
}

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
			return g, nil
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

func (c *Core) discard(next, prev *snapshot) {
	if next == nil {
		return
	}
	for id, g := range next.exts {
		if prev == nil || prev.exts[id] != g {
			g.Stop()
		}
	}
	if next.pprof != nil && (prev == nil || prev.pprof != next.pprof) {
		next.pprof.close()
	}
}

func (c *Core) publish(next, prev *snapshot, hash string) {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	accounts := c.routed(next)
	if err := next.Router.SetAccounts(accounts, sameAccount); err != nil {
		log.Printf("updating accounts: %v", err)
	}
	if prev != nil {
		next.Router.CarryLists(prev.Router, sameAccountAndCred)
		c.state.Retain(prev.Router, next.Router, sameAccount)
		known := func(id string) bool { _, ok := c.account(next, id); return ok }
		c.state.RetainBans(known)
		c.state.RetainQuotas(known)
	}
	for id, g := range next.exts {
		g.SetPolicy(policy(c.extensionConfig(next, id)))
	}
	u := next.Config.Usage
	c.usage.SetLimits(u.MaxEvents, time.Duration(u.MaxAgeMS)*time.Millisecond)
	c.usage.SetKeepDays(u.KeepDaysValue())
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
	if prev == nil && c.routing != nil {
		c.loadRouting(c.routing)
	}
	swapPprof(next, prev)
	c.checkAccounts(next, inService(accounts))
	c.logUnservedSteps(next)
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

func (c *Core) logUnservedSteps(s *snapshot) {
	chains := s.Config.Routing.Chains
	if len(chains) == 0 {
		return
	}
	c.bgWG.Add(1)
	go func() {
		defer c.bgWG.Done()
		for _, name := range slices.Sorted(maps.Keys(chains)) {
			if err := s.Router.CheckSteps(c.bgCtx, chains[name]); err != nil && c.bgCtx.Err() == nil {
				log.Printf("routing.chains.%s: %v", name, err)
			}
		}
	}()
}

func (c *Core) extensionConfig(s *snapshot, id string) server.ExtensionConfig {
	if s.Config.Extension.ID == id {
		return s.Config.Extension
	}
	return s.Config.Extensions[id]
}

func (c *Core) configured(s *snapshot) []route.Account {
	var out []route.Account
	for _, a := range s.Config.Accounts() {
		if c.opts.SafeMode || s.exts[a.Extension] != nil {
			out = append(out, a)
		}
	}
	return out
}

func (c *Core) routed(s *snapshot) []route.Account {
	accounts := c.configured(s)
	all := s.Config.Accounts()
	for _, r := range c.creds.Records() {
		held := ""
		switch {
		case r.Disabled:
			held = route.HealthDisabled
		case r.State == cred.StateReauth:
			held = route.HealthNeedsLogin
		case !r.Usable():
			continue
		}
		if s.exts[r.Extension] == nil {
			continue
		}
		if slices.ContainsFunc(all, func(x route.Account) bool { return x.ID == r.ID }) {
			if held == "" {
				log.Printf("account %q is both in the config and added by login; using the config's", r.ID)
			}
			continue
		}
		a := r.Account
		a.ID, a.Credential, a.Managed, a.Extension, a.Held = r.ID, nil, true, r.Extension, held
		accounts = append(accounts, c.extensionConfig(s, r.Extension).LoginDefaults(a, r.Credential.Kind))
	}
	return accounts
}

func inService(accounts []route.Account) []route.Account {
	return slices.DeleteFunc(slices.Clone(accounts), func(a route.Account) bool { return a.Held != "" })
}

func (c *Core) accountsChanged() {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	s := c.cur.Load()
	accounts := c.routed(s)
	if err := s.Router.SetAccounts(accounts, sameAccount); err != nil {
		log.Printf("updating accounts: %v", err)
	}
	c.checkAccounts(s, inService(accounts))
}

func (c *Core) reserved(id string) bool {
	s := c.cur.Load()
	return s != nil && slices.ContainsFunc(s.Config.Accounts(), func(a route.Account) bool { return a.ID == id })
}

func (c *Core) takenByConfig(id string) bool {
	s := c.cur.Load()
	return s != nil && slices.ContainsFunc(s.Config.Accounts(), func(a route.Account) bool {
		return strings.EqualFold(a.ID, id) || c.state.PublicID(a.ID) == id
	})
}

func sameAccount(old, cur *route.Account) bool {
	return old.Extension == cur.Extension && sameJSON(old.Settings, cur.Settings)
}

func sameAccountAndCred(old, cur *route.Account) bool {
	return sameAccount(old, cur) && reflect.DeepEqual(old.Credential, cur.Credential)
}

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

func (c *Core) Stop() {
	c.reloading.Lock()
	defer c.reloading.Unlock()
	c.stopped = true
	c.bgCancel()
	c.bgMu.Lock()
	c.bgClosed = true
	c.bgMu.Unlock()
	c.checks.stop()
	if c.setup != nil {
		c.setup.stop()
	}
	c.stopRouting()
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
	c.bgWG.Wait()
	if s := c.cur.Load(); s != nil && s.pprof != nil {
		s.pprof.close()
	}
	c.creds.Close()
	if c.usage != nil {
		if err := c.usage.Close(); err != nil {
			log.Printf("usage: %v", err)
		}
	}
}

func (c *Core) keyID(k string) string {
	if c.keyIDs != nil {
		return c.keyIDs.ID(k)
	}
	return c.redactor.ID(k)
}

type ExtensionStatus = management.ExtensionStatus

type Status = management.Status

func (c *Core) Status() Status {
	version, commit, built := buildinfo.Get()
	c.mu.Lock()
	firstRun := c.setup == nil || c.setup.isFirstRun()
	s := Status{SafeMode: c.opts.SafeMode, FirstRun: firstRun, Desired: c.desired, Active: c.active, Extensions: []ExtensionStatus{},
		Version: version, Commit: commit, Built: built, ManagementAPI: management.APIVersion}
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

type failedStream struct{ ev llm.Event }

func failed(e *llm.Error) route.Stream {
	return &failedStream{llm.Event{Type: llm.EventError, Error: e}}
}

func (s *failedStream) Next() llm.Event { return s.ev }
func (s *failedStream) Close()          {}

type hostSession struct{ *host.Session }

func (s hostSession) Turn(ctx context.Context, a protocol.Account, req llm.Request) server.Stream {
	return s.Session.Turn(ctx, a, req)
}

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

type leasedLogin struct {
	*host.LoginCall
	release func()
	once    sync.Once
}

func (l *leasedLogin) Close() {
	l.LoginCall.Close()
	l.once.Do(l.release)
}

type failedLogin struct{ final protocol.Reply }

func (l *failedLogin) Next() protocol.Reply { return l.final }
func (l *failedLogin) Send(map[string]string) error {
	return fmt.Errorf("the login is not running: %w", net.ErrClosed)
}
func (l *failedLogin) Close() {}
