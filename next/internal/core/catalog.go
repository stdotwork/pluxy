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

var modelRefreshPoll = time.Minute

var catalogueRefreshPoll = time.Hour

func catalog(exts map[string]*host.Managed) route.CatalogFunc {
	return func(ctx context.Context, a route.Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		g := exts[a.Extension]
		if g == nil {
			return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", a.Extension)}
		}
		list, err := g.Models(ctx, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: credential, ProxyURL: a.ProxyURL, Headers: a.Headers})
		if list != nil && g.Info().ProtocolMinor < host.CapabilitiesMinor {
			for i := range list.Models {
				list.Models[i].Capabilities = nil
			}
		}
		return list, err
	}
}

func (c *Core) startModelRefresh() {
	if c.opts.SafeMode {
		return
	}
	c.bgWG.Add(1)
	go c.modelRefreshLoop(modelRefreshPoll)
	if c.catalogue != nil {
		c.bgWG.Add(1)
		go c.catalogueRefreshLoop(catalogueRefreshPoll)
	}
}

func (c *Core) catalogueRefreshLoop(interval time.Duration) {
	defer c.bgWG.Done()
	refreshIfStale := func() {
		if c.bgCtx.Err() != nil || c.catalogue == nil {
			return
		}
		now := time.Now
		if c.catalogueNow != nil {
			now = c.catalogueNow
		}
		if c.catalogue.Stale(now()) {
			_ = c.catalogue.Refresh(c.bgCtx)
		}
	}
	refreshIfStale()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.bgCtx.Done():
			return
		case <-t.C:
			refreshIfStale()
		}
	}
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

func (c *Core) credentialChanged(id string) {
	c.bgMu.Lock()
	defer c.bgMu.Unlock()
	if c.bgClosed {
		return
	}
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
