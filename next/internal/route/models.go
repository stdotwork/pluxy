package route

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type CatalogFunc func(ctx context.Context, a Account, credential *protocol.Credential) (*protocol.ModelList, *llm.Error)

var catalogRetry = time.Minute

var catalogRefresh = 30 * time.Minute

var catalogRefreshJitter = 3 * time.Minute

var catalogTimeout = 15 * time.Second

type catalogs struct {
	mu      sync.Mutex
	entries map[string]*catalogEntry
}

type catalogEntry struct {
	done       chan struct{}
	list       *protocol.ModelList
	at         time.Time
	refreshAt  time.Time
	refreshing chan struct{}
	lastKept   bool
	lastError  string
}

type RefreshResult struct {
	Account string `json:"account"`
	Models  int    `json:"models"`
	Kept    bool   `json:"kept,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (r *Router) UseCatalog(f CatalogFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.catalog = f
	r.lists.clear()
}

type CatalogueFunc func(extension string, settings json.RawMessage, id string) (protocol.Model, bool)

func (r *Router) UseCatalogue(f CatalogueFunc) {
	r.mu.Lock()
	r.catalogue = f
	r.mu.Unlock()
}

func (c *catalogs) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

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

func (r *Router) listOf(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc, now time.Time) *protocol.ModelList {
	if catalog == nil {
		return nil
	}
	c := &r.lists
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
		if current {
			if c.entries == nil {
				c.entries = map[string]*catalogEntry{}
			}
			c.entries[a.ID] = e
		}
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
	fellBack := e.list.From == protocol.ModelListFromAccount && list != nil &&
		list.From != protocol.ModelListFromAccount && len(list.Models) > 0
	if refresh && list != nil && len(list.Models) > 0 && !fellBack {
		e.list = list
		e.refreshAt = r.nextRefresh(doneAt)
		e.lastError = ""
		e.lastKept = false
		return
	}
	e.refreshAt = r.nextRefresh(doneAt)
	e.lastKept = true
	if errMsg == "" && fellBack {
		errMsg = "the model list read fell back to the built-in list, so the last live list was kept"
	}
	if errMsg == "" && list != nil {
		errMsg = "extension returned an empty model list"
	}
	e.lastError = errMsg
}

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

func (r *Router) readList(ctx context.Context, a *Account, creds Credentials, catalog CatalogFunc) (*protocol.ModelList, string) {
	if r.state.IsBanned(a.ID) {
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

func (r *Router) Models(ctx context.Context) []protocol.Model {
	served := r.ServedModels(ctx)
	out := make([]protocol.Model, len(served))
	for i, m := range served {
		out[i] = m.Model
	}
	return out
}

type ServedModel struct {
	protocol.Model
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	From            string   `json:"from,omitempty"`
	Upstream        string   `json:"upstream"`
	Extension       string   `json:"extension"`
	Accounts        []string `json:"accounts"`
}

func (r *Router) ServedModels(ctx context.Context) []ServedModel {
	accounts, creds := r.knownSnapshot()
	accounts = r.routed(accounts)
	catalog := r.catalogFunc()
	catalogue := r.catalogueFunc()
	now := r.now()
	lists := make([]*protocol.ModelList, len(accounts))
	var wg sync.WaitGroup
	for i, a := range accounts {
		wg.Go(func() { lists[i] = r.listOf(ctx, a, creds, catalog, now) })
	}
	wg.Wait()
	l := modelLists{lists: lists, names: make([][]Name, len(accounts)), served: map[string][]string{}}
	var order []string
	seen := map[string]bool{}
	for i, a := range accounts {
		var ids []string
		if lists[i] != nil {
			ids = modelIDs(lists[i])
		}
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
			continue
		}
		steps := []Step{{Model: name}}
		serving, _ := split(r.targets(steps, accounts, l.served))
		if e, ok := r.servedEntry(name, steps, serving, accounts, l, catalogue, false); ok {
			out = append(out, e)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.cfg.Chains)) {
		steps := r.cfg.Chains[name]
		serving, _ := split(r.targets(steps, accounts, l.served))
		if e, ok := r.servedEntry(name, steps, serving, accounts, l, catalogue, true); ok {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b ServedModel) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func MergeModel(acct, cat, ext *protocol.Model) (m protocol.Model, maxOutputTokens int, from string) {
	for _, src := range []*protocol.Model{acct, ext, cat} {
		if src == nil {
			continue
		}
		if m.ID == "" {
			m.ID = src.ID
		}
		if m.Name == "" {
			m.Name = src.Name
		}
		if m.DisplayName == "" {
			m.DisplayName = src.DisplayName
		}
		if m.Description == "" {
			m.Description = src.Description
		}
		if m.Version == "" {
			m.Version = src.Version
		}
		if m.Created == 0 {
			m.Created = src.Created
		}
		if m.OwnedBy == "" {
			m.OwnedBy = src.OwnedBy
		}
		if m.InputTokenLimit == 0 {
			m.InputTokenLimit = src.InputTokenLimit
		}
		if m.OutputTokenLimit == 0 {
			m.OutputTokenLimit = src.OutputTokenLimit
		}
		if len(m.GenerationMethods) == 0 && len(src.GenerationMethods) > 0 {
			m.GenerationMethods = slices.Clone(src.GenerationMethods)
		}
		if len(m.InputModalities) == 0 && len(src.InputModalities) > 0 {
			m.InputModalities = slices.Clone(src.InputModalities)
		}
		if len(m.OutputModalities) == 0 && len(src.OutputModalities) > 0 {
			m.OutputModalities = slices.Clone(src.OutputModalities)
		}
	}

	type source struct {
		m    *protocol.Model
		from string
	}
	sources := []source{
		{acct, protocol.ModelListFromAccount},
		{cat, protocol.ModelListFromCatalogue},
		{ext, protocol.ModelListFromExtension},
	}

	var ctxLimit, outLimit int
	var ctxFrom, outFrom, capFrom string

	for _, s := range sources {
		if s.m == nil {
			continue
		}
		if v := cmp.Or(s.m.ContextLength, s.m.InputTokenLimit); v > 0 {
			if ctxLimit == 0 || v < ctxLimit {
				ctxLimit = v
				ctxFrom = s.from
			}
		}
		if v := cmp.Or(s.m.MaxCompletionTokens, s.m.OutputTokenLimit); v > 0 {
			if outLimit == 0 || v < outLimit {
				outLimit = v
				outFrom = s.from
			}
		}
		if s.m.Capabilities != nil {
			if m.Capabilities == nil {
				cp := *s.m.Capabilities
				cp.ReasoningLevels = slices.Clone(cp.ReasoningLevels)
				m.Capabilities = &cp
				if capFrom == "" {
					capFrom = s.from
				}
			} else {
				if len(m.Capabilities.ReasoningLevels) == 0 && len(s.m.Capabilities.ReasoningLevels) > 0 {
					m.Capabilities.ReasoningLevels = slices.Clone(s.m.Capabilities.ReasoningLevels)
				}
				if m.Capabilities.DeprecatedAt == 0 && s.m.Capabilities.DeprecatedAt > 0 {
					m.Capabilities.DeprecatedAt = s.m.Capabilities.DeprecatedAt
				}
			}
		}
	}

	m.ContextLength = ctxLimit
	m.MaxCompletionTokens = outLimit
	if m.InputTokenLimit != 0 || m.OutputTokenLimit != 0 {
		m.InputTokenLimit = ctxLimit
		m.OutputTokenLimit = outLimit
	}
	maxOutputTokens = outLimit
	from = cmp.Or(ctxFrom, outFrom, capFrom)
	return m, maxOutputTokens, from
}

type modelLists struct {
	lists  []*protocol.ModelList
	names  [][]Name
	served map[string][]string
}

func (r *Router) servedEntry(name string, steps []Step, targets []target, accounts []*Account, l modelLists, catalogue CatalogueFunc, chain bool) (ServedModel, bool) {
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
		e.Model, e.MaxOutputTokens, e.From = r.listedModel(catalogue, t.a, l.names[i][j], l.lists[i], name)
		e.Upstream, e.Extension = t.model, t.a.Extension
	}
	if !found && chain && len(targets) > 0 {
		t := targets[0]
		i := slices.Index(accounts, t.a)
		n := Name{Public: steps[t.step].Model, Upstream: t.model, Base: t.model}
		e.Model, e.MaxOutputTokens, e.From = r.listedModel(catalogue, t.a, n, l.lists[i], name)
		e.Upstream, e.Extension = t.model, t.a.Extension
		found = true
	}
	return e, found
}

func (r *Router) listedModel(catalogue CatalogueFunc, a *Account, n Name, list *protocol.ModelList, name string) (protocol.Model, int, string) {
	var acct, ext, cat *protocol.Model
	if len(a.Models) == 0 && list != nil {
		if i := slices.IndexFunc(list.Models, func(x protocol.Model) bool { return x.ID == n.Upstream }); i >= 0 {
			m := list.Models[i]
			if list.From == protocol.ModelListFromAccount {
				acct = &m
			} else {
				ext = &m
			}
		}
	}
	if catalogue != nil {
		if cm, ok := catalogue(a.Extension, a.Settings, n.Upstream); ok {
			cat = &cm
		}
	}
	m, maxOut, from := MergeModel(acct, cat, ext)
	if len(a.Models) > 0 {
		m.DisplayName, m.Created = n.Upstream, r.created.Unix()
	} else if m.Name != "" {
		m.Name = renameResource(m.Name, n.Upstream, name)
	}
	m.ID = name
	if n.DisplayName != "" {
		m.DisplayName = n.DisplayName
	}
	owner := a.Extension
	if list != nil && list.OwnedBy != "" {
		owner = list.OwnedBy
	}
	if m.OwnedBy == "" {
		m.OwnedBy = owner
	}
	return m, maxOut, from
}

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
