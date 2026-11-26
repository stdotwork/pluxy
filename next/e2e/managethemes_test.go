package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

func TestManageThemesAPI(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgContent := func(port int) []byte {
		b, _ := json.Marshal(map[string]any{
			"listen":         fmt.Sprintf("127.0.0.1:%d", port),
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
		})
		return b
	}

	oceanRaw := "{\n  \"name\": \"Ocean Blue\",\n  \"appearance\": \"dark\",\n  \"custom\": [1, 2, 3]\n}"

	t.Run("first run stores and selects", func(t *testing.T) {
		base, _, _ := startOnFreePort(t, filepath.Join(binDir, "pluxy"), cfgPath, cfgContent, "-watch-interval=0")

		// Management key is required.
		code, _ := mcall(t, base, "", http.MethodGet, "/themes", nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("unauth GET /themes = %d, want 401", code)
		}

		// Store theme and check bytes returned verbatim.
		code, body := mcall(t, base, managementKey, http.MethodPut, "/themes/t3/ocean", oceanRaw)
		if code != http.StatusOK {
			t.Fatalf("PUT /themes/t3/ocean = %d: %s", code, body)
		}
		var sum mgmt.ThemeSummary
		if err := json.Unmarshal(body, &sum); err != nil || sum.ID != "t3/ocean" || sum.Name != "Ocean Blue" || sum.Bytes != len(oceanRaw) {
			t.Fatalf("PUT reply = %s (%v)", body, err)
		}

		req, _ := http.NewRequest(http.MethodGet, base+mgmt.Prefix+"/themes/t3/ocean", nil)
		req.Header.Set("Authorization", "Bearer "+managementKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		gotBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || string(gotBytes) != oceanRaw {
			t.Fatalf("GET /themes/t3/ocean = %d %q %q, want exact bytes", resp.StatusCode, resp.Header.Get("Content-Type"), gotBytes)
		}

		// Select the stored theme.
		code, body = mcall(t, base, managementKey, http.MethodPut, "/theme", map[string]string{"id": "t3/ocean"})
		if code != http.StatusOK {
			t.Fatalf("PUT /theme = %d: %s", code, body)
		}
	})

	t.Run("restart preserves themes and current, enforces limits and safety", func(t *testing.T) {
		base, _, _ := startOnFreePort(t, filepath.Join(binDir, "pluxy"), cfgPath, cfgContent, "-watch-interval=0")

		code, body := mcall(t, base, managementKey, http.MethodGet, "/themes", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /themes after restart = %d: %s", code, body)
		}
		var list mgmt.ThemesReply
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatalf("decode GET /themes: %v (%s)", err, body)
		}
		if list.Current != "t3/ocean" || len(list.Themes) != 1 || list.Themes[0].ID != "t3/ocean" || list.Themes[0].Name != "Ocean Blue" {
			t.Fatalf("after restart GET /themes = %+v", list)
		}

		code, body = mcall(t, base, managementKey, http.MethodGet, "/themes/t3/ocean", nil)
		if code != http.StatusOK || string(body) != oceanRaw {
			t.Fatalf("after restart GET /themes/t3/ocean = %d %q", code, body)
		}

		// Select built-in ID that core has no file for, then clear, then re-select and delete to verify current clears.
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":"pluxy/paper"}`); code != http.StatusOK {
			t.Fatalf("PUT /theme built-in = %d: %s", code, body)
		}
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":""}`); code != http.StatusOK {
			t.Fatalf("PUT /theme clear = %d: %s", code, body)
		}
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":"t3/ocean"}`); code != http.StatusOK {
			t.Fatalf("PUT /theme re-select = %d: %s", code, body)
		}
		if code, body := mcall(t, base, managementKey, http.MethodDelete, "/themes/t3/ocean", nil); code != http.StatusNoContent {
			t.Fatalf("DELETE /themes/t3/ocean = %d: %s", code, body)
		}
		if code, _ := mcall(t, base, managementKey, http.MethodDelete, "/themes/t3/ocean", nil); code != http.StatusNotFound {
			t.Fatalf("DELETE absent = %d, want 404", code)
		}
		_, body = mcall(t, base, managementKey, http.MethodGet, "/themes", nil)
		_ = json.Unmarshal(body, &list)
		if list.Current != "" || len(list.Themes) != 0 {
			t.Fatalf("after deleting current theme, GET /themes = %+v, want empty current and themes", list)
		}

		// PUT /theme unknown fields / missing id / invalid id -> 400.
		for _, bad := range []string{`{"id":"pluxy/paper","unknown":1}`, `{}`, `{"id":"../bad"}`, `{"id":"Upper/case"}`} {
			if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", bad); code != http.StatusBadRequest {
				t.Fatalf("PUT /theme %s = %d (%s), want 400", bad, code, body)
			}
		}

		// Over 256 KiB -> 413.
		overLimit := `{"pad":"` + strings.Repeat("x", 256<<10) + `"}`
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/themes/t3/too-big", overLimit); code != http.StatusRequestEntityTooLarge {
			t.Fatalf("PUT over 256 KiB = %d (%s), want 413", code, body)
		}

		// Non-object JSON bodies -> 400 invalid_request.
		for _, nonObj := range []string{"[]", `"hello"`, "null", "42", "{}{}"} {
			code, body := mcall(t, base, managementKey, http.MethodPut, "/themes/t3/bad-body", nonObj)
			if code != http.StatusBadRequest {
				t.Fatalf("PUT non-object %s = %d (%s), want 400", nonObj, code, body)
			}
		}

		// IDs trying to leave the folder or violating [a-z0-9-]{1,64}.
		noRedirect := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
		for _, p := range []struct {
			path       string
			wantStatus int
		}{
			{"/themes/%2e%2e/escape", http.StatusBadRequest},
			{"/themes/t3/%2e%2e", http.StatusBadRequest},
			{"/themes/a%2fb/c", http.StatusBadRequest},
			{"/themes/t3/a%2fb", http.StatusBadRequest},
			{"/themes/%20/name", http.StatusBadRequest},
			{"/themes/Upper/name", http.StatusBadRequest},
			{"/themes/t3/Upper", http.StatusBadRequest},
			{"/themes/../escape", 0}, // net/http mux cleans/redirects/404s; must not be 2xx
			{"/themes//empty", 0},    // empty part
		} {
			req, err := http.NewRequest(http.MethodPut, base+mgmt.Prefix+p.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+managementKey)
			req.Header.Set("Content-Type", "application/json")
			resp, err := noRedirect.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if p.wantStatus != 0 && resp.StatusCode != p.wantStatus {
				t.Fatalf("PUT %s = %d, want %d", p.path, resp.StatusCode, p.wantStatus)
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				t.Fatalf("PUT %s unexpectedly succeeded: %d", p.path, resp.StatusCode)
			}
		}
		// A traversal ID such as ../escape would write beside the themes folder, which is
		// state/themes: state/escape.json. A write that dropped the themes folder would land in
		// state/themes/escape.json instead.
		for _, outside := range []string{
			filepath.Join(dir, "state", "escape.json"),
			filepath.Join(dir, "state", "themes", "escape.json"),
		} {
			if _, err := os.Stat(outside); !os.IsNotExist(err) {
				t.Fatalf("path traversal created %s", outside)
			}
		}

		// Symlink in state/themes must not be followed by GET or listed by GET /themes.
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{"name":"Secret"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		pubDir := filepath.Join(dir, "state", "themes", "t3")
		if err := os.MkdirAll(pubDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(pubDir, "sym.json")); err != nil {
			t.Fatal(err)
		}
		if code, _ := mcall(t, base, managementKey, http.MethodGet, "/themes/t3/sym", nil); code != http.StatusNotFound {
			t.Fatalf("GET symlink theme = %d, want 404", code)
		}

		// 64-theme cap: 65th rejected with 422 refused; replacing existing succeeds.
		for i := range 64 {
			id := fmt.Sprintf("/themes/t3/cap-%02d", i)
			if code, body := mcall(t, base, managementKey, http.MethodPut, id, `{}`); code != http.StatusOK {
				t.Fatalf("PUT %s = %d: %s", id, code, body)
			}
		}
		code, body = mcall(t, base, managementKey, http.MethodPut, "/themes/t3/cap-65", `{}`)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("65th PUT = %d (%s), want 422", code, body)
		}
		var errBody mgmt.ErrorBody
		if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error.Code != "refused" {
			t.Fatalf("65th PUT error = %s, want code refused", body)
		}
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/themes/t3/cap-00", `{"name":"Replaced"}`); code != http.StatusOK {
			t.Fatalf("replace at cap = %d (%s), want 200", code, body)
		}
	})

	t.Run("built-in IDs cannot be stored and unknown IDs cannot be selected", func(t *testing.T) {
		base, _, _ := startOnFreePort(t, filepath.Join(binDir, "pluxy"), cfgPath, cfgContent, "-watch-interval=0")

		code, body := mcall(t, base, managementKey, http.MethodPut, "/themes/pluxy/paper", `{"name":"Mine"}`)
		var errBody mgmt.ErrorBody
		if err := json.Unmarshal(body, &errBody); err != nil || code != http.StatusUnprocessableEntity || errBody.Error.Code != "refused" ||
			!strings.Contains(errBody.Error.Message, "built-in") { // the store is at its cap here, which is also a 422
			t.Fatalf("PUT /themes/pluxy/paper = %d %s, want 422 refused for the built-in's ID", code, body)
		}
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":"t3/cap-00"}`); code != http.StatusOK {
			t.Fatalf("PUT /theme stored = %d: %s", code, body)
		}
		code, body = mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":"nope/nothing"}`)
		errBody = mgmt.ErrorBody{}
		if err := json.Unmarshal(body, &errBody); err != nil || code != http.StatusNotFound || errBody.Error.Code != "not_found" {
			t.Fatalf("PUT /theme unknown = %d %s, want 404 not_found", code, body)
		}
		_, body = mcall(t, base, managementKey, http.MethodGet, "/themes", nil)
		var list mgmt.ThemesReply
		if err := json.Unmarshal(body, &list); err != nil || list.Current != "t3/cap-00" {
			t.Fatalf("GET /themes after the refusal = %s, want current t3/cap-00", body)
		}
		// A built-in this core does not list, from a newer panel or app.
		if code, body := mcall(t, base, managementKey, http.MethodPut, "/theme", `{"id":"pluxy/from-a-newer-ui"}`); code != http.StatusOK {
			t.Fatalf("PUT /theme of the built-ins' publisher = %d: %s", code, body)
		}
	})
}

