package cred

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Providers are the provider extensions accounts log in and renew through, by extension ID.
type Providers interface {
	// Provider returns the extension with ID id while it runs, else nil. Each call on it goes
	// to one version of the extension, the one current when the call starts.
	Provider(id string) Provider
	// Running returns the IDs of the extensions that run now.
	Running() []string
}

// Provider is one provider extension accounts log in and renew through.
type Provider interface {
	Info() protocol.Info
	Login(ctx context.Context, account protocol.Account, login protocol.Login) LoginCall
	Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error)
}

// LoginCall is a running login call on the provider (host.LoginCall).
type LoginCall interface {
	Next() protocol.Reply
	Send(input map[string]string) error
	Close()
}

// Renewal timing.
var (
	// refreshLead: without a refresh_at, a credential is renewed when it expires within this
	// long.
	refreshLead = 5 * time.Minute
	// After a renewal that failed in a way that may pass, automatic renewal waits this long
	// (or as long as the provider said), as today's core does.
	failedBackoff = 5 * time.Minute
	// After a renewal that gave a credential already due for renewal again, automatic renewal
	// waits this long, so a provider issuing short-lived tokens is not asked on every request.
	ineffectiveBackoff = 30 * time.Second
	// A credential renewed this recently and refused again by the upstream is not renewed
	// again: the refusal is not about its age.
	renewedRecently = time.Minute
	// loginTimeout bounds a login that waits for a person: the named liveness limit on login
	// calls.
	loginTimeout = 15 * time.Minute
	// loginKeep is how long a finished login stays visible to the management API.
	loginKeep = 10 * time.Minute
)

// Errors from Manager besides the Store's.
var (
	// ErrInvalid: the request is malformed.
	ErrInvalid = errors.New("invalid request")
	// ErrConflict: the request clashes with the current state.
	ErrConflict = errors.New("conflict")
)

// Manager is core's credential authority: it hands credentials to routing, renews them (one
// renewal per credential at a time, shared by every caller, applied only to the version it
// renewed) through the extension that owns the account, and runs logins. It is safe for
// concurrent use.
type Manager struct {
	store    *Store
	provs    Providers
	reserved func(id string) bool
	now      func() time.Time

	// ctx outlives every caller: renewals and logins run on it, so a client that gives up
	// waiting never cancels a renewal halfway (the provider may already have rotated the
	// refresh token, RFC 9700 section 4.14). Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu sync.Mutex
	// flights is the per-credential refresh lock: at most one renewal per account runs, and
	// callers that need the same version renewed wait for it.
	flights  map[string]*flight
	renewals map[string]*renewal
	logins   map[string]*login
	loginSeq uint64
	onChange func()
}

type flight struct {
	version uint64
	done    chan struct{}
	rec     Record
	err     *llm.Error
}

// renewal is what the last renewal of an account left behind. It is about one credential
// (reg, cred): once the account holds another, it no longer applies.
type renewal struct {
	reg  string
	cred protocol.Credential
	// refused: the provider refused this credential's grant, and recording that failed.
	refused bool
	at      time.Time  // when it ended
	until   time.Time  // no automatic renewal before this
	err     *llm.Error // the failure that set until, if it failed
}

// Options configure a Manager.
type Options struct {
	// Reserved reports IDs that core's config already uses, which logins may not take.
	Reserved func(id string) bool
}

// NewManager returns the manager for store's accounts on provs.
func NewManager(store *Store, provs Providers, opts Options) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{store: store, provs: provs, reserved: opts.Reserved,
		now: time.Now, ctx: ctx, cancel: cancel,
		flights: map[string]*flight{}, renewals: map[string]*renewal{}, logins: map[string]*login{}}
	if m.reserved == nil {
		m.reserved = func(string) bool { return false }
	}
	return m
}

// OnChange sets f to be called after the set of usable accounts may have changed.
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

// Close cancels running renewals and logins and waits for them to end.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

