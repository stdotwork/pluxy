package cred

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/private"
)

type FileBackend struct {
	Dir string
}

const (
	ext       = ".json"
	tmpPrefix = ".tmp-"
)

var beforeRoot = func(dir string) {}

func (f FileBackend) open(create bool) (*os.Root, error) {
	dir := private.Dir{Path: f.Dir, Setting: "credentials_dir", Redo: "sign the accounts in again"}
	checked, err := dir.Open(create)
	if err != nil {
		return nil, err
	}
	defer checked.Close()
	if create && runtime.GOOS != "windows" {
		if err := checked.Chmod(private.DirMode); err != nil {
			return nil, err
		}
	}
	beforeRoot(f.Dir)
	return dir.Root(checked)
}

func storeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("credential store: %w", err)
}

func (f FileBackend) Load() ([]Record, []Broken, error) {
	recs, broken, err := f.load()
	return recs, broken, storeError(err)
}

func (f FileBackend) load() ([]Record, []Broken, error) {
	root, err := f.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	d, err := root.Open(".")
	if err != nil {
		return nil, nil, err
	}
	entries, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return nil, nil, err
	}
	var out []Record
	var broken []Broken
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, tmpPrefix) {
			_ = root.Remove(name)
			continue
		}
		if e.IsDir() || !strings.HasSuffix(name, ext) {
			continue
		}
		b, err := f.readRecord(root, name)
		if err != nil {
			return nil, nil, err
		}
		if b == nil {
			continue
		}
		var r Record
		if err := json.Unmarshal(b, &r); err != nil {
			broken = append(broken, f.broken(name, fmt.Errorf("%s: %w", name, err)))
			continue
		}
		if r.ID+ext != name {
			broken = append(broken, f.broken(name, fmt.Errorf("%s holds account %q", name, r.ID)))
			continue
		}
		out = append(out, r)
	}
	return out, broken, nil
}

func (f FileBackend) broken(name string, err error) Broken {
	path := filepath.Join(f.Dir, name)
	log.Printf("credential store: skipped %s: %v", path, err)
	return Broken{ID: strings.TrimSuffix(name, ext), Path: path, Err: err}
}

func (f FileBackend) readRecord(root *os.Root, name string) ([]byte, error) {
	path := filepath.Join(f.Dir, name)
	skip := func(why string) ([]byte, error) {
		log.Printf("credential store: skipped %s: %s", path, why)
		return nil, nil
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return skip("it is not a regular file (a link?); core reads only the record files it wrote")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if st, err := file.Stat(); err != nil {
		return nil, err
	} else if !st.Mode().IsRegular() {
		return skip("it is not a regular file (a link?); core reads only the record files it wrote")
	}
	if ok, owner, err := private.Owner(file); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	} else if !ok {
		return skip("it is owned by " + owner + ", not by this user. Take ownership of it from an elevated prompt " +
			"(for example: takeown /f \"" + path + "\"), or remove it and sign the account in again")
	}
	ok, err := private.FilePrivate(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	b, err := io.ReadAll(file)
	if err != nil || ok {
		return b, err
	}
	// Closed first: Windows does not replace a file that is open.
	file.Close()
	if err := private.WriteFile(root, f.Dir, tmpPrefix, name, b); err != nil {
		return nil, fmt.Errorf("making %s private: %w", name, err)
	}
	log.Printf("credential store: made %s private: its own access list let in more than this user and SYSTEM", path)
	return b, nil
}

func (f FileBackend) Save(r Record) error {
	return storeError(f.save(r))
}

func (f FileBackend) save(r Record) error {
	if !ValidID(r.ID) {
		return fmt.Errorf("account ID %q is not valid", r.ID)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	root, err := f.open(true)
	if err != nil {
		return err
	}
	defer root.Close()
	return private.WriteFile(root, f.Dir, tmpPrefix, r.ID+ext, b)
}

func (f FileBackend) Delete(id string) error {
	return storeError(f.delete(id))
}

func (f FileBackend) delete(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("account ID %q is not valid", id)
	}
	root, err := f.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(id + ext); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return private.SyncDir(root)
}

func CreatePrivate(path string, b []byte) error {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return CreatePrivateIn(root, dir, name, b)
}

func CreatePrivateIn(root *os.Root, dir, name string, b []byte) error {
	tmp, tmpName, err := private.CreateTemp(root, dir, tmpPrefix)
	if err != nil {
		return err
	}
	defer root.Remove(tmpName)
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := root.Link(tmpName, name); err != nil {
		return err
	}
	return private.SyncDir(root)
}
