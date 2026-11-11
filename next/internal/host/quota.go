package host

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// ReportsQuota reports whether an extension that described itself with info reads its accounts'
// quota: it implements minor version 5 or later and lists the quota operation.
func ReportsQuota(info protocol.Info) bool {
	return info.ProtocolMinor >= 5 && slices.Contains(info.Operations, protocol.OpQuota)
}

// Quota runs a quota call: what account's plan allows and how much of it is used. Every string
// of the account's credential and every secret in its settings is removed from the reply, as
// from every other reply.
func (e *Extension) Quota(ctx context.Context, account protocol.Account) (*protocol.Quota, *llm.Error) {
	proxy, perr := e.proxied(&account)
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
	call := protocol.Call{Type: protocol.TypeCall, ID: e.nextID(), Op: protocol.OpQuota, Account: &account}
	if err := protocol.WriteFrame(conn, call); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	var r protocol.Reply
	if err := protocol.ReadFrame(conn, &r); err != nil {
		return nil, callError(ctx, e, redact, err)
	}
	switch {
	case r.Type == protocol.TypeQuotaResult && r.Quota != nil:
		q := r.Quota
		if len(q.Limits) > protocol.MaxQuotaLimits {
			return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s reported %d quota limits, more than %d", e.manifest.ID, len(q.Limits), protocol.MaxQuotaLimits)}
		}
		if raw := bytes.TrimSpace(q.Raw); len(raw) > 0 && raw[0] != '{' {
			return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s sent a quota raw that is not a JSON object", e.manifest.ID)}
		}
		out := &protocol.Quota{Plan: redact.String(q.Plan), PlanEndsAt: q.PlanEndsAt, Limits: make([]protocol.QuotaLimit, 0, len(q.Limits)), Raw: redact.Raw(q.Raw)}
		for i, l := range q.Limits {
			if l.ID == "" {
				return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s reported quota limit %d without an id", e.manifest.ID, i)}
			}
			for _, f := range []*float64{l.UsedPercent, l.Used, l.Limit, l.Remaining} {
				if f != nil && (math.IsNaN(*f) || math.IsInf(*f, 0)) {
					return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s reported quota limit %q with an amount that is not a number", e.manifest.ID, redact.String(l.ID))}
				}
			}
			l.ID, l.Label, l.Unit = redact.String(l.ID), redact.String(l.Label), redact.String(l.Unit)
			l.Models = slices.Clone(l.Models)
			for j := range l.Models {
				l.Models[j] = redact.String(l.Models[j])
			}
			out.Limits = append(out.Limits, l)
		}
		return out, nil
	case r.Type == llm.EventError && r.Error != nil:
		return nil, redact.Event(r.Event).Error
	}
	return nil, &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("extension %s replied %q to a quota call", e.manifest.ID, redact.String(r.Type))}
}

// Quota runs a quota call, starting the program first if it is idle. The caller holds a lease
// for the call. An extension that does not report quota (ReportsQuota) gets no call: the reply
// is an unsupported error.
func (g *Managed) Quota(ctx context.Context, account protocol.Account) (*protocol.Quota, *llm.Error) {
	if !ReportsQuota(g.Info()) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("extension %s does not report quota", g.Info().ID), Scope: llm.ScopeRequest}
	}
	defer g.beginCall()()
	ext, e := g.ensure(ctx)
	if e != nil {
		return nil, e
	}
	return ext.Quota(ctx, account)
}