// Accounts returns the usable accounts, for routing, each with the extension it belongs to.
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

// Records returns every stored account, including unusable ones.
func (m *Manager) Records() []Record { return m.store.List() }

// LastRenewalError returns why the last automatic renewal of r's credential failed, while
// automatic renewal is waiting because of it.
func (m *Manager) LastRenewalError(r Record) *llm.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rn := m.renewalFor(r); rn != nil && rn.err != nil && rn.until.After(m.now()) {
		return rn.err
	}
	return nil
}

// renewalFor returns what the last renewal of r's current credential left behind, or nil.
// Call with m.mu held.
func (m *Manager) renewalFor(r Record) *renewal {
	rn := m.renewals[r.ID]
	if rn == nil || rn.reg != r.Registration || !sameCredential(rn.cred, r.Credential) {
		return nil
	}
	return rn
}

// Refreshable reports whether core renews r's credential: it is not a static key, and the
// extension it belongs to runs and can refresh.
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

// Credential returns the credential an attempt on account id presents to extension, renewing
// it first when it expires within refreshLead. An account of another extension is refused:
// its credential never reaches extension.
func (m *Manager) Credential(ctx context.Context, extension, id string) (*protocol.Credential, uint64, *llm.Error) {
	rec, ok := m.store.Get(id)
	if !ok || rec.Extension != extension {
		return nil, 0, credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q of extension %s is not in the credential store", id, extension))
	}
	if !rec.Usable() {
		return nil, 0, credentialError(llm.CodeAuth, fmt.Sprintf("account %q is %s", id, describeState(rec)))
	}
	m.mu.Lock()
	rn := m.renewalFor(rec)
	m.mu.Unlock()
	if rn != nil && rn.refused {
		return nil, 0, credentialError(llm.CodeAuth, fmt.Sprintf("account %q: the provider refused the grant; it needs a new login", id))
	}
	now := m.now()
	expired := rec.Credential.ExpiresAt != 0 && rec.Credential.ExpiresAt <= now.Unix()
	if !m.due(rec) && expired && m.Refreshable(rec) {
		// Renewal is waiting after a failure, and the credential no longer works.
		msg := fmt.Sprintf("account %q: the credential has expired and renewal is waiting after a failure", id)
		if last := m.LastRenewalError(rec); last != nil {
			msg += ": " + last.Message
		}
		out := credentialError(llm.CodeUnavailable, msg)
		if until := m.retryAt(rec); until.After(now) {
			out.RetryAfterMS = until.Sub(now).Milliseconds()
		}
		return nil, 0, out
	}
	if m.due(rec) {
		r, e := m.renew(ctx, rec, false)
		now = m.now()
		switch {
		case e == nil:
			rec = r
			if !rec.Usable() {
				return nil, 0, credentialError(llm.CodeAuth, fmt.Sprintf("account %q is %s", id, describeState(rec)))
			}
		case e.Code == llm.CodeCancelled && ctx.Err() != nil:
			return nil, 0, &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
		case rec.Credential.ExpiresAt > now.Unix() && !rejectedGrant(e):
			// Renewal failed but the credential still works for now: use it.
		default:
			out := credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q: the credential has expired and renewing it failed: %s", id, e.Message))
			if rejectedGrant(e) {
				out.Code = llm.CodeAuth
			} else if until := m.retryAt(rec); until.After(now) {
				out.RetryAfterMS = until.Sub(now).Milliseconds()
			}
			return nil, 0, out
		}
	}
	c := rec.Credential
	return &c, rec.Version, nil
}

