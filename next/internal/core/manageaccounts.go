package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// LoginMethods lists one extension's ways of signing in to an account.
type LoginMethods struct {
	Extension string                 `json:"extension"`
	Methods   []protocol.LoginMethod `json:"methods"`
}

// AccountModel is one model an account serves, as GET /accounts/{id}/models lists it: the
// model by its upstream name, with what the extension says about it, and the names clients ask
// for it by after the account's exclusions, aliases and prefix.
type AccountModel struct {
	protocol.Model
	// Names are the names clients use for the model on this account; empty when the account's
	// excluded_models leave it out.
	Names []string `json:"names"`
	// Excluded is true when the account's excluded_models leave the model out.
	Excluded bool `json:"excluded,omitempty"`
}

// TestResult is the outcome of POST /accounts/{id}/test.
type TestResult struct {
	OK bool `json:"ok"`
	// Model is the model the request named; Served is the one that answered, when the
	// upstream said.
	Model  string `json:"model"`
	Served string `json:"served_model,omitempty"`
	// LatencyMS is the time from sending the request to its last event.
	LatencyMS int64 `json:"latency_ms"`
	// Reply is the start of the model's text reply.
	Reply        string     `json:"reply,omitempty"`
	FinishReason string     `json:"finish_reason,omitempty"`
	Usage        *llm.Usage `json:"usage,omitempty"`
	Error        *llm.Error `json:"error,omitempty"`
}

// testPrompt is what an account test sends, and testMaxTokens the longest reply it asks for:
// a request as small as the old panels' connection test.
const (
	testPrompt    = "Hi"
	testMaxTokens = 16
	testReplyMax  = 200 // runes of the reply kept
)

// handleAccounts adds the account calls that need the running extensions or routing state:
//
//	GET  /v0/management/login-methods          each extension's login methods and their inputs
//	POST /v0/management/accounts/{id}/cooldown/reset  clear the account's cooldowns
//	GET  /v0/management/accounts/{id}/models   the models the account serves
//	POST /v0/management/accounts/{id}/test     send the account one small request
//	GET  /v0/management/accounts/{id}/quota    the account's plan and usage, from its provider
func (c *Core) handleAccounts(m *server.Management) {
	m.Handle("GET /login-methods", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		out := []LoginMethods{}
		for _, id := range slices.Sorted(maps.Keys(s.exts)) {
			info := s.exts[id].Info()
			if slices.Contains(info.Operations, protocol.OpLogin) {
				out = append(out, LoginMethods{Extension: id, Methods: info.LoginMethods})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"extensions": out})
	})
	m.Handle("POST /accounts/{id}/cooldown/reset", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		a, ok := c.account(s, r.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("no account %q", r.PathValue("id")))
			return
		}
		cleared := c.state.Reset(a, time.Now())
		v, _ := m.View(a.ID)
		writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared, "account": v})
	})
	m.Handle("GET /accounts/{id}/models", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		models, err := c.accountModels(r.Context(), s, r.PathValue("id"))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": models})
	})
	m.Handle("GET /accounts/{id}/quota", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		q, err := c.accountQuota(r.Context(), s, r.PathValue("id"))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"quota": q, "fetched_at": time.Now().UTC()})
	})
	m.Handle("POST /accounts/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if r.ContentLength != 0 && !readJSON(w, r, &req) {
			return
		}
		s, release := c.acquire()
		defer release()
		res, err := c.testAccount(r.Context(), s, r.PathValue("id"), strings.TrimSpace(req.Model))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": res})
	})
}

// errNoAccount and errNoModel are the account calls' own refusals.
var (
	errNoAccount = errors.New("no such account")
	errNoModel   = errors.New("no model to test with")
)

