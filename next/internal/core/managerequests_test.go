package core

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

func TestManagementRequestsBehindRemoteGateAndLockout(t *testing.T) {
	calls := [][2]string{{http.MethodGet, "/requests"}, {http.MethodPost, "/requests/20260101000000-0000000000000000/cancel"}}
	from := func(h http.Handler, peer, key, method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, management.Prefix+target, nil)
		req.RemoteAddr = peer
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	local, _ := startManagementCore(t, managementTestConfig)
	if rec := manageCall(t, local, http.MethodGet, "/requests", ""); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"requests":[]}` {
		t.Errorf("GET /requests from this machine: %d %s, want an empty list", rec.Code, rec.Body)
	}
	if rec := manageCall(t, local, calls[1][0], calls[1][1], ""); rec.Code != http.StatusNotFound || errorCodeOf(t, rec) != "not_found" {
		t.Errorf("cancel of a trace nobody is serving: %d %s, want 404 not_found", rec.Code, rec.Body)
	}
	for _, c := range calls {
		if rec := from(local, "192.0.2.10:1", "mgmt-key", c[0], c[1]); rec.Code != http.StatusForbidden || errorCodeOf(t, rec) != "remote_disabled" {
			t.Errorf("%s %s from another machine: %d %s, want 403 remote_disabled", c[0], c[1], rec.Code, rec.Body)
		}
	}

	remote, _ := startManagementCore(t, strings.Replace(managementTestConfig, `{`, `{"management_allow_remote":true,`, 1))
	for _, c := range calls {
		if rec := from(remote, "192.0.2.20:1", "mgmt-key", c[0], c[1]); rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s from another machine with remote management on: %d %s", c[0], c[1], rec.Code, rec.Body)
		}
	}
	for range 5 {
		if rec := from(remote, "192.0.2.20:1", "wrong-key", http.MethodGet, "/requests"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong key: %d %s, want 401", rec.Code, rec.Body)
		}
	}
	for _, c := range calls {
		if rec := from(remote, "192.0.2.20:1", "mgmt-key", c[0], c[1]); rec.Code != http.StatusForbidden || errorCodeOf(t, rec) != "blocked" {
			t.Errorf("%s %s with the right key after five wrong ones: %d %s, want 403 blocked", c[0], c[1], rec.Code, rec.Body)
		}
	}
}
