package core

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/internal/ignite"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
)

const IgnitionEndpoint = "ignition"

const ignitionTimeout = 2 * time.Minute

func (c *Core) runIgnition(ctx context.Context, r *routing) {
	defer close(r.ignited)
	if c.opts.SafeMode {
		return
	}
	tick := time.NewTicker(min(time.Second, max(r.every/15, 10*time.Millisecond)))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.ignitionRound(ctx)
		}
	}
}

func (c *Core) ignitionRound(ctx context.Context) {
	s := c.cur.Load()
	if s == nil || s.Router == nil {
		return
	}
	p := c.ignition
	if !s.Config.Routing.Ignition.On() {
		p.Set(nil, nil, time.Now())
		return
	}
	accounts := map[string]route.Account{}
	var refs []ignite.Ref
	for _, a := range s.Router.Accounts() {
		accounts[a.ID] = a
		refs = append(refs, ignite.Ref{ID: a.ID, Extension: a.Extension})
	}
	p.Set(s.Config.Routing.Ignition, refs, time.Now())
	for _, id := range p.Due(time.Now()) {
		if ctx.Err() != nil {
			return
		}
		if a, ok := accounts[id]; ok {
			c.igniteAccount(ctx, a)
		}
	}
}

func (c *Core) igniteAccount(ctx context.Context, a route.Account) {
	p := c.ignition
	q, e := readings{c}.Reading(ctx, a)
	now := time.Now()
	switch {
	case e != nil && e.Code == llm.CodeUnsupported:
		p.NoReading(a.ID, e.Message)
		return
	case e != nil:
		p.ReadFailed(a.ID, e.Message, now)
		return
	}
	hl := c.state.Health(a, now, route.Hold{})
	h := ignite.Health{OK: hl.State == route.HealthOK, Why: hl.Why}
	if hl.Until != nil {
		h.Until = *hl.Until
	}
	for _, f := range p.Plan(a.ID, q, h, now) {
		if ctx.Err() != nil {
			return
		}
		model, fail := c.ignite(ctx, a, f)
		p.Done(a.ID, f.Window.ID, model, fail, time.Now())
	}
}

func (c *Core) ignite(ctx context.Context, a route.Account, f ignite.Fire) (string, *ignite.Failure) {
	ctx, cancel := context.WithTimeout(ctx, ignitionTimeout)
	defer cancel()
	s, release := c.acquire()
	defer release()
	model := f.Model
	if model == "" {
		reply, err := c.accountModels(ctx, s, a.ID)
		if err != nil {
			log.Printf("ignition: account %q: window %s: its models cannot be listed: %v", a.ID, f.Window.ID, err)
			return "", &ignite.Failure{Message: "the account's models cannot be listed: " + err.Error()}
		}
		var ids []string
		for _, m := range reply.Models {
			if !m.Excluded {
				ids = append(ids, m.ID)
			}
		}
		if model = ignite.ChooseModel(ids, f.Window.Models); model == "" {
			log.Printf("ignition: account %q: window %s: the account lists no model to start it with; set one in routing.ignition", a.ID, f.Window.ID)
			return "", &ignite.Failure{Message: "the account lists no model to start the window with; set one in routing.ignition", Hard: true}
		}
	}
	start := time.Now()
	res, err := c.testAccount(ctx, s, a.ID, model)
	if err != nil {
		log.Printf("ignition: account %q: window %s: %v", a.ID, f.Window.ID, err)
		return model, &ignite.Failure{Message: err.Error(), Hard: errors.Is(err, errNoAccount) || errors.Is(err, errNoModel)}
	}
	if s.Usage != nil {
		ev := usage.Event{Timestamp: start.UTC(), Endpoint: IgnitionEndpoint, Model: model, ServedModel: res.Served,
			Provider: a.Extension, Account: a.ID, Attempts: 1, LatencyMS: res.LatencyMS, Tokens: usage.TokensOf(res.Usage)}
		if res.Served == model {
			ev.ServedModel = ""
		}
		if res.OK {
			ev.Status = http.StatusOK
		} else {
			ev.Failed, ev.ErrorCode = true, res.Error.Code
		}
		s.Usage.Add(ev)
	}
	if res.OK {
		log.Printf("ignition: account %q: sent the request that starts window %s, with %s", a.ID, f.Window.ID, model)
		return model, nil
	}
	log.Printf("ignition: account %q: window %s: the request with %s failed: %s", a.ID, f.Window.ID, model, res.Error.Message)
	return model, &ignite.Failure{Message: res.Error.Message, Hard: hardFailure(res.Error)}
}

func hardFailure(e *llm.Error) bool {
	switch e.Code {
	case llm.CodeAuth, llm.CodePermission, llm.CodeAccountClosed, llm.CodeNotFound, llm.CodeInvalidRequest, llm.CodeUnsupported:
		return true
	}
	return false
}
