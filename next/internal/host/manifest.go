// Package host is core's side of the extension protocol: it reads a manifest, starts the
// extension program, does the handshake and runs calls.
package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/extconfig"
)

// ManifestFile is the manifest's file name inside a package folder.
const ManifestFile = "pluxy-extension.json"

// Manifest is a provider package manifest (version 0), optionally with app contributions (1).
type Manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	ID              string            `json:"id"`
	Version         string            `json:"version"`
	Protocol        []int             `json:"protocol"`
	Programs        map[string]string `json:"programs"`
	// UI is the package's pages, if any (next/ui/extension-pages/README.md).
	UI *UI `json:"ui,omitempty"`
	// Config is the schema of the extension's settings, if any.
	Config *ConfigSection `json:"config,omitempty"`
	// App belongs to the desktop host. Core never executes or serves it.
	App json.RawMessage `json:"app,omitempty"`

	dir string
}

// UIVersion is the version of the extension pages contract core serves.
const UIVersion = 0

// Permissions a package's pages may declare.
const (
	PermConfigRead  = "config.read"
	PermConfigWrite = "config.write"
)

// Limits of a package's ui folder.
const (
	maxUIFiles = 1000
	maxUIBytes = 32 << 20
	maxPages   = 20
)

// UI is the ui part of a manifest.
type UI struct {
	Version     int      `json:"version"`
	Dir         string   `json:"dir"`
	Permissions []string `json:"permissions,omitempty"`
	Pages       []Page   `json:"pages"`
}

// Page is one extension page.
type Page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Short string `json:"short"`
	Path  string `json:"path"`
}

// ConfigSection is the config part of a manifest.
type ConfigSection struct {
	Version int             `json:"version"`
	Schema  json.RawMessage `json:"schema"`

	schema *extconfig.Schema
}

// ConfigVersion is the version of the settings schema format core reads.
const ConfigVersion = 0

// Settings returns the parsed settings schema, or nil when the extension has none.
func (m *Manifest) Settings() *extconfig.Schema {
	if m.Config == nil {
		return nil
	}
	return m.Config.schema
}

// UIDir returns the absolute path of the package's ui folder, or "" when it has no pages.
func (m *Manifest) UIDir() string {
	if m.UI == nil {
		return ""
	}
	return filepath.Join(m.dir, filepath.FromSlash(m.UI.Dir))
}

var pageID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (u *UI) validate() error {
	if u.Version != UIVersion {
		return fmt.Errorf("ui.version %d is not supported (this core serves %d)", u.Version, UIVersion)
	}
	if u.Dir == "" || !filepath.IsLocal(filepath.FromSlash(u.Dir)) {
		return errors.New("ui.dir must be a folder inside the package folder")
	}
	for _, p := range u.Permissions {
		if p != PermConfigRead && p != PermConfigWrite {
			return fmt.Errorf("ui.permissions: %q is not a permission (%s, %s)", p, PermConfigRead, PermConfigWrite)
		}
	}
	if len(u.Pages) == 0 || len(u.Pages) > maxPages {
		return fmt.Errorf("ui.pages must list 1 to %d pages", maxPages)
	}
	seen := map[string]bool{}
	for i, p := range u.Pages {
		switch {
		case !pageID.MatchString(p.ID):
			return fmt.Errorf("ui.pages[%d].id %q is not 1 to 32 lower-case letters, digits and '-'", i, p.ID)
		case seen[p.ID]:
			return fmt.Errorf("ui.pages[%d].id %q is used twice", i, p.ID)
		case p.Title == "" || utf8.RuneCountInString(p.Title) > 60:
			return fmt.Errorf("ui.pages[%d].title must be 1 to 60 characters", i)
		case p.Short == "" || utf8.RuneCountInString(p.Short) > 8:
			return fmt.Errorf("ui.pages[%d].short must be 1 to 8 characters", i)
		case !strings.HasSuffix(p.Path, ".html") || !filepath.IsLocal(filepath.FromSlash(p.Path)) || strings.Contains(p.Path, `\`):
			return fmt.Errorf("ui.pages[%d].path must be an .html file inside ui.dir", i)
		}
		seen[p.ID] = true
	}
	return nil
}

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
)

// ValidID reports whether id is a publisher-qualified extension ID, publisher/name.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// ValidVersion reports whether v may be a package version: it names a folder.
func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

// LoadManifest reads and validates the manifest at path.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	m.dir = filepath.Dir(abs)
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.ManifestVersion != 0 && m.ManifestVersion != 1 {
		return fmt.Errorf("manifest_version %d is not supported", m.ManifestVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q is not of the form publisher/name", m.ID)
	}
	if !ValidVersion(m.Version) {
		return fmt.Errorf("version %q is not 1 to 64 letters, digits, '.', '+', '_' and '-' starting with a letter or digit", m.Version)
	}
	if m.ManifestVersion == 1 && len(m.App) > 0 && string(m.App) != "null" {
		var app AppContribution
		if err := json.Unmarshal(m.App, &app); err != nil {
			return fmt.Errorf("app: %w", err)
		}
		if err := app.validate(); err != nil {
			return err
		}
		if len(m.Protocol) == 0 && len(m.Programs) == 0 {
			return errors.New("app-only package: install it in the desktop app")
		}
		if len(m.Programs) == 0 {
			return errors.New("provider programs must be declared separately from app.programs")
		}
	}
	if len(m.Protocol) == 0 {
		return errors.New("protocol must list at least one version")
	}
	for platform, prog := range m.Programs {
		if !filepath.IsLocal(filepath.FromSlash(prog)) {
			return fmt.Errorf("program for %s must be a path inside the package folder", platform)
		}
	}
	if m.UI != nil {
		if err := m.UI.validate(); err != nil {
			return err
		}
	}
	if m.Config != nil {
		return m.Config.validate()
	}
	return nil
}

func (c *ConfigSection) validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("config.version %d is not supported (this core reads %d)", c.Version, ConfigVersion)
	}
	s, err := extconfig.Parse(c.Schema)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	c.schema = s
	return nil
}

// uiFiles returns every file in the ui folder, by its slash-separated path inside it, in order.
// The folder may hold only regular files and folders, within the limits; each page's file must
// be there.
func (m *Manifest) uiFiles() ([]string, error) {
	dir := m.UIDir()
	if dir == "" {
		return nil, nil
	}
	var names []string
	var total int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("ui folder may hold only regular files and folders: %s is not one", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		if len(names) > maxUIFiles || total > maxUIBytes {
			return fmt.Errorf("ui folder holds more than %d files or %d MiB", maxUIFiles, maxUIBytes>>20)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("extension %s: %w", m.ID, err)
	}
	for _, p := range m.UI.Pages {
		if !slices.Contains(names, p.Path) {
			return nil, fmt.Errorf("extension %s: page %s: %s is not in the ui folder", m.ID, p.ID, p.Path)
		}
	}
	return names, nil
}

// Program returns the absolute path of the program for this platform.
func (m *Manifest) Program() (string, error) {
	platform := runtime.GOOS + "/" + runtime.GOARCH
	prog, ok := m.Programs[platform]
	if !ok {
		names := make([]string, 0, len(m.Programs))
		for p := range m.Programs {
			names = append(names, p)
		}
		return "", fmt.Errorf("extension %s has no program for %s (has: %s)", m.ID, platform, strings.Join(names, ", "))
	}
	return filepath.Join(m.dir, filepath.FromSlash(prog)), nil
}
