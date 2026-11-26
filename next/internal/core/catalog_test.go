package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/catalogue"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The refresh after a credential is renewed, the account's kept list as accountModels reads it,
// and the management calls that read and refresh model lists. The refresh loop itself is not
// started here: safe mode leaves it out (startModelRefresh), and its due refreshes are covered by
// TestModelListRefreshTimerAndJitter in the route package.
func TestCredentialChangeRefreshesModelListAndManagementAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-management-key"}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	oldPoll := modelRefreshPoll
	modelRefreshPoll = 10 * time.Millisecond
	defer func() { modelRefreshPoll = oldPoll }()

	c, err := Start(Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	if _, err := c.creds.Create(cred.Record{
		ID:         "acct",
		Extension:  "x",
		State:      cred.StateActive,
		Credential: protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"},
	}); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		models = []string{"m1"}
		calls  = make(chan string, 8)
	)
	r := c.cur.Load().Router
	r.UseCatalog(func(_ context.Context, a route.Account, _ *protocol.Credential) (*protocol.ModelList, *llm.Error) {
		mu.Lock()
		cur := append([]string(nil), models...)
		mu.Unlock()
		out := make([]protocol.Model, len(cur))
		for i, id := range cur {
			out[i] = protocol.Model{ID: id}
		}
		calls <- a.ID
		return &protocol.ModelList{Models: out, From: protocol.ModelListFromAccount}, nil
	})
	if err := r.SetAccounts([]route.Account{{ID: "acct", Extension: "x"}}, nil); err != nil {
		t.Fatal(err)
	}
	// Prime the kept list.
	if got := r.Models(context.Background()); len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("initial models = %+v", got)
	}
	<-calls

	// Simulate credentialChanged (renewal / re-login) triggering a refresh in Core.
	mu.Lock()
	models = []string{"m1", "m2"}
	mu.Unlock()
	c.credentialChanged("acct")
	select {
	case id := <-calls:
		if id != "acct" {
			t.Fatalf("refreshed id = %q, want acct", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("credentialChanged did not refresh account model list")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := r.Models(context.Background())
		var ids []string
		for _, m := range got {
			ids = append(ids, m.ID)
		}
		if slices.Equal(ids, []string{"m1", "m2"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("models after credentialChanged = %v, want [m1 m2]", ids)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// accountModels reads the router's kept list for a routed account rather than bypassing it.
	s := c.cur.Load()
	acctModels, err := c.accountModels(context.Background(), s, "acct")
	if err != nil || len(acctModels.Models) != 2 || acctModels.Models[0].ID != "m1" || acctModels.Models[1].ID != "m2" || acctModels.From != protocol.ModelListFromAccount {
		t.Fatalf("accountModels = %+v, %v; want m1, m2 (from=account) from router's kept list", acctModels, err)
	}

	// Verify GET /accounts/{id}/models includes "from".
	h := c.Management()
	acctReq := httptest.NewRequest(http.MethodGet, server.ManagementPrefix+"/accounts/acct/models", nil)
	acctReq.Header.Set("Authorization", "Bearer fake-management-key")
	acctReq.RemoteAddr = "127.0.0.1:12345"
	acctRec := httptest.NewRecorder()
	h.ServeHTTP(acctRec, acctReq)
	if acctRec.Code != http.StatusOK {
		t.Fatalf("GET /accounts/acct/models: status %d %s", acctRec.Code, acctRec.Body.String())
	}
	var acctReply management.AccountModelsReply
	if err := json.Unmarshal(acctRec.Body.Bytes(), &acctReply); err != nil || acctReply.From != protocol.ModelListFromAccount || len(acctReply.Models) != 2 {
		t.Fatalf("GET /accounts/acct/models reply = %+v, err=%v", acctReply, err)
	}

	// Verify POST /models/refresh management handler validation and error codes.
	do := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, server.ManagementPrefix+"/models/refresh", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer fake-management-key")
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(`{"unknown":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: status %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := do(`{"account":"nobody"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown account: status %d %s, want 404", rec.Code, rec.Body.String())
	}
	if rec := do(""); rec.Code != http.StatusOK {
		t.Errorf("empty body: status %d %s, want 200", rec.Code, rec.Body.String())
	} else {
		var reply management.RefreshModelsReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatalf("decoding empty-body reply: %v", err)
		}
	}
}

func TestCatalogueRefreshUsesStateAge(t *testing.T) {
	const body = `{
		"openai": {"models": {"gpt-refresh-age": {
			"limit": {"context": 100000, "output": 10000},
			"tool_call": true
		}}}
	}`
	writeState := func(t *testing.T, path string, modTime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}
	start := func(t *testing.T, statePath string, interval time.Duration, now func() time.Time) *atomic.Int32 {
		t.Helper()
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		cat := catalogue.New(catalogue.Options{StatePath: statePath, URL: srv.URL, Timeout: time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		c := &Core{bgCtx: ctx, bgCancel: cancel, catalogue: cat, catalogueNow: now}
		c.bgWG.Add(1)
		go c.catalogueRefreshLoop(interval)
		t.Cleanup(func() {
			cancel()
			c.bgWG.Wait()
		})
		return &calls
	}

	t.Run("stale file refreshes soon after start without waiting for the poll", func(t *testing.T) {
		now := time.Now()
		statePath := filepath.Join(t.TempDir(), "state", "catalogue.json")
		writeState(t, statePath, now.Add(-25*time.Hour))
		calls := start(t, statePath, 2*time.Second, func() time.Time { return now })
		deadline := time.Now().Add(time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("refreshes after start with a stale file = %d, want 1", got)
		}
	})

	t.Run("fresh file is not refreshed by the poll", func(t *testing.T) {
		now := time.Now()
		statePath := filepath.Join(t.TempDir(), "state", "catalogue.json")
		writeState(t, statePath, now.Add(-time.Hour))
		calls := start(t, statePath, 25*time.Millisecond, func() time.Time { return now })
		time.Sleep(250 * time.Millisecond)
		if got := calls.Load(); got != 0 {
			t.Fatalf("refreshes of a fresh file = %d, want 0", got)
		}
	})

	t.Run("age passing 24 hours refreshes once", func(t *testing.T) {
		base := time.Now()
		var offset atomic.Int64
		now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
		statePath := filepath.Join(t.TempDir(), "state", "catalogue.json")
		writeState(t, statePath, base.Add(-time.Hour))

		var calls atomic.Int32
		refreshed := make(chan struct{}, 1)
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			refreshed <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(unblock)
		cat := catalogue.New(catalogue.Options{StatePath: statePath, URL: srv.URL, Timeout: time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		c := &Core{bgCtx: ctx, bgCancel: cancel, catalogue: cat, catalogueNow: now}
		c.bgWG.Add(1)
		go c.catalogueRefreshLoop(25 * time.Millisecond)
		t.Cleanup(func() {
			cancel()
			c.bgWG.Wait()
		})

		time.Sleep(250 * time.Millisecond)
		if got := calls.Load(); got != 0 {
			t.Fatalf("refreshes before the file is 24 hours old = %d, want 0", got)
		}
		offset.Store(int64(25 * time.Hour))
		select {
		case <-refreshed:
		case <-time.After(time.Second):
			t.Fatal("no refresh after the file passed 24 hours")
		}
		offset.Store(int64(time.Since(base)))
		unblock()
		time.Sleep(250 * time.Millisecond)
		if got := calls.Load(); got != 1 {
			t.Fatalf("refreshes after one successful stale refresh = %d, want 1", got)
		}
	})
}
