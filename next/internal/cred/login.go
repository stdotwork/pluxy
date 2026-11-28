package cred

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const (
	LoginPending   = "pending"
	LoginDone      = "done"
	LoginFailed    = "failed"
	LoginCancelled = "cancelled"
)

type LoginRequest struct {
	Account      route.Account
	Extension    string
	Label        string
	Registration string
	Method       string
	Input        map[string]string
	Reauth       bool
}

type LoginView struct {
	ID        string           `json:"id"`
	Account   string           `json:"account"`
	Extension string           `json:"extension"`
	Method    string           `json:"method"`
	State     string           `json:"state"`
	Prompt    *protocol.Prompt `json:"prompt,omitempty"`
	Error     *llm.Error       `json:"error,omitempty"`
}

type login struct {
	id, account, method string
	extension           string
	reauth              bool
	registration        string
	label               string
	fields              route.Account
	call                LoginCall
	cancel              context.CancelFunc
	first               chan struct{}
	firstOnce           bool

	state  string
	prompt *protocol.Prompt
	err    *llm.Error
	ended  time.Time
}

func (l *login) view() LoginView {
	return LoginView{ID: l.id, Account: l.account, Extension: l.extension, Method: l.method, State: l.state, Prompt: l.prompt, Error: l.err}
}

func (l *login) notifyFirst() {
	if !l.firstOnce {
		l.firstOnce = true
		close(l.first)
	}
}

