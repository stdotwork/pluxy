package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type catalogCredentials struct{ renewals int }

func (c *catalogCredentials) Credential(context.Context, string, string) (*protocol.Credential, uint64, *llm.Error) {
	c.renewals++
	return nil, 0, &llm.Error{Code: llm.CodeAuth, Message: "credential renewal called"}
}
func (*catalogCredentials) Rejected(context.Context, string, uint64) bool { return false }
func (*catalogCredentials) StoredCredential(string, string) (*protocol.Credential, *llm.Error) {
	return &protocol.Credential{Kind: protocol.CredentialOAuth}, nil
}

func catalogGET(t *testing.T, g *Generation) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/v1/models?client_version=0.153.0", nil)
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	NewHandler(fixedSource{g}).ServeHTTP(w, r)
	return w
}

func catalogGeneration(t *testing.T, accounts []route.Account) (*Generation, *route.State, *[]string, *catalogCredentials) {
	t.Helper()
	state := route.NewState()
	var attempts []string
	gen := func(_ context.Context, _ string, a protocol.Account, _ llm.Request) route.Stream {
		attempts = append(attempts, a.ID)
		return &events{evs: []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}}, {Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}}}
	}
	router, err := route.New(route.Config{MaxAttempts: 1}, accounts, gen, state)
	if err != nil {
		t.Fatal(err)
	}
	creds := &catalogCredentials{}
	router.UseCredentials(creds)
	router.UseCatalog(func(context.Context, route.Account, *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		return &protocol.ModelList{Models: []protocol.Model{{ID: "gpt-6.1-sol"}}}, nil
	})
	g := &Generation{Config: &Config{ClientKeys: []string{"k"}}, Router: router, Attempt: gen, Usage: usage.NewStore()}
	g.Endpoint = func(_ context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		if ext != "pluxy/codex" {
			return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true}}}}
		}
		if a.Credential != nil || len(a.Settings) != 0 || a.ID != "c" && a.ID != "managed" {
			t.Errorf("catalog endpoint account = %+v; want the first Codex account ID only", a)
		}
		if req.Model != "" {
			t.Errorf("catalog endpoint model = %q", req.Model)
		}
		evs := bodyReply("application/json", `{"models":[]}`)
		evs[0].Response.Format = "codex.models"
		return &events{evs: evs}
	}
	return g, state, &attempts, creds
}

func TestCatalogWithoutCodexAccounts(t *testing.T) {
	for _, accounts := range [][]route.Account{nil, {{ID: "other", Extension: "other"}}} {
		g, _, _, _ := catalogGeneration(t, accounts)
		w := catalogGET(t, g)
		if w.Code != 200 || w.Body.String() != `{"models":[]}` {
			t.Errorf("catalog without Codex: status %d body %s", w.Code, w.Body)
		}
	}
}

