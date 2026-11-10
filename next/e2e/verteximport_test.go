package e2e

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	geminiBuild    sync.Once
	geminiManifest string
	geminiErr      error
)

// geminiExtension builds the Gemini extension once and returns its manifest's path.
func geminiExtension(t *testing.T) string {
	t.Helper()
	geminiBuild.Do(func() {
		dir := filepath.Join(binDir, "vertex-import")
		if geminiErr = os.MkdirAll(dir, 0o755); geminiErr != nil {
			return
		}
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, "gemini"), "../extensions/gemini")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if geminiErr = cmd.Run(); geminiErr != nil {
			return
		}
		var b []byte
		if b, geminiErr = os.ReadFile("../extensions/gemini/pluxy-extension.json"); geminiErr != nil {
			return
		}
		geminiManifest = filepath.Join(dir, "pluxy-extension.json")
		geminiErr = os.WriteFile(geminiManifest, b, 0o644)
	})
	if geminiErr != nil {
		t.Fatal(geminiErr)
	}
	return geminiManifest
}

// importKey is a service account key pair made for a test.
type importKey struct {
	priv  *rsa.PrivateKey
	pem   string
	email string
}

func newImportKey(t *testing.T, email string) importKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return importKey{priv: k, email: email, pem: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
}

// json returns the key file Google would give for project, its token_uri the fake's.
func (k importKey) json(project, tokenURI string) []byte {
	m := map[string]string{"type": "service_account", "private_key_id": "e2e", "private_key": k.pem, "client_email": k.email, "token_uri": tokenURI}
	if project != "" {
		m["project_id"] = project
	}
	b, _ := json.Marshal(m)
	return b
}

// fakeTokenEndpoint answers Google's JWT bearer grant for the service accounts it knows. It
// checks the signed assertion against the public key it holds for the account and keeps every
// assertion it was sent.
type fakeTokenEndpoint struct {
	*httptest.Server
	mu         sync.Mutex
	known      map[string]*rsa.PublicKey // by client_email
	assertions []string
	down       bool
}

func newFakeTokenEndpoint(t *testing.T) *fakeTokenEndpoint {
	f := &fakeTokenEndpoint{known: map[string]*rsa.PublicKey{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTokenEndpoint) accept(k importKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.known[k.email] = &k.priv.PublicKey
}

func (f *fakeTokenEndpoint) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// sent returns the assertions the endpoint received.
func (f *fakeTokenEndpoint) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.assertions...)
}

func (f *fakeTokenEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	assertion := r.PostForm.Get("assertion")
	f.mu.Lock()
	f.assertions = append(f.assertions, assertion)
	down := f.down
	f.mu.Unlock()
	if down {
		http.Error(w, "token endpoint down", http.StatusServiceUnavailable)
		return
	}
	refuse := func(desc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, desc)
	}
	parts := strings.Split(assertion, ".")
	if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || len(parts) != 3 {
		refuse("Invalid JWT.")
		return
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct{ Iss, Scope, Aud string }
	if err != nil || json.Unmarshal(claims, &c) != nil {
		refuse("Invalid JWT.")
		return
	}
	f.mu.Lock()
	pub := f.known[c.Iss]
	f.mu.Unlock()
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if pub == nil || err != nil || rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		refuse("Invalid JWT Signature.")
		return
	}
	if c.Aud != f.URL+"/token" || c.Scope != "https://www.googleapis.com/auth/cloud-platform" {
		refuse("Invalid JWT: bad audience or scope.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"access_token":"e2e-access-token","expires_in":3599,"token_type":"Bearer"}`)
}

// startVertexImportCore runs core with the Gemini extension, the management API and the given
// configured accounts, and returns its base URL and credentials folder.
func startVertexImportCore(t *testing.T, accounts ...any) (base, credDir string) {
	t.Helper()
	t.Setenv("PLUXY_TEST_LOGIN_HOSTS", "1")
	credDir = filepath.Join(t.TempDir(), "credentials")
	if accounts == nil {
		accounts = []any{}
	}
	base = runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": credDir,
		"extension":       map[string]any{"manifest": geminiExtension(t), "accounts": accounts},
	})
	return base, credDir
}

