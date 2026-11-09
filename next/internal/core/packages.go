package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/protocol"
)

// ErrNotStarted wraps the failure of a package change whose reload did not go through (the
// package did not start, or the config file does not load); nothing changed.
var ErrNotStarted = errors.New("not applied")

// loadPackages reads the record of installed packages at start, after clearing what an
// interrupted install left behind. With no record yet and a bundled registry, it installs and
// enables every package the bundled registry lists, through the same steps as any install.
func (c *Core) loadPackages(cfg *server.Config) (packages.State, error) {
	if err := packages.ClearStaging(c.pkgDir); err != nil {
		return nil, err
	}
	st, ok, err := packages.LoadState(c.pkgDir)
	if err != nil || ok || c.opts.Bundled == "" {
		return st, err
	}
	bundled := packages.Registry{Name: server.BundledRegistry, URL: c.opts.Bundled}
	in := c.installer(nil)
	in.Registries = []packages.Registry{bundled}
	ctx := context.Background()
	ix, err := packages.ReadIndex(ctx, in.Client, bundled)
	if err != nil {
		log.Printf("bundled packages not installed: %v", err)
		return st, nil
	}
	for _, id := range slices.Sorted(maps.Keys(ix.Packages)) {
		staged, err := in.Fetch(ctx, packages.Request{ID: id, Registry: bundled.Name})
		if err != nil {
			log.Printf("bundled package %s not installed: %v", id, err)
			continue
		}
		p, err := staged.Commit(nil)
		if err != nil {
			log.Printf("bundled package %s not installed: %v", id, err)
			continue
		}
		p.Enabled = true
		st[id] = p
		log.Printf("bundled package %s %s installed", id, p.Version)
	}
	// Saved even when empty: the first start is over, and a removed bundled package stays
	// removed.
	if err := packages.SaveState(c.pkgDir, st); err != nil {
		return nil, err
	}
	return st, nil
}

// installer returns an installer for the packages folder with cfg's registries, then the
// bundled one.
func (c *Core) installer(cfg *server.Config) *packages.Installer {
	in := &packages.Installer{Dir: c.pkgDir, Protocols: c.opts.Host.Versions}
	if len(in.Protocols) == 0 {
		in.Protocols = []int{protocol.Version}
	}
	if cfg != nil {
		in.Registries = slices.Clone(cfg.Packages.Registries)
	}
	if c.opts.Bundled != "" {
		in.Registries = append(in.Registries, packages.Registry{Name: server.BundledRegistry, URL: c.opts.Bundled})
	}
	return in
}

// InstallRequest is what POST /packages asks for.
type InstallRequest struct {
	ID       string `json:"id"`
	Version  string `json:"version,omitempty"`
	Registry string `json:"registry,omitempty"`
	Enable   bool   `json:"enable,omitempty"`
}

// PackageView is an installed package as management shows it.
type PackageView struct {
	packages.Installed
	// State is "disabled", the running extension's state, "failed" (enabled but it could not
	// start; Error says why) or "stopped" (enabled, in safe mode).
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Packages returns the installed packages.
func (c *Core) Packages() []PackageView {
	s := c.cur.Load()
	out := []PackageView{}
	for _, p := range s.pkgs.Sorted() {
		out = append(out, c.view(s, p))
	}
	return out
}

func (c *Core) view(s *snapshot, p packages.Installed) PackageView {
	v := PackageView{Installed: p, State: "disabled"}
	v.URL = packages.RedactURL(p.URL)
	switch g := s.exts[p.ID]; {
	case !p.Enabled:
	case g != nil && g.Path() == p.Manifest(c.pkgDir):
		v.State = g.State()
	case s.failed[p.ID] != "":
		v.State, v.Error = "failed", s.failed[p.ID]
	default:
		v.State = host.StateStopped
	}
	return v
}

func (c *Core) packageView(id string) (PackageView, bool) {
	s := c.cur.Load()
	p, ok := s.pkgs[id]
	if !ok {
		return PackageView{}, false
	}
	return c.view(s, p), true
}

// Install installs a package from a registry: it fetches and checks the release outside any
// lock on core's live state, moves it into place, then publishes the new record in a reload.
// A new version of an installed package replaces the old one, which drains when it runs; when
// the new version does not start, the old one stays.
func (c *Core) Install(ctx context.Context, req InstallRequest) (PackageView, error) {
	c.pkgOps.Lock()
	defer c.pkgOps.Unlock()
	staged, err := c.installer(c.Config()).Fetch(ctx, packages.Request{ID: req.ID, Version: req.Version, Registry: req.Registry})
	if err != nil {
		return PackageView{}, err
	}
	old, had := c.cur.Load().pkgs[req.ID]
	if had && old.Version == staged.Version {
		if old.SHA256 != staged.SHA256 {
			staged.Discard()
			return PackageView{}, fmt.Errorf("%w: %s %s is installed with archive digest %s; registry %s now pins %s. Remove it first to install the new archive",
				packages.ErrRefused, req.ID, old.Version, old.SHA256, staged.Registry, staged.SHA256)
		}
		if fp, err := host.Fingerprint(old.Manifest(c.pkgDir)); err == nil && fp == old.Fingerprint {
			// Installed and intact: nothing to replace.
			staged.Discard()
			if req.Enable && !old.Enabled {
				return c.setEnabled(req.ID, true)
			}
			v, _ := c.packageView(req.ID)
			return v, nil
		}
		// Its files changed since it was installed: the checked release replaces them.
	}
	p, err := staged.Commit(c.inUse)
	if err != nil {
		return PackageView{}, err
	}
	p.Enabled = req.Enable || (had && old.Enabled)
	strict := map[string]bool{req.ID: p.Enabled}
	c.reloading.Lock()
	err = c.transact(true, "installing "+req.ID+" "+p.Version, func(st packages.State) { st[req.ID] = p }, strict, nil)
	recorded, disabled := err == nil, false
	if err != nil && !had && p.Enabled {
		// It did not start: keep it installed, disabled.
		p.Enabled = false
		recorded = c.transact(true, "installing "+req.ID+" "+p.Version+" disabled", func(st packages.State) { st[req.ID] = p }, nil, nil) == nil
		disabled = recorded
	}
	c.reloading.Unlock()
	if !recorded {
		// Nothing changed: the old version (if any) stays, the new files go.
		c.retire(p)
		return PackageView{}, fmt.Errorf("%w: %v", ErrNotStarted, err)
	}
	if disabled {
		v, _ := c.packageView(req.ID)
		return v, fmt.Errorf("%w: %s %s is installed but did not start, so it stays disabled: %v", ErrNotStarted, req.ID, p.Version, err)
	}
	if had && old.Version != p.Version {
		c.retire(old)
	}
	log.Printf("package %s %s installed from registry %s", req.ID, p.Version, p.Registry)
	v, _ := c.packageView(req.ID)
	return v, nil
}

// SetEnabled enables or disables an installed package.
func (c *Core) SetEnabled(id string, enabled bool) (PackageView, error) {
	c.pkgOps.Lock()
	defer c.pkgOps.Unlock()
	return c.setEnabled(id, enabled)
}

func (c *Core) setEnabled(id string, enabled bool) (PackageView, error) {
	p, ok := c.cur.Load().pkgs[id]
	if !ok {
		return PackageView{}, fmt.Errorf("%w: package %s is not installed", packages.ErrNotFound, id)
	}
	if p.Enabled != enabled {
		p.Enabled = enabled
		verb := map[bool]string{true: "enabling ", false: "disabling "}[enabled]
		c.reloading.Lock()
		err := c.transact(true, verb+id, func(st packages.State) { st[id] = p }, map[string]bool{id: enabled}, nil)
		c.reloading.Unlock()
		if err != nil {
			return PackageView{}, fmt.Errorf("%w: %v", ErrNotStarted, err)
		}
	}
	v, _ := c.packageView(id)
	return v, nil
}

// RemovePackage disables a package and removes its files once no generation runs them. Its
// accounts stay.
func (c *Core) RemovePackage(id string) error {
	c.pkgOps.Lock()
	defer c.pkgOps.Unlock()
	p, ok := c.cur.Load().pkgs[id]
	if !ok {
		return fmt.Errorf("%w: package %s is not installed", packages.ErrNotFound, id)
	}
	c.reloading.Lock()
	err := c.transact(true, "removing "+id, func(st packages.State) { delete(st, id) }, nil, nil)
	c.reloading.Unlock()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotStarted, err)
	}
	c.retire(p)
	log.Printf("package %s %s removed", id, p.Version)
	return nil
}

