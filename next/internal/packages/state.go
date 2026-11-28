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

const StateFile = "packages.json"

const StateFormat = 0

type Installed struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Registry    string    `json:"registry"`
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	Fingerprint string    `json:"fingerprint"`
	Enabled     bool      `json:"enabled"`
	InstalledAt time.Time `json:"installed_at"`
}

func (in Installed) Dir(root string) string { return filepath.Join(root, in.rel()) }

func (in Installed) rel() string {
	pub, name, _ := strings.Cut(in.ID, "/")
	return filepath.Join(pub, name, in.Version)
}

func (in Installed) Manifest(root string) string {
	return filepath.Join(in.Dir(root), host.ManifestFile)
}

type State map[string]Installed

func (s State) Clone() State { return maps.Clone(s) }

func (s State) Sorted() []Installed {
	out := slices.Collect(maps.Values(s))
	slices.SortFunc(out, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	return out
}

type stateFile struct {
	Format   int         `json:"format"`
	Packages []Installed `json:"packages"`
}

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
	defer os.Remove(tmp)
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
