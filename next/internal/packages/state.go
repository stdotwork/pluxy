package packages

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
)

// StateFile is the record of installed packages inside the packages folder.
const StateFile = "packages.json"

// StateFormat is the version of the StateFile format.
const StateFormat = 0

// Installed is one installed package.
type Installed struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Registry string `json:"registry"`
	// URL is where the archive came from.
	URL string `json:"url"`
	// SHA256 is the archive's digest, as the registry pinned it.
	SHA256 string `json:"sha256"`
	// Fingerprint is the digest of the unpacked manifest and program (host.Fingerprint); core
	// runs the package only while its files still have it.
	Fingerprint string    `json:"fingerprint"`
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installed_at"`
}

// Dir returns the package's folder under the packages folder root.
func (in Installed) Dir(root string) string { return filepath.Join(root, in.rel()) }

// rel returns the package's folder relative to the packages folder.
func (in Installed) rel() string {
	pub, name, _ := strings.Cut(in.ID, "/")
	return filepath.Join(pub, name, in.Version)
}

// Manifest returns the path of the package's manifest under root.
func (in Installed) Manifest(root string) string {
	return filepath.Join(in.Dir(root), host.ManifestFile)
}

// State is what is installed, by package ID.
type State map[string]Installed

// Clone returns a copy of s.
func (s State) Clone() State { return maps.Clone(s) }

// Sorted returns the installed packages by ID.
func (s State) Sorted() []Installed {
	out := slices.Collect(maps.Values(s))
	slices.SortFunc(out, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	return out
}

type stateFile struct {
	Format   int         `json:"format"`
	Packages []Installed `json:"packages"`
}

// LoadState reads the record in the packages folder dir. ok is false when there is none yet.
func LoadState(dir string) (s State, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, StateFile))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var f stateFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, false, fmt.Errorf("%s: %w", filepath.Join(dir, StateFile), err)
	}
	if f.Format != StateFormat {
		return nil, false, fmt.Errorf("%s has format %d; this core reads %d", filepath.Join(dir, StateFile), f.Format, StateFormat)
	}
	s = State{}
	for _, in := range f.Packages {
		if !host.ValidID(in.ID) || !host.ValidVersion(in.Version) || s[in.ID].ID != "" {
			return nil, false, fmt.Errorf("%s: package %q %q is not valid or is listed twice", filepath.Join(dir, StateFile), in.ID, in.Version)
		}
		s[in.ID] = in
	}
	return s, true, nil
}

// SaveState writes s as the record in the packages folder dir, replacing the old record whole:
// after a crash the folder holds the old record or the new one.
func SaveState(dir string, s State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(stateFile{Format: StateFormat, Packages: s.Sorted()}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, StateFile+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // after a successful rename there is nothing left to remove
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, StateFile))
}