func (m *Manager) StartLogin(ctx context.Context, req LoginRequest) (LoginView, error) {
	id := req.Account.ID
	fresh := id == "" && !req.Reauth
	if !fresh && !ValidID(id) {
		return LoginView{}, fmt.Errorf("%w: account ID %q is not valid: use letters, digits, '.', '_' and '-' (at most 128)", ErrInvalid, id)
	}
	if !fresh && m.reserved(id) {
		return LoginView{}, fmt.Errorf("%w: account %q is set in core's config", ErrConflict, id)
	}
	ext := req.Extension
	if rec, ok := m.store.Get(id); ok && req.Reauth && ext == "" {
		ext = rec.Extension
	}
	if ext == "" {
		running := m.provs.Running()
		switch len(running) {
		case 0:
			return LoginView{}, fmt.Errorf("%w: no extension runs to log in with", ErrConflict)
		case 1:
			ext = running[0]
		default:
			return LoginView{}, fmt.Errorf("%w: name the extension to log in with (extension): %s run", ErrInvalid, strings.Join(running, ", "))
		}
	}
	prov := m.provs.Provider(ext)
	if prov == nil {
		return LoginView{}, fmt.Errorf("%w: extension %s does not run", ErrConflict, ext)
	}
	info := prov.Info()
	if !slices.Contains(info.Operations, protocol.OpLogin) {
		return LoginView{}, fmt.Errorf("%w: extension %s does not log in", ErrConflict, info.ID)
	}
	if !slices.ContainsFunc(info.LoginMethods, func(lm protocol.LoginMethod) bool { return lm.Method == req.Method }) {
		return LoginView{}, fmt.Errorf("%w: extension %s has no login method %q", ErrInvalid, info.ID, req.Method)
	}
	l := &login{account: id, extension: ext, method: req.Method, reauth: req.Reauth, fields: req.Account, label: req.Label, first: make(chan struct{}), state: LoginPending}
	m.mu.Lock()
	m.dropOldLogins()
	if fresh {
		id = m.newAccountID()
		l.account, l.fields.ID = id, id
	}
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
	case exists && rec.Extension != ext:
		m.mu.Unlock()
		return LoginView{}, fmt.Errorf("%w: account %q belongs to extension %s", ErrConflict, id, rec.Extension)
	case exists && req.Registration != "" && rec.Registration != req.Registration:
		m.mu.Unlock()
		return LoginView{}, fmt.Errorf("%w: account %q was removed and added again", ErrConflict, id)
	case exists:
		l.registration, l.fields = rec.Registration, rec.Account
	case !fresh && m.idTaken(id):
		m.mu.Unlock()
		return LoginView{}, idTakenError(id)
	}
	m.loginSeq++
	l.id = "login-" + strconv.FormatUint(m.loginSeq, 10)
	lctx, cancel := context.WithTimeout(m.ctx, LoginTimeout)
	l.cancel = cancel
	m.logins[l.id] = l
	m.wg.Add(1)
	m.mu.Unlock()

	call := prov.Login(lctx, protocol.Account{ID: id, Settings: l.fields.Settings, ProxyURL: l.fields.ProxyURL, Headers: l.fields.Headers}, protocol.Login{Method: req.Method, Input: req.Input})
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
				if errors.Is(err, ErrExists) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStale) || errors.Is(err, ErrIdentity) ||
					errors.Is(err, ErrConflict) {
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

func (m *Manager) finishLogin(l *login, g *protocol.Grant) error {
	if l.reauth {
		for {
			cur, ok := m.store.Get(l.account)
			if !ok || cur.Registration != l.registration {
				return fmt.Errorf("account %q was removed during the login", l.account)
			}
			if err := sameIdentity(cur, g); err != nil {
				log.Printf("credentials: new login to account %q refused: %v", l.account, err)
				return err
			}
			_, err := m.store.Update(l.account, cur.Registration, cur.Version, func(r *Record) {
				r.Credential = *g.Credential
				r.State, r.Reason = StateActive, ""
				if g.Label != "" {
					if !r.Named {
						r.Label = g.Label
					}
					r.ProviderLabel = g.Label
				}
				if len(g.Identity) > 0 {
					r.Identity = g.Identity
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
			if m.onReauth != nil {
				m.onReauth(l.account)
			}
			m.changed()
			m.credentialChanged(l.account)
			return nil
		}
	}
	if m.reserved(l.account) {
		return fmt.Errorf("%w: account %q is now set in core's config", ErrConflict, l.account)
	}
	if m.configTaken(l.account) {
		return fmt.Errorf("%w: account ID %q or its public ID is now an account's ID or public ID in core's config; choose another account ID", ErrConflict, l.account)
	}
	label := l.label
	if label == "" {
		label = g.Label
	}
	fields := l.fields
	if len(g.Settings) > 0 {
		var defaults, explicit map[string]json.RawMessage
		_ = json.Unmarshal(g.Settings, &defaults)
		_ = json.Unmarshal(fields.Settings, &explicit)
		maps.Copy(defaults, explicit)
		fields.Settings, _ = json.Marshal(defaults)
	}
	_, err := m.store.Create(Record{ID: l.account, Extension: l.extension, State: StateActive, Label: label, Named: l.label != "", ProviderLabel: g.Label,
		Identity: g.Identity, Account: fields, Credential: *g.Credential})
	if err != nil {
		return err
	}
	log.Printf("credentials: account %q added", l.account)
	m.changed()
	return nil
}

func sameIdentity(cur Record, g *protocol.Grant) error {
	if len(cur.Identity) > 0 {
		for _, k := range slices.Sorted(maps.Keys(cur.Identity)) {
			if v, ok := g.Identity[k]; !ok || !strings.EqualFold(v, cur.Identity[k]) {
				return fmt.Errorf("%w (its %s differs from account %q's); to use the other account, add it as a new one", ErrIdentity, k, cur.ID)
			}
		}
		return nil
	}
	if want := cur.identityLabel(); want != "" && g.Label != "" && !strings.EqualFold(want, g.Label) {
		return fmt.Errorf("%w (its label differs from account %q's); to use the other account, add it as a new one", ErrIdentity, cur.ID)
	}
	return nil
}

func (m *Manager) newAccountID() string {
	b := make([]byte, 4)
	for {
		if _, err := io.ReadFull(m.random, b); err != nil {
			panic("cred: reading random bytes: " + err.Error())
		}
		if id := "acc_" + hex.EncodeToString(b); !m.idTaken(id) {
			return id
		}
	}
}

func (m *Manager) publicIDOf(id string) string {
	if m.publicID == nil {
		return id
	}
	return m.publicID(id)
}

func (m *Manager) configTaken(id string) bool {
	return m.taken != nil && (m.taken(id) || m.taken(m.publicIDOf(id)))
}

func (m *Manager) idTaken(id string) bool {
	if m.configTaken(id) {
		return true
	}
	pub := m.publicIDOf(id)
	clash := func(k string) bool {
		pk := m.publicIDOf(k)
		return strings.EqualFold(k, id) || strings.EqualFold(k, pub) || pk == id || pk == pub
	}
	if slices.ContainsFunc(m.store.List(), func(r Record) bool { return clash(r.ID) }) ||
		slices.ContainsFunc(m.store.Broken(), func(b Broken) bool { return clash(b.ID) }) {
		return true
	}
	for _, l := range m.logins {
		if l.state == LoginPending && clash(l.account) {
			return true
		}
	}
	return false
}

func idTakenError(id string) error {
	return fmt.Errorf("%w: account ID %q or its public ID is another account's ID or public ID; choose another account ID", ErrConflict, id)
}

func (m *Manager) Login(id string) (LoginView, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.logins[id]
	if !ok {
		return LoginView{}, false
	}
	return l.view(), true
}

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
	if err := call.Send(input); err != nil {
		if errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("%w: login %s has finished", ErrConflict, id)
		}
		return err
	}
	return nil
}

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

func (m *Manager) dropOldLogins() {
	for id, l := range m.logins {
		if l.state != LoginPending && m.now().Sub(l.ended) > loginKeep {
			delete(m.logins, id)
		}
	}
}