// importPart is one part of an import request's multipart body.
type importPart struct{ name, filename, content string }

// importRaw sends POST /vertex/import with the given parts (and path, which may carry a query)
// and returns the status and the decoded body.
func importRaw(t *testing.T, base, path, auth string, parts ...importPart) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.filename != "" {
			w, err = mw.CreateFormFile(p.name, p.filename)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, p.content)
	}
	mw.Close()
	return importBody(t, base, path, auth, mw.FormDataContentType(), &body)
}

func importBody(t *testing.T, base, path, auth, contentType string, body io.Reader) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/v0/management"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("POST %s: status %d, body %q is not JSON", path, resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

// importKeyFile imports a key file with optional extra form fields.
func importKeyFile(t *testing.T, base, path string, key []byte, fields ...importPart) (int, map[string]any) {
	t.Helper()
	return importRaw(t, base, path, managementKey, append([]importPart{{"file", "key.json", string(key)}}, fields...)...)
}

func errorOf(t *testing.T, body map[string]any) (code, message string) {
	t.Helper()
	e, _ := body["error"].(map[string]any)
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	if code == "" {
		t.Fatalf("no error code in %v", body)
	}
	return code, message
}

// storedAccount reads account id's record from the credentials folder.
type storedAccount struct {
	Version uint64
	Label   string
	Account struct {
		Settings struct {
			Vertex struct{ Project, Location string }
		}
	}
	Credential struct {
		Kind string
		Data struct {
			ClientEmail string `json:"client_email"`
			PrivateKey  string `json:"private_key"`
			TokenURI    string `json:"token_uri"`
		}
	}
}

