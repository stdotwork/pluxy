package cred

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

const RecordFormat = 0

const (
	StateActive = "active"
	StateReauth = "reauth_required"
)

type Record struct {
	Format        int                 `json:"format"`
	ID            string              `json:"id"`
	Extension     string              `json:"extension"`
	Registration  string              `json:"registration"`
	Version       uint64              `json:"version"`
	State         string              `json:"state"`
	Disabled      bool                `json:"disabled,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	Label         string              `json:"label,omitempty"`
	Named         bool                `json:"named,omitempty"`
	ProviderLabel string              `json:"provider_label,omitempty"`
	Identity      map[string]string   `json:"identity,omitempty"`
	Account       route.Account       `json:"account"`
	Credential    protocol.Credential `json:"credential"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

func (r Record) identityLabel() string {
	if r.Named || r.ProviderLabel != "" {
		return r.ProviderLabel
	}
	return r.Label
}

func (r *Record) setLabel(label string) {
	r.ProviderLabel = r.identityLabel()
	r.Label, r.Named = label, label != ""
	if label == "" {
		r.Label = r.ProviderLabel
	}
}

func (r Record) Usable() bool { return r.State == StateActive && !r.Disabled }

type Broken struct {
	ID   string
	Path string
	Err  error
}

func (b Broken) Why() string { return b.Path + ": " + b.Err.Error() }

type Backend interface {
	Load() ([]Record, []Broken, error)
	Save(Record) error
	Delete(id string) error
}

var (
	ErrExists   = errors.New("an account with this ID already exists")
	ErrNotFound = errors.New("no such account")
	ErrStale    = errors.New("the account changed since it was read")
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) && !strings.HasSuffix(id, ".") }

type Store struct {
	mu     sync.Mutex
	b      Backend
	recs   map[string]Record
	broken []Broken
	now    func() time.Time
}

type OpenError struct{ Err error }

func (e *OpenError) Error() string { return e.Err.Error() }
func (e *OpenError) Unwrap() error { return e.Err }

func Open(b Backend) (*Store, error) {
	s, err := open(b)
	if err != nil {
		return nil, &OpenError{err}
	}
	return s, nil
}

func open(b Backend) (*Store, error) {
	recs, broken, err := b.Load()
	if err != nil {
		return nil, err
	}
	s := &Store{b: b, recs: map[string]Record{}, broken: broken, now: time.Now}
	for _, r := range recs {
		if r.Format != RecordFormat {
			return nil, fmt.Errorf("credential store: account %q has record format %d; this core reads %d", r.ID, r.Format, RecordFormat)
		}
		dup := false
		for id := range s.recs {
			dup = dup || strings.EqualFold(id, r.ID)
		}
		if dup || !ValidID(r.ID) {
			return nil, fmt.Errorf("credential store: account ID %q is not valid or is used twice", r.ID)
		}
		s.recs[r.ID] = r
	}
	slices.SortFunc(s.broken, func(a, b Broken) int { return strings.Compare(a.ID, b.ID) })
	return s, nil
}

func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	return r, ok
}

func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.recs))
	for _, r := range s.recs {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (s *Store) Broken() []Broken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.broken)
}

func (s *Store) Create(r Record) (Record, error) {
	if !ValidID(r.ID) {
		return Record{}, fmt.Errorf("account ID %q is not valid: use letters, digits, '.', '_' and '-'", r.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.recs {
		// IDs name files, and on case-insensitive file systems (macOS, Windows) IDs that
		// differ only in case name the same file.
		if strings.EqualFold(id, r.ID) {
			return Record{}, ErrExists
		}
	}
	r.Format, r.Version, r.Registration = RecordFormat, 1, newRegistration()
	r.CreatedAt = s.now().UTC()
	r.UpdatedAt = r.CreatedAt
	r.Account.ID, r.Account.Credential = r.ID, nil
	if err := s.b.Save(r); err != nil {
		return Record{}, err
	}
	s.recs[r.ID] = r
	s.broken = slices.DeleteFunc(s.broken, func(b Broken) bool { return strings.EqualFold(b.ID, r.ID) })
	return r, nil
}

func (s *Store) Update(id, registration string, version uint64, change func(*Record)) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.recs[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	if cur.Registration != registration || cur.Version != version {
		return cur, ErrStale
	}
	next := cur
	change(&next)
	next.ID, next.Format, next.Registration, next.Extension, next.CreatedAt = cur.ID, RecordFormat, cur.Registration, cur.Extension, cur.CreatedAt
	next.Account.ID, next.Account.Credential = cur.ID, nil
	next.Version = cur.Version + 1
	next.UpdatedAt = s.now().UTC()
	if err := s.b.Save(next); err != nil {
		return cur, err
	}
	s.recs[id] = next
	return next, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, known := s.recs[id]
	broken := slices.ContainsFunc(s.broken, func(b Broken) bool { return b.ID == id })
	if !known && !broken {
		return ErrNotFound
	}
	if err := s.b.Delete(id); err != nil {
		return err
	}
	delete(s.recs, id)
	s.broken = slices.DeleteFunc(s.broken, func(b Broken) bool { return b.ID == id })
	return nil
}

func newRegistration() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
