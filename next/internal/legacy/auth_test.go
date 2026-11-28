package legacy

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// FakeIDToken is an unsigned JWT with the claims of a ChatGPT sign-in, for tests: it names
// accountID and email.
func FakeIDToken(accountID, email string) string {
	enc := base64.RawURLEncoding.EncodeToString
	claims, _ := json.Marshal(map[string]any{
		"email":                       email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID, "chatgpt_plan_type": "plus"},
	})
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(claims) + ".sig"
}

// fakePrivateKey is a new RSA key in PEM, as a service account key holds one.
func fakePrivateKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func writeFiles(t *testing.T, files map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	for name, v := range files {
		b, ok := v.([]byte)
		if !ok {
			b, _ = json.Marshal(v)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadAuthDir(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	exp := now.Add(10 * 24 * time.Hour)
	key := fakePrivateKey(t)
	dir := writeFiles(t, map[string]any{
		"codex-ab12cd34-user@example.com-plus.json": map[string]any{
			"type": "codex", "access_token": "fake-codex-access", "refresh_token": "fake-codex-refresh",
			"id_token": FakeIDToken("acct-1", "user@example.com"), "account_id": "acct-1",
			"email": "user@example.com", "expired": exp.Format(time.RFC3339), "websockets": true,
			"disabled": true, "priority": "3", "weight": 2, "prefix": " /team/ ", "headers": map[string]any{"X-A": "b"},
			"excluded_models": []any{"GPT-Image-*", " gpt-x "},
			"model_aliases": []any{map[string]any{"name": "gpt-6.1-sol", "alias": "sol", "fork": true, "display-name": "Sol"},
				map[string]any{"name": "gpt-6", "alias": "SOL"}, map[string]any{"name": "same", "alias": "same"}},
		},
		"claude-user@example.com.json": map[string]any{
			"type": "claude", "access_token": "fake-claude-access", "refresh_token": "fake-claude-refresh",
			"email": "user@example.com", "label": "work", "expired": exp.Format(time.RFC3339), "excluded_models": []any{},
		},
		"kimi-ai-1700000000000.json": map[string]any{
			"type": "kimi-ai", "access_token": "fake-kimi-access", "refresh_token": "fake-kimi-refresh",
			"device_id": "device-1", "base_url": "http://127.0.0.1:1/coding",
		},
		"vertex-my-project.json": map[string]any{
			"type": "vertex", "project_id": "my-project", "email": "sa@my-project.iam.gserviceaccount.com",
			"service_account": map[string]any{
				"type": "service_account", "project_id": "my-project", "private_key_id": "kid",
				"client_email": "sa@my-project.iam.gserviceaccount.com", "private_key": key,
				"token_uri": "https://oauth2.googleapis.com/token",
			},
		},
		"antigravity-user@example.com.json": map[string]any{
			"type": "antigravity", "access_token": "fake-ag-access", "refresh_token": "fake-ag-refresh", "expires_in": 3599,
			"timestamp": 1767225600000, "expired": exp.Format(time.RFC3339), "email": "user@example.com",
			"project_id": "proj-1", "base_url": "http://127.0.0.1:2", "antigravity_capability_hints": map[string]any{"model_ids": []any{"m"}},
		},
		"xai-user-ab12.json": map[string]any{
			"type": "xai", "auth_kind": "oauth", "access_token": "fake-xai-access", "refresh_token": "fake-xai-refresh",
			"expired": exp.Format(time.RFC3339), "email": "grok@example.com", "base_url": "http://127.0.0.1:3/v1",
			"token_endpoint": "https://auth.x.ai/oauth2/token",
		},
		"meta-minted.json": map[string]any{
			"type": "meta", "auth_kind": "oauth", "access_token": "fake-meta-key", "api_key": "fake-meta-key",
			"dca_token": "dca:fake-meta-device", "base_url": "https://api.meta.ai/v1", "email": "muse@example.com",
		},
		"meta-unminted.json": map[string]any{"type": "meta", "auth_kind": "oauth", "access_token": "dca:fake-meta-only"},
		"devin-user.json": map[string]any{
			"type": "devin", "auth_kind": "oauth", "api_key": "fake-devin-token", "session_token": "fake-devin-token",
			"user_name": "dev-user", "user_id": "u1", "base_url": "http://127.0.0.1:4",
		},
		"devin-empty.json": map[string]any{"type": "devin"},
		"gemini-old.json":  map[string]any{"type": "gemini", "access_token": "fake"},
		"notype.json":      map[string]any{"access_token": "fake"},
		"nulltype.json":    map[string]any{"type": nil, "access_token": "fake"},
		"blanktype.json":   map[string]any{"type": " ", "access_token": "fake"},
		"broken.json":      []byte("{"),
		"codex-empty.json": map[string]any{"type": "codex"},
		"notes.txt":        []byte("not a credential"),
	})
	files, skipped, err := ReadAuthDir(dir, ImportOptions{CodexIdentity: false, ImageGenerationTool: true, Images: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AuthFile{}
	for _, f := range files {
		got[f.Record.ID] = f
	}
	if len(got) != 9 {
		t.Fatalf("read %d accounts, want 9: %v", len(got), got)
	}

	c := got["codex-ab12cd34-user-example.com-plus"]
	if c.Record.Extension != Codex || !c.Record.Disabled || c.Record.Label != "user@example.com" ||
		c.Record.Account.Priority != 3 || c.Record.Account.Weight == nil || *c.Record.Account.Weight != 2 {
		t.Errorf("codex record: %+v", c.Record)
	}
	equalJSON(t, c.Record.Account.Settings, `{"websockets": true, "codex_identity": false}`)
	cc := c.Record.Credential
	if cc.Kind != protocol.CredentialOAuth || cc.AccessToken != "fake-codex-access" || cc.RefreshToken != "fake-codex-refresh" ||
		cc.ExpiresAt != exp.Unix() || cc.RefreshAt != exp.Add(-96*time.Hour).Unix() {
		t.Errorf("codex credential: %+v", cc)
	}
	equalJSON(t, cc.Data, `{"id_token": "`+FakeIDToken("acct-1", "user@example.com")+`", "account_id": "acct-1"}`)
	// The file's headers are the account's (they were named as not imported before).
	if len(c.Ignored) != 0 || !reflect.DeepEqual(c.Record.Account.Headers, map[string]string{"X-A": "b"}) {
		t.Errorf("codex ignored = %v, headers = %v; want none ignored and headers X-A: b", c.Ignored, c.Record.Account.Headers)
	}
	// The file's own naming settings, read as today's core reads them.
	if a := c.Record.Account; a.Prefix != "team" || !reflect.DeepEqual(a.ExcludedModels, []string{"gpt-image-*", "gpt-x"}) ||
		!reflect.DeepEqual(a.ModelAlias, []route.ModelAlias{{Name: "gpt-6.1-sol", Alias: "sol", Fork: true, DisplayName: "Sol"}}) {
		t.Errorf("codex naming: prefix %q, excluded %v, aliases %+v", a.Prefix, a.ExcludedModels, a.ModelAlias)
	}

	cl := got["claude-user-example.com"]
	if cl.Record.Extension != Claude || cl.Record.Label != "work" || cl.Record.Credential.RefreshAt != exp.Add(-time.Hour).Unix() ||
		cl.Record.Account.Settings != nil || len(cl.Ignored) != 0 {
		t.Errorf("claude: %+v", cl)
	}

	k := got["kimi-ai-1700000000000"]
	equalJSON(t, k.Record.Account.Settings, `{"domain": "kimi.ai", "base_url": "http://127.0.0.1:1/coding"}`)
	equalJSON(t, k.Record.Credential.Data, `{"device_id": "device-1"}`)
	if k.Record.Extension != Kimi || k.Record.Credential.RefreshAt != 0 {
		t.Errorf("kimi: %+v", k.Record)
	}

	v := got["vertex-my-project"]
	if v.Record.Extension != Gemini || v.Record.Credential.Kind != "service_account" || v.Record.Credential.AccessToken != "" {
		t.Errorf("vertex: %+v", v.Record)
	}
	equalJSON(t, v.Record.Account.Settings, `{"vertex": {"project": "my-project", "location": "us-central1"}}`)
	// Only the key's secret part is the credential: core removes every credential string from
	// replies, so plain fields such as the project would be cut out of model output.
	equalJSON(t, v.Record.Credential.Data, `{"client_email": "sa@my-project.iam.gserviceaccount.com", "private_key": `+mustJSON(key)+`}`)

	x := got["xai-user-ab12"]
	xc := x.Record.Credential
	if x.Record.Extension != XAI || x.Record.Label != "grok@example.com" || xc.Kind != protocol.CredentialOAuth ||
		xc.AccessToken != "fake-xai-access" || xc.RefreshToken != "fake-xai-refresh" ||
		xc.ExpiresAt != exp.Unix() || xc.RefreshAt != exp.Add(-5*time.Minute).Unix() {
		t.Errorf("xai: %+v", x.Record)
	}
	equalJSON(t, xc.Data, `{"token_endpoint": "https://auth.x.ai/oauth2/token"}`)
	equalJSON(t, x.Record.Account.Settings, `{"base_url": "http://127.0.0.1:3/v1"}`)

	// An Antigravity file is the archived extension's account: the Google Cloud project stays with
	// the credential and base_url is a setting.
	ag := got["antigravity-user-example.com"]
	agc := ag.Record.Credential
	if ag.Record.Extension != Antigravity || ag.Record.Label != "user@example.com" || agc.Kind != protocol.CredentialOAuth ||
		agc.AccessToken != "fake-ag-access" || agc.RefreshToken != "fake-ag-refresh" ||
		agc.ExpiresAt != exp.Unix() || agc.RefreshAt != exp.Add(-30*time.Minute).Unix() || len(ag.Ignored) != 0 {
		t.Errorf("antigravity: %+v", ag)
	}
	equalJSON(t, agc.Data, `{"project_id": "proj-1"}`)
	equalJSON(t, ag.Record.Account.Settings, `{"base_url": "http://127.0.0.1:2"}`)

	mm := got["meta-minted"]
	mc := mm.Record.Credential
	if mm.Record.Extension != Meta || mm.Record.Label != "muse@example.com" || mc.Kind != protocol.CredentialOAuth ||
		mc.AccessToken != "fake-meta-key" || mc.RefreshToken != "dca:fake-meta-device" || mc.RefreshAt != 0 {
		t.Errorf("meta: %+v", mm.Record)
	}
	equalJSON(t, mc.Data, `{"base_url": "https://api.meta.ai/v1"}`)
	// A login with no key minted yet has only the device token, so core mints a key at once.
	mu := got["meta-unminted"].Record.Credential
	if mu.AccessToken != "" || mu.RefreshToken != "dca:fake-meta-only" || mu.RefreshAt != now.Unix() {
		t.Errorf("meta without a key: %+v", mu)
	}

	d := got["devin-user"]
	if d.Record.Extension != Devin || d.Record.Label != "dev-user" || d.Record.Credential.Kind != protocol.CredentialAPIKey ||
		d.Record.Credential.APIKey != "fake-devin-token" {
		t.Errorf("devin: %+v", d.Record)
	}
	equalJSON(t, d.Record.Account.Settings, `{"base_url": "http://127.0.0.1:4"}`)

	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.File] = s.Reason
	}
	for file, want := range map[string]string{
		"devin-empty.json": "it holds no token",
		"gemini-old.json":  "type gemini is not supported (CLIProxyAPI does not load it either)",
		"notype.json":      "it names no type (CLIProxyAPI does not load it either)",
		"nulltype.json":    "it names no type (CLIProxyAPI does not load it either)",
		"blanktype.json":   "it names no type (CLIProxyAPI does not load it either)",
		"broken.json":      "not a JSON object",
		"codex-empty.json": "it holds no token",
	} {
		if reasons[file] != want {
			t.Errorf("%s skipped for %q, want %q", file, reasons[file], want)
		}
	}
	if len(skipped) != 7 {
		t.Errorf("skipped %v", skipped)
	}
}

// A Vertex file opts in to the Interactions API when "interactions" or "native_interactions" is JSON
// true at the top of the file, as today's core reads them: the text "true", a false value and a key
// inside service_account do not.
func TestVertexInteractionsFlags(t *testing.T) {
	key := fakePrivateKey(t)
	vertex := func(top map[string]any) map[string]any {
		m := map[string]any{"type": "vertex", "project_id": "my-project",
			"service_account": map[string]any{"client_email": "sa@my-project.iam.gserviceaccount.com", "private_key": key}}
		for k, v := range top {
			m[k] = v
		}
		return m
	}
	inside := vertex(nil)
	inside["service_account"].(map[string]any)["interactions"] = true
	dir := writeFiles(t, map[string]any{
		"vertex-on.json":     vertex(map[string]any{"interactions": true}),
		"vertex-native.json": vertex(map[string]any{"native_interactions": true}),
		"vertex-text.json":   vertex(map[string]any{"interactions": "true", "native_interactions": "true"}),
		"vertex-off.json":    vertex(map[string]any{"interactions": false, "native_interactions": false}),
		"vertex-inside.json": inside,
		"vertex-plain.json":  vertex(nil),
	})
	files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AuthFile{}
	for _, f := range files {
		byID[f.Record.ID] = f
	}
	if len(byID) != 6 {
		t.Fatalf("read %d accounts, want 6: %v", len(byID), byID)
	}
	on := `{"vertex": {"project": "my-project", "location": "us-central1"}, "interactions": true}`
	off := `{"vertex": {"project": "my-project", "location": "us-central1"}}`
	for id, want := range map[string]string{"vertex-on": on, "vertex-native": on, "vertex-text": off,
		"vertex-off": off, "vertex-inside": off, "vertex-plain": off} {
		equalJSON(t, byID[id].Record.Account.Settings, want)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestImportSkipsKnownAccounts(t *testing.T) {
	dir := writeFiles(t, map[string]any{
		"claude-a.json": map[string]any{"type": "claude", "access_token": "fake-a"},
		"claude-b.json": map[string]any{"type": "claude", "access_token": "fake-b"},
		"claude-c.json": map[string]any{"type": "claude", "access_token": "fake-c"},
	})
	credDir := filepath.Join(t.TempDir(), "credentials")
	store, err := cred.Open(cred.FileBackend{Dir: credDir})
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reserved := func(id string) bool { return id == "claude-c" }
	imported, skipped, err := Import(store, files, reserved)
	if err != nil || len(imported) != 2 || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "config already has") {
		t.Fatalf("first import: %v %v %v", imported, skipped, err)
	}
	// Running it again imports nothing new.
	imported, skipped, err = Import(store, files, reserved)
	if err != nil || len(imported) != 0 || len(skipped) != 3 {
		t.Fatalf("second import: %v %v %v", imported, skipped, err)
	}
	// The records are on disk, readable by the store core opens.
	again, err := cred.Open(cred.FileBackend{Dir: credDir})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := again.Get("claude-a"); !ok || r.Extension != Claude || r.Credential.AccessToken != "fake-a" || r.State != cred.StateActive {
		t.Errorf("stored record: %+v", r)
	}
	// The auth folder is left as it was.
	if names, _ := AuthFiles(dir); len(names) != 3 {
		t.Errorf("auth folder now holds %v", names)
	}
}

// conflictCreator refuses one ID as a running core's manager refuses an ID a login will take.
type conflictCreator struct {
	Creator
	id string
}

func (c conflictCreator) Create(r cred.Record) (cred.Record, error) {
	if r.ID == c.id {
		return cred.Record{}, fmt.Errorf("%w: a login for account %q is running", cred.ErrConflict, r.ID)
	}
	return c.Creator.Create(r)
}

// An ID the store refuses as taken by something else is skipped like one it already has: the
// other accounts still import.
func TestImportSkipsConflictingAccount(t *testing.T) {
	dir := writeFiles(t, map[string]any{
		"claude-a.json": map[string]any{"type": "claude", "access_token": "fake-a"},
		"claude-b.json": map[string]any{"type": "claude", "access_token": "fake-b"},
	})
	store, err := cred.Open(cred.FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	imported, skipped, err := Import(conflictCreator{store, "claude-a"}, files, func(string) bool { return false })
	if err != nil || len(imported) != 1 || imported[0].Record.ID != "claude-b" ||
		len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "login") {
		t.Fatalf("import: imported %v, skipped %v, err %v", imported, skipped, err)
	}
	if _, ok := store.Get("claude-a"); ok {
		t.Error("claude-a was stored")
	}
}

// The Kimi site is decided as today's core decides it: domain, then base_url's host, then type.
func TestKimiDomain(t *testing.T) {
	for _, c := range []struct{ domain, baseURL, typ, want string }{
		{"", "", "kimi", "kimi.com"},
		{"", "", "kimi-ai", "kimi.ai"},
		{"ai", "", "kimi", "kimi.ai"},
		{"", "https://api.kimi.ai/coding", "kimi", "kimi.ai"},
		{"com", "https://api.kimi.ai/coding", "kimi-ai", "kimi.com"},
		{"", "http://127.0.0.1:1/coding", "kimi-ai", "kimi.ai"},
		{"", "http://127.0.0.1:1/coding", "", ""},
	} {
		if got := kimiDomain(c.domain, c.baseURL, c.typ); got != c.want {
			t.Errorf("kimiDomain(%q, %q, %q) = %q, want %q", c.domain, c.baseURL, c.typ, got, c.want)
		}
	}
}

func TestLargeFileIsSkipped(t *testing.T) {
	big := `{"type":"claude","access_token":"` + strings.Repeat("a", MaxAuthFile) + `"}`
	dir := writeFiles(t, map[string]any{"claude-big.json": []byte(big)})
	files, skipped, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	if err != nil || len(files) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "larger than") {
		t.Errorf("files %v, skipped %v, err %v", files, skipped, err)
	}
}

// The credential store may not be today's auth folder or inside it, by any path to it.
func TestCheckImportFolders(t *testing.T) {
	root := t.TempDir()
	auth := filepath.Join(root, "auths")
	if err := os.Mkdir(auth, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(auth, link); err != nil {
		t.Fatal(err)
	}
	for credDir, ok := range map[string]bool{
		auth:                                   false,
		auth + "/":                             false,
		filepath.Join(auth, "credentials"):     false,
		link:                                   false,
		filepath.Join(link, "new", "deeper"):   false,
		filepath.Join(root, "credentials"):     true,
		filepath.Join(root, "auths-elsewhere"): true,
	} {
		err := CheckImportFolders(auth, credDir)
		if (err == nil) != ok {
			t.Errorf("CheckImportFolders(auths, %s) = %v", credDir, err)
		}
		if !ok && err != nil && !strings.Contains(err.Error(), "is CLIProxyAPI's auth folder or inside it") {
			t.Errorf("CheckImportFolders(auths, %s) = %q, want CLIProxyAPI auth folder message", credDir, err)
		}
	}
}

// A credential file's request_retry (or request-retry), a number or a numeric text, is its
// account's request_retry, as today's core reads it: request_retry first, a negative one
// inherits routing's count, and one that is not a number gives way to request-retry.
func TestAuthFileRequestRetry(t *testing.T) {
	file := func(fields map[string]any) map[string]any {
		f := map[string]any{"type": "codex", "access_token": "fake-codex-access", "email": "user@example.com"}
		for k, v := range fields {
			f[k] = v
		}
		return f
	}
	dir := writeFiles(t, map[string]any{
		"codex-a.json": file(map[string]any{"request_retry": 3}),
		"codex-b.json": file(map[string]any{"request_retry": " 0 "}),
		"codex-c.json": file(map[string]any{"request_retry": -1, "request-retry": 5}),
		"codex-d.json": file(map[string]any{"request_retry": "soon", "request-retry": 2}),
		"codex-e.json": file(nil),
	})
	files, _, err := ReadAuthDir(dir, ImportOptions{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, f := range files {
		var v any
		if n := f.Record.Account.RequestRetry; n != nil {
			v = *n
		}
		got[f.Record.ID] = v
	}
	want := map[string]any{"codex-a": 3, "codex-b": 0, "codex-c": nil, "codex-d": 2, "codex-e": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request_retry by file = %v, want %v", got, want)
	}
}

// A file's proxy_url (or proxy-url), as today's core writes it, is the account's proxy_url. A
// value this core cannot use skips the file, with a reason that leaves out the password, rather
// than import an account that would go without its proxy.
func TestAuthFileProxyURL(t *testing.T) {
	file := func(fields map[string]any) map[string]any {
		f := map[string]any{"type": "codex", "access_token": "fake-codex-access", "email": "user@example.com"}
		for k, v := range fields {
			f[k] = v
		}
		return f
	}
	dir := writeFiles(t, map[string]any{
		"codex-a.json": file(map[string]any{"proxy_url": "socks5://user:fake-proxy-pass@proxy.example:1080"}),
		"codex-b.json": file(map[string]any{"proxy-url": " direct "}),
		"codex-c.json": file(map[string]any{"proxy_url": "", "proxy-url": "http://proxy.example:3128"}),
		"codex-d.json": file(nil),
		"codex-e.json": file(map[string]any{"proxy_url": "ftp://user:fake-proxy-pass@proxy.example"}),
		"codex-f.json": file(map[string]any{"proxy_url": 8080}),
	})
	files, skipped, err := ReadAuthDir(dir, ImportOptions{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Record.ID] = f.Record.Account.ProxyURL
		if len(f.Ignored) != 0 {
			t.Errorf("%s: ignored %v", f.File, f.Ignored)
		}
	}
	want := map[string]string{"codex-a": "socks5://user:fake-proxy-pass@proxy.example:1080", "codex-b": "direct",
		"codex-c": "http://proxy.example:3128", "codex-d": ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("proxy_url by file = %v, want %v", got, want)
	}
	if len(skipped) != 2 || skipped[0].File != "codex-e.json" || skipped[1].File != "codex-f.json" ||
		!strings.Contains(skipped[0].Reason, "proxy_url") || strings.Contains(skipped[0].Reason, "fake-proxy-pass") {
		t.Errorf("skipped = %+v, want codex-e and codex-f with a reason naming proxy_url and not the password", skipped)
	}
}

// A file's headers that copy a client header come across as "$" and its name; a copy of the
// client's key header and $CPA-SESSION-ID are named under ignored, with why, and not brought.
func TestAuthFileCopiedHeaders(t *testing.T) {
	dir := writeFiles(t, map[string]any{"codex-a.json": map[string]any{"type": "codex", "access_token": "fake-codex-access",
		"headers": map[string]any{"X-Opencode-Session": "$x-opencode-session", "User-Agent": " $ User-Agent ",
			"X-Cpa": "$CPA-SESSION-ID", "X-Key": "$Authorization", "X-Title": "pluxy"}}})
	files, skipped, err := ReadAuthDir(dir, ImportOptions{}, time.Now())
	if err != nil || len(files) != 1 || len(skipped) != 0 {
		t.Fatalf("files %+v, skipped %+v, err %v", files, skipped, err)
	}
	want := map[string]string{"X-Opencode-Session": "$x-opencode-session", "User-Agent": "$User-Agent", "X-Title": "pluxy"}
	if got := files[0].Record.Account.Headers; !reflect.DeepEqual(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	ignored := strings.Join(files[0].Ignored, "\n")
	for _, w := range []string{"headers.X-Cpa (its value holds $CPA-SESSION-ID, a CLIProxyAPI session ID that pluxy does not have)", "headers.X-Key (it copies the client's Authorization header"} {
		if !strings.Contains(ignored, w) {
			t.Errorf("ignored does not say %q:\n%s", w, ignored)
		}
	}
	if len(files[0].Ignored) != 2 {
		t.Errorf("ignored = %v, want the two headers only", files[0].Ignored)
	}
}

// An imported xAI login gets "inject_x_search": true when the config turns on
// upstream.xai.inject-x-search (or its older spelling), and no such setting by default, as an
// imported Codex login gets its own options.
func TestXAIInjectXSearchImport(t *testing.T) {
	dir := writeFiles(t, map[string]any{
		"xai-user.json": map[string]any{"type": "xai", "access_token": "fake-xai-access", "refresh_token": "fake-xai-refresh", "base_url": "http://127.0.0.1:3/v1"},
	})
	for _, tc := range []struct{ name, yaml, want string }{
		{"default", "port: 8317", `{"base_url": "http://127.0.0.1:3/v1"}`},
		{"false", "upstream: {xai: {inject-x-search: false}}", `{"base_url": "http://127.0.0.1:3/v1"}`},
		{"v8", "upstream: {xai: {inject-x-search: true}}", `{"base_url": "http://127.0.0.1:3/v1", "inject_x_search": true}`},
		{"older spelling", "oauth: {providers: {xai: {inject-x-search: true}}}", `{"base_url": "http://127.0.0.1:3/v1", "inject_x_search": true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, _, err := ReadAuthDir(dir, translate(t, tc.yaml).Import, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 {
				t.Fatalf("imported %d accounts, want 1", len(files))
			}
			equalJSON(t, files[0].Record.Account.Settings, tc.want)
		})
	}
}

// siwcFile is a Codex file as today's core writes it after a Sign in with ChatGPT login: the
// ChatGPT sign-in at the top and the SIWC token set in "siwc".
func siwcFile(extra map[string]any) map[string]any {
	m := map[string]any{
		"type": "codex", "access_token": "fake-outer-access", "refresh_token": "fake-outer-refresh",
		"email": "user@example.com", "priority": 4, "weight": 2, "prefix": "team", "websockets": true,
		"disable_cooling": true, "request_retry": 3, "excluded_models": []any{"gpt-x"},
		"model_aliases": []any{map[string]any{"name": "gpt-6", "alias": "six"}},
		"headers":       map[string]any{"X-A": "b"},
		"siwc": map[string]any{
			"access_token": "fake-siwc-access", "refresh_token": "fake-siwc-refresh", "id_token": "fake-siwc-id",
			"client_id": "fake-client", "device_id": "fake-device", "scope": "openid fake.scope",
			"expires_at": "2026-11-20T12:00:00Z", "expired": "2026-11-19T12:00:00Z",
		},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func readSIWC(t *testing.T, file map[string]any, opts ImportOptions) map[string]AuthFile {
	t.Helper()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	files, _, err := ReadAuthDir(writeFiles(t, map[string]any{"codex-user.json": file}), opts, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]AuthFile{}
	for _, f := range files {
		got[f.Record.ID] = f
	}
	return got
}

// A file with a siwc section gives a second account, in use when use_siwc is on, with the SIWC
// token set, the file's routing settings and only the settings SIWC reads.
func TestSIWCAccount(t *testing.T) {
	opts := ImportOptions{CodexIdentity: false, ImageGenerationTool: false, Images: false, SIWCIdentifier: "my-agent"}
	got := readSIWC(t, siwcFile(map[string]any{"use_siwc": true}), opts)
	if len(got) != 2 {
		t.Fatalf("read %d accounts, want 2: %v", len(got), got)
	}
	outer, s := got["codex-user"], got["codex-user-siwc"]
	equalJSON(t, outer.Record.Account.Settings, `{"websockets": true, "codex_identity": false, "image_generation_tool": false, "images": false}`)
	if outer.Record.Credential.AccessToken != "fake-outer-access" || outer.Record.Disabled {
		t.Errorf("the file's own account changed: %+v", outer.Record)
	}
	if s.File != "codex-user.json" || s.Record.Extension != Codex || s.Record.Disabled || s.Record.Label != "user@example.com" ||
		s.Record.State != cred.StateActive {
		t.Errorf("siwc record: %+v", s.Record)
	}
	exp := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	want := protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "fake-siwc-access", RefreshToken: "fake-siwc-refresh",
		ExpiresAt: exp.Unix(), RefreshAt: exp.Add(-5 * time.Minute).Unix()}
	c := s.Record.Credential
	data := c.Data
	c.Data = nil
	if !reflect.DeepEqual(c, want) {
		t.Errorf("credential = %+v, want %+v", c, want)
	}
	equalJSON(t, data, `{"siwc": true, "client_id": "fake-client", "device_id": "fake-device", "scope": "openid fake.scope", "id_token": "fake-siwc-id"}`)
	equalJSON(t, s.Record.Account.Settings, `{"websockets": true, "signin_with_chatgpt_identifier": "my-agent"}`)
	a, b := s.Record.Account, outer.Record.Account
	if a.Priority != 4 || a.Weight == nil || *a.Weight != 2 || a.Prefix != b.Prefix || a.Prefix == "" || a.DisableCooling == nil || !*a.DisableCooling ||
		a.RequestRetry == nil || *a.RequestRetry != 3 || !reflect.DeepEqual(a.ExcludedModels, b.ExcludedModels) || len(a.ExcludedModels) != 1 ||
		!reflect.DeepEqual(a.ModelAlias, b.ModelAlias) || len(a.ModelAlias) != 1 || len(s.Ignored) != 0 ||
		!reflect.DeepEqual(a.Headers, map[string]string{"X-A": "b"}) || !reflect.DeepEqual(a.Headers, b.Headers) {
		t.Errorf("siwc account settings: %+v, ignored %v", a, s.Ignored)
	}
	// Without the identifier option, or the websockets switch, the settings are left out.
	got = readSIWC(t, siwcFile(map[string]any{"use_siwc": "true", "websockets": false}), DefaultImportOptions)
	if s := got["codex-user-siwc"]; s.Record.Disabled || s.Record.Account.Settings != nil {
		t.Errorf("use_siwc as text: %+v", s.Record)
	}
}

// The SIWC account is off unless the file says use_siwc, and off when the file is disabled.
func TestSIWCDisabled(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"absent":   nil,
		"false":    {"use_siwc": false},
		"text":     {"use_siwc": "no"},
		"disabled": {"use_siwc": true, "disabled": true},
	} {
		got := readSIWC(t, siwcFile(extra), DefaultImportOptions)
		if len(got) != 2 || !got["codex-user-siwc"].Record.Disabled {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// A siwc section without a token gives no account; one with only an access token (under the
// older name "access") or only a refresh token does, and a file whose own tokens are empty is
// skipped while its SIWC account still imports.
func TestSIWCTokens(t *testing.T) {
	if got := readSIWC(t, siwcFile(map[string]any{"use_siwc": true, "siwc": map[string]any{"client_id": "fake-client"}}), DefaultImportOptions); len(got) != 1 {
		t.Errorf("a siwc section without a token gave %v", got)
	}
	got := readSIWC(t, siwcFile(map[string]any{"use_siwc": true, "siwc": map[string]any{"access": "fake-old-access", "expired": "2026-11-20T12:00:00Z"}}), DefaultImportOptions)
	if s, ok := got["codex-user-siwc"]; !ok || s.Record.Credential.AccessToken != "fake-old-access" ||
		s.Record.Credential.ExpiresAt != time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("access/expired spelling: %+v", got)
	}
	got = readSIWC(t, siwcFile(map[string]any{"use_siwc": true, "siwc": map[string]any{"refresh_token": "fake-r"}}), DefaultImportOptions)
	if s, ok := got["codex-user-siwc"]; !ok || s.Record.Credential.RefreshToken != "fake-r" || s.Record.Credential.RefreshAt != 0 || s.Record.Credential.Data == nil {
		t.Errorf("refresh token only: %+v", got)
	}
	file := siwcFile(map[string]any{"use_siwc": true})
	delete(file, "access_token")
	delete(file, "refresh_token")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	files, skipped, err := ReadAuthDir(writeFiles(t, map[string]any{"codex-user.json": file}), DefaultImportOptions, now)
	if err != nil || len(files) != 1 || files[0].Record.ID != "codex-user-siwc" || len(skipped) != 1 || skipped[0].Reason != "it holds no token" {
		t.Errorf("tokenless file: %+v, skipped %v, %v", files, skipped, err)
	}
}

// The suffix keeps the account ID valid and within its length even for a file name at the limit.
func TestSIWCAccountIDLength(t *testing.T) {
	name := strings.Repeat("a", 128)
	files, _, err := ReadAuthDir(writeFiles(t, map[string]any{name + ".json": siwcFile(nil)}), DefaultImportOptions, time.Now())
	if err != nil || len(files) != 2 {
		t.Fatalf("%v %v", files, err)
	}
	if id := files[1].Record.ID; len(id) != 128 || !strings.HasSuffix(id, "-siwc") || files[0].Record.ID == id {
		t.Errorf("ids %q %q", files[0].Record.ID, id)
	}
}

// A SIWC account does not take the ID another file imports as: foo.json's SIWC account beside a
// foo-siwc.json gets foo-siwc-2, and reading the folder again gives the same IDs.
func TestSIWCAccountIDCollision(t *testing.T) {
	dir := writeFiles(t, map[string]any{"foo.json": siwcFile(nil), "foo-siwc.json": siwcFile(nil)})
	for range 2 {
		files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, f := range files {
			ids = append(ids, f.File+":"+f.Record.ID)
		}
		slices.Sort(ids)
		want := []string{"foo-siwc.json:foo-siwc", "foo-siwc.json:foo-siwc-siwc", "foo.json:foo", "foo.json:foo-siwc-2"}
		if !slices.Equal(ids, want) {
			t.Errorf("ids %v, want %v", ids, want)
		}
	}
}

// One file read on its own gives what reading a folder holding only it gives: a Codex file's own
// account and its SIWC account, as foo and foo-siwc; a file over MaxAuthFile is skipped, and a
// file that is not JSON says so.
func TestReadAuthFile(t *testing.T) {
	b, _ := json.Marshal(siwcFile(nil))
	files, skipped := ReadAuthFile("foo.json", b, DefaultImportOptions, time.Now())
	var ids []string
	for _, f := range files {
		ids = append(ids, f.File+":"+f.Record.ID)
	}
	if want := []string{"foo.json:foo", "foo.json:foo-siwc"}; !slices.Equal(ids, want) || len(skipped) != 0 {
		t.Errorf("ids %v, skipped %v; want %v", ids, skipped, want)
	}
	for _, c := range []struct{ content, reason string }{
		{`{"type":"claude","access_token":"` + strings.Repeat("a", MaxAuthFile) + `"}`, "larger than 1024 KiB"},
		{`{"type":"claude",`, "not a JSON object"},
	} {
		files, skipped := ReadAuthFile("x.json", []byte(c.content), DefaultImportOptions, time.Now())
		if len(files) != 0 || !slices.Equal(skipped, []Skipped{{File: "x.json", Reason: c.reason}}) {
			t.Errorf("files %v, skipped %v; want skipped %q", files, skipped, c.reason)
		}
	}
}

// upstream.claude.model-level-cooling reaches imported Claude accounts as model_level_cooling.
func TestImportClaudeModelLevelCooling(t *testing.T) {
	dir := writeFiles(t, map[string]any{"claude-a.json": map[string]any{"type": "claude", "access_token": "fake-a"}})
	opts := DefaultImportOptions
	opts.ClaudeModelLevelCooling = true
	files, _, err := ReadAuthDir(dir, opts, time.Now())
	if err != nil || len(files) != 1 {
		t.Fatalf("read %v, %v", files, err)
	}
	equalJSON(t, files[0].Record.Account.Settings, `{"model_level_cooling": true}`)
}

// An Antigravity file with no project_id still imports, and its account is renewed at once when it
// has a refresh token, because a request needs the Google Cloud project and the extension's
// renewal finds it. A file with no token at all is skipped, as every other type is.
func TestAntigravityWithoutProject(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	read := func(file map[string]any) ([]AuthFile, []Skipped) {
		b, _ := json.Marshal(file)
		return ReadAuthFile("antigravity-a.json", b, DefaultImportOptions, now)
	}

	files, skipped := read(map[string]any{"type": "antigravity", "access_token": "fake-a", "refresh_token": "fake-r"})
	if len(files) != 1 || len(skipped) != 0 {
		t.Fatalf("read %v, skipped %v", files, skipped)
	}
	c := files[0].Record.Credential
	if c.Data != nil || c.RefreshAt != now.Unix() || files[0].Record.Account.Settings != nil {
		t.Errorf("no project: data %s, refresh_at %d (want %d), settings %s", c.Data, c.RefreshAt, now.Unix(), files[0].Record.Account.Settings)
	}

	// Without a refresh token there is nothing to renew with: the account is left as it is.
	files, _ = read(map[string]any{"type": "antigravity", "access_token": "fake-a"})
	if len(files) != 1 || files[0].Record.Credential.RefreshAt != 0 {
		t.Errorf("no project, no refresh token: %+v", files)
	}

	files, skipped = read(map[string]any{"type": "antigravity", "project_id": "proj-1"})
	if len(files) != 0 || !slices.Equal(skipped, []Skipped{{File: "antigravity-a.json", Reason: "it holds no token"}}) {
		t.Errorf("no token: files %v, skipped %v", files, skipped)
	}
}

// signInProvider is an extension that signs every login in at once with grant, for new logins to
// imported accounts.
type signInProvider struct{ grant *protocol.Grant }

func (p *signInProvider) Info() protocol.Info {
	return protocol.Info{ID: Codex, Operations: []string{protocol.OpLogin},
		LoginMethods: []protocol.LoginMethod{{Method: "device", Kind: protocol.LoginDevice}}}
}

func (p *signInProvider) Login(context.Context, protocol.Account, protocol.Login) cred.LoginCall {
	return &signInCall{grant: p.grant}
}

func (p *signInProvider) Refresh(context.Context, protocol.Account) (*protocol.Credential, *llm.Error) {
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "not used"}
}

func (p *signInProvider) Provider(string) cred.Provider { return p }
func (p *signInProvider) Running() []string             { return []string{Codex} }

type signInCall struct {
	grant *protocol.Grant
	done  bool
}

func (c *signInCall) Next() protocol.Reply {
	if !c.done {
		c.done = true
		return protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone}, Grant: c.grant}
	}
	return protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeCancelled}}}
}
func (c *signInCall) Send(map[string]string) error { return nil }
func (c *signInCall) Close()                       {}

// A credential file's own label is a name a person gave the account; its e-mail (Devin: its user
// name) is the provider's label. Imported, taking the name away shows the e-mail, or no label
// when the file has none, and a new sign-in is checked against the e-mail, never the name: the
// same e-mail (letter case aside) is let in and the name stays, another e-mail is refused. The
// SIWC account of a Codex file has the file's labels.
func TestImportedFileLabelIsANameAndEmailTheProviders(t *testing.T) {
	dir := writeFiles(t, map[string]any{
		"codex-user.json":     siwcFile(map[string]any{"label": "work", "use_siwc": true}),
		"codex-nameonly.json": map[string]any{"type": "codex", "access_token": "fake-a", "label": "home"},
		"codex-plain.json":    map[string]any{"type": "codex", "access_token": "fake-b", "email": "plain@example.com"},
		"devin-user.json": map[string]any{
			"type": "devin", "auth_kind": "oauth", "api_key": "fake-devin-token", "session_token": "fake-devin-token",
			"user_name": "dev-user", "user_id": "u1", "base_url": "http://127.0.0.1:4", "label": "mine",
		},
	})
	files, _, err := ReadAuthDir(dir, DefaultImportOptions, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	store, err := cred.Open(cred.FileBackend{Dir: filepath.Join(t.TempDir(), "credentials")})
	if err != nil {
		t.Fatal(err)
	}
	p := &signInProvider{}
	m := cred.NewManager(store, p, cred.Options{})
	t.Cleanup(m.Close)
	if imported, skipped, err := Import(m, files, func(string) bool { return false }); err != nil || len(imported) != 5 {
		t.Fatalf("import: imported %v, skipped %v, %v", imported, skipped, err)
	}
	signIn := func(id, email string) cred.LoginView {
		t.Helper()
		p.grant = &protocol.Grant{Label: email, Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "fake-new-" + email}}
		v, err := m.StartLogin(context.Background(), cred.LoginRequest{Account: route.Account{ID: id}, Method: "device", Reauth: true})
		if err != nil {
			t.Fatalf("signing in to %s again: %v", id, err)
		}
		return v
	}
	none := ""

	// Named, signed in again: the e-mail is let in, and the name stays.
	if v := signIn("codex-user", "someone@example.com"); v.State != cred.LoginFailed {
		t.Errorf("a new sign-in to codex-user with another e-mail ended %s, want failed", v.State)
	}
	if v := signIn("codex-user", "USER@example.com"); v.State != cred.LoginDone {
		t.Errorf("a new sign-in to codex-user with its file's e-mail ended %s: %+v; want done", v.State, v.Error)
	}
	if r, _ := store.Get("codex-user"); r.Label != "work" {
		t.Errorf("codex-user's label after the new sign-in = %q, want the file's label", r.Label)
	}

	// The name taken away: the provider's label shows, or none.
	for id, want := range map[string][2]string{
		"codex-user-siwc": {"work", "user@example.com"},
		"codex-nameonly":  {"home", ""},
		"codex-plain":     {"plain@example.com", "plain@example.com"},
		"devin-user":      {"mine", "dev-user"},
	} {
		if r, _ := store.Get(id); r.Label != want[0] {
			t.Errorf("%s imported with label %q, want %q", id, r.Label, want[0])
		}
		if r, err := m.SetAccount(id, nil, nil, &none); err != nil || r.Label != want[1] {
			t.Errorf("%s with its name taken away: label %q, %v; want %q", id, r.Label, err, want[1])
		}
	}
	if v := signIn("codex-user-siwc", "User@Example.com"); v.State != cred.LoginDone {
		t.Errorf("a new sign-in to codex-user-siwc with its file's e-mail ended %s: %+v; want done", v.State, v.Error)
	}
}
