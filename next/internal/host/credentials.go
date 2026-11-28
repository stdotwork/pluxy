package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func (e *Extension) Login(ctx context.Context, account protocol.Account, login protocol.Login) *LoginCall {
	account.Credential = nil
	l := &LoginCall{ext: e, ctx: ctx, account: account.ID, settings: e.holdSettings(account), secretInputs: map[string]bool{}}
	for _, m := range e.info.LoginMethods {
		if m.Method == login.Method {
			l.markSecret(m.Inputs)
		}
	}
	l.addSecrets(login.Input)
	proxy, perr := e.outbound(&account, nil)
	if perr != nil {
		l.fail(perr)
		return l
	}
	if proxy != nil {
		l.addForms(outboundSecrets(proxy))
	}
	l.pieces = l.pieces.add(e.accountPieces(account))
	conn, lerr := e.dial(ctx)
	if lerr != nil {
		l.fail(lerr)
		return l
	}
	l.conn = conn
	l.stop = context.AfterFunc(ctx, func() { conn.Close() })
	l.release = e.out.hold()
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpLogin, Account: &account, Login: &login}
	if err := protocol.WriteFrame(conn, call); err != nil {
		l.fail(l.readError(err))
	}
	return l
}

type LoginCall struct {
	ext     *Extension
	ctx     context.Context
	account string
	conn    net.Conn
	stop    func() bool

	wmu      sync.Mutex
	secrets  map[string]string
	settings *protocol.Credential
	redact   *protocol.Redactor
	final    *protocol.Reply
	onClose  func()
	release  func()

	pieces       pieces
	secretInputs map[string]bool

	rmu     sync.Mutex
	reading bool
	closed  bool
}

func (l *LoginCall) endRelease(closing bool) {
	l.rmu.Lock()
	if closing {
		l.closed = true
	} else {
		l.reading = false
	}
	ready := l.closed && !l.reading
	l.rmu.Unlock()
	if ready && l.release != nil {
		l.release()
	}
}

func (l *LoginCall) markSecret(inputs []protocol.LoginInput) {
	for _, in := range inputs {
		if in.Secret {
			l.secretInputs[in.Name] = true
		}
	}
}

func (l *LoginCall) addSecrets(input map[string]string) {
	l.add(input, true)
}

func (l *LoginCall) addForms(forms map[string]string) {
	l.add(forms, false)
}

func (l *LoginCall) add(input map[string]string, typed bool) {
	var found pieces
	for k, v := range input {
		var obj map[string]json.RawMessage
		switch {
		case l.secretInputs[k] && json.Unmarshal([]byte(v), &obj) == nil:
			found = append(found, l.ext.accountPieces(protocol.Account{Settings: json.RawMessage(v)})...)
		case l.secretInputs[k]:
			found = append(found, protocol.AccountSecrets(protocol.Account{Credential: &protocol.Credential{APIKey: v}})...)
		default:
			found = append(found, protocol.AccountSecretsWith(protocol.Account{}, urlOnly(v))...)
		}
	}
	l.pieces = l.pieces.add(found)
	for k, v := range input {
		if l.secrets == nil {
			l.secrets = map[string]string{}
		}
		whole := []string{v}
		if typed {
			whole = wholeSecrets(v, l.secretInputs[k])
		}
		for _, s := range whole {
			l.secrets[fmt.Sprintf("%s.%d", k, len(l.secrets))] = s
		}
	}
	l.redact = protocol.NewRedactor(inputCredential(l.secrets), l.settings)
	l.ext.held.add(l.account+" login input", inputCredential(l.secrets))
}

func (l *LoginCall) redactor() *protocol.Redactor {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.redact
}

func (l *LoginCall) errorPieces() pieces {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return slices.Clone(l.pieces)
}

const pathChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~/"

const minBareName = 8

