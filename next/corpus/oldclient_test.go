package corpus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The old core's requests of a management case name the fake upstream and the auth file's
// auth_index through placeholders; the runner fills both in.
func TestExpandOldRequests(t *testing.T) {
	var gotAuth string
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"x.json","auth_index":"0123abcd"}]}`))
	}))
	defer old.Close()

	c := &Case{
		Client: ClientRequest{Path: "/new"},
		OldClient: &ClientRequest{Path: "/v0/management/api-call", Auth: AuthManagementKey,
			Body: json.RawMessage(`{"auth_index":"{{auth_index}}","url":"{{upstream}}/usage","header":{"Authorization":"Bearer $TOKEN$"}}`)},
		OldClientMore: []ClientRequest{{Path: "/more?u={{upstream}}", Headers: map[string]string{"X-Index": "{{auth_index}}"}}},
	}
	reqs, err := expandOldRequests(context.Background(), c, old.URL, "http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+ManagementKey {
		t.Errorf("the auth-files list was asked with %q, want the management key", gotAuth)
	}
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2", len(reqs))
	}
	if want := `{"auth_index":"0123abcd","url":"http://127.0.0.1:9/usage","header":{"Authorization":"Bearer $TOKEN$"}}`; string(reqs[0].Body) != want {
		t.Errorf("first request body = %s, want %s", reqs[0].Body, want)
	}
	if reqs[1].Path != "/more?u=http://127.0.0.1:9" || reqs[1].Headers["X-Index"] != "0123abcd" {
		t.Errorf("second request = %+v", reqs[1])
	}
	if c.OldClient.Path != "/v0/management/api-call" || strings.Contains(string(reqs[0].Body), "{{") {
		t.Errorf("a placeholder was left in a request, or the case was changed")
	}
}

// Without an old_client the old core gets the case's client request, and no auth-files list is asked
// for (an old core with the management API off would answer 404).
func TestOldRequestsDefaultToClient(t *testing.T) {
	c := &Case{Client: ClientRequest{Path: "/v1/chat/completions"}}
	reqs := c.oldRequests()
	if len(reqs) != 1 || reqs[0].Path != "/v1/chat/completions" {
		t.Errorf("oldRequests = %+v, want the client request alone", reqs)
	}
	if c.UsesManagement() {
		t.Errorf("a case without a management key uses the management API")
	}
}

func TestUsesManagementWhenEitherCoreIsCalledWithTheKey(t *testing.T) {
	for name, c := range map[string]*Case{
		"client":     {Client: ClientRequest{Path: "/a", Auth: AuthManagementKey}},
		"old_client": {Client: ClientRequest{Path: "/a"}, OldClient: &ClientRequest{Path: "/b", Auth: AuthManagementKey}},
		"more":       {Client: ClientRequest{Path: "/a"}, OldClient: &ClientRequest{Path: "/b"}, OldClientMore: []ClientRequest{{Path: "/c", Auth: AuthManagementKey}}},
	} {
		if !c.UsesManagement() {
			t.Errorf("%s: the case does not use the management API", name)
		}
	}
}

func TestOldClientValidation(t *testing.T) {
	base := func() *Case {
		return &Case{Description: "d", Client: ClientRequest{Path: "/a"}}
	}
	c := base()
	c.OldClientMore = []ClientRequest{{Path: "/c"}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "old_client_more needs old_client") {
		t.Errorf("old_client_more without old_client: %v", err)
	}
	c = base()
	c.OldClient = &ClientRequest{}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("old_client without a path: %v", err)
	}
	c = base()
	c.OldClient = &ClientRequest{Path: "/b", WebSocket: []ClientStep{{Drop: true}}}
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "websocket") {
		t.Errorf("old_client with a websocket: %v", err)
	}
	c = base()
	c.OldClient = &ClientRequest{Path: "/b"}
	c.OldClientMore = []ClientRequest{{Path: "/c"}}
	if err := c.validate(); err != nil {
		t.Errorf("a valid old_client was refused: %v", err)
	}
}
