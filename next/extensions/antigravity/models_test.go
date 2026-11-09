package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const modelsPath = "/v1internal:fetchAvailableModels"

// modelIDs lists the models of an account served by srv, which answers the model list request
// with status and body (or its default reply when status is 0).
func modelIDs(t *testing.T, srv *agtest.Server, accountID string, status int, body string) (ids []string, list *protocol.ModelList) {
	t.Helper()
	if status != 0 {
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != modelsPath {
				return false
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, body)
			return true
		}
	}
	access, refresh := srv.Grant()
	list, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{ID: accountID, Settings: srv.Settings(), Credential: signedIn(access, refresh)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list.Models {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids, list
}

// TestListModels checks the list against what today's core lists for an Antigravity login whose
// Cloud Code answers each way (run against today's core with a fake Cloud Code), and the request
// the extension sends.
func TestListModels(t *testing.T) {
	// The reply the corpus's Cloud Code gives.
	srv := agtest.New(t)
	ids, list := modelIDs(t, srv, "corpus", 0, "")
	if want := []string{"claude-sonnet-5-5-high", "gemini-3.8-flash-high"}; !slices.Equal(ids, want) {
		t.Errorf("models %v, want %v", ids, want)
	}
	if list.OwnedBy != "antigravity" {
		t.Errorf("owned_by %q, want antigravity", list.OwnedBy)
	}
	want := protocol.Model{
		ID: "gemini-3.8-flash-high", DisplayName: "Gemini 3.8 Flash", Description: "Gemini 3.8 Flash (High)", OwnedBy: "antigravity",
		ContextLength: 1048576, MaxCompletionTokens: 65536,
		InputModalities: []string{"text", "image", "audio", "video"}, OutputModalities: []string{"text"},
	}
	if got := list.Models[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("gemini-3.8-flash-high is %+v, want %+v", got, want)
	}

	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Path != modelsPath {
		t.Fatalf("requests %+v, want one to %s", reqs, modelsPath)
	}
	if string(reqs[0].Body) != `{"project":"`+agtest.Project+`"}` || reqs[0].Header.Get("User-Agent") != userAgent ||
		reqs[0].Header.Get("Content-Type") != "application/json" || reqs[0].Header.Get("Authorization") == "" {
		t.Errorf("request %s, headers %v", reqs[0].Body, reqs[0].Header)
	}

	// A reply naming every catalog model and one more: the unknown one is not listed.
	all := `{"models":{"some-unknown-model":{},"claude-opus-4-6-thinking":{},"claude-sonnet-4-6":{},"claude-opus-5-5-high":{},` +
		`"claude-sonnet-5-5-high":{},"gemini-4-argon-high":{},"gemini-3.8-flash-high":{},"gemini-3-flash":{},` +
		`"gemini-3.1-flash-image":{},"gpt-oss-120b-medium":{},"gemini-3.1-flash-lite":{},"gemini-3.5-flash-lite":{}}}`
	ids, _ = modelIDs(t, agtest.New(t), "all", 200, all)
	if len(ids) != 11 || slices.Contains(ids, "some-unknown-model") {
		t.Errorf("models %v, want the 11 catalog models", ids)
	}

	// IDs match without case or surrounding spaces.
	ids, _ = modelIDs(t, agtest.New(t), "case", 200, `{"models":{"CLAUDE-OPUS-4-6-THINKING":{},"claude-sonnet-4-6":{}," claude-opus-5-5-high ":{}}}`)
	if want := []string{"claude-opus-4-6-thinking", "claude-opus-5-5-high", "claude-sonnet-4-6"}; !slices.Equal(ids, want) {
		t.Errorf("models %v, want %v", ids, want)
	}

	// Without a list from Cloud Code, an account has no models.
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"server error":      {500, `{"error":{"code":500}}`},
		"unauthenticated":   {401, `{"error":{"code":401}}`},
		"forbidden":         {403, `{"error":{"code":403}}`},
		"not found":         {404, `{"error":{"code":404}}`},
		"not JSON":          {200, `not json`},
		"no models object":  {200, `{"webSearchModelIds":["gemini-3-flash"]}`},
		"null models":       {200, `{"models":null}`},
		"empty models list": {200, `{"models":{}}`},
	} {
		if ids, _ := modelIDs(t, agtest.New(t), "none-"+name, tc.status, tc.body); len(ids) != 0 {
			t.Errorf("%s: models %v, want none", name, ids)
		}
	}
}

// TestListModelsKeepsLastList checks that a failed request lists what the account's last
// successful request listed.
func TestListModelsKeepsLastList(t *testing.T) {
	srv := agtest.New(t)
	access, refresh := srv.Grant()
	account := protocol.Account{ID: "keep", Settings: srv.Settings(), Credential: signedIn(access, refresh)}
	list := func() []string {
		l, err := listModels(context.Background(), sdk.ModelsCall{Account: account})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, m := range l.Models {
			ids = append(ids, m.ID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := list(); len(got) != 2 {
		t.Fatalf("first list %v", got)
	}
	srv.Revoke(access)
	if got := list(); !slices.Equal(got, []string{"claude-sonnet-5-5-high", "gemini-3.8-flash-high"}) {
		t.Errorf("list after a refused request %v, want the last one", got)
	}
	// An empty list is a list too: it replaces the last one.
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != modelsPath {
			return false
		}
		json.NewEncoder(w).Encode(map[string]any{"models": map[string]any{}})
		return true
	}
	if got := list(); len(got) != 0 {
		t.Errorf("list after an empty reply %v, want none", got)
	}
}

// A kept list unused for longer than lastModelsTTL is dropped: a failed request then lists none,
// and lists of other accounts that went unused are removed too.
func TestListModelsDropsOldLists(t *testing.T) {
	clock := time.Now()
	modelsNow = func() time.Time { return clock }
	t.Cleanup(func() { modelsNow = time.Now })

	srv := agtest.New(t)
	access, refresh := srv.Grant()
	call := func(id string) []protocol.Model {
		l, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{ID: id, Settings: srv.Settings(), Credential: signedIn(access, refresh)}})
		if err != nil {
			t.Fatal(err)
		}
		return l.Models
	}
	if len(call("old-a")) != 2 || len(call("old-b")) != 2 {
		t.Fatal("first lists are not the corpus's two models")
	}
	srv.Revoke(access)
	clock = clock.Add(lastModelsTTL - time.Minute)
	if len(call("old-a")) != 2 {
		t.Error("a list used within lastModelsTTL was not kept")
	}
	clock = clock.Add(2 * time.Minute) // old-b unused for longer than lastModelsTTL, old-a not
	call("other")                      // fails, and drops old-b's list
	lastMu.Lock()
	kept := len(lastModels)
	lastMu.Unlock()
	if kept != 1 {
		t.Errorf("%d kept lists, want old-a's only", kept)
	}
	if got := call("old-b"); len(got) != 0 {
		t.Errorf("old-b after lastModelsTTL unused: %v, want none", got)
	}
	clock = clock.Add(lastModelsTTL + time.Minute)
	if got := call("old-a"); len(got) != 0 {
		t.Errorf("list after lastModelsTTL unused: %v, want none", got)
	}
}