// No "@", ";", "%", "," or a second address that Go reads as part of the path or host, which would
// hide a password.
func wholeSecrets(v string, secret bool) []string {
	if secret {
		return []string{v}
	}
	t := strings.TrimSpace(v)
	u, err := url.Parse(t)
	if err != nil || u.Host == "" || u.Opaque != "" || u.Scheme != "http" && u.Scheme != "https" ||
		strings.ContainsAny(t, "?#") || strings.IndexFunc(t, unicode.IsSpace) >= 0 ||
		strings.Trim(u.EscapedPath(), pathChars) != "" ||
		strings.Trim(u.Host, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-.:[]") != "" {
		return []string{v}
	}
	if u.User == nil {
		return nil
	}
	authority := t[strings.Index(t, "://")+3:]
	authority, _, _ = strings.Cut(authority, "/")
	name, p, hasPassword := u.User.Username(), "", false
	if p, hasPassword = u.User.Password(); p == "" {
		hasPassword = false
	}
	forms := []string{authority[:strings.LastIndexByte(authority, '@')], u.User.String(), name}
	var out []string
	for _, f := range forms {
		if len(f) >= minBareName || hasPassword && f != name {
			out = append(out, f)
		}
	}
	if hasPassword {
		out = append(out, p, name+":"+p)
	}
	return out
}

func urlOnly(s string) []string {
	if u, err := url.Parse(strings.TrimSpace(s)); err == nil && u.Host != "" && strings.Contains(s, "://") {
		return []string{s}
	}
	return nil
}

func (l *LoginCall) Next() protocol.Reply {
	l.rmu.Lock()
	l.reading = true
	l.rmu.Unlock()
	defer l.endRelease(false)
	if l.final != nil {
		return *l.final
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(l.conn, &r); err != nil {
		l.fail(l.readError(err))
		return *l.final
	}
	switch {
	case r.Type == protocol.TypeLoginPrompt && r.Prompt != nil:
		l.wmu.Lock()
		l.markSecret(r.Prompt.Inputs)
		l.wmu.Unlock()
		p := redactPrompt(l.redactor(), *r.Prompt)
		return protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &p}
	case r.Type == protocol.TypeLoginDone && validGrant(r.Grant):
		l.ext.held.add(l.account, r.Grant.Credential)
		grant := protocol.NewRedactor(r.Grant.Credential)
		redact := func(s string) string { return l.redactor().String(grant.String(s)) }
		if err := validIdentity(r.Grant.Identity, redact); err != "" {
			l.fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a grant whose identity %s", l.ext.manifest.ID, err)})
			return *l.final
		}
		if err := validGrantSettings(r.Grant.Settings); err != "" {
			l.fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a grant whose settings %s", l.ext.manifest.ID, err)})
			return *l.final
		}
		if bytes.Equal(bytes.TrimSpace(r.Grant.Settings), []byte("null")) {
			r.Grant.Settings = nil
		}
		done := protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone}, Grant: r.Grant}
		l.final = &done
		l.Close()
		return done
	case r.Type == llm.EventError && r.Error != nil:
		ev := l.ext.normalizeEvent(l.errorPieces().event(l.redactor().Event(r.Event)))
		l.final = &protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: ev.Error}}
		l.Close()
		return *l.final
	}
	l.fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent %q during a login", l.ext.manifest.ID, l.redactor().String(r.Type))})
	return *l.final
}

func (l *LoginCall) Send(input map[string]string) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.conn == nil {
		return fmt.Errorf("the login is not running: %w", net.ErrClosed)
	}
	l.addSecrets(input)
	err := protocol.WriteFrame(l.conn, protocol.Call{Type: protocol.TypeLoginInput, Input: input})
	if errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("the login has ended: %w", err)
	}
	return err
}

func (l *LoginCall) Close() {
	if l.onClose != nil {
		defer l.onClose()
	}
	if l.stop != nil {
		l.stop()
	}
	if l.conn != nil {
		l.conn.Close()
	}
	l.endRelease(true)
}

func (l *LoginCall) fail(e *llm.Error) {
	l.final = &protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: e}}
	l.Close()
}

