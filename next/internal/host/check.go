package host

import (
	"context"
	"fmt"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// ChecksAccounts reports whether an extension that described itself with info checks its
// accounts: it implements minor version 4 or later and lists the check operation.
func ChecksAccounts(info protocol.Info) bool {
	return info.ProtocolMinor >= 4 && slices.Contains(info.Operations, protocol.OpCheck)
}

// Check runs a check call: whether account works. Every string of the account's credential is
// removed from the reply, as from every other reply.
func (e *Extension) Check(ctx context.Context, account protocol.Account) (*protocol.CheckResult, *llm.Error) {
	proxy, perr := e.proxied(&account)
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
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpCheck, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, err)
	}
	redact := protocol.NewRedactor(account.Credential, proxy)
	switch {
	case r.Type == protocol.TypeCheckResult && r.Check != nil:
		return &protocol.CheckResult{Detail: redact.String(r.Check.Detail)}, nil
	case r.Type == llm.EventError && r.Error != nil:
		return nil, redact.Event(r.Event).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a check call", e.manifest.ID, r.Type)}
}

// Check runs a check call, starting the program first if it is idle. The caller holds a lease
// for the call. An extension that does not check accounts (ChecksAccounts) gets no call: the
// reply is code unsupported.
func (g *Managed) Check(ctx context.Context, account protocol.Account) (*protocol.CheckResult, *llm.Error) {
	if !ChecksAccounts(g.Info()) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("extension %s does not check accounts", g.Info().ID)}
	}
	defer g.beginCall()()
	ext, e := g.ensure(ctx)
	if e != nil {
		return nil, e
	}
	return ext.Check(ctx, account)
}
