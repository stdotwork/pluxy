package legacy

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
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
			"disabled": true, "priority": "3", "weight": 2, "prefix": "team", "headers": map[string]any{"X-A": "b"},
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
		"broken.json":      []byte("{"),
		"codex-empty.json": map[string]any{"type": "codex"},
		"notes.txt":        []byte("not a credential"),
	})
	files, skipped, err := ReadAuthDir(dir, ImportOptions{CodexIdentity: false, ImageGenerationTool: true}, now)
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
	if want := []string{"prefix", "headers"}; !reflect.DeepEqual(c.Ignored, want) {
		t.Errorf("codex ignored = %v, want %v", c.Ignored, want)
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

	ag := got["antigravity-user-example.com"]
	if ag.Record.Extension != Antigravity || ag.Record.Label != "user@example.com" {
		t.Errorf("antigravity record: %+v", ag.Record)
	}
	agc := ag.Record.Credential
	if agc.Kind != protocol.CredentialOAuth || agc.AccessToken != "fake-ag-access" || agc.RefreshToken != "fake-ag-refresh" ||
		agc.ExpiresAt != exp.Unix() || agc.RefreshAt != exp.Add(-30*time.Minute).Unix() {
		t.Errorf("antigravity credential: %+v", agc)
	}
	equalJSON(t, agc.Data, `{"project_id": "proj-1"}`)
	equalJSON(t, ag.Record.Account.Settings, `{"base_url": "http://127.0.0.1:2"}`)

	x := got["xai-user-ab12"]
	xc := x.Record.Credential
	if x.Record.Extension != XAI || x.Record.Label != "grok@example.com" || xc.Kind != protocol.CredentialOAuth ||
		xc.AccessToken != "fake-xai-access" || xc.RefreshToken != "fake-xai-refresh" ||
		xc.ExpiresAt != exp.Unix() || xc.RefreshAt != exp.Add(-5*time.Minute).Unix() {
		t.Errorf("xai: %+v", x.Record)
	}
	equalJSON(t, xc.Data, `{"token_endpoint": "https://auth.x.ai/oauth2/token"}`)
	equalJSON(t, x.Record.Account.Settings, `{"base_url": "http://127.0.0.1:3/v1"}`)

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
		"gemini-old.json":  "today's core does not load it either",
		"notype.json":      "today's core does not load it either",
		"broken.json":      "not a JSON object",
		"codex-empty.json": "it holds no token",
	} {
		if reasons[file] != want {
			t.Errorf("%s skipped for %q, want %q", file, reasons[file], want)
		}
	}
	if len(skipped) != 5 {
		t.Errorf("skipped %v", skipped)
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
	pending, _ := Pending(dir, func(id string) bool { _, ok := store.Get(id); return ok || reserved(id) })
	if pending != 2 {
		t.Errorf("pending before = %d, want 2", pending)
	}
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
	pending, _ = Pending(dir, func(id string) bool { _, ok := again.Get(id); return ok || reserved(id) })
	if pending != 0 {
		t.Errorf("pending after = %d, want 0", pending)
	}
	// The auth folder is left as it was.
	if names, _ := AuthFiles(dir); len(names) != 3 {
		t.Errorf("auth folder now holds %v", names)
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
	big := `{"type":"claude","access_token":"` + strings.Repeat("a", maxAuthFile) + `"}`
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
		if err := CheckImportFolders(auth, credDir); (err == nil) != ok {
			t.Errorf("CheckImportFolders(auths, %s) = %v", credDir, err)
		}
	}
}
