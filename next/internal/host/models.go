package host

import (
	"context"
	"fmt"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// ListsModels reports whether an extension that described itself with info lists its accounts'
// models: it implements minor version 3 or later and lists the models operation.
func ListsModels(info protocol.Info) bool {
	return info.ProtocolMinor >= 3 && slices.Contains(info.Operations, protocol.OpModels)
}

// MaxListedModels bounds how many models one models call may return; more is a protocol error.
const MaxListedModels = 10000

// Models runs a models call: the models account serves. Every string of the account's
// credential is removed from the reply, as from every other reply.
func (e *Extension) Models(ctx context.Context, account protocol.Account) (*protocol.ModelList, *llm.Error) {
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
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpModels, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, err)
	}
	redact := protocol.NewRedactor(account.Credential, proxy)
	switch {
	case r.Type == protocol.TypeModelsResult && r.Models != nil:
		if len(r.Models.Models) > MaxListedModels {
			return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s listed %d models, more than %d", e.manifest.ID, len(r.Models.Models), MaxListedModels)}
		}
		list := &protocol.ModelList{OwnedBy: redact.String(r.Models.OwnedBy), Models: make([]protocol.Model, 0, len(r.Models.Models))}
		for _, m := range r.Models.Models {
			if m.ID == "" {
				return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s listed a model without an id", e.manifest.ID)}
			}
			list.Models = append(list.Models, redactModel(redact, m))
		}
		return list, nil
	case r.Type == llm.EventError && r.Error != nil:
		return nil, redact.Event(r.Event).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a models call", e.manifest.ID, r.Type)}
}

func redactModel(r *protocol.Redactor, m protocol.Model) protocol.Model {
	for _, s := range []*string{&m.ID, &m.Name, &m.DisplayName, &m.Description, &m.Version, &m.OwnedBy} {
		*s = r.String(*s)
	}
	for _, list := range [][]string{m.GenerationMethods, m.InputModalities, m.OutputModalities} {
		for i := range list {
			list[i] = r.String(list[i])
		}
	}
	return m
}

// Models runs a models call, starting the program first if it is idle. The caller holds a lease
// for the call. An extension that does not list models (ListsModels) gets no call: the reply is
// an empty list.
func (g *Managed) Models(ctx context.Context, account protocol.Account) (*protocol.ModelList, *llm.Error) {
	if !ListsModels(g.Info()) {
		return &protocol.ModelList{}, nil
	}
	defer g.beginCall()()
	ext, e := g.ensure(ctx)
	if e != nil {
		return nil, e
	}
	return ext.Models(ctx, account)
}
