package cred

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Login states.
const (
	LoginPending   = "pending"
	LoginDone      = "done"
	LoginFailed    = "failed"
	LoginCancelled = "cancelled"
)

// LoginRequest starts a login.
type LoginRequest struct {
	// Account is the account to add: its ID, routing fields and settings. For a new login to an
	// existing account (Reauth) only the ID is read; the account keeps its fields.
	Account route.Account
	// Method is one of the provider's login methods.
	Method string
	// Input is what the user entered when starting the login.
	Input map[string]string
	// Reauth signs in again to an existing account, replacing its credential.
	Reauth bool
}

// LoginView is a login as the management API shows it.
type LoginView struct {
	ID      string           `json:"id"`
	Account string           `json:"account"`
	Method  string           `json:"method"`
	State   string           `json:"state"`
	Prompt  *protocol.Prompt `json:"prompt,omitempty"`
	Error   *llm.Error       `json:"error,omitempty"`
}

type login struct {
	id, account, method string
	reauth              bool
	registration        string // the account's, for a reauth
	fields              route.Account
	call                LoginCall // set under Manager.mu once the call has started
	cancel              context.CancelFunc
	first               chan struct{} // closed after the first frame
	firstOnce           bool

	// Guarded by Manager.mu.
	state  string
	prompt *protocol.Prompt
	err    *llm.Error
	ended  time.Time
}

func (l *login) view() LoginView {
	return LoginView{ID: l.id, Account: l.account, Method: l.method, State: l.state, Prompt: l.prompt, Error: l.err}
}

// notifyFirst marks the first frame seen. Call with Manager.mu held.
func (l *login) notifyFirst() {
	if !l.firstOnce {
		l.firstOnce = true
		close(l.first)
	}
}

// StartLogin starts a login and returns it once the provider has sent its first prompt or
// finished, or ctx ends.
func (m *Manager) StartLogin(ctx context.Context, req LoginRequest) (LoginView, error) {
	id := req.Account.ID
	if !ValidID(id) {
		return LoginView{}, fmt.Errorf("%w: account ID %q is not valid: use letters, digits, '.', '_' and '-' (at most 128)", ErrInvalid, id)
	}
	if m.reserved(id) {
		return LoginView{}, fmt.Errorf("%w: account %q is set in core's config", ErrConflict, id)
	}
	info := m.prov.Info()
	if !slices.Contains(info.Operations, protocol.OpLogin) {
		return LoginView{}, fmt.Errorf("%w: extension %s does not log in", ErrConflict, info.ID)
	}
	if !slices.ContainsFunc(info.LoginMethods, func(lm protocol.LoginMethod) bool { return lm.Method == req.Method }) {
		return LoginView{}, fmt.Errorf("%w: extension %s has no login method %q", ErrInvalid, info.ID, req.Method)
	}
	l := &login{account: id, method: req.Method, reauth: req.Reauth, fields: req.Account, first: make(chan struct{}), state: LoginPending}
	m.mu.Lock()
	m.dropOldLogins()
	for _, o := range m.logins {
		if o.account == id && o.state == LoginPending {
			m.mu.Unlock()
			return LoginView{}, fmt.Errorf("%w: a login for account %q is already running (%s)", ErrConflict, id, o.id)
		}
	}
	rec, exists := m.store.Get(id)
	switch {
	case exists && !req.Reauth:
		m.mu.Unlock()
		return LoginView{}, ErrExists
	case !exists && req.Reauth:
		m.mu.Unlock()
		return LoginView{}, ErrNotFound
	case exists && rec.Extension != m.provID:
		m.mu.Unlock()
		return LoginView{}, fmt.Errorf("%w: account %q belongs to extension %s", ErrConflict, id, rec.Extension)
	case exists:
		l.registration, l.fields = rec.Registration, rec.Account
	}
	m.loginSeq++
	l.id = "login-" + strconv.FormatUint(m.loginSeq, 10)
	lctx, cancel := context.WithTimeout(m.ctx, loginTimeout)
	l.cancel = cancel
	m.logins[l.id] = l
	m.wg.Add(1)
	m.mu.Unlock()

	call := m.prov.Login(lctx, protocol.Account{ID: id, Settings: l.fields.Settings}, protocol.Login{Method: req.Method, Input: req.Input})
	m.mu.Lock()
	l.call = call
	m.mu.Unlock()
	go m.runLogin(l)
	select {
	case <-l.first:
	case <-ctx.Done():
	}
	return m.loginView(l), nil
}