func TestCatalogBypassesRouting(t *testing.T) {
	for _, scenario := range []string{"cooldown", "busy", "older quota", "round robin", "other first", "endpoint failure"} {
		t.Run(scenario, func(t *testing.T) {
			a := route.Account{ID: "c", Extension: "pluxy/codex", Models: []route.ModelEntry{{Name: "gpt-6.1-sol"}}, MaxConcurrency: 1}
			if scenario == "older quota" {
				a.MaxConcurrency = 0
			}
			accounts := []route.Account{a}
			if scenario == "round robin" {
				accounts = append(accounts, route.Account{ID: "d", Extension: "pluxy/codex", Models: a.Models})
			}
			if scenario == "other first" {
				accounts = append([]route.Account{{ID: "other", Extension: "other", Models: a.Models}}, accounts...)
			}
			g, state, attempts, creds := catalogGeneration(t, accounts)
			if scenario == "endpoint failure" {
				g.Endpoint = func(context.Context, string, protocol.Account, llm.Request) route.Stream {
					return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeQuota, Status: 429, Scope: llm.ScopeCredential, RetryAfterMS: 3600000}}}}
				}
			}
			req := route.Request{LLM: llm.Request{Model: "gpt-6.1-sol"}}
			if scenario == "round robin" {
				req.LLM.Model = ""
				req.AnyModel = true
			}
			var older route.Stream
			if scenario == "cooldown" || scenario == "older quota" {
				req.Attempt = func(context.Context, string, protocol.Account, llm.Request) route.Stream {
					return &events{evs: []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}}, {Type: llm.EventError, Error: &llm.Error{Code: llm.CodeQuota, Scope: llm.ScopeCredential, RetryAfterMS: 3600000}}}}
				}
				older = g.Router.Generate(t.Context(), req)
				older.Next()
				if scenario == "cooldown" {
					older.Next()
					older.Close()
					older = nil
				}
			} else if scenario == "busy" || scenario == "round robin" {
				older = g.Router.Generate(t.Context(), req)
				older.Next()
				if scenario == "round robin" {
					older.Next()
					older.Close()
					older = nil
				}
			}
			before := state.Runtime(a, time.Now())
			w := catalogGET(t, g)
			wantStatus := 200
			if scenario == "endpoint failure" {
				wantStatus = 429
			}
			if w.Code != wantStatus {
				t.Errorf("catalog during %s: status %d body %s", scenario, w.Code, w.Body)
			}
			if after := state.Runtime(a, time.Now()); !reflect.DeepEqual(before, after) {
				t.Errorf("catalog changed routing state: before=%+v after=%+v", before, after)
			}
			if n := len(g.Usage.Events(usage.Filter{}, 0, 100).Events); n != 0 {
				t.Errorf("catalog recorded %d usage events", n)
			}
			if creds.renewals != 0 {
				t.Errorf("catalog renewed credentials %d times", creds.renewals)
			}
			if older != nil {
				defer older.Close()
				ev := older.Next()
				if scenario == "older quota" && (ev.Type != llm.EventError || len(state.Runtime(a, time.Now()).Cooldowns) == 0) {
					t.Error("catalog success masked older quota failure")
				}
			}
			if scenario == "round robin" {
				s := g.Router.Generate(t.Context(), req)
				s.Next()
				s.Close()
				if !reflect.DeepEqual(*attempts, []string{"c", "d"}) {
					t.Errorf("catalog moved round robin: attempts %v", *attempts)
				}
			}
		})
	}
}

func TestCatalogDoesNotRenewForNames(t *testing.T) {
	g, _, _, creds := catalogGeneration(t, []route.Account{{ID: "managed", Extension: "pluxy/codex", Managed: true}})
	w := catalogGET(t, g)
	if w.Code != 200 || creds.renewals != 0 {
		t.Fatalf("catalog status=%d credential renewals=%d", w.Code, creds.renewals)
	}
}

func TestCatalogSharedNamesAndOptionalDisplay(t *testing.T) {
	g, _, _, _ := catalogGeneration(t, []route.Account{
		{ID: "other", Extension: "other", Models: []route.ModelEntry{{Name: "different", Alias: "shared"}}},
		{ID: "c", Extension: "pluxy/codex", ModelAlias: []route.ModelAlias{{Name: "gpt-6.1-sol", Alias: "shared", Fork: true}}},
	})
	g.Endpoint = func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
		var body struct{ Models []map[string]any }
		if err := json.Unmarshal(req.Native.Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Models) != 2 {
			t.Fatalf("catalog names = %v", body.Models)
		}
		for _, m := range body.Models {
			if _, ok := m["display_name"]; ok {
				t.Error("empty display_name was sent")
			}
			want := m["name"] == "shared"
			if got, _ := m["non_codex"].(bool); got != want {
				t.Errorf("name %v non_codex=%v want %v", m["name"], got, want)
			}
		}
		evs := bodyReply("application/json", `{"models":[]}`)
		evs[0].Response.Format = "codex.models"
		return &events{evs: evs}
	}
	if w := catalogGET(t, g); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
}
