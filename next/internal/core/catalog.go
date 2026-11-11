package core

import (
	"context"
	"fmt"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

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