func (m *Manager) loginView(l *login) LoginView {
	m.mu.Lock()
	defer m.mu.Unlock()
	return l.view()
}

func (m *Manager) runLogin(l *login) {
	defer m.wg.Done()
	defer l.cancel()
	defer l.call.Close()
	for {
		r := l.call.Next()
		switch r.Type {
		case protocol.TypeLoginPrompt:
			m.mu.Lock()
			l.prompt = r.Prompt
			l.notifyFirst()
			m.mu.Unlock()
			continue
		case protocol.TypeLoginDone:
			err := m.finishLogin(l, r.Grant)
			m.mu.Lock()
			if err != nil {
				l.state, l.err = LoginFailed, &llm.Error{Code: llm.CodeInternal, Message: err.Error()}
				if errors.Is(err, ErrExists) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStale) {
					l.err.Code = llm.CodeInvalidRequest
				}
			} else {
				l.state = LoginDone
			}
		default:
			m.mu.Lock()
			l.state, l.err = LoginFailed, r.Error
			if r.Error != nil && r.Error.Code == llm.CodeCancelled {
				l.state = LoginCancelled
			}
		}
		l.prompt = nil
		l.ended = m.now()
		l.notifyFirst()
		m.mu.Unlock()
		return
	}
}

// finishLogin stores a login's credential: a new account, or the new credential of an account
// signed in to again (which also clears a refused grant and fences off any renewal of the old
// credential still running).
func (m *Manager) finishLogin(l *login, g *protocol.Grant) error {
	if l.reauth {
		for {
			cur, ok := m.store.Get(l.account)
			if !ok || cur.Registration != l.registration {
				return fmt.Errorf("account %q was removed during the login", l.account)
			}
			_, err := m.store.Update(l.account, cur.Registration, cur.Version, func(r *Record) {
				r.Credential = *g.Credential
				r.State, r.Reason = StateActive, ""
				if g.Label != "" {
					r.Label = g.Label
				}
			})
			if errors.Is(err, ErrStale) {
				continue
			}
			if err != nil {
				return err
			}
			m.mu.Lock()
			delete(m.renewals, l.account)
			m.mu.Unlock()
			log.Printf("credentials: account %q signed in again", l.account)
			m.changed()
			return nil
		}
	}
	if m.reserved(l.account) {
		return fmt.Errorf("account %q is now set in core's config", l.account)
	}
	_, err := m.store.Create(Record{ID: l.account, Extension: m.provID, State: StateActive, Label: g.Label, Account: l.fields, Credential: *g.Credential})
	if err != nil {
		return err
	}
	log.Printf("credentials: account %q added", l.account)
	m.changed()
	return nil
}

// Login returns login id.
func (m *Manager) Login(id string) (LoginView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logins[id]
	if !ok {
		return LoginView{}, false
	}
	return l.view(), true
}

// LoginInput passes what the user entered to a running login.
func (m *Manager) LoginInput(id string, input map[string]string) error {
	m.mu.Lock()
	l, ok := m.logins[id]
	var call LoginCall
	pending := ok && l.state == LoginPending
	if ok {
		call = l.call
	}
	m.mu.Unlock()
	switch {
	case !ok:
		return ErrNotFound
	case !pending:
		return fmt.Errorf("%w: login %s has finished", ErrConflict, id)
	case call == nil:
		return fmt.Errorf("%w: login %s is still starting; try again", ErrConflict, id)
	}
	return call.Send(input)
}

// CancelLogin cancels a running login.
func (m *Manager) CancelLogin(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logins[id]
	if !ok {
		return ErrNotFound
	}
	l.cancel()
	return nil
}

// dropOldLogins forgets logins that finished more than loginKeep ago. Call with m.mu held.
func (m *Manager) dropOldLogins() {
	for id, l := range m.logins {
		if l.state != LoginPending && m.now().Sub(l.ended) > loginKeep {
			delete(m.logins, id)
		}
	}
}
