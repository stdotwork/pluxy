// Package host is core's side of the extension protocol: it reads a manifest, starts the
// extension program, does the handshake and runs calls.
package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ManifestFile is the manifest's file name inside a package folder.
const ManifestFile = "pluxy-extension.json"

// Manifest is a package manifest, version 0.
type Manifest struct {
	ManifestVersion int               `json:"manifest_version"`
	ID              string            `json:"id"`
	Version         string            `json:"version"`
	Protocol        []int             `json:"protocol"`
	Programs        map[string]string `json:"programs"`

	dir string
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*/[a-z0-9][a-z0-9.-]*$`)

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
	if m.ManifestVersion != 0 {
		return fmt.Errorf("manifest_version %d is not supported", m.ManifestVersion)
	}
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("id %q is not of the form publisher/name", m.ID)
	}
	if m.Version == "" {
		return errors.New("version is required")
	}
	if len(m.Protocol) == 0 {
		return errors.New("protocol must list at least one version")
	}
	for platform, prog := range m.Programs {
		if !filepath.IsLocal(filepath.FromSlash(prog)) {
			return fmt.Errorf("program for %s must be a path inside the package folder", platform)
		}
	}
	return nil
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
