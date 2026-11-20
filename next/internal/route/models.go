package route

import (
	"cmp"
	"context"
	"log"
	"maps"
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

// catalogRetry is how long a model list that could not be read on the first try counts as
// unknown before it is asked for again.
var catalogRetry = time.Minute

// catalogRefresh is how often a known model list is asked for again, before jitter.
var catalogRefresh = 30 * time.Minute

// catalogRefreshJitter spreads refreshes past catalogRefresh so accounts do not all refresh at
// once.
var catalogRefreshJitter = 3 * time.Minute

// catalogTimeout bounds one read of an account's model list: a read that takes longer counts as
// failed, so requests waiting for a first read go on with the list unknown and a refresh keeps
// the last good list.
var catalogTimeout = 15 * time.Second

// catalogs keeps each account's model list from its extension, keyed by account ID: for the
// model lists clients ask for, and for routing, which sends an account without models of its
// own only the models its list names. Known lists refresh every catalogRefresh (with jitter),
// on credential renewal or login, and on demand via RefreshModels; a read that fails, times out
// or comes back empty keeps the last good list.
type catalogs struct {
	mu sync.Mutex
	// entries are keyed by account ID. All fields of a catalogEntry except done are guarded by
	// mu. A stored ModelList is never mutated after creation.
	entries map[string]*catalogEntry
}

type catalogEntry struct {
	done       chan struct{} // closed once the first read ends
	list       *protocol.ModelList
	at         time.Time     // when a first read failed (list nil)
	refreshAt  time.Time     // when a known list should be asked for again
	refreshing chan struct{} // non-nil while a refresh of a known list runs; closed when done
	lastKept   bool
	lastError  string
}

// RefreshResult is one account's outcome of Router.RefreshModels.
type RefreshResult struct {
	Account string `json:"account"`
	Models  int    `json:"models"`
	Kept    bool   `json:"kept,omitempty"`
	Error   string `json:"error,omitempty"`
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

// retain drops entries for accounts removed from list or marked changed, or clears everything
// when clearAll is true (SetAccounts with same == nil).
func (c *catalogs) retain(list, changed []*Account, clearAll bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if clearAll {
		c.entries = nil
		return
	}
	for id := range c.entries {
		keep := slices.ContainsFunc(list, func(a *Account) bool { return a.ID == id }) &&
			!slices.ContainsFunc(changed, func(a *Account) bool { return a.ID == id })
		if !keep {
			delete(c.entries, id)
		}
	}
}

// CarryLists copies kept model lists from prev for accounts present in both routers that same
// reports unchanged, so a config reload does not drop known lists or re-read them needlessly.
func (r *Router) CarryLists(prev *Router, same func(old, cur *Account) bool) {
	if prev == nil {
		return
	}
	prevAccounts, _ := prev.knownSnapshot()
	accounts, _ := r.knownSnapshot()
	r.lists.mu.Lock()
	defer r.lists.mu.Unlock()
	prev.lists.mu.Lock()
	defer prev.lists.mu.Unlock()
	for _, a := range accounts {
		i := slices.IndexFunc(prevAccounts, func(o *Account) bool { return o.ID == a.ID })
		if i < 0 || (same != nil && !same(prevAccounts[i], a)) {
			continue
		}
		pe := prev.lists.entries[a.ID]
		if pe == nil || !isClosed(pe.done) || pe.list == nil {
			continue
		}
		if r.lists.entries == nil {
			r.lists.entries = map[string]*catalogEntry{}
		}
		if _, exists := r.lists.entries[a.ID]; exists {
			continue
		}
		done := make(chan struct{})
		close(done)
		r.lists.entries[a.ID] = &catalogEntry{
			done:      done,
			list:      pe.list,
			refreshAt: pe.refreshAt,
		}
	}
}

func (r *Router) nextRefresh(now time.Time) time.Time {
	var j time.Duration
	if r.rand != nil && catalogRefreshJitter > 0 {
		j = time.Duration(r.rand() * float64(catalogRefreshJitter))
	}
	return now.Add(catalogRefresh + j)
}

// listOf returns account a's model list from its extension: nil when it is not known (no
// catalog, or the first read could not be read). It waits for a first read another request
// started, and gives up waiting when ctx ends. When a known list is due for its periodic
// refresh, it triggers the refresh in the background and returns the kept list at once so
// requests never wait for a refresh.
func (r *Router) listOf(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc, now time.Time) *protocol.ModelList {
	if catalog == nil {
		return nil
	}
	c := &r.lists
	// Held while the entry is added, so SetAccounts cannot replace the accounts in between.
	r.mu.RLock()
	current := slices.Contains(r.known, a)
	c.mu.Lock()
	if a.Held != "" {
		var e *catalogEntry
		if current {
			e = c.entries[a.ID]
		}
		c.mu.Unlock()
		r.mu.RUnlock()
		if e == nil {
			return nil
		}
		select {
		case <-e.done:
			c.mu.Lock()
			list := e.list
			c.mu.Unlock()
			return list
		case <-ctx.Done():
			return nil
		}
	}
	var e *catalogEntry
	if current {
		e = c.entries[a.ID]
		if e != nil && isClosed(e.done) && e.list == nil && now.Sub(e.at) >= catalogRetry {
			e = nil
		}
	}
	if e == nil {
		e = &catalogEntry{done: make(chan struct{})}
		// A request still routing over accounts set before reads their list without keeping
		// it: the kept lists are the current accounts'.
		if current {
			if c.entries == nil {
				c.entries = map[string]*catalogEntry{}
			}
			c.entries[a.ID] = e
		}
		// One read serves every request waiting for it, so a request that ends does not end it;
		// it has a time limit of its own instead.
		r.startFirstReadLocked(context.WithoutCancel(ctx), e, *a, creds, catalog)
	} else if isClosed(e.done) && e.list != nil && !now.Before(e.refreshAt) && e.refreshing == nil {
		r.startRefreshLocked(e, *a, creds, catalog)
	}
	c.mu.Unlock()
	r.mu.RUnlock()
	select {
	case <-e.done:
		c.mu.Lock()
		list := e.list
		c.mu.Unlock()
		return list
	case <-ctx.Done():
		return nil
	}
}

// startFirstReadLocked starts an asynchronous first read of e for acct, closing e.done when
// it finishes. Caller must hold r.lists.mu.
func (r *Router) startFirstReadLocked(ctx context.Context, e *catalogEntry, acct Account, creds Credentials, catalog CatalogFunc) {
	go func() {
		rctx, cancel := context.WithTimeout(ctx, catalogTimeout)
		defer cancel()
		list, errMsg := r.readList(rctx, &acct, creds, catalog)
		doneAt := r.now()
		r.lists.mu.Lock()
		r.applyReadLocked(e, list, errMsg, doneAt, false)
		close(e.done)
		r.lists.mu.Unlock()
	}()
}

// startRefreshLocked starts an asynchronous refresh of e, keeping any existing e.list in place
// until a new non-empty list replaces it. Caller must hold r.lists.mu.
func (r *Router) startRefreshLocked(e *catalogEntry, acct Account, creds Credentials, catalog CatalogFunc) chan struct{} {
	ch := make(chan struct{})
	e.refreshing = ch
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), catalogTimeout)
		defer cancel()
		list, errMsg := r.readList(rctx, &acct, creds, catalog)
		doneAt := r.now()
		r.lists.mu.Lock()
		r.applyReadLocked(e, list, errMsg, doneAt, true)
		e.refreshing = nil
		close(ch)
		r.lists.mu.Unlock()
	}()
	return ch
}

