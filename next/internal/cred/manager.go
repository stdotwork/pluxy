package cred

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Providers interface {
	Provider(id string) Provider
	Running() []string
}

type Provider interface {
	Info() protocol.Info
	Login(ctx context.Context, account protocol.Account, login protocol.Login) LoginCall
	Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error)
}

type LoginCall interface {
	Next() protocol.Reply
	Send(input map[string]string) error
	Close()
}

var (
	refreshLead        = 5 * time.Minute
	failedBackoff      = 5 * time.Minute
	ineffectiveBackoff = 30 * time.Second
	renewedRecently    = time.Minute
	LoginTimeout       = 15 * time.Minute
	loginKeep          = 10 * time.Minute
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrConflict = errors.New("conflict")
	ErrIdentity = errors.New("the login signed in to a different account")
)

type Manager struct {
	store    *Store
	provs    Providers
	reserved func(id string) bool
	publicID func(id string) string
	taken    func(id string) bool
	now      func() time.Time
	random   io.Reader

	// a client that gives up waiting never cancels a renewal halfway (the provider may already have
	// rotated the refresh token, RFC 9700 section 4.14).
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu                 sync.Mutex
	flights            map[string]*flight
	renewals           map[string]*renewal
	logins             map[string]*login
	loginSeq           uint64
	onChange           func()
	onCredentialChange func(id string)
	onReauth           func(id string)
	onRemove           func(id string)
}

type flight struct {
	version uint64
	done    chan struct{}
	rec     Record
	err     *llm.Error
}

type renewal struct {
	reg      string
	cred     protocol.Credential
	settings json.RawMessage
	refused  bool
	at       time.Time
	until    time.Time
	err      *llm.Error
}

type Options struct {
	Reserved func(id string) bool
	PublicID func(id string) string
	Taken    func(id string) bool
	OnReauth func(id string)
	OnRemove func(id string)
}

func NewManager(store *Store, provs Providers, opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{store: store, provs: provs, reserved: opts.Reserved, publicID: opts.PublicID, taken: opts.Taken,
		onReauth: opts.OnReauth, onRemove: opts.OnRemove, now: time.Now, random: rand.Reader, ctx: ctx, cancel: cancel,
		flights: map[string]*flight{}, renewals: map[string]*renewal{}, logins: map[string]*login{}}
	if m.reserved == nil {
		m.reserved = func(string) bool { return false }
	}
	return m
}

func (m *Manager) OnChange(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onChange = f
}

func (m *Manager) changed() {
	m.mu.Lock()
	f := m.onChange
	m.mu.Unlock()
	if f != nil {
		f()
	}
}

func (m *Manager) OnCredentialChange(f func(id string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onCredentialChange = f
}

func (m *Manager) credentialChanged(id string) {
	m.mu.Lock()
	f := m.onCredentialChange
	m.mu.Unlock()
	if f != nil {
		f(id)
	}
}

func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) Accounts() []route.Account {
	var out []route.Account
	for _, r := range m.store.List() {
		if r.Usable() {
			a := r.Account
			a.ID, a.Credential, a.Managed, a.Extension = r.ID, nil, true, r.Extension
			out = append(out, a)
		}
	}
	return out
}

func (m *Manager) Records() []Record { return m.store.List() }

func (m *Manager) Broken() []Broken { return m.store.Broken() }

func (m *Manager) StoredCredential(extension, id string) (*protocol.Credential, *llm.Error) {
	rec, ok := m.store.Get(id)
	if !ok || rec.Extension != extension || !rec.Usable() {
		return nil, credentialError(llm.CodeUnavailable, fmt.Sprintf("the account of extension %s is unavailable", extension))
	}
	return &rec.Credential, nil
}

func (m *Manager) LastRenewalError(r Record) *llm.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rn := m.renewalFor(r); rn != nil && rn.err != nil && rn.until.After(m.now()) {
		return rn.err
	}
	return nil
}

func (m *Manager) renewalFor(r Record) *renewal {
	rn := m.renewals[r.ID]
	if rn == nil || rn.reg != r.Registration || !sameCredential(rn.cred, r.Credential) || !bytes.Equal(rn.settings, r.Account.Settings) {
		return nil
	}
	return rn
}

func (m *Manager) Refreshable(r Record) bool {
	if r.Credential.Kind == protocol.CredentialAPIKey {
		return false
	}
	p := m.provs.Provider(r.Extension)
	return p != nil && slices.Contains(p.Info().Operations, protocol.OpRefresh)
}

func credentialError(code, msg string) *llm.Error {
	return &llm.Error{Code: code, Scope: llm.ScopeCredential, Retryable: true, Message: msg}
}

