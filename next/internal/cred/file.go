package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FileBackend keeps each record in its own JSON file in one folder: the minimal portable
// backend. The folder is created readable only by its owner (0700) and each file likewise
// (0600). A save writes a temporary file in the same folder, syncs it and renames it over the
// old one, so a crash leaves the old record or the new one.
//
// shortcut: no file lock, so two cores must not share a folder (their refreshes would not be
// serialised against each other); add a lock or a lease when one folder serves several cores.
type FileBackend struct {
	Dir string
}

const (
	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
	ext                  = ".json"
	tmpGlob              = ".tmp-*"
)

// Load reads every record in the folder. A missing folder holds no records.
func (f FileBackend) Load() ([]Record, error) {
	entries, err := os.ReadDir(f.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			// Left by a save that crashed before its rename; the record it was replacing is
			// still whole.
			_ = os.Remove(filepath.Join(f.Dir, name))
			continue
		}
		if e.IsDir() || !strings.HasSuffix(name, ext) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(f.Dir, name))
		if err != nil {
			return nil, err
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("credential store: %s: %w", name, err)
		}
		if r.ID+ext != name {
			return nil, fmt.Errorf("credential store: %s holds account %q", name, r.ID)
		}
		out = append(out, r)
	}
	return out, nil
}

// Save writes r atomically.
func (f FileBackend) Save(r Record) error {
	if !ValidID(r.ID) {
		return fmt.Errorf("credential store: account ID %q is not valid", r.ID)
	}
	if err := os.MkdirAll(f.Dir, dirMode); err != nil {
		return err
	}
	// MkdirAll leaves an existing folder as it is and applies the umask to a new one.
	if err := os.Chmod(f.Dir, dirMode); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, tmpGlob) // created 0600
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(fileMode); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(f.Dir, r.ID+ext)); err != nil {
		return err
	}
	done = true
	return syncDir(f.Dir)
}

// Delete removes the record for id.
func (f FileBackend) Delete(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("credential store: account ID %q is not valid", id)
	}
	if err := os.Remove(filepath.Join(f.Dir, id+ext)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(f.Dir)
}

// syncDir makes a rename or removal in dir durable. Windows cannot sync a folder; its renames
// are durable once they return.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
