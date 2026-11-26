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
	"slices"
	"strings"
	"testing"
	"time"

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

// A file that is not one JSON object is not a stored theme: list leaves it out, get says it is
// not there, and it does not count towards the 64-theme cap.
func TestFilesThatAreNotThemes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "themes")
	st := newThemeStore(dir)
	if err := os.MkdirAll(filepath.Join(dir, "junk"), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxStoredThemes {
		p := filepath.Join(dir, "junk", fmt.Sprintf("junk-%02d.json", i))
		if err := os.WriteFile(p, []byte("not a theme"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reply, err := st.list()
	if err != nil || len(reply.Themes) != 0 {
		t.Fatalf("list with only files that are not themes = %+v, %v; want no themes", reply, err)
	}
	if _, err := st.get("junk", "junk-00"); !errors.Is(err, errNotTheme) {
		t.Errorf("get of a file that is not a theme = %v, want errNotTheme", err)
	}
	if _, err := st.put("junk", "real", "", []byte(`{"name":"Real"}`)); err != nil {
		t.Fatalf("put with 64 files that are not themes: %v, want it stored", err)
	}

	// At the cap a file that is not a theme does not stand in for the one being stored, and a
	// stored theme is still replaced.
	for i := range maxStoredThemes - 1 {
		if _, err := st.put("cap", fmt.Sprintf("t-%02d", i), "", []byte(`{}`)); err != nil {
			t.Fatalf("put #%d: %v", i, err)
		}
	}
	if _, err := st.put("junk", "junk-00", "", []byte(`{}`)); !errors.Is(err, errThemeLimit) {
		t.Errorf("put over a file that is not a theme at the cap = %v, want errThemeLimit", err)
	}
	if _, err := st.put("junk", "real", "Updated", []byte(`{"name":"Updated"}`)); err != nil {
		t.Errorf("replacing a stored theme at the cap: %v, want it replaced", err)
	}
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

	// A file in a publisher's folder that is not one JSON object is not a stored theme: GET
	// answers 404 as it does for an absent one, and list leaves it out.
	themesDir := filepath.Join(StateDir(cfgPath), "themes")
	if err := os.MkdirAll(filepath.Join(themesDir, "junk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesDir, "junk", "not-a-theme.json"), []byte("not a theme"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := do(http.MethodGet, "/themes/junk/not-a-theme", ""); r.Code != http.StatusNotFound {
		t.Errorf("GET a file that is not a theme = %d (%s), want 404", r.Code, r.Body.String())
	}
	if r := do(http.MethodGet, "/themes", ""); r.Code != http.StatusOK || strings.Contains(r.Body.String(), "junk/not-a-theme") {
		t.Errorf("GET /themes = %d (%s), want the file left out", r.Code, r.Body.String())
	}

	// A file at the publisher's path refuses PUT with the change refusal wrong input gets, not
	// with an internal error, and GET and DELETE say there is no such theme.
	if err := os.WriteFile(filepath.Join(themesDir, "blocked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := do(http.MethodPut, "/themes/blocked/one", `{}`); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT with a file at the publisher's path = %d (%s), want 422", r.Code, r.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		if r := do(method, "/themes/blocked/one", ""); r.Code != http.StatusNotFound {
			t.Errorf("%s with a file at the publisher's path = %d (%s), want 404", method, r.Code, r.Body.String())
		}
	}
}

// The built-in themes live in the UI: a built-in added there without core's list fails here.
func TestBuiltInThemeIDsMatchTheUI(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "ui", "themes", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the UI's theme files: %v, %v", files, err)
	}
	var inUI []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var theme struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &theme); err != nil || theme.ID == "" {
			t.Fatalf("%s: id %q, %v", f, theme.ID, err)
		}
		inUI = append(inUI, theme.ID)
	}
	slices.Sort(inUI)
	inCore := slices.Sorted(slices.Values(builtInThemeIDs))
	if !slices.Equal(inCore, inUI) {
		t.Fatalf("core lists the built-in themes %q, the UI has %q", inCore, inUI)
	}
}

func themeAPI(t *testing.T) (do func(method, target, body string) *httptest.ResponseRecorder, themesDir string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "mgmt-key"}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	h := c.Management()
	return func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, management.Prefix+target, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer mgmt-key")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}, filepath.Join(StateDir(cfgPath), "themes")
}

func themeError(t *testing.T, r *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	var body management.ErrorBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %q: %v", r.Body.String(), err)
	}
	return body.Error.Code, body.Error.Message
}

func currentTheme(t *testing.T, do func(method, target, body string) *httptest.ResponseRecorder) string {
	t.Helper()
	var got management.ThemesReply
	r := do(http.MethodGet, "/themes", "")
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &got) != nil {
		t.Fatalf("GET /themes = %d %q", r.Code, r.Body.String())
	}
	return got.Current
}

