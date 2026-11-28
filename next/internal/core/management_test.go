package core

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

// startManagementCore starts core in safe mode on the config body and returns its management API
// and the config file's path.
func startManagementCore(t *testing.T, config string) (http.Handler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c.Management(), path
}

// manageCall calls the management API from this machine, as pluxy does.
func manageCall(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, management.Prefix+target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer mgmt-key")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// errorCodeOf returns the error code in a failed call's body.
func errorCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body management.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("reply %q is not an error body: %v", rec.Body, err)
	}
	return body.Error.Code
}

const managementTestConfig = `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`

// GET /status says which version of the management API core serves, as docs/management-api.md
// ("Rules every call follows") promises: a number that goes up when a call changes in a way an
// older client would misread.
func TestStatusCarriesTheManagementAPIVersion(t *testing.T) {
	h, _ := startManagementCore(t, managementTestConfig)
	rec := manageCall(t, h, http.MethodGet, "/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /status = %d (%s)", rec.Code, rec.Body)
	}
	var body struct {
		ManagementAPI *int `json:"management_api"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %s: %v", rec.Body, err)
	}
	if body.ManagementAPI == nil {
		t.Fatalf("GET /status has no management_api: %s", rec.Body)
	}
	if *body.ManagementAPI != management.APIVersion {
		t.Errorf("management_api = %d, want management.APIVersion %d", *body.ManagementAPI, management.APIVersion)
	}
	if management.APIVersion < 1 {
		t.Errorf("management.APIVersion = %d, want at least 1", management.APIVersion)
	}
}

// A body larger than core reads is a 413 invalid_request from every call that reads one, not a
// 400 from some of them.
func TestManagementOversizedBodyIs413(t *testing.T) {
	h, _ := startManagementCore(t, managementTestConfig)
	tooBig := strings.Repeat("x", maxManagementBody+1)
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/packages"},
		{http.MethodPatch, "/extensions/pluxy/claude/config"},
		{http.MethodPost, "/models/refresh"},
		{http.MethodPost, "/config/validate"},
		{http.MethodPut, "/config"},
		{http.MethodPatch, "/settings"},
		{http.MethodPut, "/chains/gpt-6.1-sol"},
		{http.MethodPost, "/setup/apply"},
		{http.MethodPost, "/setup/import"},
		{http.MethodPut, "/theme"},
		{http.MethodPost, "/accounts"},
		{http.MethodPatch, "/accounts/a"},
		{http.MethodPost, "/accounts/a/login"},
		{http.MethodPost, "/accounts/a/test"},
		{http.MethodPost, "/logins/login-1/input"},
	} {
		rec := manageCall(t, h, call.method, call.path, tooBig)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s with a %d-byte body = %d (%s), want 413", call.method, call.path, len(tooBig), rec.Code, rec.Body)
			continue
		}
		if code := errorCodeOf(t, rec); code != "invalid_request" {
			t.Errorf("%s %s: error code = %q, want invalid_request", call.method, call.path, code)
		}
	}
}

// GET /setup/clients/{id}/paste names the address core is reached at: a caller on this machine
// gets core's own, as the command shows it, and a caller on another machine the address it
// reached core by, from its Host and whether the connection is TLS.
func TestClientPasteBaseURLIsTheAddressThatReachedCore(t *testing.T) {
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:8317","management_allow_remote":true,`+
		`"client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	paste := management.Prefix + "/setup/clients/claude-code/paste"
	get := func(target, remote string, holdTLS bool, headers map[string]string) management.ClientPaste {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RemoteAddr = remote
		req.Header.Set("Authorization", "Bearer mgmt-key")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if holdTLS {
			req.TLS = &tls.ConnectionState{}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s from %s = %d (%s)", target, remote, rec.Code, rec.Body)
		}
		var p management.ClientPaste
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatalf("reply %s: %v", rec.Body, err)
		}
		return p
	}

	// This machine, as before.
	if p := get("http://127.0.0.1:8317"+paste, "127.0.0.1:1234", false, nil); p.BaseURL != "http://127.0.0.1:8317" {
		t.Errorf("a caller on this machine: base_url = %q, want core's own address", p.BaseURL)
	}
	// Another machine reaches core at the address it used, and its files point there.
	for _, c := range []struct {
		target, remote string
		holdTLS        bool
		want           string
	}{
		{"http://core.example.test:8443" + paste, "203.0.113.5:4321", false, "http://core.example.test:8443"},
		{"https://core.example.test" + paste, "203.0.113.5:4321", true, "https://core.example.test"},
	} {
		p := get(c.target, c.remote, c.holdTLS, nil)
		if p.BaseURL != c.want {
			t.Errorf("a caller at %s: base_url = %q, want %q", c.target, p.BaseURL, c.want)
		}
		for _, f := range p.Files {
			if !strings.Contains(f.Content, c.want) {
				t.Errorf("%s does not point at %q:\n%s", f.Path, c.want, f.Content)
			}
		}
	}
	// A proxy on this machine passing another machine's request on is that machine's caller.
	p := get("http://core.example.test:8443"+paste, "127.0.0.1:1234", false, map[string]string{"X-Forwarded-For": "203.0.113.5"})
	if p.BaseURL != "http://core.example.test:8443" {
		t.Errorf("a caller passed on from another machine: base_url = %q, want the address it reached", p.BaseURL)
	}
	// A Host that is not an address core can name is refused rather than guessed at.
	req := httptest.NewRequest(http.MethodGet, paste, nil)
	req.Host = "not an address"
	req.RemoteAddr = "203.0.113.5:4321"
	req.Header.Set("Authorization", "Bearer mgmt-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || errorCodeOf(t, rec) != "invalid_request" {
		t.Errorf("a Host that is not an address = %d (%s), want 400 invalid_request", rec.Code, rec.Body)
	}
}

// The routing timeouts are part of GET and PATCH /settings, checked as the config checks them:
// each field set on its own, null removing them all, and a bad one changing nothing.
func TestSettingsRoutingTimeouts(t *testing.T) {
	h, path := startManagementCore(t, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],`+
		`"management_key":"mgmt-key","routing":{"timeouts":{"header_ms":15000}}}`)
	// timeouts reads GET /settings's routing.timeouts as a client sees it, fields named.
	timeouts := func() map[string]int64 {
		t.Helper()
		rec := manageCall(t, h, http.MethodGet, "/settings", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d (%s)", rec.Code, rec.Body)
		}
		var s struct {
			Routing struct {
				Timeouts *map[string]int64 `json:"timeouts"`
			} `json:"routing"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
			t.Fatalf("settings %s: %v", rec.Body, err)
		}
		if s.Routing.Timeouts == nil {
			return nil
		}
		return *s.Routing.Timeouts
	}
	same := func(got, want map[string]int64) bool {
		if len(got) != len(want) {
			return false
		}
		for k, v := range want {
			if g, ok := got[k]; !ok || g != v {
				return false
			}
		}
		return true
	}

	// GET shows what the config sets.
	if got, want := timeouts(), map[string]int64{"header_ms": 15000}; !same(got, want) {
		t.Errorf("GET /settings routing.timeouts = %v, want the config's %v", got, want)
	}

	// PATCH changes the fields it names and leaves the rest.
	rec := manageCall(t, h, http.MethodPatch, "/settings", `{"routing":{"timeouts":{"idle_ms":90000,"total_ms":0}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /settings routing.timeouts = %d (%s)", rec.Code, rec.Body)
	}
	if got, want := timeouts(), map[string]int64{"header_ms": 15000, "idle_ms": 90000, "total_ms": 0}; !same(got, want) {
		t.Errorf("after the PATCH routing.timeouts = %v, want %v", got, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Routing struct {
			Timeouts map[string]int64 `json:"timeouts"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("config file %s: %v", b, err)
	}
	if got, want := file.Routing.Timeouts, map[string]int64{"header_ms": 15000, "idle_ms": 90000, "total_ms": 0}; !same(got, want) {
		t.Errorf("the config file holds routing.timeouts = %v, want %v:\n%s", got, want, b)
	}

	// The checks the config applies, and unknown fields, refuse the change and write nothing.
	for _, body := range []string{
		`{"routing":{"timeouts":{"header_ms":-1}}}`,
		`{"routing":{"timeouts":{"idle_ms":2592000001}}}`,
		`{"routing":{"timeouts":{"header_seconds":1}}}`,
		`{"routing":{"timeouts":5}}`,
	} {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if rec := manageCall(t, h, http.MethodPatch, "/settings", body); rec.Code != http.StatusBadRequest || errorCodeOf(t, rec) != "invalid_request" {
			t.Errorf("PATCH %s = %d (%s), want 400 invalid_request", body, rec.Code, rec.Body)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("PATCH %s changed the config file:\n%s", body, after)
		}
	}

	// null removes them, back to core's own defaults.
	if rec := manageCall(t, h, http.MethodPatch, "/settings", `{"routing":{"timeouts":null}}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH routing.timeouts null = %d (%s)", rec.Code, rec.Body)
	}
	if got := timeouts(); got != nil {
		t.Errorf("after null routing.timeouts = %v, want none", got)
	}
	if b, err := os.ReadFile(path); err != nil || strings.Contains(string(b), `"timeouts"`) {
		t.Errorf("the config file still holds timeouts (%v):\n%s", err, b)
	}
}
