package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestModelRefreshLoopAndCredentialChange(t *testing.T) {
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
		return &protocol.ModelList{Models: out}, nil
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
	if err != nil || len(acctModels) != 2 || acctModels[0].ID != "m1" || acctModels[1].ID != "m2" {
		t.Fatalf("accountModels = %+v, %v; want m1, m2 from router's kept list", acctModels, err)
	}

	// Verify POST /models/refresh management handler validation and error codes.
	h := c.Management()
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
