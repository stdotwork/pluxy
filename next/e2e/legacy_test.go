package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
)

// bundledRegistry writes a local registry offering each extension built in binDir (ID to the
// program's path under binDir) as version 0.1.0, and returns its index's path.
func bundledRegistry(t *testing.T, programs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	pkgs := map[string]any{}
	for id, prog := range programs {
		body, err := os.ReadFile(filepath.Join(binDir, prog))
		if err != nil {
			t.Fatal(err)
		}
		m, _ := json.Marshal(map[string]any{
			"manifest_version": 0, "id": id, "version": "0.1.0", "protocol": []int{0},
			"programs": map[string]string{platform: "bin/program"},
		})
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, f := range []struct {
			name string
			mode int64
			body []byte
			typ  byte
		}{{"pluxy-extension.json", 0o644, m, tar.TypeReg}, {"bin/", 0o755, nil, tar.TypeDir}, {"bin/program", 0o755, body, tar.TypeReg}} {
			if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: f.typ}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(f.body); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(id, "/", "-") + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(buf.Bytes())
		pkgs[id] = map[string]any{"releases": []any{map[string]any{
			"version": "0.1.0", "protocol": []int{0},
			"artifacts": map[string]any{platform: map[string]any{"url": name, "sha256": hex.EncodeToString(sum[:]), "size": buf.Len()}},
		}}}
	}
	index, _ := json.Marshal(map[string]any{"registry_version": 0, "packages": pkgs})
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, index, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// freePort returns a TCP port on 127.0.0.1 that was free a moment ago. A config.yaml cannot
// ask for port 0: there, as in today's core, 0 means 8317.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeIDToken is an unsigned JWT with the claims of a ChatGPT sign-in.
func fakeIDToken(accountID, email string) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID, "chatgpt_plan_type": "plus"},
	})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + ".sig"
}

// folderDigest is a digest of every file name and content in dir.
func folderDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		h.Write([]byte(e.Name() + "\x00"))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// replaceOnce replaces the one occurrence of old in s.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if n := strings.Count(s, old); n != 1 {
		t.Fatalf("%q occurs %d times in the example config", old, n)
	}
	return strings.Replace(s, old, new, 1)
}

