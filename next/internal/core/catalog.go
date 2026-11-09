package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// catalogs keeps each account's model list from its extension for one snapshot, so a model list
// asks each extension once per account. An entry is kept for the account's settings and
// credential it was read with; a renewed credential reads the list again.
type catalogs struct {
	mu      sync.Mutex
	entries map[string]catalogEntry // by extension and account ID
}

type catalogEntry struct {
	hash [32]byte
	list *protocol.ModelList
}

// lookup returns the route.CatalogFunc for the extensions exts.
func (c *catalogs) lookup(exts map[string]*host.Managed) route.CatalogFunc {
	return c.cached(func(ctx context.Context, ext string, a protocol.Account) (*protocol.ModelList, *llm.Error) {
		g := exts[ext]
		if g == nil {
			return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", ext)}
		}
		return g.Models(ctx, a)
	})
}

// cached returns a route.CatalogFunc that reads lists with models and keeps them.
func (c *catalogs) cached(models func(ctx context.Context, ext string, a protocol.Account) (*protocol.ModelList, *llm.Error)) route.CatalogFunc {
	return func(ctx context.Context, a route.Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		key := a.Extension + "\x00" + a.ID
		cb, _ := json.Marshal(credential)
		hash := sha256.Sum256(append(append(cb, 0), a.Settings...))
		c.mu.Lock()
		e, ok := c.entries[key]
		c.mu.Unlock()
		if ok && e.hash == hash {
			return e.list, nil
		}
		list, err := models(ctx, a.Extension, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: credential})
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.entries == nil {
			c.entries = map[string]catalogEntry{}
		}
		c.entries[key] = catalogEntry{hash: hash, list: list}
		c.mu.Unlock()
		return list, nil
	}
}
