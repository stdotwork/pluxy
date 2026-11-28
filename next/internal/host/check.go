package host

import (
	"context"
	"fmt"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func ChecksAccounts(info protocol.Info) bool {
	return info.ProtocolMinor >= 4 && slices.Contains(info.Operations, protocol.OpCheck)
}

func (e *Extension) Check(ctx context.Context, account protocol.Account) (*protocol.CheckResult, *llm.Error) {
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
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpCheck, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	switch {
	case r.Type == protocol.TypeCheckResult && r.Check != nil:
		return &protocol.CheckResult{Detail: redact.String(r.Check.Detail)}, nil
	case r.Type == llm.EventError && r.Error != nil:
		return nil, e.normalizeEvent(e.accountPieces(account).event(redact.Event(r.Event))).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a check call", e.manifest.ID, redact.String(r.Type))}
}

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
