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

const ManifestFile = "pluxy-extension.json"

type Manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	ID              string            `json:"id"`
	Version         string            `json:"version"`
	Protocol        []int             `json:"protocol"`
	Programs        map[string]string `json:"programs"`
	UI              *UI               `json:"ui,omitempty"`
	Config          *ConfigSection    `json:"config,omitempty"`
	App             json.RawMessage   `json:"app,omitempty"`

	dir string
}

const UIVersion = 0

const (
	PermConfigRead  = "config.read"
	PermConfigWrite = "config.write"
)

const (
	maxUIFiles = 1000
	maxUIBytes = 32 << 20
	maxPages   = 20
)

type UI struct {
	Version     int      `json:"version"`
	Dir         string   `json:"dir"`
	Permissions []string `json:"permissions,omitempty"`
	Pages       []Page   `json:"pages"`
}

type Page struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Short string `json:"short"`
	Path  string `json:"path"`
}

type ConfigSection struct {
	Version int             `json:"version"`
	Schema  json.RawMessage `json:"schema"`

	schema *extconfig.Schema
}

const ConfigVersion = 0

func (m *Manifest) Settings() *extconfig.Schema {
	if m.Config == nil {
		return nil
	}
	return m.Config.schema
}

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

func ValidID(id string) bool { return idPattern.MatchString(id) }

func ValidVersion(v string) bool { return versionPattern.MatchString(v) }

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
