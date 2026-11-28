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
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

type LoginMethods struct {
	Extension string                 `json:"extension"`
	Methods   []protocol.LoginMethod `json:"methods"`
}

type AccountModel = management.AccountModel

type TestResult = management.TestResult

const (
	testPrompt    = "Hi"
	testMaxTokens = 16
	testReplyMax  = 200
)

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
		writeJSON(w, http.StatusOK, management.CooldownResetReply{Account: v, Cleared: cleared})
	})
	m.Handle("GET /accounts/{id}/models", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		reply, err := c.accountModels(r.Context(), s, r.PathValue("id"))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
	m.Handle("GET /accounts/{id}/quota", func(w http.ResponseWriter, r *http.Request) {
		s, release := c.acquire()
		defer release()
		reply, err := c.accountQuota(r.Context(), s, r.PathValue("id"))
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, reply)
	})
	m.Handle("POST /accounts/{id}/test", func(w http.ResponseWriter, r *http.Request) {
		var req management.TestRequest
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
		writeJSON(w, http.StatusOK, management.TestReply{Result: res})
	})
}

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

func (c *Core) accountModels(ctx context.Context, s *snapshot, id string) (management.AccountModelsReply, error) {
	a, ok := c.account(s, id)
	if !ok {
		return management.AccountModelsReply{}, fmt.Errorf("%w %q", errNoAccount, id)
	}
	var (
		list []protocol.Model
		from string
	)
	if len(a.Models) > 0 {
		for _, m := range a.Models {
			list = append(list, protocol.Model{ID: m.Name})
		}
	} else {
		cl, err := c.listModels(ctx, s, a)
		if err != nil {
			return management.AccountModelsReply{}, err
		}
		list = cl.Models
		from = cl.From
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
	return management.AccountModelsReply{Models: out, From: from}, nil
}

func (c *Core) listModels(ctx context.Context, s *snapshot, a route.Account) (*protocol.ModelList, error) {
	if s.Router != nil {
		if list := s.Router.AccountList(ctx, a.ID); list != nil {
			return list, nil
		}
	}
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

func (c *Core) accountQuota(ctx context.Context, s *snapshot, id string) (*management.QuotaReply, error) {
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
		q, e := g.Quota(ctx, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: cr, ProxyURL: a.ProxyURL, Headers: a.Headers})
		if e == nil {
			at := time.Now().UTC()
			c.state.NoteQuota(a.ID, q, at)
			return &management.QuotaReply{FetchedAt: at, Quota: q}, nil
		}
		if e.Code == llm.CodeAuth && a.Managed && try == 0 && c.creds.Rejected(ctx, a.ID, version) {
			continue
		}
		return nil, e
	}
}

func (c *Core) testAccount(ctx context.Context, s *snapshot, id, model string) (TestResult, error) {
	a, ok := c.account(s, id)
	if !ok {
		return TestResult{}, fmt.Errorf("%w %q", errNoAccount, id)
	}
	if model == "" {
		reply, err := c.accountModels(ctx, s, id)
		if err != nil {
			return TestResult{}, err
		}
		for _, m := range reply.Models {
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

func runTest(ctx context.Context, s *snapshot, a route.Account, cr *protocol.Credential, req llm.Request) TestResult {
	var res TestResult
	st := s.Attempt(ctx, a.Extension, protocol.Account{ID: a.ID, Settings: a.Settings, Credential: cr, ProxyURL: a.ProxyURL, Headers: a.Headers}, req)
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
