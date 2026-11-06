// Package core holds core's live state: the published config snapshot, the extension
// generation it names, and the reload transaction that replaces them.
//
// A reload reads the config file, validates it, prepares what it needs (starting a new
// extension generation when the extension's manifest or program changed) and only then
// publishes the new snapshot in one step. Each request keeps the snapshot and extension
// generation it started with. A reload that fails at any step leaves the active snapshot in
// place and records why. A replaced extension generation drains: it finishes the requests that
// started on it, until its deadline, and then stops.
//
// Accounts added by login live in one credential store for core's whole run; each published
// router routes over the config's accounts and those the store holds for its extension.
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
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
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
	ext      *host.Managed // nil in safe mode
}

// Core is core's live state.
type Core struct {
	opts  Options
	state *route.State
	creds *cred.Manager // opened by the first load

	// accounts serialises setting the accounts a router routes over: a change in the
	// credential store and a publish never interleave.
	accounts sync.Mutex

	reloading sync.Mutex // one reload at a time
	stopped   bool       // set by Stop under reloading: no reload after it
	cur       atomic.Pointer[snapshot]

	mu       sync.Mutex
	desired  Revision
	active   Revision
	draining []*host.Managed
}

// Start loads the config file and publishes it as revision 1. Unless in safe mode, it starts
// the extension's program to check it; the program then runs only while it is needed.
func Start(opts Options) (*Core, error) {
	c := &Core{opts: opts, state: route.NewState()}
	if err := c.reload(true, "start"); err != nil {
		if c.creds != nil {
			c.creds.Close()
		}
		return nil, err
	}
	c.creds.OnChange(c.accountsChanged)
	return c, nil
}

// Config returns the active config.
func (c *Core) Config() *server.Config { return c.cur.Load().Config }

// Acquire returns the current generation for one request; release ends the request's hold on
// it, so a replaced extension generation can finish draining.
func (c *Core) Acquire() (*server.Generation, func()) {
	s, release := c.acquire()
	return &s.Generation, release
}

