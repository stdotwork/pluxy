// Package cred is core's credential authority: it keeps the accounts added by login and their
// credentials, versioned, in a store behind the Backend interface, and renews credentials
// through the provider extension that owns them. Core owns the record; the provider owns the
// login and refresh protocol.
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

// RecordFormat is the version of the Record format.
const RecordFormat = 0

// Account states. A disabled account (Record.Disabled, the user's choice) is a separate thing:
// enabling it again does not clear a refusal.
const (
	// StateActive: the credential is in use.
	StateActive = "active"
	// StateReauth: the provider refused the grant itself (for example OAuth invalid_grant); only
	// a new login brings the account back.
	StateReauth = "reauth_required"
)

// Record is one account in the store.
type Record struct {
	Format int    `json:"format"`
	ID     string `json:"id"`
	// Extension is the ID of the provider extension the account belongs to. Only that
	// extension ever receives the credential.
	Extension string `json:"extension"`
	// Registration identifies this registration of the ID: removing an account and adding it
	// again gives a new one, so a renewal begun before the removal can never land on the new
	// account.
	Registration string `json:"registration"`
	// Version goes up by one with every change to the record. A renewal applies only to the
	// version it renewed.
	Version  uint64 `json:"version"`
	State    string `json:"state"`
	Disabled bool   `json:"disabled,omitempty"`
	// Reason says why the account needs a new login.
	Reason string `json:"reason,omitempty"`
	// Label names the account for people (for example an e-mail address); not secret.
	Label string `json:"label,omitempty"`
	// Account holds the account's routing fields and settings. Its credential is always nil;
	// the credential is Credential.
	Account    route.Account       `json:"account"`
	Credential protocol.Credential `json:"credential"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

// Usable reports whether requests may be routed to the account.
func (r Record) Usable() bool { return r.State == StateActive && !r.Disabled }

// Backend keeps records durably. Save replaces a record whole and atomically: after a crash
// the backend holds the old record or the new one, never a mix. Implementations need not be
// safe for concurrent use; Store serialises calls.
type Backend interface {
	Load() ([]Record, error)
	Save(Record) error
	Delete(id string) error
}

// Errors from Store.
var (
	ErrExists   = errors.New("an account with this ID already exists")
	ErrNotFound = errors.New("no such account")
	// ErrStale: the record changed (or was removed and added again) since the caller read it,
	// so the caller's change was refused.
	ErrStale = errors.New("the account changed since it was read")
)

// idPattern is what an account ID in the store may look like; IDs name files.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidID reports whether id may name a stored account.
func ValidID(id string) bool { return idPattern.MatchString(id) && !strings.HasSuffix(id, ".") }

// Store is the versioned record store. Every change is a compare-and-swap on the record's
// registration and version, written through to the backend before it is visible. It is safe
// for concurrent use.
type Store struct {
	mu   sync.Mutex
	b    Backend
	recs map[string]Record
	now  func() time.Time
}

// Open loads the records the backend holds.
func Open(b Backend) (*Store, error) {
	recs, err := b.Load()
	if err != nil {
		return nil, err
	}
	s := &Store{b: b, recs: map[string]Record{}, now: time.Now}
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
	return s, nil
}

// Get returns the record for id.
func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.recs[id]
	return r, ok
}

// List returns every record, by ID.
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

// Create adds a record under a new registration, at version 1.
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
	return r, nil
}

// Update applies change to the record for id, if its registration and version are still the
// given ones, and returns the new record at the next version. Otherwise it changes nothing and
// returns ErrStale (or ErrNotFound when the account is gone).
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

// Delete removes the record for id.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recs[id]; !ok {
		return ErrNotFound
	}
	if err := s.b.Delete(id); err != nil {
		return err
	}
	delete(s.recs, id)
	return nil
}

func newRegistration() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
