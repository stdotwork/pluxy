package extconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stdotwork/pluxy/next/internal/private"
)

// maxFile bounds a settings file: above the most a schema's limits allow (50 fields of 4,096
// characters, each at most 6 bytes as escaped JSON, is about 1.2 MiB). Core refuses to save
// more, so it can always read what it saved.
const maxFile = 4 << 20

// Store keeps each extension's settings in its own file under a folder:
// {dir}/{publisher}/{name}.json. Each file can be read only by core's user (and, on Windows,
// SYSTEM): on unix the folders are made 0700 and each file 0600; on Windows the folder dir is
// checked and made private (internal/private) each time settings are loaded or saved, each
// publisher's folder takes its entries, and each file gets the private access list as it is
// made. A save writes a temporary file, syncs it and renames it over the old one.
type Store struct {
	dir string
	mu  sync.Mutex // one change at a time, so a change never loses another
}

// NewStore returns the store in dir. The folder is made when settings are first saved.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// names returns the publisher's folder and the file name of extension id's settings.
func (st *Store) names(id string) (pub, file string, err error) {
	pub, name, ok := strings.Cut(id, "/")
	if !ok || pub == "" || name == "" || strings.ContainsAny(name, `/\`) || !filepath.IsLocal(pub) || !filepath.IsLocal(name) {
		return "", "", fmt.Errorf("extension ID %q is not publisher/name", id)
	}
	return pub, name + ".json", nil
}

// publisher checks the store's folder (private.Dir.Open) and opens the publisher's folder pub in
// it as a root, making both when create is set.
func (st *Store) publisher(pub string, create bool) (*os.Root, error) {
	top, err := private.Dir{Path: st.dir, Redo: "set the extensions' settings again"}.OpenRoot(create)
	if err != nil {
		return nil, err
	}
	defer top.Close()
	if create {
		if err := top.Mkdir(pub, private.DirMode); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	return top.OpenRoot(pub)
}

// Load returns the settings stored for extension id; none when nothing is stored.
func (st *Store) Load(id string) (Stored, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.load(id)
}

func (st *Store) load(id string) (Stored, error) {
	none := Stored{Values: Values{}}
	pub, file, err := st.names(id)
	if err != nil {
		return Stored{}, err
	}
	root, err := st.publisher(pub, false)
	if errors.Is(err, fs.ErrNotExist) {
		return none, nil
	}
	if err != nil {
		return Stored{}, err
	}
	defer root.Close()
	f, err := root.Open(file)
	if errors.Is(err, fs.ErrNotExist) {
		return none, nil
	}
	if err != nil {
		return Stored{}, err
	}
	defer f.Close()
	if ok, owner, err := private.Owner(f); err != nil {
		return Stored{}, fmt.Errorf("settings of %s: %w", id, err)
	} else if !ok {
		// Core did not write it: another account may have put it there before the folder was
		// private. The next save replaces it.
		log.Printf("settings of %s: skipped %s: it is owned by %s, not by this user", id,
			filepath.Join(st.dir, pub, file), owner)
		return none, nil
	}
	var v Stored
	if err := json.NewDecoder(&limited{f, maxFile}).Decode(&v); err != nil {
		return Stored{}, fmt.Errorf("settings of %s: %w", id, err)
	}
	if v.Values == nil {
		v.Values = Values{}
	}
	return v, nil
}

// Change applies change to the settings of extension id as schema s allows (Schema.Merge),
// saves them and returns them. Nothing is saved when the result breaks the schema.
func (st *Store) Change(id string, s *Schema, change Values) (Stored, error) {
	fail := func(err error) (Stored, error) { return Stored{}, err }
	st.mu.Lock()
	defer st.mu.Unlock()
	stored, err := st.load(id)
	if err != nil {
		return fail(err)
	}
	next, err := s.Merge(stored, change)
	if err != nil {
		return fail(&Invalid{err})
	}
	pub, file, _ := st.names(id) // load checked it
	b, err := json.Marshal(next)
	if err != nil {
		return fail(err)
	}
	if len(b) > maxFile {
		return fail(&Invalid{fmt.Errorf("the settings would be larger than %d MiB", maxFile>>20)})
	}
	root, err := st.publisher(pub, true)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	if err := private.WriteFile(root, filepath.Join(st.dir, pub), ".settings-", file, b); err != nil {
		return fail(err)
	}
	return next, nil
}

// Invalid is a change the schema refuses.
type Invalid struct{ Err error }

func (e *Invalid) Error() string { return e.Err.Error() }
func (e *Invalid) Unwrap() error { return e.Err }

// limited reads at most n bytes and then fails, so an oversized file is an error, not a cut.
type limited struct {
	f *os.File
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("file is larger than 1 MiB")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.f.Read(p)
	l.n -= int64(n)
	return n, err
}