func TestCLIThemes(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgContent := func(port int) []byte {
		b, _ := json.Marshal(map[string]any{
			"listen":         fmt.Sprintf("127.0.0.1:%d", port),
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
		})
		return b
	}
	base, _, _ := startOnFreePort(t, filepath.Join(binDir, "pluxy"), cfgPath, cfgContent, "-watch-interval=0")
	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}

	// 1. {"name":"Solarized Dark"} has no theme_version: the T3 name wins over the file name.
	fileNoID := filepath.Join(dir, "Solarized Dark_v2.json")
	rawNoID := []byte("{\n  \"name\": \"Solarized Dark\"\n}")
	if err := os.WriteFile(fileNoID, rawNoID, 0o600); err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, env, "theme", "add", fileNoID)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "t3/solarized-dark" {
		t.Fatalf("theme add derived from name: %s", r)
	}

	// 2. {"id":"acme/amber","name":"Amber"} has no theme_version: acme/amber is not a T3 ID,
	// so it derives from the name; --id still wins.
	fileWithID := filepath.Join(dir, "ignored.json")
	rawWithID := []byte(`{"id":"acme/amber","name":"Amber"}`)
	if err := os.WriteFile(fileWithID, rawWithID, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, env, "theme", "add", fileWithID)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "t3/amber" {
		t.Fatalf("theme add invalid T3 id: %s", r)
	}
	r = runCLI(t, env, "theme", "add", fileWithID, "--id", "acme/custom", "--json")
	var sum mgmt.ThemeSummary
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &sum) != nil || sum.ID != "acme/custom" {
		t.Fatalf("theme add --id --json: %s", r)
	}

	// 3. {"name":"Night Fall"} in my-file.json becomes t3/night-fall; a pluxy file with
	// {"theme_version":0,"id":"Bad_ID","name":"Ignored Name"} falls back to its file name.
	fileT3Name := filepath.Join(dir, "my-file.json")
	rawT3Name := []byte(`{"name":"Night Fall"}`)
	if err := os.WriteFile(fileT3Name, rawT3Name, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, env, "theme", "add", fileT3Name)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "t3/night-fall" {
		t.Fatalf("theme add T3 name over file name: %s", r)
	}

	filePluxyInvalidID := filepath.Join(dir, "Old File Name.json")
	rawPluxyInvalidID := []byte(`{"theme_version":0,"id":"Bad_ID","name":"Ignored Name"}`)
	if err := os.WriteFile(filePluxyInvalidID, rawPluxyInvalidID, 0o600); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, env, "theme", "add", filePluxyInvalidID)
	if r.code != 0 || strings.TrimSpace(r.stdout) != "t3/old-file-name" {
		t.Fatalf("theme add pluxy file fallback: %s", r)
	}

	// 4. show prints stored JSON unchanged.
	r = runCLI(t, env, "theme", "show", "t3/solarized-dark", "--json")
	if r.code != 0 || !bytes.Equal([]byte(r.stdout), rawNoID) {
		t.Fatalf("theme show --json: %s, want exact %q", r, rawNoID)
	}
	r = runCLI(t, env, "theme", "show", "t3/amber")
	if r.code != 0 || strings.TrimSpace(r.stdout) != string(rawWithID) {
		t.Fatalf("theme show: %s", r)
	}
	r = runCLI(t, env, "theme", "show", "t3/night-fall", "--json")
	if r.code != 0 || !bytes.Equal([]byte(r.stdout), rawT3Name) {
		t.Fatalf("theme show T3 name --json: %s, want exact %q", r, rawT3Name)
	}
	r = runCLI(t, env, "theme", "show", "t3/old-file-name", "--json")
	if r.code != 0 || !bytes.Equal([]byte(r.stdout), rawPluxyInvalidID) {
		t.Fatalf("theme show pluxy fallback --json: %s, want exact %q", r, rawPluxyInvalidID)
	}

	// 5. set + list + list --json.
	if r = runCLI(t, env, "theme", "set", "pluxy/paper"); r.code != 0 || !strings.Contains(r.stdout, "pluxy/paper") {
		t.Fatalf("theme set: %s", r)
	}
	r = runCLI(t, env, "theme", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "current: pluxy/paper") {
		t.Fatalf("theme list: %s", r)
	}
	for _, id := range []string{"t3/solarized-dark", "t3/amber", "acme/custom", "t3/night-fall", "t3/old-file-name"} {
		if !strings.Contains(r.stdout, id) {
			t.Fatalf("theme list is missing %s: %s", id, r)
		}
	}
	r = runCLI(t, env, "theme", "list", "--json")
	var listReply mgmt.ThemesReply
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &listReply) != nil || listReply.Current != "pluxy/paper" || len(listReply.Themes) != 5 {
		t.Fatalf("theme list --json: %s", r)
	}

	// 6. clear + remove.
	if r = runCLI(t, env, "theme", "clear"); r.code != 0 {
		t.Fatalf("theme clear: %s", r)
	}
	if r = runCLI(t, env, "theme", "remove", "t3/amber"); r.code != 0 {
		t.Fatalf("theme remove: %s", r)
	}
	if r = runCLI(t, env, "theme", "remove", "acme/missing"); r.code != 5 {
		t.Fatalf("theme remove missing: %s, want exit 5", r)
	}

	// 7. A built-in's ID and a bad ID are refused, and say what to do.
	r = runCLI(t, env, "theme", "add", fileWithID, "--id", "pluxy/paper")
	if r.code != 6 || r.stdout != "" || !strings.Contains(r.stderr, "pluxy/paper is a built-in theme's ID") {
		t.Fatalf("theme add --id pluxy/paper: %s, want exit 6 and the reason", r)
	}
	r = runCLI(t, env, "theme", "add", fileWithID, "--id", "Bad Id")
	if r.code != 2 || r.stdout != "" || !strings.Contains(r.stderr, "publisher/name") || strings.Contains(r.stderr, "404") {
		t.Fatalf("theme add --id Bad Id: %s, want exit 2 and what an ID looks like", r)
	}

	// 8. An ID no theme has is not selected.
	if r = runCLI(t, env, "theme", "set", "t3/night-fall"); r.code != 0 {
		t.Fatalf("theme set stored: %s", r)
	}
	r = runCLI(t, env, "theme", "set", "nope/nothing")
	if r.code != 5 || r.stdout != "" || !strings.Contains(r.stderr, "nope/nothing") || !strings.Contains(r.stderr, "pluxy theme list") {
		t.Fatalf("theme set nope/nothing: %s, want exit 5 and where the IDs are", r)
	}
	if r = runCLI(t, env, "theme", "list"); !strings.Contains(r.stdout, "current: t3/night-fall") {
		t.Fatalf("theme list after the refusal: %s, want the selection unchanged", r)
	}

	// 9. add keeps the ID alone on standard output and says on standard error that it is not on,
	// unless it already is.
	r = runCLI(t, env, "theme", "add", fileWithID, "--id", "acme/second")
	if r.code != 0 || r.stdout != "acme/second\n" || !strings.Contains(r.stderr, "pluxy theme set acme/second") {
		t.Fatalf("theme add of a theme that is not on: %s", r)
	}
	r = runCLI(t, env, "theme", "add", fileT3Name)
	if r.code != 0 || r.stdout != "t3/night-fall\n" || r.stderr != "" {
		t.Fatalf("theme add of the theme that is on: %s, want no line on standard error", r)
	}

	// 10. The group's help.
	for _, flag := range []string{"--help", "-h"} {
		r = runCLI(t, nil, "theme", flag)
		if r.code != 0 || !strings.Contains(r.stdout, "theme add <file>") || r.stderr != "" {
			t.Fatalf("theme %s: %s, want exit 0 and the commands on standard output", flag, r)
		}
	}
}