// Today's example config, with its listener, management key and auth folder set and one
// openai-compatibility provider added, starts core after today's auth folder is imported:
// core serves a request through the provider section's fake upstream and one through an
// imported Kimi subscription on a fake Kimi, and lists the imported accounts.
func TestTodaysConfigAndAuthFolder(t *testing.T) {
	u := answeringUpstream(t)
	kimi := kimitest.New(t)
	kimiAccess, kimiRefresh := kimi.Grant()
	work := t.TempDir()

	// Today's auth folder, with fake credentials.
	authDir := filepath.Join(work, "old-auths")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
	for name, v := range map[string]map[string]any{
		"kimi-1700000000000.json": {"type": "kimi", "access_token": kimiAccess, "refresh_token": kimiRefresh,
			"token_type": "Bearer", "device_id": "device-e2e", "base_url": kimi.URL + "/coding", "expired": expired},
		"codex-ab12cd34-user@example.com-plus.json": {"type": "codex", "access_token": "fake-codex-access",
			"refresh_token": "fake-codex-refresh", "id_token": fakeIDToken("acct-e2e", "user@example.com"),
			"account_id": "acct-e2e", "email": "user@example.com", "expired": expired},
		"claude-user@example.com.json": {"type": "claude", "access_token": "fake-claude-access",
			"refresh_token": "fake-claude-refresh", "email": "user@example.com", "expired": expired, "disabled": true},
		"antigravity-user@example.com.json": {"type": "antigravity", "access_token": "fake-antigravity-access"},
	} {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(authDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := folderDigest(t, authDir)

	// Today's example config, changed only where a test must.
	example, err := os.ReadFile("../internal/legacy/testdata/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(example)
	cfg = replaceOnce(t, cfg, "\n  host: \"\"\n", "\n  host: \"127.0.0.1\"\n")
	cfg = replaceOnce(t, cfg, "\n  port: 8317\n", "\n  port: "+strconv.Itoa(freePort(t))+"\n")
	cfg = replaceOnce(t, cfg, "\n  secret-key: \"\"\n", "\n  secret-key: \""+managementKey+"\"\n")
	cfg = replaceOnce(t, cfg, "\n  auth-dir: \"~/.cli-proxy-api\"\n", "\n  auth-dir: \""+authDir+"\"\n")
	cfg += `
openai-compatibility:
  - name: "local"
    base-url: "` + u.URL + `/v1"
    api-key-entries:
      - api-key: "` + upstreamKey + `"
    models:
      - name: "gpt-4o"
`
	path := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	bundled := bundledRegistry(t, map[string]string{
		"pluxy/openai-compatible": "openai-compatible",
		"pluxy/kimi":              filepath.Join("kimi", "kimi"),
		"pluxy/codex":             filepath.Join("codex", "codex"),
		"pluxy/claude":            filepath.Join("claude", "claude"),
	})

	// Import while core is stopped.
	out, err := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path, "-import-auth", authDir).CombinedOutput()
	t.Logf("import:\n%s", out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"imported kimi-1700000000000.json as account kimi-1700000000000 (pluxy/kimi)",
		"imported codex-ab12cd34-user@example.com-plus.json as account codex-ab12cd34-user-example.com-plus (pluxy/codex)",
		"imported claude-user@example.com.json as account claude-user-example.com (pluxy/claude)",
		"skipped antigravity-user@example.com.json: no extension signs in to antigravity accounts yet",
		"3 imported, 1 skipped",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("import output lacks %q", want)
		}
	}
	if folderDigest(t, authDir) != before {
		t.Error("the import changed today's auth folder")
	}

	base, logs := runCoreFile(t, path, "-bundled", bundled)
	logs.waitFor(t, "config.yaml: not supported yet, ignored: routing.retry.request-retry")
	clientKey := "your-api-key-1" // the example's first client key

	// A request through the provider section's account.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp := post(t, ctx, base, clientKey, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("gpt-4o: status %d: %s", resp.StatusCode, body)
	}
	if _, auth := u.received(); len(auth) != 1 || auth[0] != "Bearer "+upstreamKey {
		t.Errorf("upstream saw authorization %v", auth)
	}

	// A request through the imported Kimi subscription.
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions",
		strings.NewReader(`{"model":"kimi-k2.8","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pluxy-Account", "kimi-1700000000000")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Hello") {
		t.Fatalf("kimi: status %d: %s", resp.StatusCode, body)
	}
	reqs := kimi.Requests()
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "Bearer "+kimiAccess || reqs[0].Header.Get("X-Msh-Device-Id") != "device-e2e" {
		t.Errorf("fake Kimi saw %d requests: %+v", len(reqs), reqs)
	}

	// The imported accounts are listed as added by login.
	var accounts struct {
		Accounts []struct {
			ID       string `json:"id"`
			Source   string `json:"source"`
			Kind     string `json:"kind"`
			Disabled bool   `json:"disabled"`
		} `json:"accounts"`
	}
	if code := manage(t, base, http.MethodGet, "/accounts", nil, &accounts); code != http.StatusOK {
		t.Fatalf("GET /accounts: %d", code)
	}
	got := map[string]string{}
	for _, a := range accounts.Accounts {
		got[a.ID] = a.Source + " " + a.Kind + " " + strconv.FormatBool(a.Disabled)
	}
	for id, want := range map[string]string{
		"local":                                "config api_key false",
		"kimi-1700000000000":                   "login oauth false",
		"codex-ab12cd34-user-example.com-plus": "login oauth false",
		"claude-user-example.com":              "login oauth true",
	} {
		if got[id] != want {
			t.Errorf("account %s: %q, want %q (all: %v)", id, got[id], want, got)
		}
	}
	if strings.Contains(logs.waitFor(t, ""), "not imported yet") {
		t.Error("core says files are not imported yet after the import")
	}
}