func (m *Manager) Credential(ctx context.Context, extension, id string) (*protocol.Credential, uint64, *llm.Error) {
	rec, ok := m.store.Get(id)
	if !ok || rec.Extension != extension {
		return nil, 0, credentialError(llm.CodeUnavailable, fmt.Sprintf("the account of extension %s is not in the credential store", extension))
	}
	if !rec.Usable() {
		return nil, 0, credentialError(llm.CodeAuth, "the account is "+describeState(rec))
	}
	m.mu.Lock()
	rn := m.renewalFor(rec)
	m.mu.Unlock()
	if rn != nil && rn.refused {
		return nil, 0, credentialError(llm.CodeAuth, "the provider refused the grant; the account needs a new login")
	}
	now := m.now()
	expired := rec.Credential.ExpiresAt != 0 && rec.Credential.ExpiresAt <= now.Unix()
	if !m.due(rec) && expired && m.Refreshable(rec) {
		return nil, 0, m.waitingError(rec, now)
	}
	if m.due(rec) {
		r, e := m.renew(ctx, rec, false)
		now = m.now()
		switch {
		case e == nil && (r.Registration != rec.Registration || r.Extension != extension):
			return nil, 0, credentialError(llm.CodeUnavailable, "the account was removed")
		case e == nil:
			rec = r
			if !rec.Usable() {
				return nil, 0, credentialError(llm.CodeAuth, "the account is "+describeState(rec))
			}
		case e.Code == llm.CodeCancelled && ctx.Err() != nil:
			return nil, 0, &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
		case rec.Credential.ExpiresAt > now.Unix() && !rejectedGrant(e) && !keyless(rec.Credential):
		default:
			out := credentialError(llm.CodeUnavailable, "the credential has expired and renewing it failed: "+e.Message)
			if rejectedGrant(e) {
				out.Code = llm.CodeAuth
			} else if until := m.retryAt(rec); until.After(now) {
				out.RetryAfterMS = until.Sub(now).Milliseconds()
			}
			return nil, 0, out
		}
	}
	if keyless(rec.Credential) && m.Refreshable(rec) {
		return nil, 0, m.waitingError(rec, now)
	}
	c := rec.Credential
	return &c, rec.Version, nil
}

func (m *Manager) waitingError(rec Record, now time.Time) *llm.Error {
	msg := "the credential has expired and renewal is waiting after a failure"
	if last := m.LastRenewalError(rec); last != nil {
		msg += ": " + last.Message
	}
	out := credentialError(llm.CodeUnavailable, msg)
	if until := m.retryAt(rec); until.After(now) {
		out.RetryAfterMS = until.Sub(now).Milliseconds()
	}
	return out
}

func (m *Manager) Rejected(ctx context.Context, id string, version uint64) bool {
	rec, ok := m.store.Get(id)
	if !ok || !m.Refreshable(rec) {
		return false
	}
	if rec.Version != version {
		return rec.Usable()
	}
	m.mu.Lock()
	rn := m.renewalFor(rec)
	recent := rn != nil && rn.err == nil && m.now().Sub(rn.at) < renewedRecently
	m.mu.Unlock()
	if recent {
		return false
	}
	r, e := m.renew(ctx, rec, false)
	return e == nil && r.Version != version && r.Usable()
}

func (m *Manager) Refresh(ctx context.Context, id string) (Record, error) {
	rec, ok := m.store.Get(id)
	if !ok {
		return Record{}, ErrNotFound
	}
	if !m.Refreshable(rec) {
		return rec, fmt.Errorf("%w: account %q's credential cannot be refreshed", ErrConflict, id)
	}
	if rec.Disabled {
		return rec, fmt.Errorf("%w: account %q is disabled", ErrConflict, id)
	}
	r, e := m.renew(ctx, rec, true)
	if e != nil {
		return r, e
	}
	return r, nil
}

func renewAt(c protocol.Credential) int64 {
	switch {
	case c.RefreshAt != 0:
		return c.RefreshAt
	case c.ExpiresAt != 0:
		return c.ExpiresAt - int64(refreshLead/time.Second)
	}
	return 0
}

func keyless(c protocol.Credential) bool {
	return c.Kind == protocol.CredentialOAuth && c.AccessToken == ""
}

