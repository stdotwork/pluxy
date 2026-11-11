package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Login starts a login call for account (its ID and settings; any credential is left out).
// The call is cancelled when ctx ends or the call is closed.
func (e *Extension) Login(ctx context.Context, account protocol.Account, login protocol.Login) *LoginCall {
	account.Credential = nil
	// What the user entered (an API key, a pasted redirect URL with its code) counts as secret
	// in everything the call sends back.
	l := &LoginCall{ext: e, ctx: ctx, account: account.ID}
	l.addSecrets(login.Input)
	proxy, perr := e.outbound(&account)
	if perr != nil {
		l.fail(perr)
		return l
	}
	if proxy != nil {
		l.addSecrets(outboundSecrets(proxy))
	}
	conn, lerr := e.dial(ctx)
	if lerr != nil {
		l.fail(lerr)
		return l
	}
	l.conn = conn
	l.stop = context.AfterFunc(ctx, func() { conn.Close() })
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpLogin, Account: &account, Login: &login}
	if err := protocol.WriteFrame(conn, call); err != nil {
		l.fail(l.readError(err))
	}
	return l
}

// LoginCall is a running login. Next returns its prompts and then exactly one terminal frame
// (login.done or error), which it keeps returning.
type LoginCall struct {
	ext     *Extension
	ctx     context.Context
	account string
	conn    net.Conn
	stop    func() bool

	wmu     sync.Mutex // guards writes, secrets and redact
	secrets map[string]string
	redact  *protocol.Redactor
	final   *protocol.Reply
	onClose func() // ends the call on its managed generation; safe to call more than once
}

// addSecrets adds what the user entered to what is removed from the frames the call returns.
func (l *LoginCall) addSecrets(input map[string]string) {
	for k, v := range input {
		if l.secrets == nil {
			l.secrets = map[string]string{}
		}
		l.secrets[fmt.Sprintf("%s.%d", k, len(l.secrets))] = v
	}
	l.redact = inputRedactor(l.secrets)
	l.ext.held.add(l.account+" login input", inputCredential(l.secrets))
}

func (l *LoginCall) redactor() *protocol.Redactor {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return l.redact
}

// Next returns the next frame: a login.prompt, or the terminal login.done or error.
func (l *LoginCall) Next() protocol.Reply {
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
		p := redactPrompt(l.redactor(), *r.Prompt)
		return protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &p}
	case r.Type == protocol.TypeLoginDone && validGrant(r.Grant):
		l.ext.held.add(l.account, r.Grant.Credential)
		if err := validIdentity(r.Grant.Identity); err != "" {
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
		// Identity is kept as it is, even a value the credential also holds (an account ID in
		// its data): core stores it beside the credential and compares it, and never shows it.
		done := protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone}, Grant: r.Grant}
		l.final = &done
		l.Close()
		return done
	case r.Type == llm.EventError && r.Error != nil:
		ev := l.redactor().Event(r.Event)
		l.final = &protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: ev.Error}}
		l.Close()
		return *l.final
	}
	l.fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent %q during a login", l.ext.manifest.ID, r.Type)})
	return *l.final
}

// Send passes what the user entered to the login.
func (l *LoginCall) Send(input map[string]string) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.conn == nil {
		return errors.New("the login is not running")
	}
	l.addSecrets(input)
	return protocol.WriteFrame(l.conn, protocol.Call{Type: protocol.TypeLoginInput, Input: input})
}

// Close ends the login; if it had not finished, the extension sees it cancelled.
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
}

func (l *LoginCall) fail(e *llm.Error) {
	l.final = &protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: e}}
	l.Close()
}

func (l *LoginCall) readError(err error) *llm.Error {
	return callError(l.ctx, l.ext, err)
}

// Refresh runs a refresh call: the extension renews account's credential and returns the new
// one. Errors have the sent credential removed from them.
func (e *Extension) Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error) {
	proxy, perr := e.outbound(&account)
	if perr != nil {
		return nil, perr
	}
	e.held.add(account.ID, account.Credential)
	conn, lerr := e.dial(ctx)
	if lerr != nil {
		return nil, lerr
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpRefresh, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, err)
	}
	switch {
	case r.Type == protocol.TypeRefreshDone && validGrant(r.Grant):
		e.held.add(account.ID, r.Grant.Credential)
		return r.Grant.Credential, nil
	case r.Type == llm.EventError && r.Error != nil:
		redact := protocol.NewRedactor(account.Credential, proxy)
		return nil, redact.Event(r.Event).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a refresh", e.manifest.ID, r.Type)}
}

// validIdentity says what is wrong with a grant's identity, or "" when nothing is.
func validIdentity(id map[string]string) string {
	if len(id) > protocol.MaxIdentityFields {
		return fmt.Sprintf("has more than %d fields", protocol.MaxIdentityFields)
	}
	for k, v := range id {
		switch {
		case k == "" || len(k) > protocol.MaxIdentityKey:
			return fmt.Sprintf("has a field name that is empty or longer than %d bytes", protocol.MaxIdentityKey)
		case v == "" || len(v) > protocol.MaxIdentityValue:
			return fmt.Sprintf("field %q is empty or longer than %d bytes", k, protocol.MaxIdentityValue)
		}
	}
	return ""
}

func validGrant(g *protocol.Grant) bool {
	return g != nil && g.Credential != nil && g.Credential.Kind != ""
}

// dial opens a call connection.
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

// callError says why a login or refresh call's connection failed.
func callError(ctx context.Context, e *Extension, err error) *llm.Error {
	if ctx != nil && ctx.Err() != nil {
		return &llm.Error{Code: llm.CodeCancelled, Message: "the call was cancelled"}
	}
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	if errors.Is(err, protocol.ErrFrameTooLarge) || errors.As(err, &se) || errors.As(err, &te) {
		return &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a bad frame: %v", e.manifest.ID, err)}
	}
	return &llm.Error{Code: llm.CodeExtensionCrashed, Message: fmt.Sprintf("extension %s ended the call without finishing it", e.manifest.ID)}
}

// inputRedactor returns the Redactor for what a user entered.
func inputRedactor(input map[string]string) *protocol.Redactor {
	if len(input) == 0 {
		return nil
	}
	return protocol.NewRedactor(inputCredential(input))
}

// inputCredential holds what a user entered as a credential, so all of it counts as secret.
func inputCredential(input map[string]string) *protocol.Credential {
	if len(input) == 0 {
		return nil
	}
	data, _ := json.Marshal(input)
	return &protocol.Credential{Kind: "input", Data: data}
}

// redactPrompt removes what the user entered from every text a prompt shows, in case the
// extension echoes it.
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

// validGrantSettings checks the non-secret object at the extension's trust boundary.
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