// Rejected renews account id's credential after the upstream refused version, and reports
// whether a newer usable one is ready.
func (m *Manager) Rejected(ctx context.Context, id string, version uint64) bool {
	rec, ok := m.store.Get(id)
	if !ok || !m.Refreshable(rec) {
		return false
	}
	if rec.Version != version {
		return rec.Usable() // renewed (or replaced) since that attempt began
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

// Refresh renews account id's credential now, whatever its expiry and backoff.
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

// renewAt returns when c should be renewed, in Unix seconds; 0 for never.
func renewAt(c protocol.Credential) int64 {
	switch {
	case c.RefreshAt != 0:
		return c.RefreshAt
	case c.ExpiresAt != 0:
		return c.ExpiresAt - int64(refreshLead/time.Second)
	}
	return 0
}

// due reports whether rec's credential should be renewed before use.
func (m *Manager) due(rec Record) bool {
	at := renewAt(rec.Credential)
	if at == 0 || !m.Refreshable(rec) {
		return false
	}
	now := m.now()
	if now.Unix() < at {
		return false
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

// renew renews seen's credential, or waits for the renewal of that version already running.
// When the record has moved past seen's version, it returns the current record: someone else
// renewed or replaced the credential. force skips the backoff after earlier failures.
func (m *Manager) renew(ctx context.Context, seen Record, force bool) (Record, *llm.Error) {
	for {
		m.mu.Lock()
		cur, ok := m.store.Get(seen.ID)
		if !ok || cur.Registration != seen.Registration {
			m.mu.Unlock()
			return Record{}, credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q was removed", seen.ID))
		}
		if cur.Version != seen.Version {
			m.mu.Unlock()
			return cur, nil
		}
		f := m.flights[seen.ID]
		if f != nil && f.version != cur.Version {
			// A renewal of an older version is finishing; wait for it, then look again.
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

// runRenewal is one renewal: it holds the account's refresh lock (its flight) from start to
// finish.
func (m *Manager) runRenewal(seen Record, f *flight) {
	defer m.wg.Done()
	var rec Record
	var e *llm.Error
	// Read the record again now the lock is held: the caller's copy may already be out of
	// date.
	cur, ok := m.store.Get(seen.ID)
	switch {
	case !ok || cur.Registration != seen.Registration:
		e = credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q was removed", seen.ID))
	case cur.Version != seen.Version:
		rec = cur
	default:
		p := m.provs.Provider(cur.Extension)
		if p == nil {
			rec, e = cur, credentialError(llm.CodeExtensionUnavailable, fmt.Sprintf("account %q: extension %s does not run", cur.ID, cur.Extension))
			break
		}
		acct := protocol.Account{ID: cur.ID, Settings: cur.Account.Settings, Credential: &cur.Credential}
		cred, perr := p.Refresh(m.ctx, acct)
		rec, e = m.apply(cur, cred, perr)
	}
	m.mu.Lock()
	f.rec, f.err = rec, e
	delete(m.flights, seen.ID)
	m.mu.Unlock()
	close(f.done)
}

// rejectedGrant reports whether a refresh failed because the provider refused the grant
// itself: only a new login helps.
func rejectedGrant(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeAuth && e.Scope == llm.ScopeCredential && !e.Retryable
}

// apply records the outcome of renewing cur's credential. A new credential is written only
// if the record is still at cur's registration and version; otherwise the result is refused
// and the newer record stands.
func (m *Manager) apply(cur Record, cred *protocol.Credential, perr *llm.Error) (Record, *llm.Error) {
	now := m.now()
	switch {
	case perr == nil:
		next, err := m.updateRenewed(cur, func(r *Record) {
			r.Credential = *cred
			r.State, r.Reason = StateActive, ""
		})
		switch {
		case errors.Is(err, ErrNotFound):
			log.Printf("credentials: account %q: refused a renewed credential: the account was removed while it was being renewed", cur.ID)
			return Record{}, credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q was removed", cur.ID))
		case errors.Is(err, ErrStale):
			log.Printf("credentials: account %q: refused a renewed credential: the account's credential was replaced while it was being renewed", cur.ID)
			return next, nil
		case err != nil:
			log.Printf("credentials: account %q: saving the renewed credential failed: %v", cur.ID, err)
			return cur, credentialError(llm.CodeInternal, fmt.Sprintf("account %q: saving the renewed credential failed", cur.ID))
		}
		rn := &renewal{at: now}
		if at := renewAt(*cred); at != 0 && now.Unix() >= at {
			// Ineffective: the new credential is already due for renewal again.
			rn.until = now.Add(ineffectiveBackoff)
		}
		m.setRenewal(next, rn)
		return next, nil
	case rejectedGrant(perr):
		next, err := m.updateRenewed(cur, func(r *Record) {
			r.State, r.Reason = StateReauth, perr.Message
		})
		switch {
		case errors.Is(err, ErrNotFound):
			return Record{}, credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q was removed", cur.ID))
		case errors.Is(err, ErrStale):
			// The credential was replaced meanwhile (a new login): the refusal was about the old one.
			return next, nil
		case err != nil:
			log.Printf("credentials: account %q: the provider refused the grant, and saving that failed: %v", cur.ID, err)
			m.setRenewal(cur, &renewal{at: now, until: now.Add(failedBackoff), err: perr, refused: true})
			return cur, perr
		}
		log.Printf("credentials: account %q needs a new login: %s", cur.ID, perr.Message)
		m.setRenewal(next, &renewal{at: now, err: perr})
		m.changed()
		return next, perr
	case perr.Code == llm.CodeCancelled:
		return cur, perr
	default:
		if latest, ok := m.store.Get(cur.ID); !ok {
			return Record{}, credentialError(llm.CodeUnavailable, fmt.Sprintf("account %q was removed", cur.ID))
		} else if latest.Registration != cur.Registration || !sameCredential(latest.Credential, cur.Credential) {
			// The failure was about a credential that has since been replaced: it says
			// nothing about the new one.
			return latest, nil
		}
		wait := failedBackoff
		if perr.RetryAfterMS > 0 {
			wait = time.Duration(perr.RetryAfterMS) * time.Millisecond
		}
		log.Printf("credentials: account %q: renewing the credential failed (next try in %s): %s", cur.ID, wait.Round(time.Second), perr.Message)
		m.setRenewal(cur, &renewal{at: now, until: now.Add(wait), err: perr})
		return cur, perr
	}
}

// updateRenewed applies change to the account renewed from cur, if the account still holds
// cur's credential under cur's registration. Changes to anything else made meanwhile (a
// disable, a label) do not refuse it: change is applied on top of them. A replaced credential
// returns ErrStale with the current record; a removed account, ErrNotFound.
func (m *Manager) updateRenewed(cur Record, change func(*Record)) (Record, error) {
	base := cur
	for {
		next, err := m.store.Update(base.ID, base.Registration, base.Version, change)
		if !errors.Is(err, ErrStale) {
			return next, err
		}
		if next.Registration != cur.Registration || !sameCredential(next.Credential, cur.Credential) {
			return next, ErrStale
		}
		base = next
	}
}

// sameCredential reports whether a and b are the same credential.
func sameCredential(a, b protocol.Credential) bool {
	return reflect.DeepEqual(a, b)
}

// setRenewal records what a renewal left behind for r's credential.
func (m *Manager) setRenewal(r Record, rn *renewal) {
	rn.reg, rn.cred = r.Registration, r.Credential
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

// SetDisabled disables or enables account id. Enabling does not clear a refused grant.
func (m *Manager) SetDisabled(id string, disabled bool) (Record, error) {
	for {
		cur, ok := m.store.Get(id)
		if !ok {
			return Record{}, ErrNotFound
		}
		next, err := m.store.Update(id, cur.Registration, cur.Version, func(r *Record) { r.Disabled = disabled })
		if errors.Is(err, ErrStale) {
			continue // a renewal landed meanwhile; apply the change to the new version
		}
		if err == nil {
			m.changed()
		}
		return next, err
	}
}

// Remove removes account id and cancels a login running for it. A renewal still running for
// it finds the account gone and its result is refused.
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
	m.changed()
	return nil
}