func writeAccountError(w http.ResponseWriter, err error) {
	var le *llm.Error
	switch {
	case errors.As(err, &le):
		code := http.StatusBadGateway
		switch le.Code {
		case llm.CodeExtensionUnavailable:
			code = http.StatusServiceUnavailable
		case llm.CodeUnsupported:
			code = http.StatusUnprocessableEntity
		}
		writeJSON(w, code, map[string]any{"error": route.Brief(le)})
	case errors.Is(err, errNoAccount):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, errNoModel):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

// account returns the account with ID id in snapshot s: the config's account of that ID, else
// the one added by login (Managed) with its extension's defaults for logins, whether or not it
// receives requests now.
func (c *Core) account(s *snapshot, id string) (route.Account, bool) {
	for _, a := range s.Config.Accounts() {
		if a.ID == id {
			return a, true
		}
	}
	for _, rec := range c.creds.Records() {
		if rec.ID == id {
			a := rec.Account
			a.ID, a.Extension, a.Managed, a.Credential = rec.ID, rec.Extension, true, nil
			return c.extensionConfig(s, a.Extension).LoginDefaults(a, rec.Credential.Kind), true
		}
	}
	return route.Account{}, false
}

// credential returns the credential an attempt on a presents now, and its version for a
// managed account. A managed account's is renewed first when it is due.
func (c *Core) credential(ctx context.Context, a route.Account) (*protocol.Credential, uint64, error) {
	if !a.Managed {
		return a.Credential, 0, nil
	}
	cr, v, e := c.creds.Credential(ctx, a.Extension, a.ID)
	if e != nil {
		return nil, 0, e
	}
	return cr, v, nil
}

// accountModels returns the models account id serves: its config's models when it lists them,
// else the ones its extension lists for it.
func (c *Core) accountModels(ctx context.Context, s *snapshot, id string) ([]AccountModel, error) {
	a, ok := c.account(s, id)
	if !ok {
		return nil, fmt.Errorf("%w %q", errNoAccount, id)
	}
	var list []protocol.Model
	if len(a.Models) > 0 {
		for _, m := range a.Models {
			list = append(list, protocol.Model{ID: m.Name})
		}
	} else {
		cl, err := c.listModels(ctx, s, a)
		if err != nil {
			return nil, err
		}
		list = cl.Models
		for i := range list {
			if list[i].OwnedBy == "" {
				list[i].OwnedBy = cl.OwnedBy
			}
		}
	}
	ids := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, m := range list {
		if !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	byUpstream := map[string][]string{}
	for _, n := range a.Names(ids, s.Config.Routing.ForceModelPrefix) {
		byUpstream[n.Upstream] = append(byUpstream[n.Upstream], n.Public)
	}
	out := []AccountModel{}
	seen = map[string]bool{}
	for _, m := range list {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		names := byUpstream[m.ID]
		if names == nil {
			names = []string{}
		}
		out = append(out, AccountModel{Model: m, Names: names, Excluded: len(names) == 0})
	}
	return out, nil
}

// listModels asks account a's extension for its model list now (the protocol's models call),
// rather than reading the list routing keeps, so the management view is current.
func (c *Core) listModels(ctx context.Context, s *snapshot, a route.Account) (*protocol.ModelList, error) {
	cr, _, err := c.credential(ctx, a)
	if err != nil {
		return nil, err
	}
	list, e := catalog(s.exts)(ctx, a, cr)
	if e != nil {
		return nil, e
	}
	return list, nil
}

// accountQuota reads account id's quota through its extension. A credential the upstream refuses
// is renewed and tried once more, as for a request.
func (c *Core) accountQuota(ctx context.Context, s *snapshot, id string) (*protocol.Quota, error) {
	a, ok := c.account(s, id)
	if !ok {
		return nil, fmt.Errorf("%w %q", errNoAccount, id)
	}
	g := s.exts[a.Extension]
	if g == nil {
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", a.Extension)}
	}
	for try := 0; ; try++ {
		cr, version, err := c.credential(ctx, a)
		if err != nil {
			return nil, err
		}
		q, e := g.Quota(ctx, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: cr})
		if e == nil {
			return q, nil
		}
		if e.Code == llm.CodeAuth && a.Managed && try == 0 && c.creds.Rejected(ctx, a.ID, version) {
			continue
		}
		return nil, e
	}
}

// testAccount sends account id one small request for model, straight to its extension: routing,
// its cooldowns and its state are left as they are. With no model it uses the first the account
// serves. A credential the upstream refuses is renewed and tried once more, as for a request.
func (c *Core) testAccount(ctx context.Context, s *snapshot, id, model string) (TestResult, error) {
	a, ok := c.account(s, id)
	if !ok {
		return TestResult{}, fmt.Errorf("%w %q", errNoAccount, id)
	}
	if model == "" {
		models, err := c.accountModels(ctx, s, id)
		if err != nil {
			return TestResult{}, err
		}
		for _, m := range models {
			if !m.Excluded {
				model = m.ID
				break
			}
		}
		if model == "" {
			return TestResult{}, fmt.Errorf("%w: account %q lists no models; send model", errNoModel, id)
		}
	}
	maxTokens := testMaxTokens
	req := llm.Request{Model: model, Items: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleUser,
		Content: []llm.Part{{Type: llm.PartText, Text: testPrompt}}}}, Options: llm.Options{MaxOutputTokens: &maxTokens}}
	res := TestResult{Model: model}
	start := time.Now()
	for try := 0; ; try++ {
		cr, version, err := c.credential(ctx, a)
		if err != nil {
			var le *llm.Error
			if !errors.As(err, &le) {
				return TestResult{}, err
			}
			res.Error = route.Brief(le)
			break
		}
		res = runTest(ctx, s, a, cr, req)
		if res.Error != nil && res.Error.Code == llm.CodeAuth && a.Managed && try == 0 && c.creds.Rejected(ctx, a.ID, version) {
			continue
		}
		break
	}
	res.Model, res.LatencyMS = model, time.Since(start).Milliseconds()
	res.OK = res.Error == nil
	return res, nil
}

// runTest sends req to account a with credential cr and reads the reply.
func runTest(ctx context.Context, s *snapshot, a route.Account, cr *protocol.Credential, req llm.Request) TestResult {
	var res TestResult
	st := s.Attempt(ctx, a.Extension, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: cr}, req)
	defer st.Close()
	var reply strings.Builder
	for {
		ev := st.Next()
		switch ev.Type {
		case llm.EventResponseStart:
			if ev.Response != nil {
				res.Served = ev.Response.Model
			}
		case llm.EventTextDelta:
			if utf8.RuneCountInString(reply.String()) < testReplyMax {
				reply.WriteString(ev.Delta)
			}
		case llm.EventFinish:
			if ev.Finish != nil {
				res.FinishReason = ev.Finish.Reason
			}
			res.Usage = ev.Usage
		case llm.EventError:
			res.Error = route.Brief(ev.Error)
			if res.Error == nil {
				res.Error = &llm.Error{Code: llm.CodeInternal, Message: "the attempt failed without an error"}
			}
		}
		if ev.Terminal() {
			break
		}
	}
	res.Reply = truncateRunes(reply.String(), testReplyMax)
	if res.Usage != nil {
		u := *res.Usage
		u.Raw = nil
		res.Usage = &u
	}
	return res
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
