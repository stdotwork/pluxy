package core

import (
	"context"
	"fmt"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// modelRefreshPoll is how often the background refresh loop checks for kept model lists whose
// 30-minute refresh interval (with jitter) has elapsed.
var modelRefreshPoll = time.Minute

// catalog returns the route.CatalogFunc that asks the extensions exts for an account's model
// list (the router keeps the lists).
func catalog(exts map[string]*host.Managed) route.CatalogFunc {
	return func(ctx context.Context, a route.Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		g := exts[a.Extension]
		if g == nil {
			return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", a.Extension)}
		}
		return g.Models(ctx, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: credential, ProxyURL: a.ProxyURL, Headers: a.Headers})
	}
}

func (c *Core) startModelRefresh() {
	if c.opts.SafeMode {
		return
	}
	c.bgWG.Add(1)
	go c.modelRefreshLoop(modelRefreshPoll)
}

func (c *Core) modelRefreshLoop(interval time.Duration) {
	defer c.bgWG.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.bgCtx.Done():
			return
		case <-t.C:
			s, release := c.acquire()
			if c.bgCtx.Err() == nil && s != nil && s.Router != nil {
				s.Router.RefreshDue(c.bgCtx)
			}
			release()
		}
	}
}

// credentialChanged refreshes account id's kept model list after its credential is renewed or
// it signs in again. Runs in the background so a renewal inside a request never waits on or
// deadlocks with the model list read.
func (c *Core) credentialChanged(id string) {
	c.bgWG.Add(1)
	go func() {
		defer c.bgWG.Done()
		if c.bgCtx.Err() != nil {
			return
		}
		s, release := c.acquire()
		defer release()
		if c.bgCtx.Err() != nil || s == nil || s.Router == nil {
			return
		}
		_ = s.Router.RefreshModels(c.bgCtx, id)
	}()
}
