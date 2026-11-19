package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
)

// No error that reaches a client names an account by the ID it is configured under or the ID a
// person typed when adding it: either can be an email address or a label. The account is named by
// its public ID in the X-Pluxy-Account header and the pluxy error member only. These tests make
// the credential store refuse accounts in each way a client can meet (removed or disabled while a
// request waits on a renewal, a renewal that failed, a grant the provider refused) and read
// everything core sent back.

const (
	configEmailID = "someone@example.com" // a config account's ID may be anything
	loginLabelID  = "jane-example.com"    // an ID typed at login: letters, digits, '.', '_' and '-'
)

// startLeakCore runs core with the example OAuth provider and a config account named like an email
// address that the provider answers with a 429, so that the account added by login (tried second)
// is the one whose refusal the client reads last.
func startLeakCore(t *testing.T, srv *oauthtest.Server) string {
	t.Helper()
	return runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": configEmailID, "priority": 1,
				"settings":   map[string]string{"base_url": srv.URL},
				"credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("rate_limited")},
			}},
		},
	})
}

// leakReply is what one client request got back, flattened for a search.
type leakReply struct {
	status  int
	message string // error.message of an error body
	all     string // every header and the body
}

func readLeakReply(t *testing.T, resp *http.Response) leakReply {
	t.Helper()
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var b strings.Builder
	for name, vs := range resp.Header {
		for _, v := range vs {
			b.WriteString(name + ": " + v + "\n")
		}
	}
	b.Write(raw)
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return leakReply{status: resp.StatusCode, message: e.Error.Message, all: b.String()}
}

func leakChat(t *testing.T, base string) leakReply {
	t.Helper()
	return readLeakReply(t, post(t, context.Background(), base, clientKey, plainBody))
}

// noRealIDs fails the test when the reply holds an account's real ID or an '@', or is not an error
// that says something.
func noRealIDs(t *testing.T, what string, r leakReply) {
	t.Helper()
	if r.status < 400 || r.message == "" {
		t.Errorf("%s: status %d, message %q; want an error with a message\n%s", what, r.status, r.message, r.all)
	}
	for _, id := range []string{configEmailID, loginLabelID, "someone", "jane-example"} {
		if strings.Contains(r.all, id) {
			t.Errorf("%s: the reply holds %q:\n%s", what, id, r.all)
		}
	}
	if strings.Contains(r.all, "@") {
		t.Errorf("%s: the reply holds an '@':\n%s", what, r.all)
	}
}

// expiring is an AccessTTL whose first token (the login's) is already due for renewal.
func expiring(n int) time.Duration {
	if n == 1 {
		return time.Second
	}
	return time.Hour
}

// A renewal that fails, or that the provider refuses, leaves the account's token expired: the
// client's error does not name the account, on the request that tried the renewal and on the ones
// after it, which meet the account's state instead.
func TestClientErrorsOfRenewalDoNotNameTheAccount(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"unavailable", "invalid_grant"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			srv := oauthtest.New(t)
			srv.AccessTTL = expiring
			base := startLeakCore(t, srv)
			deviceLogin(t, base, srv, loginLabelID)
			time.Sleep(1200 * time.Millisecond) // the first token has expired
			srv.RefreshFailure = failure
			for i := range 3 {
				r := leakChat(t, base)
				noRealIDs(t, failure+" request "+string(rune('1'+i)), r)
			}
		})
	}
}

// An account removed or disabled while a request waits on its renewal: the request fails, and its
// error does not name the account.
func TestClientErrorsOfRemovedAndDisabledDoNotNameTheAccount(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		want []string // the message says one of these
		act  func(t *testing.T, base string)
	}{
		{"removed", []string{"was removed", "not in the credential store"}, func(t *testing.T, base string) {
			if code := manage(t, base, "DELETE", "/accounts/"+loginLabelID, nil, nil); code != http.StatusNoContent {
				t.Fatalf("removing: status %d", code)
			}
		}},
		{"disabled", []string{"is disabled"}, func(t *testing.T, base string) {
			if code := manage(t, base, "PATCH", "/accounts/"+loginLabelID, map[string]any{"disabled": true}, nil); code != http.StatusOK {
				t.Fatalf("disabling: status %d", code)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := oauthtest.New(t)
			srv.AccessTTL = expiring
			base := startLeakCore(t, srv)
			deviceLogin(t, base, srv, loginLabelID)

			release := srv.BlockRefresh()
			defer release()
			done := make(chan leakReply, 1)
			go func() { done <- leakChat(t, base) }()
			select {
			case <-srv.RefreshStarted():
			case <-time.After(10 * time.Second):
				t.Fatal("no refresh reached the provider")
			}
			c.act(t, base)
			release()
			r := <-done
			noRealIDs(t, c.name, r)
			if !slices.ContainsFunc(c.want, func(w string) bool { return strings.Contains(r.message, w) }) {
				t.Errorf("%s: message %q says none of %q", c.name, r.message, c.want)
			}
		})
	}
}