func readStored(t *testing.T, credDir, id string) storedAccount {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(credDir, id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s storedAccount
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func listedIDs(t *testing.T, base string) []string {
	t.Helper()
	var list struct{ Accounts []accountView }
	if code := manage(t, base, "GET", "/accounts", nil, &list); code != http.StatusOK {
		t.Fatalf("GET /accounts: %d", code)
	}
	var ids []string
	for _, a := range list.Accounts {
		ids = append(ids, a.ID)
	}
	return ids
}

// An imported service account key becomes the account vertex-<project> through the Gemini
// extension's service_account login: the fake token endpoint receives the signed assertion, the
// account is listed with its credential kind and label, and its settings name the project and the
// location (us-central1 unless one is sent, as a form field or a query parameter).
func TestVertexImport(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, credDir := startVertexImportCore(t)

	t.Run("default location", func(t *testing.T) {
		k := newImportKey(t, "import@proj-a.iam.gserviceaccount.example")
		tok.accept(k)
		code, body := importKeyFile(t, base, "/vertex/import", k.json("proj-a", tok.URL+"/token"))
		if code != http.StatusOK || body["status"] != "ok" || body["account"] != "vertex-proj-a" || body["project_id"] != "proj-a" ||
			body["email"] != k.email || body["location"] != "us-central1" {
			t.Fatalf("import = %d %v", code, body)
		}
		if n := len(tok.sent()); n != 1 {
			t.Fatalf("the token endpoint got %d assertions, want 1", n)
		}
		var list struct {
			Accounts []struct{ ID, Source, Extension, Kind, State, Label string }
		}
		manage(t, base, "GET", "/accounts", nil, &list)
		if len(list.Accounts) != 1 {
			t.Fatalf("accounts = %+v", list.Accounts)
		}
		a := list.Accounts[0]
		if a.ID != "vertex-proj-a" || a.Source != "login" || a.Extension != "pluxy/gemini" || a.Kind != "service_account" ||
			a.State != "active" || a.Label != "proj-a ("+k.email+")" {
			t.Fatalf("listed account = %+v", a)
		}
		s := readStored(t, credDir, "vertex-proj-a")
		if s.Account.Settings.Vertex.Project != "proj-a" || s.Account.Settings.Vertex.Location != "us-central1" ||
			s.Credential.Kind != "service_account" || s.Credential.Data.ClientEmail != k.email || s.Credential.Data.PrivateKey != k.pem {
			t.Fatalf("stored account = %+v", s)
		}
	})

	t.Run("location", func(t *testing.T) {
		k := newImportKey(t, "import@proj-b.iam.gserviceaccount.example")
		tok.accept(k)
		key := k.json("proj-b", tok.URL+"/token")
		// The form field wins over the query, as in today's core.
		code, body := importKeyFile(t, base, "/vertex/import?location=asia-east1", key, importPart{name: "location", content: " europe-west4 "})
		if code != http.StatusOK || body["location"] != "europe-west4" || body["account"] != "vertex-proj-b" {
			t.Fatalf("import with a form field = %d %v", code, body)
		}
		if got := readStored(t, credDir, "vertex-proj-b").Account.Settings.Vertex.Location; got != "europe-west4" {
			t.Fatalf("stored location = %q", got)
		}

		k2 := newImportKey(t, "import@proj-c.iam.gserviceaccount.example")
		tok.accept(k2)
		code, body = importKeyFile(t, base, "/vertex/import?location=asia-east1", k2.json("proj-c", tok.URL+"/token"))
		if code != http.StatusOK || body["location"] != "asia-east1" {
			t.Fatalf("import with a query parameter = %d %v", code, body)
		}
		if got := readStored(t, credDir, "vertex-proj-c").Account.Settings.Vertex.Location; got != "asia-east1" {
			t.Fatalf("stored location = %q", got)
		}
	})

	t.Run("project ID with separators", func(t *testing.T) {
		k := newImportKey(t, "import@sep.iam.gserviceaccount.example")
		tok.accept(k)
		code, body := importKeyFile(t, base, "/vertex/import", k.json(" example.com:my proj ", tok.URL+"/token"))
		if code != http.StatusOK || body["account"] != "vertex-example.com_my-proj" || body["project_id"] != "example.com:my proj" {
			t.Fatalf("import = %d %v", code, body)
		}
		if got := readStored(t, credDir, "vertex-example.com_my-proj").Account.Settings.Vertex.Project; got != "example.com:my proj" {
			t.Fatalf("stored project = %q", got)
		}
	})
}

// Every way an import fails answers 400 for a request or key that cannot be accepted and 5xx when
// Google's token endpoint cannot say, and adds no account.
func TestVertexImportErrors(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, _ := startVertexImportCore(t)
	k := newImportKey(t, "import@errs.iam.gserviceaccount.example")
	tok.accept(k)
	good := k.json("errs", tok.URL+"/token")

	withField := func(key []byte, field string, value any) []byte {
		var m map[string]any
		if err := json.Unmarshal(key, &m); err != nil {
			t.Fatal(err)
		}
		if value == nil {
			delete(m, field)
		} else {
			m[field] = value
		}
		b, _ := json.Marshal(m)
		return b
	}
	stranger := newImportKey(t, "stranger@errs.iam.gserviceaccount.example") // the endpoint does not know it
	badPEM := withField(good, "private_key", "-----BEGIN PRIVATE KEY-----\nbm90IGEga2V5\n-----END PRIVATE KEY-----\n")

	for _, tc := range []struct {
		name     string
		do       func() (int, map[string]any)
		status   int
		code     string
		contains string
	}{
		{"no file field", func() (int, map[string]any) {
			return importRaw(t, base, "/vertex/import", managementKey, importPart{name: "location", content: "us-east1"})
		}, 400, "file_required", "file required"},
		{"not multipart", func() (int, map[string]any) {
			return importBody(t, base, "/vertex/import", managementKey, "application/json", bytes.NewReader(good))
		}, 400, "file_required", "file required"},
		{"not JSON", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", []byte("not json"))
		}, 400, "invalid_json", "invalid json"},
		{"JSON that is not an object", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", []byte(`["a"]`))
		}, 400, "invalid_json", "invalid json"},
		{"no private_key", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "private_key", nil))
		}, 400, "invalid_service_account", "private_key"},
		{"no private_key and no project_id", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(withField(good, "private_key", nil), "project_id", nil))
		}, 400, "invalid_service_account", "private_key"},
		{"no project_id", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "project_id", nil))
		}, 400, "project_id_missing", "project_id missing"},
		{"blank project_id", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "project_id", "  "))
		}, 400, "project_id_missing", "project_id missing"},
		{"no client_email", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "client_email", nil))
		}, 400, "invalid_service_account", "client_email"},
		{"private_key that is not PEM", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "private_key", "not a key"))
		}, 400, "invalid_service_account", "not PEM"},
		{"private_key that is not a key", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", badPEM)
		}, 400, "invalid_service_account", "RSA"},
		{"key the token endpoint refuses", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", stranger.json("errs", tok.URL+"/token"))
		}, 400, "invalid_service_account", "Invalid JWT Signature"},
		{"token_uri that is not https", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "token_uri", "http://example.com/token"))
		}, 400, "invalid_service_account", "token_uri"},
		{"bad location", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", good, importPart{name: "location", content: "Not A Location"})
		}, 400, "invalid_request", "vertex.location"},
		{"token endpoint unreachable", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "token_uri", closedPort(t)+"/token"))
		}, 502, "unavailable", "unreachable"},
		{"token endpoint down", func() (int, map[string]any) {
			tok.setDown(true)
			defer tok.setDown(false)
			return importKeyFile(t, base, "/vertex/import", good)
		}, 502, "unavailable", ""},
		{"project_id with a slash", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "project_id", "a/b"))
		}, 400, "invalid_request", "vertex.project"},
		{"project ID that is not an account ID", func() (int, map[string]any) {
			return importKeyFile(t, base, "/vertex/import", withField(good, "project_id", "bad*id"))
		}, 400, "invalid_request", "account ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := tc.do()
			c, msg := errorOf(t, body)
			if code != tc.status || c != tc.code || !strings.Contains(msg, tc.contains) {
				t.Fatalf("import = %d %s %q, want %d %s containing %q", code, c, msg, tc.status, tc.code, tc.contains)
			}
			if ids := listedIDs(t, base); len(ids) != 0 {
				t.Fatalf("a failed import left accounts %v", ids)
			}
		})
	}

	t.Run("body over the limit", func(t *testing.T) {
		big := withField(good, "padding", strings.Repeat("x", 2<<20))
		code, body := importKeyFile(t, base, "/vertex/import", big)
		if c, _ := errorOf(t, body); code != http.StatusRequestEntityTooLarge || c != "invalid_request" {
			t.Fatalf("import = %d %v, want 413", code, body)
		}
		if ids := listedIDs(t, base); len(ids) != 0 {
			t.Fatalf("a refused import left accounts %v", ids)
		}
	})
}

