package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	mgmt "github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

type singleEventStream struct {
	ev llm.Event
}

func (s *singleEventStream) Next() llm.Event { return s.ev }
func (s *singleEventStream) Close()          {}

func TestPatchConfigAccountUnban(t *testing.T) {
	cfg := &Config{
		Extension: ExtensionConfig{
			ID: "ext",
			Accounts: []route.Account{
				{ID: "cfg-acct", Extension: "ext"},
			},
		},
	}
	state := route.NewState()
	attempt := func(_ context.Context, _ string, _ protocol.Account, _ llm.Request) route.Stream {
		return &singleEventStream{ev: llm.Event{
			Type:  llm.EventError,
			Error: &llm.Error{Code: llm.CodeAccountClosed, Status: 400, Retryable: true, Scope: llm.ScopeCredential, Message: "This organization has been disabled."},
		}}
	}
	r, err := route.New(route.Config{}, cfg.Accounts(), attempt, state)
	if err != nil {
		t.Fatal(err)
	}
	s := r.Generate(context.Background(), route.Request{LLM: llm.Request{Model: "m"}})
	for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
	}
	s.Close()
	if !state.IsBanned("cfg-acct") {
		t.Fatal("cfg-acct was not banned by account_closed")
	}

	m := NewManagement(func() *Config { return cfg }, nil, state)

	for _, body := range []string{
		`{"disabled": true}`,
		`{"disabled": false, "priority": 2}`,
		`{"priority": 2}`,
	} {
		req := httptest.NewRequest(http.MethodPatch, "/accounts/cfg-acct", strings.NewReader(body))
		req.SetPathValue("id", "cfg-acct")
		rec := httptest.NewRecorder()
		m.patch(rec, req)
		if rec.Code != http.StatusConflict {
			t.Errorf("PATCH %s on cfg-acct: status = %d, want 409 (%s)", body, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPatch, "/accounts/cfg-acct", strings.NewReader(`{"disabled": false}`))
	req.SetPathValue("id", "cfg-acct")
	rec := httptest.NewRecorder()
	m.patch(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH disabled:false on cfg-acct: status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var reply mgmt.AccountReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Account.Health.State != mgmt.HealthOK || state.IsBanned("cfg-acct") {
		t.Fatalf("after PATCH disabled:false: account=%+v isBanned=%v, want ok and unbanned", reply.Account.Health, state.IsBanned("cfg-acct"))
	}
}
