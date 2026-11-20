package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

func TestStateDir(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if got, want := StateDir(cfgPath), filepath.Join(dir, "state"); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestThemeIDValidation(t *testing.T) {
	valid := []string{"pluxy/default", "t3/a-1", "a/b", strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64)}
	for _, id := range valid {
		if !validThemeID(id) {
			t.Errorf("validThemeID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"", "default", "/default", "t3/", "t3//a", "a/b/c",
		"../a", "a/..", "a/b/..", "a%2fb/c", "T3/a", "t3/A", "t3/a_b", "t3/a.b", "t3/a b",
		"current/paper",
		strings.Repeat("a", 65) + "/b", "a/" + strings.Repeat("b", 65),
	}
	for _, id := range invalid {
		if validThemeID(id) {
			t.Errorf("validThemeID(%q) = true, want false", id)
		}
	}
}

func TestThemeStoreLimitsAndSymlinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "themes")
	st := newThemeStore(dir)

	reply, err := st.list()
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if reply.Current != "" || len(reply.Themes) != 0 || reply.Themes == nil {
		t.Fatalf("empty list = %+v, want empty non-nil themes slice", reply)
	}

	raw := []byte("{\n  \"name\": \"Custom Blue\",\n  \"appearance\": \"dark\"\n}\n")
	sum, err := st.put("t3", "blue", "Custom Blue", raw)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if sum.ID != "t3/blue" || sum.Name != "Custom Blue" || sum.Bytes != len(raw) {
		t.Fatalf("put summary = %+v", sum)
	}
	got, err := st.get("t3", "blue")
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("get = (%q, %v), want exact bytes %q", got, err, raw)
	}

	if runtime.GOOS != "windows" {
		for _, p := range []string{dir, filepath.Join(dir, "t3")} {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("dir %s mode = %v (%v), want 0700", p, fi.Mode().Perm(), err)
			}
		}
		fi, err := os.Stat(filepath.Join(dir, "t3", "blue.json"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %v (%v), want 0600", fi.Mode().Perm(), err)
		}
	}

	// Symlink pointing outside themes/ is refused by get and ignored by list.
	secret := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(secret, []byte(`{"name":"Leaked"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "t3", "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.get("t3", "link"); !errors.Is(err, errNotRegular) {
		t.Errorf("get symlink err = %v, want errNotRegular", err)
	}
	// Symlinked publisher directory is also refused.
	outsidePub := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsidePub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsidePub, "escape.json"), []byte(`{"name":"Escape"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePub, filepath.Join(dir, "evil")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.get("evil", "escape"); !errors.Is(err, errNotRegular) {
		t.Errorf("get symlinked publisher err = %v, want errNotRegular", err)
	}
	reply, err = st.list()
	if err != nil {
		t.Fatalf("list with symlinks: %v", err)
	}
	if len(reply.Themes) != 1 || reply.Themes[0].ID != "t3/blue" {
		t.Fatalf("list with symlinks = %+v, want only t3/blue", reply.Themes)
	}

	// Select, verify current persists, delete selected clears current.
	if err := st.setCurrent("t3/blue"); err != nil {
		t.Fatal(err)
	}
	reply, err = st.list()
	if err != nil || reply.Current != "t3/blue" {
		t.Fatalf("list after setCurrent = (%+v, %v)", reply, err)
	}
	if err := st.remove("t3", "blue"); err != nil {
		t.Fatal(err)
	}
	reply, err = st.list()
	if err != nil || reply.Current != "" || len(reply.Themes) != 0 {
		t.Fatalf("list after remove current = (%+v, %v)", reply, err)
	}

	// 64 stored themes max; 65th refused, replacing an existing allowed.
	for i := range maxStoredThemes {
		name := fmt.Sprintf("theme-%02d", i)
		if _, err := st.put("t3", name, "", []byte(`{}`)); err != nil {
			t.Fatalf("put #%d: %v", i, err)
		}
	}
	if _, err := st.put("t3", "theme-65", "", []byte(`{}`)); !errors.Is(err, errThemeLimit) {
		t.Fatalf("65th put err = %v, want errThemeLimit", err)
	}
	if _, err := st.put("t3", "theme-00", "Updated", []byte(`{"name":"Updated"}`)); err != nil {
		t.Fatalf("replacing existing at cap failed: %v", err)
	}
	_ = fs.ErrExist
}

func TestHandleThemesHTTP(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "mgmt-key"}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	h := c.Management()

	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, management.Prefix+target, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer mgmt-key")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Unauthenticated request fails 401.
	req := httptest.NewRequest(http.MethodGet, management.Prefix+"/themes", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET /themes = %d, want 401", rec.Code)
	}

	// Bad IDs return 400 invalid_request.
	for _, path := range []string{
		"/themes/%2e%2e/bad",
		"/themes/t3/%2e%2e",
		"/themes/a%2fb/c",
		"/themes/t3/c%2fd",
		"/themes/Upper/case",
		"/themes/t3/Upper",
		"/themes/%20/name",
		"/themes/current/foo",
	} {
		if r := do(http.MethodPut, path, `{}`); r.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d (%s), want 400", path, r.Code, r.Body.String())
		}
		if r := do(http.MethodGet, path, ""); r.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d (%s), want 400", path, r.Code, r.Body.String())
		}
		if r := do(http.MethodDelete, path, ""); r.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s = %d (%s), want 400", path, r.Code, r.Body.String())
		}
	}

	// Non-object bodies return 400 invalid_request.
	for _, body := range []string{"", "null", "[]", `"hello"`, "123", "true", "{}{}", "{bad"} {
		if r := do(http.MethodPut, "/themes/t3/ok", body); r.Code != http.StatusBadRequest {
			t.Errorf("PUT non-object %q = %d (%s), want 400", body, r.Code, r.Body.String())
		}
	}

	// Over 256 KiB returns 413; exactly 256 KiB succeeds.
	tooBig := `{"pad":"` + strings.Repeat("a", maxThemeBytes) + `"}`
	if r := do(http.MethodPut, "/themes/t3/big", tooBig); r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT over limit = %d (%s), want 413", r.Code, r.Body.String())
	}
	prefix := `{"pad":"`
	exact := prefix + strings.Repeat("a", maxThemeBytes-len(prefix)-len(`"}`)) + `"}`
	if len(exact) != maxThemeBytes {
		t.Fatalf("exact len = %d", len(exact))
	}
	if r := do(http.MethodPut, "/themes/t3/exact", exact); r.Code != http.StatusOK {
		t.Fatalf("PUT exact limit = %d (%s), want 200", r.Code, r.Body.String())
	}
	if r := do(http.MethodDelete, "/themes/t3/exact", ""); r.Code != http.StatusNoContent {
		t.Fatalf("DELETE exact = %d, want 204", r.Code)
	}

	// PUT /theme strict body checks (unknown field, missing id, invalid id), built-in ID allowed.
	for _, bad := range []string{`{}`, `{"id":null}`, `{"id":"pluxy/paper","extra":1}`, `{"id":"../x"}`, `{"id":"Upper/case"}`} {
		if r := do(http.MethodPut, "/theme", bad); r.Code != http.StatusBadRequest {
			t.Errorf("PUT /theme %s = %d (%s), want 400", bad, r.Code, r.Body.String())
		}
	}
	if r := do(http.MethodPut, "/theme", `{"id":"pluxy/paper"}`); r.Code != http.StatusOK {
		t.Fatalf("PUT /theme built-in = %d (%s), want 200", r.Code, r.Body.String())
	}
	var got management.ThemesReply
	r := do(http.MethodGet, "/themes", "")
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &got) != nil || got.Current != "pluxy/paper" {
		t.Fatalf("GET /themes = %d %q", r.Code, r.Body.String())
	}
	if r := do(http.MethodPut, "/theme", `{"id":""}`); r.Code != http.StatusOK {
		t.Fatalf("PUT /theme clear = %d, want 200", r.Code)
	}
}