// Importing again an account a login added signs in to it again: the same service account's new
// key replaces the old one; another service account under the same project ID is refused and
// leaves the stored key as it was; a location the account does not have is refused.
func TestVertexImportAgain(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, credDir := startVertexImportCore(t)
	const email = "import@again.iam.gserviceaccount.example"
	first := newImportKey(t, email)
	tok.accept(first)
	if code, body := importKeyFile(t, base, "/vertex/import", first.json("again", tok.URL+"/token"), importPart{name: "location", content: "us-east1"}); code != http.StatusOK {
		t.Fatalf("first import = %d %v", code, body)
	}
	before := readStored(t, credDir, "vertex-again")

	// The same service account with a new key: the new key replaces the old one, the location
	// stays (the import names none) and the reply says so.
	second := newImportKey(t, email)
	tok.accept(second)
	code, body := importKeyFile(t, base, "/vertex/import", second.json("again", tok.URL+"/token"))
	if code != http.StatusOK || body["account"] != "vertex-again" || body["location"] != "us-east1" || body["email"] != email {
		t.Fatalf("import of the new key = %d %v", code, body)
	}
	after := readStored(t, credDir, "vertex-again")
	if after.Credential.Data.PrivateKey != second.pem || after.Version <= before.Version || after.Label != before.Label ||
		after.Account.Settings.Vertex.Location != "us-east1" {
		t.Fatalf("stored account after the new key = %+v (before %+v)", after, before)
	}
	// The key that signed in last is the new one.
	if sent := tok.sent(); len(sent) != 2 || sent[1] == sent[0] {
		t.Fatalf("assertions = %d", len(sent))
	}
	if ids := listedIDs(t, base); len(ids) != 1 {
		t.Fatalf("accounts = %v, want the one", ids)
	}

	// Another service account of the same project does not take the account over.
	other := newImportKey(t, "other@again.iam.gserviceaccount.example")
	tok.accept(other)
	code, body = importKeyFile(t, base, "/vertex/import", other.json("again", tok.URL+"/token"))
	c, msg := errorOf(t, body)
	if code != http.StatusBadRequest || c != "invalid_request" || !strings.Contains(msg, "different account") {
		t.Fatalf("import of another service account = %d %s %q, want 400 invalid_request: different account", code, c, msg)
	}
	if now := readStored(t, credDir, "vertex-again"); now.Credential.Data.PrivateKey != second.pem || now.Credential.Data.ClientEmail != email || now.Version != after.Version {
		t.Fatalf("a refused import changed the account: %+v", now)
	}

	// A location other than the account's is refused; the account's own is fine.
	code, body = importKeyFile(t, base, "/vertex/import", second.json("again", tok.URL+"/token"), importPart{name: "location", content: "europe-west1"})
	if c, msg := errorOf(t, body); code != http.StatusConflict || c != "conflict" || !strings.Contains(msg, "us-east1") {
		t.Fatalf("import with another location = %d %s %q, want 409", code, c, msg)
	}
	if code, body = importKeyFile(t, base, "/vertex/import?location=us-east1", second.json("again", tok.URL+"/token")); code != http.StatusOK || body["location"] != "us-east1" {
		t.Fatalf("import with the account's location = %d %v", code, body)
	}
}