// A built-in theme's ID cannot be stored under: the page would never list that file. One stored
// before this rule stays readable and removable.
func TestThemeStoreRefusesBuiltInIDs(t *testing.T) {
	do, themesDir := themeAPI(t)
	for _, id := range builtInThemeIDs {
		r := do(http.MethodPut, "/themes/"+id, `{"name":"Mine"}`)
		code, message := themeError(t, r)
		_, name, _ := strings.Cut(id, "/")
		if r.Code != http.StatusUnprocessableEntity || code != "refused" ||
			!strings.Contains(message, id+" is a built-in theme's ID") || !strings.Contains(message, "my/"+name) {
			t.Errorf("PUT /themes/%s = %d %s %q, want 422 refused naming the ID and my/%s", id, r.Code, code, message, name)
		}
		if r := do(http.MethodGet, "/themes/"+id, ""); r.Code != http.StatusNotFound {
			t.Errorf("GET /themes/%s after the refusal = %d, want nothing stored", id, r.Code)
		}
	}
	// Another name under the built-ins' publisher is not a built-in's ID.
	if r := do(http.MethodPut, "/themes/pluxy/mine", `{"name":"Mine"}`); r.Code != http.StatusOK {
		t.Errorf("PUT /themes/pluxy/mine = %d (%s), want 200", r.Code, r.Body.String())
	}

	if err := os.MkdirAll(filepath.Join(themesDir, "pluxy"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"name":"Stored before"}`
	if err := os.WriteFile(filepath.Join(themesDir, "pluxy", "paper.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := do(http.MethodGet, "/themes/pluxy/paper", ""); r.Code != http.StatusOK || r.Body.String() != old {
		t.Errorf("GET a file stored earlier under a built-in's ID = %d %q, want it unchanged", r.Code, r.Body.String())
	}
	if r := do(http.MethodPut, "/themes/pluxy/paper", `{"name":"New"}`); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("PUT over a file stored earlier under a built-in's ID = %d, want 422", r.Code)
	}
	if r := do(http.MethodDelete, "/themes/pluxy/paper", ""); r.Code != http.StatusNoContent {
		t.Errorf("DELETE a file stored earlier under a built-in's ID = %d (%s), want 204", r.Code, r.Body.String())
	}
}

// PUT /theme takes the empty ID, a stored theme, a built-in, and any other ID of the built-ins'
// publisher, which a newer UI may hold. Anything else is 404 and leaves the selection alone.
func TestThemeSelectionRules(t *testing.T) {
	do, themesDir := themeAPI(t)

	// On a core that has stored nothing, a refused ID makes no themes folder.
	if r := do(http.MethodPut, "/theme", `{"id":"nope/nothing"}`); r.Code != http.StatusNotFound {
		t.Errorf("PUT /theme unknown on a new core = %d (%s), want 404", r.Code, r.Body.String())
	}
	if _, err := os.Stat(themesDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("themes folder after a refused selection: %v, want none", err)
	}

	if r := do(http.MethodPut, "/themes/t3/stored", `{"name":"Stored"}`); r.Code != http.StatusOK {
		t.Fatalf("PUT stored theme = %d", r.Code)
	}
	ids := append([]string{"t3/stored", "pluxy/from-a-newer-ui"}, builtInThemeIDs...)
	for _, id := range ids {
		if r := do(http.MethodPut, "/theme", `{"id":"`+id+`"}`); r.Code != http.StatusOK {
			t.Errorf("PUT /theme %s = %d (%s), want 200", id, r.Code, r.Body.String())
		} else if got := currentTheme(t, do); got != id {
			t.Errorf("current after selecting %s = %q", id, got)
		}
	}

	// What list does not count as a stored theme cannot be selected either.
	if err := os.MkdirAll(filepath.Join(themesDir, "junk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesDir, "junk", "not-json.json"), []byte("not a theme"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"name":"Outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(themesDir, "junk", "link.json")); err != nil {
		t.Fatal(err)
	}
	if r := do(http.MethodPut, "/theme", `{"id":"t3/stored"}`); r.Code != http.StatusOK {
		t.Fatalf("PUT /theme t3/stored = %d", r.Code)
	}
	for _, id := range []string{"nope/nothing", "junk/not-json", "junk/link", "t3/absent"} {
		r := do(http.MethodPut, "/theme", `{"id":"`+id+`"}`)
		code, message := themeError(t, r)
		if r.Code != http.StatusNotFound || code != "not_found" {
			t.Errorf("PUT /theme %s = %d %s, want 404 not_found", id, r.Code, code)
		}
		for _, want := range append([]string{id, "pluxy theme list"}, builtInThemeIDs...) {
			if !strings.Contains(message, want) {
				t.Errorf("PUT /theme %s says %q, which does not name %s", id, message, want)
			}
		}
		if got := currentTheme(t, do); got != "t3/stored" {
			t.Errorf("current after refusing %s = %q, want it unchanged", id, got)
		}
	}

	if r := do(http.MethodPut, "/theme", `{"id":""}`); r.Code != http.StatusOK {
		t.Errorf("PUT /theme clear = %d", r.Code)
	}
	if got := currentTheme(t, do); got != "" {
		t.Errorf("current after clearing = %q", got)
	}
}

// A theme removed while it is being selected cannot end up selected: the removal waits until the
// selection has been checked and written.
func TestSelectingARemovedTheme(t *testing.T) {
	st := newThemeStore(filepath.Join(t.TempDir(), "state", "themes"))
	if _, err := st.put("t3", "race", "Race", []byte(`{"name":"Race"}`)); err != nil {
		t.Fatal(err)
	}
	removed := make(chan error, 2)
	st.onSelectChecked = func() {
		go func() { removed <- st.remove("t3", "race") }()
		select {
		case err := <-removed:
			t.Errorf("a removal finished between the selection's check and its write (%v)", err)
			removed <- err
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := st.setCurrent("t3/race"); err != nil {
		t.Fatal(err)
	}
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	reply, err := st.list()
	if err != nil {
		t.Fatal(err)
	}
	if reply.Current != "" || len(reply.Themes) != 0 {
		t.Fatalf("after removing the theme being selected: current %q, themes %+v; want neither", reply.Current, reply.Themes)
	}
}