// acquire returns the current snapshot with a lease on its extension generation.
func (c *Core) acquire() (*snapshot, func()) {
	for {
		s := c.cur.Load()
		if s.ext == nil {
			return s, func() {}
		}
		if s.ext.Acquire() {
			return s, s.ext.Release
		}
		if c.cur.Load() == s {
			// Stopped and not replaced: core is shutting down. The request fails in the
			// extension call.
			return s, func() {}
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
	if c.stopped {
		return errors.New("core is stopping")
	}

	b, err := os.ReadFile(c.opts.ConfigPath)
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
	c.desired = Revision{Revision: rev, Hash: hash, At: time.Now()}
	c.mu.Unlock()

	var next *snapshot
	if err == nil {
		next, err = c.prepare(rev, b, prev)
	}
	if err != nil {
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
	return nil
}

// prepare validates the config and builds everything the snapshot needs, without changing
// what is live. A new extension generation it starts is stopped again if prepare fails.
func (c *Core) prepare(rev uint64, b []byte, prev *snapshot) (*snapshot, error) {
	cfg, err := server.ParseConfig(b, c.opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	if prev != nil && cfg.Listen != prev.Config.Listen {
		return nil, fmt.Errorf("config: listen cannot change while core runs (%q to %q); restart core to apply it", prev.Config.Listen, cfg.Listen)
	}
	if prev != nil && cfg.CredentialsDir != prev.Config.CredentialsDir {
		return nil, fmt.Errorf("config: credentials_dir cannot change while core runs (%q to %q); restart core to apply it", prev.Config.CredentialsDir, cfg.CredentialsDir)
	}
	next := &snapshot{revision: rev}
	attempt := func(ctx context.Context, a protocol.Account, req llm.Request) route.Stream {
		if next.ext == nil {
			return failed(&llm.Error{Code: llm.CodeExtensionUnavailable, Message: "core is in safe mode: no extension runs"})
		}
		return next.ext.Generate(ctx, a, req)
	}
	router, err := route.New(cfg.Routing, cfg.Extension.Accounts, attempt, c.state)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	next.Generation = server.Generation{Config: cfg, Router: router}
	if !c.opts.SafeMode {
		if next.ext, err = c.prepareExtension(cfg, rev, prev); err != nil {
			return nil, err
		}
	}
	if c.creds == nil { // the first load: nothing is live yet
		var info protocol.Info
		if next.ext != nil {
			info = next.ext.Info()
		}
		store, err := cred.Open(cred.FileBackend{Dir: cfg.CredentialsDir})
		if err != nil {
			if next.ext != nil {
				next.ext.Stop()
			}
			return nil, err
		}
		c.creds = cred.NewManager(store, provider{c: c, first: info}, cred.Options{Reserved: c.reserved})
	}
	router.UseCredentials(c.creds)
	return next, nil
}

// prepareExtension returns the extension generation cfg names: prev's when its code has not
// changed, else a new one, started and handshaken.
//
// The extension's ID cannot change: the credential store holds accounts for the one extension
// that logged them in, and requests and logins in flight on the old generation still use its
// accounts. shortcut: switching to another extension takes a restart; extension packages
// (installing and removing several extensions) will lift this.
func (c *Core) prepareExtension(cfg *server.Config, rev uint64, prev *snapshot) (*host.Managed, error) {
	fp, err := host.Fingerprint(cfg.Extension.Manifest)
	if err != nil {
		return nil, err
	}
	if prev != nil && prev.ext != nil {
		if prev.ext.Path() == cfg.Extension.Manifest && prev.ext.Fingerprint() == fp && prev.ext.State() != host.StateQuarantined {
			return prev.ext, nil // same code: keep the running generation
		}
		m, err := host.LoadManifest(cfg.Extension.Manifest)
		if err != nil {
			return nil, err
		}
		if was := prev.ext.Manifest().ID; m.ID != was {
			return nil, fmt.Errorf("config: the extension cannot change from %s to %s while core runs (accounts added by login belong to one extension); restart core to switch", was, m.ID)
		}
	}
	return host.Prepare(cfg.Extension.Manifest, rev, policy(cfg.Extension), c.opts.Host)
}

// publish makes next the snapshot new requests get, carries routing state over, and drains the
// extension generation it replaces.
func (c *Core) publish(next, prev *snapshot, hash string) {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	if err := next.Router.SetAccounts(c.routed(next)); err != nil {
		log.Printf("updating accounts: %v", err)
	}
	if prev != nil {
		c.state.Retain(prev.Router, next.Router, sameAccount(prev, next))
	}
	if next.ext != nil {
		next.ext.SetPolicy(policy(next.Config.Extension))
	}
	c.mu.Lock()
	c.cur.Store(next)
	c.active = Revision{Revision: next.revision, Hash: hash, At: time.Now()}
	c.desired.Error = ""
	var old *host.Managed
	if prev != nil && prev.ext != nil && prev.ext != next.ext {
		old = prev.ext
		c.draining = append(c.draining, old)
	}
	c.mu.Unlock()
	if old != nil {
		old.Drain(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			for i, g := range c.draining {
				if g == old {
					c.draining = append(c.draining[:i], c.draining[i+1:]...)
					break
				}
			}
		})
	}
}

// routed returns the accounts s routes over: the config's, then those the credential store
// holds for the extension. A stored account whose ID the config also uses is left out.
func (c *Core) routed(s *snapshot) []route.Account {
	cfg := s.Config.Extension.Accounts
	accounts := slices.Clone(cfg)
	if s.ext == nil {
		return accounts
	}
	for _, a := range c.creds.Accounts() {
		if slices.ContainsFunc(cfg, func(x route.Account) bool { return x.ID == a.ID }) {
			log.Printf("account %q is both in the config and added by login; using the config's", a.ID)
			continue
		}
		accounts = append(accounts, a)
	}
	return accounts
}

// accountsChanged gives the active router the credential store's accounts after they changed.
func (c *Core) accountsChanged() {
	c.accounts.Lock()
	defer c.accounts.Unlock()
	s := c.cur.Load()
	if err := s.Router.SetAccounts(c.routed(s)); err != nil {
		log.Printf("updating accounts: %v", err)
	}
}

// reserved reports whether the active config sets an account with ID id, which a login may
// then not take.
func (c *Core) reserved(id string) bool {
	s := c.cur.Load()
	return s != nil && slices.ContainsFunc(s.Config.Extension.Accounts, func(a route.Account) bool { return a.ID == id })
}

// sameAccount reports whether an account with one ID is the same upstream account in both
// snapshots: the same extension (by ID; a new version of it is the same provider) and the same
// settings. Its credential may change: a credential is the account's, not its identity.
func sameAccount(prev, next *snapshot) func(old, cur *route.Account) bool {
	sameExt := prev.ext == nil || next.ext == nil || prev.ext.Manifest().ID == next.ext.Manifest().ID
	return func(old, cur *route.Account) bool {
		return sameExt && sameJSON(old.Settings, cur.Settings)
	}
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

// policy resolves the extension's run policy from its config section.
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
	c.mu.Lock()
	gens := append([]*host.Managed(nil), c.draining...)
	c.mu.Unlock()
	if s := c.cur.Load(); s != nil && s.ext != nil {
		gens = append(gens, s.ext)
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
	// Current is true for the generation new requests go to.
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
	if cur != nil && cur.ext != nil {
		s.Extensions = append(s.Extensions, ExtensionStatus{Status: cur.ext.Status(), Current: true})
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

// provider is the credential manager's view of the extension: each call goes to the extension
// generation that is current when it starts and holds a lease on it until it ends. Every
// generation has the same ID (see prepareExtension).
type provider struct {
	c     *Core
	first protocol.Info // the first generation's, until it is published; empty in safe mode
}

func (p provider) Info() protocol.Info {
	if s := p.c.cur.Load(); s != nil && s.ext != nil {
		return s.ext.Info()
	}
	return p.first
}

func (p provider) Login(ctx context.Context, account protocol.Account, login protocol.Login) cred.LoginCall {
	s, release := p.c.acquire()
	if s.ext == nil {
		release()
		return &failedLogin{protocol.Reply{Event: safeModeEvent()}}
	}
	return &leasedLogin{LoginCall: s.ext.Login(ctx, account, login), release: release}
}

func (p provider) Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error) {
	s, release := p.c.acquire()
	defer release()
	if s.ext == nil {
		return nil, safeModeEvent().Error
	}
	return s.ext.Refresh(ctx, account)
}

func safeModeEvent() llm.Event {
	return llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeExtensionUnavailable, Message: "core is in safe mode: no extension runs"}}
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
