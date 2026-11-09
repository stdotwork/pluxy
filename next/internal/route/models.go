package route

import (
	"context"
	"log"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// CatalogFunc returns the model list the extension of account a gives for it (the protocol's
// models operation), with the credential an attempt on a would present.
type CatalogFunc func(ctx context.Context, a Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error)

// Models returns every model the router's accounts serve, each under every name a client can ask
// for it by, sorted by name; a name two accounts serve is listed once, as the first account in
// order lists it. A model the account's config lists itself shows its upstream name as its
// display name, when the router was made as created and the owner its extension names; one from
// the extension's list (catalog) shows what the extension says about it. An account whose
// catalog cannot be read lists only its config's models.
func (r *Router) Models(ctx context.Context, catalog CatalogFunc) []protocol.Model {
	accounts, creds := r.snapshot()
	var out []protocol.Model
	seen := map[string]bool{}
	for _, a := range accounts {
		list := r.catalogOf(ctx, a, creds, catalog)
		owner := list.OwnedBy
		if owner == "" {
			owner = a.ID
		}
		byID := map[string]protocol.Model{}
		var ids []string
		for _, m := range list.Models {
			if _, dup := byID[m.ID]; !dup {
				byID[m.ID] = m
				ids = append(ids, m.ID)
			}
		}
		for _, n := range a.names(ids, r.cfg.ForceModelPrefix) {
			if seen[n.Public] {
				continue
			}
			seen[n.Public] = true
			var m protocol.Model
			if len(a.Models) > 0 {
				m = protocol.Model{DisplayName: n.Upstream, Created: r.created.Unix()}
			} else {
				m = byID[n.Upstream]
				m.GenerationMethods = slices.Clone(m.GenerationMethods)
				m.InputModalities = slices.Clone(m.InputModalities)
				m.OutputModalities = slices.Clone(m.OutputModalities)
				if m.Name != "" {
					m.Name = renameResource(m.Name, n.Upstream, n.Public)
				}
			}
			m.ID = n.Public
			if n.DisplayName != "" {
				m.DisplayName = n.DisplayName
			}
			if m.OwnedBy == "" {
				m.OwnedBy = owner
			}
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b protocol.Model) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// catalogOf returns account a's model list from its extension, or an empty one when it cannot
// be read. For an account whose config lists its own models only the list's owner is used.
func (r *Router) catalogOf(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc) *protocol.ModelList {
	if catalog == nil {
		return &protocol.ModelList{}
	}
	credential := a.Credential
	if a.Managed {
		if creds == nil {
			return &protocol.ModelList{}
		}
		c, _, e := creds.Credential(ctx, a.Extension, a.ID)
		if e != nil {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
			return &protocol.ModelList{}
		}
		credential = c
	}
	list, e := catalog(ctx, *a, credential)
	if e != nil {
		if e.Code != llm.CodeCancelled {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
		}
		return &protocol.ModelList{}
	}
	return list
}

// renameResource renames a model's resource name (Gemini's "models/<id>") from the model's old
// name to its new one, as today's core does for an alias or a prefix.
func renameResource(name, old, cur string) string {
	switch {
	case old == cur:
		return name
	case strings.EqualFold(name, old):
		return cur
	case strings.HasSuffix(name, "/"+old):
		return strings.TrimSuffix(name, old) + cur
	}
	return name
}
