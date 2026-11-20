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
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
)

// installBundled installs and enables the packages ids from the bundled registry of the core at
// base: a first start installs nothing.
func installBundled(t *testing.T, base string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if code := manage(t, base, "POST", "/packages", map[string]any{"id": id, "registry": "bundled", "enable": true}, nil); code != http.StatusOK {
			t.Fatalf("installing %s from the bundled registry: status %d", id, code)
		}
	}
}

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
		// Stored, not compressed: compressing a built extension in this race-instrumented test
		// process took about 2.5 s per archive, and core reads either.
		gz, _ := gzip.NewWriterLevel(&buf, gzip.NoCompression)
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

// startJSONCore starts core on a JSON config at path, as pluxy init writes one, on a free port,
// with args, and returns its address.
func startJSONCore(t *testing.T, path string, args ...string) (string, *coreLog) {
	t.Helper()
	base, _, logs := startOnFreePort(t, filepath.Join(binDir, "pluxy"), path, func(port int) []byte {
		return []byte(`{"listen": "127.0.0.1:` + strconv.Itoa(port) + `", "client_keys": ["` + clientKey + `"], "management_key": "` + managementKey + `"}`)
	}, args...)
	return base, logs
}

// importCPA runs pluxy import cpa against the core whose config is at cfgPath.
func importCPA(t *testing.T, cfgPath string, args ...string) cliResult {
	t.Helper()
	return runCLI(t, nil, append([]string{"import", "cpa", "--config", cfgPath}, args...)...)
}

// fileKeys returns every key of the YAML file b as the import report names it: the path of
// each value that is not a mapping, and of each entry of a provider section.
func fileKeys(t *testing.T, b []byte) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	sections := map[string]bool{"openai-compatibility": true}
	for _, s := range []string{"codex", "claude", "gemini", "vertex", "xai", "meta", "interactions"} {
		sections[s+"-api-key"] = true
		sections["api-keys."+s] = true
	}
	sections["api-keys.openai-compatibility"] = true
	var out []string
	var walk func(path string, n *yaml.Node)
	walk = func(path string, n *yaml.Node) {
		switch {
		case n.Kind == yaml.MappingNode && len(n.Content) > 0 && path != "requests.payload":
			for i := 0; i < len(n.Content); i += 2 {
				p := n.Content[i].Value
				if path != "" {
					p = path + "." + p
				}
				walk(p, n.Content[i+1])
			}
		case n.Kind == yaml.SequenceNode && sections[path] && len(n.Content) > 0:
			for i := range n.Content {
				out = append(out, path+"["+strconv.Itoa(i)+"]")
			}
		default:
			out = append(out, path)
		}
	}
	walk("", doc.Content[0])
	return out
}

// listed returns the WHAT and NAME columns of each row of the three lists pluxy import cpa
// prints.
func listed(out string) map[string]bool {
	got := map[string]bool{}
	in := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "Brought (") || strings.HasPrefix(line, "Changed (") || strings.HasPrefix(line, "Not brought ("):
			in = true
		case strings.HasPrefix(line, "Needs ") || line == "":
			in = false
		case in && !strings.HasPrefix(line, "WHAT "):
			cols := strings.Fields(line)
			if len(cols) >= 3 && cols[0] == "auth" && cols[1] == "file" {
				got["auth file "+cols[2]] = true
			} else if len(cols) > 0 {
				got[cols[0]] = true
			}
		}
	}
	return got
}