func (m *Manager) due(rec Record) bool {
	if !m.Refreshable(rec) {
		return false
	}
	now := m.now()
	if !keyless(rec.Credential) {
		if at := renewAt(rec.Credential); at == 0 || now.Unix() < at {
			return false
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rn := m.renewalFor(rec)
	return rn == nil || !rn.until.After(now)
}

func (m *Manager) retryAt(r Record) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rn := m.renewalFor(r); rn != nil {
		return rn.until
	}
	return time.Time{}
}

func (m *Manager) renew(ctx context.Context, seen Record, force bool) (Record, *llm.Error) {
	for {
		m.mu.Lock()
		cur, ok := m.store.Get(seen.ID)
		if !ok || cur.Registration != seen.Registration {
			m.mu.Unlock()
			return Record{}, credentialError(llm.CodeUnavailable, "the account was removed")
		}
		if cur.Version != seen.Version {
			m.mu.Unlock()
			return cur, nil
		}
		f := m.flights[seen.ID]
		if f != nil && f.version != cur.Version {
			m.mu.Unlock()
			select {
			case <-f.done:
				continue
			case <-ctx.Done():
				return Record{}, &llm.Error{Code: llm.CodeCancelled, Message: "cancelled while waiting for a credential renewal"}
			}
		}
		if f == nil {
			if rn := m.renewalFor(cur); !force && rn != nil && rn.until.After(m.now()) {
				m.mu.Unlock()
				return cur, rn.err
			}
			f = &flight{version: cur.Version, done: make(chan struct{})}
			m.flights[seen.ID] = f
			m.wg.Add(1)
			go m.runRenewal(cur, f)
		}
		m.mu.Unlock()
		select {
		case <-f.done:
			return f.rec, f.err
		case <-ctx.Done():
			return Record{}, &llm.Error{Code: llm.CodeCancelled, Message: "cancelled while waiting for a credential renewal"}
		}
	}
}

func (m *Manager) runRenewal(seen Record, f *flight) {
	defer m.wg.Done()
	var rec Record
	var e *llm.Error
	cur, ok := m.store.Get(seen.ID)
	switch {
	case !ok || cur.Registration != seen.Registration:
		e = credentialError(llm.CodeUnavailable, "the account was removed")
	case cur.Version != seen.Version:
		rec = cur
	default:
		p := m.provs.Provider(cur.Extension)
		if p == nil {
			rec, e = cur, credentialError(llm.CodeExtensionUnavailable, fmt.Sprintf("extension %s does not run", cur.Extension))
			break
		}
		acct := protocol.Account{ID: cur.ID, Settings: cur.Account.Settings, Credential: &cur.Credential, ProxyURL: cur.Account.ProxyURL, Headers: cur.Account.Headers}
		cred, perr := p.Refresh(m.ctx, acct)
		rec, e = m.apply(cur, cred, perr)
	}
	m.mu.Lock()
	f.rec, f.err = rec, e
	delete(m.flights, seen.ID)
	m.mu.Unlock()
	close(f.done)
}

func rejectedGrant(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeAuth && e.Scope == llm.ScopeCredential && !e.Retryable
}

func (m *Manager) apply(cur Record, cred *protocol.Credential, perr *llm.Error) (Record, *llm.Error) {
	now := m.now()
	switch {
	case perr == nil:
		next, err := m.updateRenewed(cur, false, func(r *Record) {
			r.Credential = *cred
			r.State, r.Reason = StateActive, ""
		})
		switch {
		case errors.Is(err, ErrNotFound):
			log.Printf("credentials: account %q: refused a renewed credential: the account was removed while it was being renewed", cur.ID)
			return Record{}, credentialError(llm.CodeUnavailable, "the account was removed")
		case errors.Is(err, ErrStale):
			log.Printf("credentials: account %q: refused a renewed credential: the account's credential was replaced while it was being renewed", cur.ID)
			return next, nil
		case err != nil:
			log.Printf("error: credentials: account %q: saving the renewed credential failed: %v", cur.ID, err)
			return cur, credentialError(llm.CodeInternal, "saving the renewed credential failed")
		}
		rn := &renewal{at: now}
		if at := renewAt(*cred); at != 0 && now.Unix() >= at || keyless(*cred) {
			rn.until = now.Add(ineffectiveBackoff)
		}
		m.setRenewal(next, rn)
		m.credentialChanged(next.ID)
		return next, nil
	case rejectedGrant(perr):
		next, err := m.updateRenewed(cur, true, func(r *Record) {
			r.State, r.Reason = StateReauth, perr.Message
		})
		switch {
		case errors.Is(err, ErrNotFound):
			return Record{}, credentialError(llm.CodeUnavailable, "the account was removed")
		case errors.Is(err, ErrStale):
			return next, nil
		case err != nil:
			log.Printf("error: credentials: account %q: the provider refused the grant, and saving that failed: %v", cur.ID, err)
			m.setRenewal(cur, &renewal{at: now, until: now.Add(failedBackoff), err: perr, refused: true})
			return cur, perr
		}
		log.Printf("error: credentials: account %q needs a new login: %s", cur.ID, perr.Message)
		m.setRenewal(next, &renewal{at: now, err: perr})
		m.changed()
		return next, perr
	case perr.Code == llm.CodeCancelled:
		return cur, perr
	default:
		if latest, ok := m.store.Get(cur.ID); !ok {
			return Record{}, credentialError(llm.CodeUnavailable, "the account was removed")
		} else if latest.Registration != cur.Registration || !sameCredential(latest.Credential, cur.Credential) ||
			!bytes.Equal(latest.Account.Settings, cur.Account.Settings) {
			return latest, nil
		}
		wait := failedBackoff
		if perr.RetryAfterMS > 0 {
			wait = time.Duration(perr.RetryAfterMS) * time.Millisecond
		}
		log.Printf("credentials: account %q: renewing the credential failed (next try in %s): %s", cur.ID, llm.WholeSeconds(wait), perr.Message)
		m.setRenewal(cur, &renewal{at: now, until: now.Add(wait), err: perr})
		return cur, perr
	}
}

func (m *Manager) updateRenewed(cur Record, sameSettings bool, change func(*Record)) (Record, error) {
	base := cur
	for {
		next, err := m.store.Update(base.ID, base.Registration, base.Version, change)
		if !errors.Is(err, ErrStale) {
			return next, err
		}
		if next.Registration != cur.Registration || !sameCredential(next.Credential, cur.Credential) ||
			sameSettings && !bytes.Equal(next.Account.Settings, cur.Account.Settings) {
			return next, ErrStale
		}
		base = next
	}
}

func sameCredential(a, b protocol.Credential) bool {
	return reflect.DeepEqual(a, b)
}

func (m *Manager) setRenewal(r Record, rn *renewal) {
	rn.reg, rn.cred, rn.settings = r.Registration, r.Credential, r.Account.Settings
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renewals[r.ID] = rn
}

func describeState(r Record) string {
	switch {
	case r.Disabled:
		return "disabled"
	case r.State == StateReauth:
		return "waiting for a new login"
	}
	return r.State
}

func (m *Manager) SetDisabled(id string, disabled bool) (Record, error) {
	return m.SetAccount(id, nil, &disabled, nil)
}

func (m *Manager) SetAccount(id string, change func(*route.Account) error, disabled *bool, label *string) (Record, error) {
	for {
		cur, ok := m.store.Get(id)
		if !ok {
			return Record{}, ErrNotFound
		}
		a := cur.Account
		if change != nil {
			if err := change(&a); err != nil {
				return Record{}, err
			}
			a.ID, a.Credential = cur.Account.ID, nil
			if err := a.Check(); err != nil {
				return Record{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
		}
		next, err := m.store.Update(id, cur.Registration, cur.Version, func(r *Record) {
			r.Account = a
			if disabled != nil {
				r.Disabled = *disabled
			}
			if label != nil {
				r.setLabel(*label)
			}
		})
		if errors.Is(err, ErrStale) {
			continue
		}
		if err == nil {
			m.changed()
		}
		return next, err
	}
}

func (m *Manager) Create(r Record) (Record, error) {
	if m.reserved(r.ID) {
		return Record{}, fmt.Errorf("%w: account %q is set in core's config", ErrConflict, r.ID)
	}
	a := r.Account
	a.ID, a.Credential = r.ID, nil
	if err := a.Check(); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	m.mu.Lock()
	for _, l := range m.logins {
		if l.state == LoginPending && strings.EqualFold(l.account, r.ID) {
			m.mu.Unlock()
			return Record{}, fmt.Errorf("%w: a login for account %q is running (%s)", ErrConflict, l.account, l.id)
		}
	}
	dup := slices.ContainsFunc(m.store.List(), func(s Record) bool { return strings.EqualFold(s.ID, r.ID) })
	if !dup && m.idTaken(r.ID) {
		m.mu.Unlock()
		return Record{}, idTakenError(r.ID)
	}
	rec, err := m.store.Create(r)
	m.mu.Unlock()
	if err != nil {
		return Record{}, err
	}
	log.Printf("credentials: account %q added", rec.ID)
	m.changed()
	return rec, nil
}

func (m *Manager) Remove(id string) error {
	if err := m.store.Delete(id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.renewals, id)
	for _, l := range m.logins {
		if l.account == id && l.state == LoginPending {
			l.cancel()
		}
	}
	m.mu.Unlock()
	if m.onRemove != nil {
		m.onRemove(id)
	}
	m.changed()
	return nil
}