// applyReadLocked stores the outcome of a catalog read on e. On a first read (e.list == nil),
// any non-nil list — including an empty one — is kept as known, while a failed read (list ==
// nil) records e.at for catalogRetry. On a refresh of a known list (refresh is true and e.list
// != nil), a failed or empty read keeps the existing list. Caller must hold r.lists.mu.
func (r *Router) applyReadLocked(e *catalogEntry, list *protocol.ModelList, errMsg string, doneAt time.Time, refresh bool) {
	if e.list == nil {
		if list != nil {
			e.list = list
			e.refreshAt = r.nextRefresh(doneAt)
			e.lastError = ""
		} else {
			e.at = doneAt
			e.lastError = errMsg
		}
		e.lastKept = false
		return
	}
	if refresh && list != nil && len(list.Models) > 0 {
		e.list = list
		e.refreshAt = r.nextRefresh(doneAt)
		e.lastError = ""
		e.lastKept = false
		return
	}
	e.refreshAt = r.nextRefresh(doneAt)
	e.lastKept = true
	if errMsg == "" && list != nil {
		errMsg = "extension returned an empty model list"
	}
	e.lastError = errMsg
}

// RefreshModels refreshes the model list of accountID (or every routed account when accountID is
// empty), keeping any previously known list when a read fails, times out or comes back empty.
func (r *Router) RefreshModels(ctx context.Context, accountID string) []RefreshResult {
	r.mu.RLock()
	catalog := r.catalog
	creds := r.creds
	var targets []*Account
	for _, a := range r.accounts {
		if accountID == "" || a.ID == accountID {
			targets = append(targets, a)
		}
	}
	r.mu.RUnlock()
	if len(targets) == 0 {
		return nil
	}
	if catalog == nil {
		out := make([]RefreshResult, len(targets))
		for i, a := range targets {
			out[i] = RefreshResult{Account: a.ID}
		}
		return out
	}
	out := make([]RefreshResult, len(targets))
	var wg sync.WaitGroup
	for i, a := range targets {
		wg.Go(func() {
			out[i] = r.refreshOne(ctx, a, creds, catalog)
		})
	}
	wg.Wait()
	return out
}

