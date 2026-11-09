package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Account checks: when core starts, and when a reload or the credential store adds or changes an
// account, core asks the account's extension whether it works (the protocol's check call, which
// never generates). The result is shown in the management API's account listing and logged.
// Checks run in the background: serving never waits for them.
const (
	checkTimeout  = 30 * time.Second // one check call
	checkParallel = 4                // check calls running at once
)

// checks holds the result of each account's last check, by account ID.
type checks struct {
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	wg     sync.WaitGroup

	mu      sync.Mutex
	stopped bool
	entries map[string]*checkEntry
}

// checkEntry is one account's check: key says what was checked (the extension generation, the
// account's settings and credential), so a change checks again.
type checkEntry struct {
	key    [32]byte
	result server.AccountCheck
}

func newChecks() *checks {
	ctx, cancel := context.WithCancel(context.Background())
	return &checks{ctx: ctx, cancel: cancel, sem: make(chan struct{}, checkParallel), entries: map[string]*checkEntry{}}
}

// result returns account id's check, or nil when core has none.
func (k *checks) result(id string) *server.AccountCheck {
	k.mu.Lock()
	defer k.mu.Unlock()
	if e := k.entries[id]; e != nil {
		r := e.result
		return &r
	}
	return nil
}

// stop cancels running checks and waits for them; no check starts after it.
func (k *checks) stop() {
	k.mu.Lock()
	k.stopped = true
	k.mu.Unlock()
	k.cancel()
	k.wg.Wait()
}

// checkAccounts starts a check of each of accounts, the accounts s routes over, that is new or
// changed since its last check, and forgets the checks of accounts no longer among them. It
// returns at once.
func (c *Core) checkAccounts(s *snapshot, accounts []route.Account) {
	k := c.checks
	records := map[string]protocol.Credential{}
	registrations := map[string]string{}
	for _, r := range c.creds.Records() {
		records[r.ID], registrations[r.ID] = r.Credential, r.Registration
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return
	}
	for id := range k.entries {
		if !slices.ContainsFunc(accounts, func(a route.Account) bool { return a.ID == id }) {
			delete(k.entries, id)
		}
	}
	for _, a := range accounts {
		g := s.exts[a.Extension]
		if g == nil {
			continue // safe mode
		}
		call := protocol.Account{ID: a.ID, Settings: a.Settings, Credential: a.Credential}
		// What identifies the check: the generation (a new one may check differently), the
		// settings, and the credential. A stored account's credential is renewed on its own, so
		// it counts by its registration, except for a static key, which only a new login changes.
		h := sha256.New()
		fmt.Fprintf(h, "%p\x00%s\x00", g, a.Extension)
		h.Write(a.Settings)
		if a.Managed {
			cred := records[a.ID]
			call.Credential = &cred
			fmt.Fprintf(h, "\x00%s", registrations[a.ID])
			if cred.Kind == protocol.CredentialAPIKey {
				b, _ := json.Marshal(cred)
				h.Write(b)
			}
		} else {
			b, _ := json.Marshal(a.Credential)
			h.Write(b)
		}
		var key [32]byte
		h.Sum(key[:0])
		if e := k.entries[a.ID]; e != nil && e.key == key {
			continue
		}
		e := &checkEntry{key: key, result: server.AccountCheck{State: server.CheckRunning}}
		k.entries[a.ID] = e
		if !host.ChecksAccounts(g.Info()) {
			c.finishCheck(e, a.ID, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("extension %s does not check accounts", a.Extension)}, false)
			continue
		}
		if exp := call.Credential; exp != nil && exp.ExpiresAt != 0 && exp.ExpiresAt <= time.Now().Unix() {
			// Renewing it here would be a call beyond the check; the next request renews it.
			c.finishCheck(e, a.ID, nil, &llm.Error{Code: llm.CodeUnsupported, Message: "the credential has expired; core renews it for the next request"}, false)
			continue
		}
		k.wg.Add(1)
		go func() {
			defer k.wg.Done()
			select {
			case k.sem <- struct{}{}:
			case <-k.ctx.Done():
				return
			}
			defer func() { <-k.sem }()
			if !g.Acquire() {
				return // replaced or stopping: a later publish checks the account again
			}
			ctx, cancel := context.WithTimeout(k.ctx, checkTimeout)
			res, err := g.Check(ctx, call)
			cancel()
			g.Release()
			if k.ctx.Err() != nil {
				return
			}
			k.mu.Lock()
			defer k.mu.Unlock()
			if k.entries[a.ID] == e {
				c.finishCheck(e, a.ID, res, err, true)
			}
		}()
	}
}

// finishCheck records and logs the outcome of account id's check in e, with c.checks.mu held.
func (c *Core) finishCheck(e *checkEntry, id string, res *protocol.CheckResult, err *llm.Error, called bool) {
	now := time.Now()
	r := server.AccountCheck{At: &now}
	switch {
	case err == nil:
		r.State = server.CheckOK
		if res != nil {
			r.Detail = res.Detail
		}
		log.Printf("account %q: check passed: %s", id, r.Detail)
	case err.Code == llm.CodeUnsupported:
		r.State, r.Detail = server.CheckNotChecked, err.Message
		if called {
			log.Printf("account %q: not checked: %s", id, err.Message)
		}
	default:
		shown := *err
		shown.Raw = nil // the upstream's reply body; the message says what failed
		r.State, r.Error = server.CheckFailed, &shown
		log.Printf("account %q: check failed: %s: %s", id, err.Code, err.Message)
	}
	e.result = r
}