// An account of the same ID in core's config is refused without a token fetch or any change.
func TestVertexImportConfigAccountIsRefused(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, _ := startVertexImportCore(t, map[string]any{
		"id":         "vertex-cfg",
		"settings":   map[string]any{"vertex": map[string]any{"project": "cfg"}},
		"credential": map[string]any{"kind": "api_key", "api_key": "express-key-e2e"},
	})
	k := newImportKey(t, "import@cfg.iam.gserviceaccount.example")
	tok.accept(k)
	code, body := importKeyFile(t, base, "/vertex/import", k.json("cfg", tok.URL+"/token"))
	if c, msg := errorOf(t, body); code != http.StatusConflict || c != "conflict" || !strings.Contains(msg, "vertex-cfg") {
		t.Fatalf("import = %d %s %q, want 409 conflict naming the account", code, c, msg)
	}
	if n := len(tok.sent()); n != 0 {
		t.Fatalf("the token endpoint got %d assertions, want none", n)
	}
	var list struct {
		Accounts []struct{ ID, Source, Kind string }
	}
	manage(t, base, "GET", "/accounts", nil, &list)
	if len(list.Accounts) != 1 || list.Accounts[0].Source != "config" || list.Accounts[0].Kind != "api_key" {
		t.Fatalf("accounts = %+v, want the config account unchanged", list.Accounts)
	}
}

// The import needs the management key like every management call.
func TestVertexImportNeedsManagementKey(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, _ := startVertexImportCore(t)
	k := newImportKey(t, "import@auth.iam.gserviceaccount.example")
	tok.accept(k)
	part := importPart{"file", "key.json", string(k.json("auth", tok.URL+"/token"))}
	for name, auth := range map[string]string{"no key": "", "wrong key": "not-the-management-key"} {
		code, body := importRaw(t, base, "/vertex/import", auth, part)
		if c, _ := errorOf(t, body); code != http.StatusUnauthorized || c != "unauthorized" {
			t.Fatalf("%s: import = %d %v, want 401", name, code, body)
		}
	}
	if n := len(tok.sent()); n != 0 {
		t.Fatalf("the token endpoint got %d assertions, want none", n)
	}
	if ids := listedIDs(t, base); len(ids) != 0 {
		t.Fatalf("accounts = %v", ids)
	}
}