// RefreshDue refreshes every routed account whose known model list has reached its scheduled
// refresh time (catalogRefresh plus jitter).
func (r *Router) RefreshDue(ctx context.Context) {
	now := r.now()
	r.mu.RLock()
	catalog := r.catalog
	creds := r.creds
	if catalog == nil {
		r.mu.RUnlock()
		return
	}
	c := &r.lists
	c.mu.Lock()
	var due []*Account
	for _, a := range r.accounts {
		e := c.entries[a.ID]
		if e != nil && isClosed(e.done) && e.list != nil && !now.Before(e.refreshAt) {
			due = append(due, a)
		}
	}
	c.mu.Unlock()
	r.mu.RUnlock()
	if len(due) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, a := range due {
		wg.Go(func() {
			_ = r.refreshOne(ctx, a, creds, catalog)
		})
	}
	wg.Wait()
}

func (r *Router) refreshOne(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc) RefreshResult {
	c := &r.lists
	r.mu.RLock()
	current := slices.Contains(r.accounts, a)
	c.mu.Lock()
	e := c.entries[a.ID]
	var waitCh chan struct{}
	switch {
	case !current:
		c.mu.Unlock()
		r.mu.RUnlock()
		return RefreshResult{Account: a.ID}
	case e == nil:
		e = &catalogEntry{done: make(chan struct{})}
		if c.entries == nil {
			c.entries = map[string]*catalogEntry{}
		}
		c.entries[a.ID] = e
		waitCh = e.done
		r.startFirstReadLocked(context.Background(), e, *a, creds, catalog)
	case !isClosed(e.done):
		waitCh = e.done
	case e.refreshing != nil:
		waitCh = e.refreshing
	default:
		waitCh = r.startRefreshLocked(e, *a, creds, catalog)
	}
	c.mu.Unlock()
	r.mu.RUnlock()

	select {
	case <-waitCh:
	case <-ctx.Done():
		c.mu.Lock()
		n := 0
		kept := false
		if e.list != nil {
			n = len(e.list.Models)
			kept = true
		}
		c.mu.Unlock()
		return RefreshResult{Account: a.ID, Models: n, Kept: kept, Error: ctx.Err().Error()}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	if e.list != nil {
		n = len(e.list.Models)
	}
	return RefreshResult{Account: a.ID, Models: n, Kept: e.lastKept, Error: e.lastError}
}

// AccountList returns the kept model list for accountID when it is routed and has a catalog,
// reading it via listOf if it has not been read yet. Returns nil when accountID is not routed
// or its list is unknown.
func (r *Router) AccountList(ctx context.Context, accountID string) *protocol.ModelList {
	r.mu.RLock()
	catalog := r.catalog
	creds := r.creds
	var acct *Account
	for _, a := range r.accounts {
		if a.ID == accountID {
			acct = a
			break
		}
	}
	r.mu.RUnlock()
	if acct == nil || catalog == nil {
		return nil
	}
	return r.listOf(ctx, acct, creds, catalog, r.now())
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// readList asks a's extension for its model list; nil when it cannot be read, along with the
// error message for reporting.
func (r *Router) readList(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc) (*protocol.ModelList, string) {
	if r.state.IsBanned(a.ID) {
		// A banned account sends nothing upstream, not even a list read; a list read before the
		// ban stays in place, as after any failed read.
		return nil, "the account is banned"
	}
	credential := a.Credential
	if a.Managed {
		if creds == nil {
			return nil, "no credential store"
		}
		c, _, e := creds.Credential(ctx, a.Extension, a.ID)
		if e != nil {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
			return nil, e.Message
		}
		credential = c
	}
	list, e := catalog(ctx, *a, credential)
	if e != nil {
		if e.Code != llm.CodeCancelled {
			log.Printf("listing the models of account %q: %s", a.ID, e.Message)
		}
		return nil, e.Message
	}
	return list, ""
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
// for it by, and each fallback chain's new name, sorted by name; a name two accounts serve is
// listed once, as ServedModels describes it: as the account that serves it first lists it. A model
// the account's config lists itself shows its upstream name as its display name, when the router
// was made as created and the owner its extension names; one from the extension's list shows
// what the extension says about it. A model with no owner from the extension is owned by the
// extension, never by the account: an account ID can be an email address or a label. An account
// whose list cannot be read lists only its config's models.
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
	// Accounts are the IDs of the accounts a request for the name uses, highest priority first
	// (with a chain, step by step).
	Accounts []string `json:"accounts"`
}

// ServedModels is Models with each model's extension, upstream name and serving accounts. A
// name's entry follows routing: its provider is the one a request for it goes to (stepTargets),
// its accounts are that provider's that serve the name, highest priority first, and its model is
// as the first of them that lists the name has it. An account that serves any model because its
// list is not known lists no name, but serves those of its provider. A name with a fallback
// chain, and a chain's new name, is served by the accounts of each of its steps, step by step, and
// described as the first of them that lists its step's model has that model, or, when none lists
// it, as the first of them. A name whose accounts are all held is not listed: no request for it
// can be served now. Accounts routing leaves out (weight 0 under the weighted strategy) are in no
// entry.
func (r *Router) ServedModels(ctx context.Context) []ServedModel {
	accounts, creds := r.knownSnapshot()
	accounts = r.routed(accounts)
	catalog := r.catalogFunc()
	now := r.now()
	lists := make([]*protocol.ModelList, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Go(func() { lists[i] = r.listOf(ctx, a, creds, catalog, now) })
	}
	wg.Wait()
	l := modelLists{lists: lists, names: make([][]Name, len(accounts)), served: map[string][]string{}}
	var order []string // the names accounts in service list, each once
	seen := map[string]bool{}
	for i, a := range accounts {
		var ids []string
		if lists[i] != nil {
			ids = modelIDs(lists[i])
		}
		// As served: an account without models of its own serves those its list names.
		if len(a.Models) == 0 && len(ids) > 0 {
			l.served[a.ID] = ids
		}
		l.names[i] = a.names(ids, r.cfg.ForceModelPrefix)
		if a.Held != "" {
			continue
		}
		for _, n := range l.names[i] {
			if !seen[n.Public] {
				seen[n.Public] = true
				order = append(order, n.Public)
			}
		}
	}
	out := []ServedModel{}
	for _, name := range order {
		if _, ok := r.cfg.Chains[name]; ok {
			continue // below
		}
		steps := []Step{{Model: name}}
		serving, _ := split(r.targets(steps, accounts, l.served))
		if e, ok := r.servedEntry(name, steps, serving, accounts, l, false); ok {
			out = append(out, e)
		}
	}
	// Each chain, under its name, new or a model's, with the accounts of every step.
	for _, name := range slices.Sorted(maps.Keys(r.cfg.Chains)) {
		steps := r.cfg.Chains[name]
		serving, _ := split(r.targets(steps, accounts, l.served))
		if e, ok := r.servedEntry(name, steps, serving, accounts, l, true); ok {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b ServedModel) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// modelLists are the model lists of a router's known accounts, by their index: each list, the
// names each account serves, and the lists of accounts without models of their own, as served
// returns them.
type modelLists struct {
	lists  []*protocol.ModelList
	names  [][]Name
	served map[string][]string
}

// servedEntry is the entry of name, served by targets, the targets of steps: its accounts,
// highest priority first within a step, and the model of the first of them that lists its step's
// model, under name. When none lists it, a chain's entry (chain true) is built from the first
// target, whose list is not known; ok is false for a name without a chain, and when there is no
// target.
func (r *Router) servedEntry(name string, steps []Step, targets []target, accounts []*Account, l modelLists, chain bool) (ServedModel, bool) {
	targets = slices.Clone(targets)
	slices.SortStableFunc(targets, func(a, b target) int { return cmp.Or(a.step-b.step, b.a.Priority-a.a.Priority) })
	var e ServedModel
	found := false
	for _, t := range targets {
		if !slices.Contains(e.Accounts, t.a.ID) {
			e.Accounts = append(e.Accounts, t.a.ID)
		}
		if found {
			continue
		}
		i := slices.Index(accounts, t.a)
		j := slices.IndexFunc(l.names[i], func(n Name) bool { return n.Public == steps[t.step].Model && n.Upstream == t.model })
		if j < 0 {
			continue
		}
		found = true
		e.Model, e.Upstream, e.Extension = r.listedModel(t.a, l.names[i][j], l.lists[i], name), t.model, t.a.Extension
	}
	if !found && chain && len(targets) > 0 {
		t := targets[0]
		i := slices.Index(accounts, t.a)
		n := Name{Public: steps[t.step].Model, Upstream: t.model, Base: t.model}
		e.Model, e.Upstream, e.Extension = r.listedModel(t.a, n, l.lists[i], name), t.model, t.a.Extension
		found = true
	}
	return e, found
}

// listedModel is the model n of account a, whose list from its extension is list, under name. A
// model the account's config lists itself shows its upstream name as its display name, when the
// router was made as created and the owner its extension names; one from the extension's list
// shows what the extension says about it.
func (r *Router) listedModel(a *Account, n Name, list *protocol.ModelList, name string) protocol.Model {
	owner := a.Extension
	if list != nil && list.OwnedBy != "" {
		owner = list.OwnedBy
	}
	var m protocol.Model
	if len(a.Models) > 0 {
		m = protocol.Model{DisplayName: n.Upstream, Created: r.created.Unix()}
	} else if list != nil {
		if i := slices.IndexFunc(list.Models, func(x protocol.Model) bool { return x.ID == n.Upstream }); i >= 0 {
			m = list.Models[i]
		}
		m.GenerationMethods = slices.Clone(m.GenerationMethods)
		m.InputModalities = slices.Clone(m.InputModalities)
		m.OutputModalities = slices.Clone(m.OutputModalities)
		if m.Name != "" {
			m.Name = renameResource(m.Name, n.Upstream, name)
		}
	}
	m.ID = name
	if n.DisplayName != "" {
		m.DisplayName = n.DisplayName
	}
	if m.OwnedBy == "" {
		m.OwnedBy = owner
	}
	return m
}

// ModelNames returns public and upstream names served by extension's accounts, using
// Models' cached catalogs and naming rules without renewing credentials. A name outside a
// fallback chain is listed only when routing chooses extension as its provider. A fallback
// chain's name is listed when a step of it can go to extension, and is NonCodex when a step can
// go to another extension, whatever else serves the name.
func (r *Router) ModelNames(ctx context.Context, extension string) []Name {
	accounts, creds := r.knownSnapshot()
	accounts = r.routed(accounts)
	creds = storedCredentials{creds}
	catalog := r.catalogFunc()
	served := r.served(ctx, accounts, creds, catalog)
	names := map[string]Name{}
	var order []string
	now := r.now()
	for _, a := range accounts {
		if a.Extension != extension {
			continue
		}
		var ids []string
		if len(a.Models) == 0 {
			if list := r.listOf(ctx, a, creds, catalog, now); list != nil {
				ids = modelIDs(list)
			}
		}
		for _, n := range a.names(ids, r.cfg.ForceModelPrefix) {
			if n.DisplayName == "" && len(a.Models) > 0 {
				n.DisplayName = n.Upstream
			}
			if _, ok := names[n.Public]; !ok {
				names[n.Public] = n
				order = append(order, n.Public)
			}
		}
	}
	var out []Name
	for _, name := range order {
		if _, chain := r.cfg.Chains[name]; chain {
			continue
		}
		serving := r.stepTargets(Step{Model: name}, 0, accounts, served)
		if len(serving) == 0 || serving[0].a.Extension != extension {
			continue
		}
		out = append(out, names[name])
	}
	// A chain decides where its name goes: it is NonCodex when a step can go to another
	// extension, and a chain's new name is listed when a step can go to this one, as that step's
	// model.
	if len(r.cfg.Chains) > 0 {
		for _, name := range slices.Sorted(maps.Keys(r.cfg.Chains)) {
			steps := r.cfg.Chains[name]
			serving, _ := split(r.targets(steps, accounts, served))
			other := slices.ContainsFunc(serving, func(t target) bool { return t.a.Extension != extension })
			i := slices.IndexFunc(serving, func(t target) bool { return t.a.Extension == extension })
			if i < 0 {
				continue
			}
			t := serving[i]
			n := Name{Upstream: t.model, Base: t.model}
			if base, ok := names[steps[t.step].Model]; ok && base.Upstream == t.model {
				n = base
			}
			n.Public, n.NonCodex = name, other
			out = append(out, n)
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
