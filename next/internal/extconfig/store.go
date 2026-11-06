package extconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// maxFile bounds a settings file: above the most a schema's limits allow (50 fields of 4,096
// characters, each at most 6 bytes as escaped JSON, is about 1.2 MiB). Core refuses to save
// more, so it can always read what it saved.
const maxFile = 4 << 20

// Store keeps each extension's settings in its own file under a folder, readable only by
// core's user: {dir}/{publisher}/{name}.json.
type Store struct {
	dir string
	mu  sync.Mutex // one change at a time, so a change never loses another
}

// NewStore returns the store in dir. The folder is made when settings are first saved.
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (st *Store) path(id string) (string, error) {
	pub, name, ok := strings.Cut(id, "/")
	if !ok || pub == "" || name == "" || strings.ContainsAny(name, `/\`) || !filepath.IsLocal(pub) || !filepath.IsLocal(name) {
		return "", fmt.Errorf("extension ID %q is not publisher/name", id)
	}
	return filepath.Join(st.dir, pub, name+".json"), nil
}

// Load returns the settings stored for extension id; none when nothing is stored.
func (st *Store) Load(id string) (Stored, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.load(id)
}

func (st *Store) load(id string) (Stored, error) {
	p, err := st.path(id)
	if err != nil {
		return Stored{}, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Stored{Values: Values{}}, nil
	}
	if err != nil {
		return Stored{}, err
	}
	defer f.Close()
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
	p, _ := st.path(id) // load checked it
	b, err := json.Marshal(next)
	if err != nil {
		return fail(err)
	}
	if len(b) > maxFile {
		return fail(&Invalid{fmt.Errorf("the settings would be larger than %d MiB", maxFile>>20)})
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".settings-*")
	if err != nil {
		return fail(err)
	}
	defer os.Remove(tmp.Name()) // after the rename, nothing is there
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fail(err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
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