// Today's example config, with its management key and auth folder set and one
// openai-compatibility provider added, and today's auth folder, come across to a running core
// with pluxy import cpa. A dry run prints the three lists, which together name every key of the
// file and every file of the folder, and writes nothing; the import then serves a request
// through the provider section's fake upstream and one through an imported Kimi subscription on
// a fake Kimi, and lists the imported accounts.
func TestImportCPA(t *testing.T) {
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
	files := map[string]map[string]any{
		"kimi-1700000000000.json": {"type": "kimi", "access_token": kimiAccess, "refresh_token": kimiRefresh,
			"token_type": "Bearer", "device_id": "device-e2e", "base_url": kimi.URL + "/coding", "expired": expired},
		"codex-ab12cd34-user@example.com-plus.json": {"type": "codex", "access_token": "fake-codex-access",
			"refresh_token": "fake-codex-refresh", "id_token": fakeIDToken("acct-e2e", "user@example.com"),
			"account_id": "acct-e2e", "email": "user@example.com", "expired": expired},
		"claude-user@example.com.json": {"type": "claude", "access_token": "fake-claude-access",
			"refresh_token": "fake-claude-refresh", "email": "user@example.com", "expired": expired, "disabled": true},
		"antigravity-user@example.com.json": {"type": "antigravity", "access_token": "fake-antigravity-access", "refresh_token": "fake-antigravity-refresh",
			"project_id": "proj-e2e", "expired": expired},
		"xai-grok@example.com.json": {"type": "xai", "auth_kind": "oauth", "access_token": "fake-xai-access", "refresh_token": "fake-xai-refresh",
			"email": "grok@example.com", "expired": expired},
		"meta-muse.json":  {"type": "meta", "auth_kind": "oauth", "api_key": "fake-meta-key", "dca_token": "dca:fake-meta-device"},
		"devin-user.json": {"type": "devin", "auth_kind": "oauth", "session_token": "fake-devin-token", "user_name": "dev-user"},
		"gemini-old.json": {"type": "gemini", "access_token": "fake-gemini-access"},
	}
	for name, v := range files {
		b, _ := json.Marshal(v)
		if err := os.WriteFile(filepath.Join(authDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := folderDigest(t, authDir)

	// Today's example config, changed only where a test must. Its host stays "": every
	// interface, which today's core listened on; the import keeps core's own listen and says so.
	example, err := os.ReadFile("../internal/legacy/testdata/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(example)
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
	yamlPath := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(yamlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	bundled := bundledRegistry(t, map[string]string{
		"pluxy/openai-compatible": "openai-compatible",
		"pluxy/kimi":              filepath.Join("kimi", "kimi"),
		"pluxy/codex":             filepath.Join("codex", "codex"),
		"pluxy/claude":            filepath.Join("claude", "claude"),
	})
	core := filepath.Join(t.TempDir(), "config.json")
	base, logs := startJSONCore(t, core, "-bundled", bundled)
	installBundled(t, base, "pluxy/openai-compatible", "pluxy/kimi", "pluxy/codex", "pluxy/claude")
	coreBefore, _ := os.ReadFile(core)
	creds := filepath.Join(filepath.Dir(core), "credentials")
	credsDigest := func() string {
		if _, err := os.Stat(creds); err != nil {
			return "none"
		}
		return folderDigest(t, creds)
	}
	credsBefore := credsDigest()

	// The dry run: three lists that together name every key of the file and every file of the
	// folder; nothing is written.
	r := importCPA(t, core, "--dry-run", yamlPath)
	t.Logf("dry run:\n%s", r)
	if r.code != 0 || !strings.Contains(r.stdout, "Dry run: nothing was written.") {
		t.Fatalf("dry run: %s", r)
	}
	got := listed(r.stdout)
	keys := fileKeys(t, []byte(cfg))
	if len(keys) < 50 {
		t.Fatalf("the example config has %d keys: the walk is wrong", len(keys))
	}
	for _, k := range keys {
		if !got[k] {
			t.Errorf("the dry run's lists do not name %s", k)
		}
	}
	for name := range files {
		if !got["auth file "+name] {
			t.Errorf("the dry run's lists do not name %s", name)
		}
	}
	if b, _ := os.ReadFile(core); !bytes.Equal(b, coreBefore) || credsDigest() != credsBefore {
		t.Error("the dry run changed core's config or credential store")
	}

	// The import.
	r = importCPA(t, core, "--json", yamlPath)
	if r.code != 0 {
		t.Fatalf("import: %s", r)
	}
	var rep struct {
		DryRun     bool `json:"dry_run"`
		Brought    []row
		Changed    []row
		NotBrought []row `json:"not_brought"`
		Needs      []any
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || rep.DryRun {
		t.Fatalf("import reply: %v %s", err, r)
	}
	find := func(rows []row, what, name string) *row {
		for i := range rows {
			if rows[i].What == what && rows[i].Name == name {
				return &rows[i]
			}
		}
		return nil
	}
	// core listens on 127.0.0.1 with a free port, and the row says it keeps that address.
	if h := find(rep.Changed, "server.host", ""); h == nil || !strings.HasPrefix(h.To, "127.0.0.1:") || !strings.Contains(h.Why, "core keeps listening on "+h.To) || !strings.Contains(h.KeepWith, "0.0.0.0:8317") {
		t.Errorf("server.host row %+v", h)
	}
	if a := find(rep.NotBrought, "auth file", "antigravity-user@example.com.json"); a == nil || !strings.Contains(a.Why, "Antigravity") {
		t.Errorf("antigravity row %+v", a)
	}
	if g := find(rep.NotBrought, "auth file", "gemini-old.json"); g == nil {
		t.Error("gemini-old.json is not under not_brought")
	}
	for _, name := range []string{"kimi-1700000000000.json", "codex-ab12cd34-user@example.com-plus.json", "claude-user@example.com.json",
		"xai-grok@example.com.json", "meta-muse.json", "devin-user.json"} {
		if find(rep.Brought, "auth file", name) == nil {
			t.Errorf("%s is not under brought", name)
		}
	}
	// Everything the example sets is brought, or asks for nothing here.
	for _, n := range rep.NotBrought {
		if n.What != "auth file" && n.What != "config-version" && !strings.Contains(n.Why, "nothing is lost") && !strings.Contains(n.Why, "did nothing") {
			t.Errorf("the example's %s is not brought: %s", n.What, n.Why)
		}
	}
	if folderDigest(t, authDir) != before {
		t.Error("the import changed today's auth folder")
	}
	if b, _ := os.ReadFile(core); strings.Contains(string(b), "your-api-key-1") {
		t.Error("the example config's public client keys came across")
	}
	logs.waitFor(t, "management API: CLIProxyAPI import")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// A request through the provider section's account.
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

	// The imported accounts are listed: the provider section's from the config, the auth
	// folder's as added by login.
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
	have := map[string]string{}
	for _, a := range accounts.Accounts {
		have[a.ID] = a.Source + " " + a.Kind + " " + strconv.FormatBool(a.Disabled)
	}
	if _, ok := have["antigravity-user-example.com"]; ok {
		t.Errorf("antigravity account was imported: %v", have["antigravity-user-example.com"])
	}
	for id, want := range map[string]string{
		"local":                                "config api_key false",
		"kimi-1700000000000":                   "login oauth false",
		"codex-ab12cd34-user-example.com-plus": "login oauth false",
		"claude-user-example.com":              "login oauth true",
		"xai-grok-example.com":                 "login oauth false",
		"meta-muse":                            "login oauth false",
		"devin-user":                           "login api_key false",
	} {
		if have[id] != want {
			t.Errorf("account %s: %q, want %q (all: %v)", id, have[id], want, have)
		}
	}
}

type row struct {
	What     string `json:"what"`
	Name     string `json:"name"`
	As       string `json:"as"`
	From     string `json:"from"`
	To       string `json:"to"`
	Why      string `json:"why"`
	KeepWith string `json:"keep_with"`
}

// A config whose management key today's core hashed comes across with a new key, shown once
// in the reply, which the pluxy command then uses; a dropped setting is named, and nothing
// prints the hash.
func TestImportCPABcryptKey(t *testing.T) {
	work := t.TempDir()
	const hash = "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234"
	yamlPath := filepath.Join(work, "config.yaml")
	cfg := "server: {host: 127.0.0.1, port: 18999, max-request-body-bytes: 1000}\n" +
		"access: {api-keys: [fake-client-key-e2e]}\n" +
		"management: {secret-key: \"" + hash + "\"}\n"
	if err := os.WriteFile(yamlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "config.json")
	startJSONCore(t, core)
	r := importCPA(t, core, yamlPath)
	if r.code != 0 {
		t.Fatalf("import: %s", r)
	}
	for _, want := range []string{"server.max-request-body-bytes", "requests are capped at 24 MiB", "management.secret-key", "bcrypt"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("the report lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r.stdout+r.stderr, hash) {
		t.Error("the output holds the hash")
	}
	b, _ := os.ReadFile(core)
	var c struct {
		ManagementKey string `json:"management_key"`
	}
	if err := json.Unmarshal(b, &c); err != nil || c.ManagementKey == managementKey || !strings.Contains(r.stdout, c.ManagementKey) {
		t.Fatalf("the new key %q is not shown once: %v\n%s", c.ManagementKey, err, r)
	}
	// The pluxy command reads the new key from the config file.
	if s := runCLI(t, nil, "status", "--config", core); s.code != 0 {
		t.Errorf("pluxy status with the new key: %s", s)
	}
}

// An import of a file core refuses prints the reason without the file's secrets.
func TestImportCPARefusalShowsNoSecret(t *testing.T) {
	work := t.TempDir()
	yamlPath := filepath.Join(work, "config.yaml")
	cfg := "management: {secret-key: fake-secret-e2e}\nrouting: {cooldown: {quota-response-mode: fake-secret-e2e}}\n"
	if err := os.WriteFile(yamlPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	core := filepath.Join(t.TempDir(), "config.json")
	startJSONCore(t, core)
	r := importCPA(t, core, "--dry-run", yamlPath)
	if r.code == 0 || !strings.Contains(r.stderr, "quota-response-mode") || strings.Contains(r.stdout+r.stderr, "fake-secret-e2e") {
		t.Errorf("import: %s\nwant a refusal without the secret", r)
	}
}

// Core also runs on today's example config itself: everything the example sets is supported,
// off or at today's default, and the example's own client keys, which are public, start core
// but refuse every client request (today's core's example API-key safe mode).
func TestTodaysExampleConfigServes(t *testing.T) {
	example, err := os.ReadFile("../internal/legacy/testdata/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	base, _, logs := startOnFreePort(t, filepath.Join(binDir, "pluxy"), path, func(port int) []byte {
		cfg := string(example)
		cfg = replaceOnce(t, cfg, "\n  host: \"\"\n", "\n  host: \"127.0.0.1\"\n")
		cfg = replaceOnce(t, cfg, "\n  port: 8317\n", "\n  port: "+strconv.Itoa(port)+"\n")
		cfg = replaceOnce(t, cfg, "\n  secret-key: \"\"\n", "\n  secret-key: \""+managementKey+"\"\n")
		return []byte(cfg)
	})
	if all := logs.waitFor(t, "config revision 1 active"); strings.Contains(all, "not supported yet, ignored") {
		t.Errorf("core named settings it does not support:\n%s", all)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp := post(t, ctx, base, "your-api-key-1", `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "unsafe_example_api_key") {
		t.Fatalf("an example key: status %d: %s, want 403 unsafe_example_api_key", resp.StatusCode, body)
	}
	if !strings.Contains(logs.waitFor(t, ""), "client_keys still holds the example config's public keys") {
		t.Error("core did not log that the example keys are configured")
	}
}
