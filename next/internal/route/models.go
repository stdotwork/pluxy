package route

import (
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// CatalogFunc returns the model list the extension of account a gives for it (the protocol's
// models operation), with the credential an attempt on a would present.
type CatalogFunc func(ctx context.Context, a Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error)

// catalogRetry is how long a model list that could not be read counts as unknown before it is
// asked for again.
var catalogRetry = time.Minute

// catalogTimeout bounds one read of an account's model list: a read that takes longer counts as
// failed, so requests waiting for it go on with the list unknown.
var catalogTimeout = 15 * time.Second

// catalogs keeps each account's model list from its extension, so it is asked once per
// account: for the model lists clients ask for, and for routing, which sends an account
// without models of its own only the models its list names. Lists are kept until the
// router's accounts are set again (a reload, or a change in the credential store).
//
// shortcut: a list is not read again when a managed account's credential is renewed, so a
// renewal that changes what the account may use (a Codex plan) shows on the next change of
// accounts; key entries by credential version if that matters.
type catalogs struct {
	mu sync.Mutex
	// entries are by account: the router's copy of it, which SetAccounts replaces, so a request
	// still routing over the accounts set before cannot leave its list for their successors.
	entries map[*Account]*catalogEntry
}

type catalogEntry struct {
	done chan struct{} // closed once list is set
	list *protocol.ModelList
	at   time.Time // when a read failed (list nil)
}

// UseCatalog sets where accounts' model lists come from; nil (the default) uses none, so an
// account without models of its own serves any model and lists none.
func (r *Router) UseCatalog(f CatalogFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.catalog = f
	r.lists.clear()
}

func (c *catalogs) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

// listOf returns account a's model list from its extension: nil when it is not known (no
// catalog, or the list could not be read). It waits for a read another request started, and
// gives up waiting when ctx ends.
func (r *Router) listOf(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc, now time.Time) *protocol.ModelList {
	if catalog == nil {
		return nil
	}
	c := &r.lists
	// Held while the entry is added, so SetAccounts cannot replace the accounts in between.
	r.mu.RLock()
	c.mu.Lock()
	e := c.entries[a]
	if e != nil && isClosed(e.done) && e.list == nil && now.Sub(e.at) >= catalogRetry {
		e = nil
	}
	if e == nil {
		e = &catalogEntry{done: make(chan struct{})}
		// A request still routing over accounts set before reads their list without keeping
		// it: the kept lists are the current accounts'.
		if slices.Contains(r.accounts, a) {
			if c.entries == nil {
				c.entries = map[*Account]*catalogEntry{}
			}
			c.entries[a] = e
		}
		// One read serves every request waiting for it, so a request that ends does not end it;
		// it has a time limit of its own instead.
		go func(a Account) {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogTimeout)
			defer cancel()
			e.list = r.readList(rctx, &a, creds, catalog)
			if e.list == nil {
				e.at = r.now()
			}
			close(e.done)
		}(*a)
	}
	c.mu.Unlock()
	r.mu.RUnlock()
	select {
	case <-e.done:
		return e.list
	case <-ctx.Done():
		return nil
	}
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// readList asks a's extension for its model list; nil when it cannot be read.
func (r *Router) readList(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc) *protocol.ModelList {
	credential := a.Credential
	if a.Managed {
		if creds == nil {
			return nil
		}
		c, _, e := creds.Credential(ctx, a.Extension, a.ID)
		if e != nil {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
			return nil
		}
		credential = c
	}
	list, e := catalog(ctx, *a, credential)
	if e != nil {
		if e.Code != llm.CodeCancelled {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
		}
		return nil
	}
	return list
}

// served returns, for each account in accounts that has no models of its own, the upstream
// names its extension lists for it; an account whose list is not known or empty has no entry
// and serves any model. The lists are read at the same time.
func (r *Router) served(ctx context.Context, accounts []*Account, creds Credentials, catalog CatalogFunc) map[string][]string {
	if catalog == nil {
		return nil
	}
	lists := make([]*protocol.ModelList, len(accounts))
	var wg sync.WaitGroup
	now := r.now()
	for i, a := range accounts {
		if len(a.Models) == 0 {
			wg.Go(func() { lists[i] = r.listOf(ctx, a, creds, catalog, now) })
		}
	}
	wg.Wait()
	out := map[string][]string{}
	for i, l := range lists {
		if l != nil && len(l.Models) > 0 {
			out[accounts[i].ID] = modelIDs(l)
		}
	}
	return out
}

// modelIDs returns the IDs in list, each once, in order.
func modelIDs(list *protocol.ModelList) []string {
	ids := make([]string, 0, len(list.Models))
	seen := map[string]bool{}
	for _, m := range list.Models {
		if !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// Models returns every model the router's accounts serve, each under every name a client can ask
// for it by, sorted by name; a name two accounts serve is listed once, as the first account in
// order lists it. A model the account's config lists itself shows its upstream name as its
// display name, when the router was made as created and the owner its extension names; one from
// the extension's list shows what the extension says about it. An account whose list cannot be
// read lists only its config's models.
func (r *Router) Models(ctx context.Context) []protocol.Model {
	served := r.ServedModels(ctx)
	out := make([]protocol.Model, len(served))
	for i, m := range served {
		out[i] = m.Model
	}
	return out
}

// ServedModel is a model of Models' list (its ID is the name clients use) with who serves it, as
// the management API shows it. Never a credential.
type ServedModel struct {
	protocol.Model
	// Upstream is the model's name upstream, as the account whose entry the list shows sends it.
	Upstream string `json:"upstream"`
	// Extension is the extension of that account.
	Extension string `json:"extension"`
	// Accounts are the IDs of every account that serves the name, in the router's order.
	Accounts []string `json:"accounts"`
}

// ServedModels is Models with each model's extension, upstream name and serving accounts. An
// account that serves any model because its list is not known lists none, as Models lists none.
func (r *Router) ServedModels(ctx context.Context) []ServedModel {
	accounts, creds := r.snapshot()
	r.mu.RLock()
	catalog := r.catalog
	r.mu.RUnlock()
	out := []ServedModel{}
	index := map[string]int{}
	for _, a := range accounts {
		// For an account whose config lists its models, only the list's owner is used.
		list := r.listOf(ctx, a, creds, catalog, r.now())
		if list == nil {
			list = &protocol.ModelList{}
		}
		owner := list.OwnedBy
		if owner == "" {
			owner = a.ID
		}
		byID := map[string]protocol.Model{}
		for _, m := range list.Models {
			if _, dup := byID[m.ID]; !dup {
				byID[m.ID] = m
			}
		}
		for _, n := range a.names(modelIDs(list), r.cfg.ForceModelPrefix) {
			if i, ok := index[n.Public]; ok {
				out[i].Accounts = append(out[i].Accounts, a.ID)
				continue
			}
			index[n.Public] = len(out)
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
			out = append(out, ServedModel{Model: m, Extension: a.Extension, Upstream: n.Upstream, Accounts: []string{a.ID}})
		}
	}
	slices.SortFunc(out, func(a, b ServedModel) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// ModelNames returns public and upstream names served by extension's accounts, using
// Models' cached catalogs and naming rules without renewing credentials. Repeated names
// keep the first mapping; NonCodex marks names also served by another extension.
func (r *Router) ModelNames(ctx context.Context, extension string) []Name {
	accounts, creds := r.snapshot()
	creds = storedCredentials{creds}
	r.mu.RLock()
	catalog := r.catalog
	r.mu.RUnlock()
	var out []Name
	seen := map[string]bool{}
	for _, a := range accounts {
		if a.Extension != extension {
			continue
		}
		list := r.listOf(ctx, a, creds, catalog, r.now())
		var ids []string
		if list != nil {
			ids = modelIDs(list)
		}
		for _, n := range a.names(ids, r.cfg.ForceModelPrefix) {
			if n.DisplayName == "" && len(a.Models) > 0 {
				n.DisplayName = n.Upstream
			}
			if !seen[n.Public] {
				seen[n.Public] = true
				out = append(out, n)
			}
		}
	}
	// Check all other routes, including accounts whose unknown catalog lets them serve any
	// model. Resolve applies exclusions, aliases and forced prefixes in the same order.
	for _, a := range accounts {
		if a.Extension == extension {
			continue
		}
		list := r.listOf(ctx, a, creds, catalog, r.now())
		var ids []string
		if list != nil {
			ids = modelIDs(list)
		}
		for i := range out {
			if _, ok := a.resolve(out[i].Public, r.cfg.ForceModelPrefix, ids); ok {
				out[i].NonCodex = true
			}
		}
	}
	slices.SortFunc(out, func(a, b Name) int { return strings.Compare(a.Public, b.Public) })
	return out
}

// storedCredentials reads a managed credential without invoking its renewal path. It is
// used only for catalog discovery, where expired tokens still carry local plan metadata.
type storedCredentials struct{ Credentials }

func (s storedCredentials) Credential(_ context.Context, extension, id string) (*protocol.Credential, uint64, *llm.Error) {
	if c, ok := s.Credentials.(interface {
		StoredCredential(string, string) (*protocol.Credential, *llm.Error)
	}); ok {
		credential, e := c.StoredCredential(extension, id)
		return credential, 0, e
	}
	return nil, 0, &llm.Error{Code: llm.CodeUnavailable, Message: "stored credential is unavailable"}
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