func (l *LoginCall) readError(err error) *llm.Error {
	return callError(l.ctx, l.ext, l.redactor(), err)
}

func (e *Extension) Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error) {
	proxy, perr := e.outbound(&account, nil)
	if perr != nil {
		return nil, perr
	}
	redact := protocol.NewRedactor(append(e.holdAccount(account), proxy)...)
	conn, lerr := e.dial(ctx)
	if lerr != nil {
		return nil, lerr
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer e.out.hold()()
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpRefresh, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	switch {
	case r.Type == protocol.TypeRefreshDone && validGrant(r.Grant):
		e.held.add(account.ID, r.Grant.Credential)
		return r.Grant.Credential, nil
	case r.Type == llm.EventError && r.Error != nil:
		return nil, e.normalizeEvent(e.accountPieces(account).event(redact.Event(r.Event))).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a refresh", e.manifest.ID, redact.String(r.Type))}
}

func validIdentity(id map[string]string, redact func(string) string) string {
	if len(id) > protocol.MaxIdentityFields {
		return fmt.Sprintf("has more than %d fields", protocol.MaxIdentityFields)
	}
	for k, v := range id {
		switch {
		case k == "" || len(k) > protocol.MaxIdentityKey:
			return fmt.Sprintf("has a field name that is empty or longer than %d bytes", protocol.MaxIdentityKey)
		case v == "" || len(v) > protocol.MaxIdentityValue:
			return fmt.Sprintf("field %q is empty or longer than %d bytes", redact(k), protocol.MaxIdentityValue)
		}
	}
	return ""
}

func validGrant(g *protocol.Grant) bool {
	return g != nil && g.Credential != nil && g.Credential.Kind != ""
}

func (e *Extension) dial(ctx context.Context) (net.Conn, *llm.Error) {
	select {
	case <-e.exited:
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s is not running", e.manifest.ID)}
	default:
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", e.socket)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &llm.Error{Code: llm.CodeCancelled, Message: "the call was cancelled"}
		}
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s is not reachable: %v", e.manifest.ID, err)}
	}
	return conn, nil
}

func callError(ctx context.Context, e *Extension, redact *protocol.Redactor, err error) *llm.Error {
	if ctx != nil && ctx.Err() != nil {
		return &llm.Error{Code: llm.CodeCancelled, Message: "the call was cancelled"}
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.Is(err, protocol.ErrFrameTooLarge) || errors.As(err, &se) || errors.As(err, &te) {
		return redacted(redact, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a bad frame: %v", e.manifest.ID, err)})
	}
	return &llm.Error{Code: llm.CodeExtensionCrashed, Message: fmt.Sprintf("extension %s ended the call without finishing it", e.manifest.ID)}
}

func inputCredential(input map[string]string) *protocol.Credential {
	if len(input) == 0 {
		return nil
	}
	data, _ := json.Marshal(input)
	return &protocol.Credential{Kind: "input", Data: data}
}

func secretsCredential(secrets []string) *protocol.Credential {
	if len(secrets) == 0 {
		return nil
	}
	data, _ := json.Marshal(secrets)
	return &protocol.Credential{Kind: "settings", Data: data}
}

func redactPrompt(r *protocol.Redactor, p protocol.Prompt) protocol.Prompt {
	for _, f := range []*string{&p.Kind, &p.Message, &p.URL, &p.UserCode, &p.VerificationURI, &p.VerificationURIComplete} {
		*f = r.String(*f)
	}
	inputs := make([]protocol.LoginInput, len(p.Inputs))
	for i, in := range p.Inputs {
		in.Name, in.Label = r.String(in.Name), r.String(in.Label)
		inputs[i] = in
	}
	if p.Inputs != nil {
		p.Inputs = inputs
	}
	return p
}

func validGrantSettings(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if len(raw) > protocol.MaxGrantSettings {
		return "exceed the 8192-byte limit"
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(raw, &settings) != nil || settings == nil {
		return "must be an object"
	}
	return ""
}