// retire marks p's files for removal once nothing uses them.
func (c *Core) retire(p packages.Installed) {
	c.mu.Lock()
	c.retired = append(c.retired, p)
	c.mu.Unlock()
	c.sweep()
}

// sweep removes the files of retired package versions that neither the record nor a running
// or draining generation uses.
func (c *Core) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.cur.Load()
	keep := c.retired[:0]
	for _, p := range c.retired {
		if cur, ok := s.pkgs[p.ID]; ok && cur.Version == p.Version {
			continue // installed again: no longer retired
		}
		if c.inUseLocked(p.Dir(c.pkgDir)) {
			keep = append(keep, p)
			continue
		}
		if err := packages.Remove(c.pkgDir, p.ID, p.Version); err != nil {
			log.Printf("removing the files of package %s %s: %v", p.ID, p.Version, err)
		}
	}
	c.retired = keep
}

// inUse reports whether a running or draining generation runs from the package folder dir.
func (c *Core) inUse(dir string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inUseLocked(dir)
}

func (c *Core) inUseLocked(dir string) bool {
	gens := slices.Clone(c.draining)
	if s := c.cur.Load(); s != nil {
		for _, g := range s.exts {
			gens = append(gens, g)
		}
	}
	for _, g := range gens {
		if g.State() != host.StateStopped && filepath.Dir(g.Path()) == dir {
			return true
		}
	}
	return false
}

// RegistryView is what one registry offers, as management shows it.
type RegistryView struct {
	packages.Registry
	Packages []RegistryPackage `json:"packages,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// RegistryPackage is one package a registry offers.
type RegistryPackage struct {
	ID       string            `json:"id"`
	Releases []RegistryRelease `json:"releases"`
}

// RegistryRelease is one release a registry offers.
type RegistryRelease struct {
	Version   string   `json:"version"`
	Protocol  []int    `json:"protocol"`
	Platforms []string `json:"platforms"`
}

// Registries reads every registry core installs from.
func (c *Core) Registries(ctx context.Context) []RegistryView {
	in := c.installer(c.Config())
	out := []RegistryView{}
	for _, r := range in.Registries {
		v := RegistryView{Registry: r}
		v.URL = packages.RedactURL(r.URL)
		ix, err := packages.ReadIndex(ctx, in.Client, r)
		if err != nil {
			v.Error = err.Error()
			out = append(out, v)
			continue
		}
		for _, id := range slices.Sorted(maps.Keys(ix.Packages)) {
			p := RegistryPackage{ID: id, Releases: []RegistryRelease{}}
			for _, rel := range ix.Packages[id].Releases {
				p.Releases = append(p.Releases, RegistryRelease{Version: rel.Version, Protocol: rel.Protocol,
					Platforms: slices.Sorted(maps.Keys(rel.Artifacts))})
			}
			v.Packages = append(v.Packages, p)
		}
		out = append(out, v)
	}
	return out
}