// An account a login added under the import's ID that is not the same project's Vertex service
// account is never signed in to with the key: a Gemini API key account, and an account for
// another project whose ID sanitises to the same account ID, are refused before any token fetch.
func TestVertexImportOtherAccountWithTheIDIsRefused(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, credDir := startVertexImportCore(t)
	k := newImportKey(t, "import@demo.iam.gserviceaccount.example")
	tok.accept(k)

	// A Vertex express key account named vertex-demo, added by login.
	var started map[string]any
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "vertex-demo", "extension": "pluxy/gemini", "method": "api_key",
		"input": map[string]string{"api_key": "express-key-e2e"}, "settings": map[string]any{"vertex": map[string]any{}}}, &started); code != http.StatusAccepted {
		t.Fatalf("POST /accounts = %d %v", code, started)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(credDir, "vertex-demo.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the API key login did not add vertex-demo")
		}
	}
	before := readStored(t, credDir, "vertex-demo")
	code, body := importKeyFile(t, base, "/vertex/import", k.json("demo", tok.URL+"/token"))
	if c, msg := errorOf(t, body); code != http.StatusConflict || c != "conflict" || !strings.Contains(msg, "vertex-demo") {
		t.Fatalf("import over an API key account = %d %s %q, want 409 conflict", code, c, msg)
	}
	if now := readStored(t, credDir, "vertex-demo"); now.Credential.Kind != "api_key" || now.Version != before.Version {
		t.Fatalf("a refused import changed the account: %+v", now)
	}

	// Two projects whose IDs give the same account ID: the second does not take the first's account.
	if code, body := importKeyFile(t, base, "/vertex/import", k.json("dom.example:proj", tok.URL+"/token")); code != http.StatusOK || body["account"] != "vertex-dom.example_proj" {
		t.Fatalf("import of dom.example:proj = %d %v", code, body)
	}
	first := readStored(t, credDir, "vertex-dom.example_proj")
	code, body = importKeyFile(t, base, "/vertex/import", k.json("dom.example_proj", tok.URL+"/token"))
	if c, msg := errorOf(t, body); code != http.StatusConflict || c != "conflict" || !strings.Contains(msg, "dom.example_proj") {
		t.Fatalf("import of the colliding project = %d %s %q, want 409 conflict", code, c, msg)
	}
	if now := readStored(t, credDir, "vertex-dom.example_proj"); now.Account.Settings.Vertex.Project != "dom.example:proj" || now.Version != first.Version {
		t.Fatalf("the colliding import changed the account: %+v", now)
	}
	if n := len(tok.sent()); n != 1 {
		t.Fatalf("the token endpoint got %d assertions, want 1 (the first project's import)", n)
	}
}

// A request with several key files is refused rather than importing one of them.
func TestVertexImportSeveralFilesAreRefused(t *testing.T) {
	tok := newFakeTokenEndpoint(t)
	base, _ := startVertexImportCore(t)
	k := newImportKey(t, "import@two.iam.gserviceaccount.example")
	tok.accept(k)
	code, body := importRaw(t, base, "/vertex/import", managementKey,
		importPart{"file", "a.json", "not json"}, importPart{"file", "b.json", string(k.json("two", tok.URL+"/token"))})
	if c, _ := errorOf(t, body); code != http.StatusBadRequest || c != "invalid_request" {
		t.Fatalf("import of two files = %d %v, want 400 invalid_request", code, body)
	}
	if ids := listedIDs(t, base); len(ids) != 0 || len(tok.sent()) != 0 {
		t.Fatalf("accounts = %v, assertions = %d; want none", ids, len(tok.sent()))
	}
}
