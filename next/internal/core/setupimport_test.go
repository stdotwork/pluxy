package core

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

// importCall sends POST /setup/import with body to c and returns the status and the reply.
func importCall(t *testing.T, c *Core, key string, body any) (int, management.ImportReport, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/setup/import", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+key)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	c.Management().ServeHTTP(rec, req)
	var rep management.ImportReport
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	return rec.Code, rep, rec.Body.String()
}

// folderState is every file name and content under dir.
func folderState(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// A dry run reports the import and writes nothing; the import then writes the config's
// settings and accounts into core's config and the auth folder's accounts into its store, and
// reports the same items.
func TestSetupImport(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`
	writeTestFile(t, cfgPath, cfg)
	cpa := t.TempDir()
	auths := filepath.Join(cpa, "auths")
	writeTestFile(t, filepath.Join(auths, "claude-a.json"), `{"type": "claude", "access_token": "fake-claude"}`)
	writeTestFile(t, filepath.Join(auths, "antigravity-a.json"), `{"type": "antigravity", "access_token": "fake-ag"}`)
	// The name makes the ID of the config's own claude-api-key account: a collision.
	writeTestFile(t, filepath.Join(auths, "claude-api-key-1.json"), `{"type": "claude", "access_token": "fake-collide"}`)
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), `
port: 8317
api-keys: [fake-cpa-client-key]
remote-management: {secret-key: "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234"}
auth-dir: auths
debug: on
claude-api-key:
  - {api-key: fake-claude-key}
`)
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	creds := c.Config().CredentialsDir
	before, credsBefore := folderState(t, dir), folderState(t, creds)

	req := management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: true}
	code, rep, body := importCall(t, c, "fake-mgmt-key", req)
	if code != http.StatusOK || !rep.DryRun {
		t.Fatalf("dry run: %d %s", code, body)
	}
	if after := folderState(t, dir); !mapsEqual(before, after) {
		t.Errorf("the dry run changed core's folder")
	}
	if after := folderState(t, creds); !mapsEqual(credsBefore, after) {
		t.Errorf("the dry run changed the credential store")
	}
	names := func(rows []management.ImportRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, strings.TrimSpace(r.What+" "+r.Name))
		}
		return out
	}
	// wantRows is the report of the dry run (dry) or of the import whose new management key is
	// key: every field of every row.
	wantRows := func(dry bool, key string) map[string][]management.ImportRow {
		keepWith := `"listen": "0.0.0.0:8317" in pluxy's config file, then restart core`
		listen := func(what, from string) management.ImportRow {
			return management.ImportRow{What: what, From: from, To: "127.0.0.1:0",
				Why: "core keeps listening on 127.0.0.1:0: listen cannot change while core runs", KeepWith: keepWith}
		}
		to := "(a new key, shown once when the import runs)"
		if !dry {
			to = key
		}
		return map[string][]management.ImportRow{
			"brought": {
				{What: "api-keys", As: "1 client key"},
				{What: "claude-api-key[0]", As: "pluxy/claude account claude-api-key-1"},
				{What: "auth-dir", Why: "the folder is not kept: core keeps no auth folder, so a later import needs --auth-dir again"},
				{What: "auth file", Name: "claude-a.json", As: "pluxy/claude account claude-a"},
			},
			"changed": {
				listen("server.host", "(not set: every interface)"),
				{What: "remote-management.secret-key", From: "(a bcrypt hash of the key)", To: to,
					Why: "today's core keeps only a bcrypt hash of its management key, which pluxy cannot use"},
				listen("port", "0.0.0.0:8317"),
			},
			"not_brought": {
				{What: "debug", Why: "this core writes no debug-level log lines"},
				{What: "auth file", Name: "antigravity-a.json", Why: "pluxy no longer ships the Antigravity provider"},
				{What: "auth file", Name: "claude-api-key-1.json", Why: `the config already has an account "claude-api-key-1"`},
			},
		}
	}
	check := func(rep management.ImportReport, key string) {
		t.Helper()
		want := wantRows(rep.DryRun, key)
		for list, got := range map[string][]management.ImportRow{"brought": rep.Brought, "changed": rep.Changed, "not_brought": rep.NotBrought} {
			if !reflect.DeepEqual(got, want[list]) {
				t.Errorf("%s:\ngot  %+v\nwant %+v", list, got, want[list])
			}
		}
		if len(rep.Needs) != 1 || rep.Needs[0].Pick != "pluxy/claude" || rep.Needs[0].Why != "2 accounts need it" {
			t.Errorf("needs %+v", rep.Needs)
		}
	}
	check(rep, "")
	if strings.Contains(body, "$2a$") || strings.Contains(body, "fake-claude") || strings.Contains(body, "fake-cpa-client-key") {
		t.Errorf("the report holds a secret: %s", body)
	}

	code, rep, body = importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: req.Config})
	if code != http.StatusOK || rep.DryRun {
		t.Fatalf("import: %d %s", code, body)
	}
	i := slices.IndexFunc(rep.Changed, func(r management.ImportRow) bool { return r.What == "remote-management.secret-key" })
	if i < 0 {
		t.Fatalf("the import has no management key row: %s", body)
	}
	key := rep.Changed[i].To
	if len(key) < 20 {
		t.Errorf("the new management key %q", key)
	}
	check(rep, key)
	got := c.Config()
	if got.ManagementKey != key {
		t.Errorf("the new management key %q is not core's (%q)", key, got.ManagementKey)
	}
	if !slices.Contains(got.ClientKeys, "fake-cpa-client-key") || !slices.Contains(got.ClientKeys, "fake-client-key-1") {
		t.Errorf("client keys %v", got.ClientKeys)
	}
	if got.Listen != "127.0.0.1:0" {
		t.Errorf("listen changed to %s", got.Listen)
	}
	ids := map[string]bool{}
	for _, a := range got.Accounts() {
		ids[a.ID] = true
	}
	for _, r := range c.creds.Records() {
		ids[r.ID] = true
	}
	if !ids["claude-api-key-1"] || !ids["claude-a"] || ids["antigravity-a"] {
		t.Errorf("accounts %v", ids)
	}

	// Importing again brings no account twice: each is named as one core already has.
	req.DryRun = false
	code, rep, body = importCall(t, c, got.ManagementKey, req)
	if code != http.StatusOK {
		t.Fatalf("second import: %d %s", code, body)
	}
	for _, it := range []string{"claude-api-key[0]", "auth file claude-a.json"} {
		if !slices.Contains(names(rep.NotBrought), it) {
			t.Errorf("second import: %s is not under not_brought: %s", it, body)
		}
	}
}

// In a real or dry import into a running core, every listen row says core keeps listening on its
// own address, with the file's reach in from and core's in to: the row of a file with no
// server.host too, which today's core listened on every interface without.
func TestSetupImportKeepsCoreListen(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"0.0.0.0:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "port: 8317\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	want := c.Config().Listen
	for _, dry := range []bool{true, false} {
		code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: dry})
		if code != http.StatusOK {
			t.Fatalf("dry %v: %d %s", dry, code, body)
		}
		rows := map[string]management.ImportRow{}
		for _, r := range rep.Changed {
			rows[r.What] = r
		}
		for _, what := range []string{"server.host", "port"} {
			r, ok := rows[what]
			if !ok {
				t.Fatalf("dry %v: %s is not under changed: %s", dry, what, body)
			}
			// The file's reach: every interface (no host) on 8317.
			if !strings.Contains(r.From, "every interface") && r.From != "0.0.0.0:8317" {
				t.Errorf("dry %v: %s from %q", dry, what, r.From)
			}
			if r.To != want {
				t.Errorf("dry %v: %s to %q, want core's %q: %s", dry, what, r.To, want, body)
			}
			if !strings.Contains(r.Why, "core keeps listening on "+want) {
				t.Errorf("dry %v: %s why %q", dry, what, r.Why)
			}
			if !strings.Contains(r.KeepWith, `"listen": "0.0.0.0:8317"`) {
				t.Errorf("dry %v: %s keep_with %q", dry, what, r.KeepWith)
			}
		}
	}
}

// An import changes only what the file sets: a setting the file leaves out keeps core's value,
// and within a section only the fields the file sets are written.
func TestSetupImportChangesOnlyWhatTheFileSets(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{
		"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-mgmt-key",
		"routing": {"session_affinity": {"enabled": true, "ttl_ms": 900000}, "request_retry": 2},
		"usage": {"keep_days": 7, "max_events": 1234},
		"logs": {"error_logs_max_files": 3}}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "logging-to-file: true\nusage-statistics-enabled: true\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	read := func() map[string]any {
		t.Helper()
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := read()
	code, _, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")})
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	after := read()

	// Only usage.enabled and logs.to_file changed: every other setting of core's file is as it
	// was.
	usage, _ := after["usage"].(map[string]any)
	logs, _ := after["logs"].(map[string]any)
	if usage["enabled"] != true || logs["to_file"] != true {
		t.Errorf("the file's settings are not brought: usage %v, logs %v", usage, logs)
	}
	for _, m := range []map[string]any{before, after} {
		if u, ok := m["usage"].(map[string]any); ok {
			delete(u, "enabled")
		}
		if l, ok := m["logs"].(map[string]any); ok {
			delete(l, "to_file")
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the import changed more than the file set:\nbefore %v\nafter  %v", before, after)
	}
	if usage["keep_days"] != float64(7) || usage["max_events"] != float64(1234) || logs["error_logs_max_files"] != float64(3) {
		t.Errorf("fields the file does not set were lost: usage %v, logs %v", usage, logs)
	}
	routing, _ := after["routing"].(map[string]any)
	if affinity, _ := routing["session_affinity"].(map[string]any); affinity["enabled"] != true || affinity["ttl_ms"] != float64(900000) {
		t.Errorf("routing.session_affinity %v", affinity)
	}
	if routing["request_retry"] != float64(2) {
		t.Errorf("routing.request_retry %v", routing["request_retry"])
	}
}

// A file that turns session affinity on without a TTL sets no TTL: core's own one stays, and the
// import's row for the setting is brought.
func TestSetupImportKeepsCoreAffinityTTL(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{
		"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-mgmt-key",
		"routing": {"session_affinity": {"enabled": true, "ttl_ms": 900000}}}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "routing: {session-affinity: true}\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")})
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Routing struct {
			SessionAffinity struct {
				Enabled bool   `json:"enabled"`
				TTLMS   *int64 `json:"ttl_ms"`
			} `json:"session_affinity"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	aff := got.Routing.SessionAffinity
	if !aff.Enabled {
		t.Errorf("the file turned session affinity on but core's is off: %s", b)
	}
	if aff.TTLMS == nil || *aff.TTLMS != 900000 {
		t.Errorf("the import set a TTL the file does not set: %s", b)
	}
	if i := slices.IndexFunc(rep.Brought, func(r management.ImportRow) bool { return r.What == "routing.session-affinity" }); i < 0 {
		t.Errorf("the setting is not under brought: %s", body)
	}
}

// Two auth files whose names make one account ID: the dry run refuses the second as the import
// does, so both reports name it as not brought.
func TestSetupImportDryRunOneAccountID(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "auths", "a b.json"), `{"type": "claude", "access_token": "fake-1"}`)
	writeTestFile(t, filepath.Join(cpa, "auths", "a-b.json"), `{"type": "claude", "access_token": "fake-2"}`)
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "auth-dir: auths\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	req := management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")}
	req.DryRun = true
	code, dry, body := importCall(t, c, "fake-mgmt-key", req)
	if code != http.StatusOK {
		t.Fatalf("dry run: %d %s", code, body)
	}
	req.DryRun = false
	code, real, body := importCall(t, c, "fake-mgmt-key", req)
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	if !reflect.DeepEqual(dry.Brought, real.Brought) || !reflect.DeepEqual(dry.Changed, real.Changed) || !reflect.DeepEqual(dry.NotBrought, real.NotBrought) {
		t.Errorf("the dry run's lists differ from the import's:\ndry  %+v\nreal %+v", dry, real)
	}
	if len(real.NotBrought) != 1 || real.NotBrought[0].Name != "a-b.json" || !strings.Contains(real.NotBrought[0].Why, "already has an account") {
		t.Errorf("not brought %+v", real.NotBrought)
	}
	if n := len(c.creds.Records()); n != 1 {
		t.Errorf("%d accounts in the store, want 1", n)
	}
}

// A change the write path refuses, such as TLS while core runs, is refused by the dry run too:
// the dry run runs the same checks.
func TestSetupImportDryRunRefusesWhatTheImportRefuses(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "tls: {enable: true, cert: c.pem, key: k.pem}\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, dry := range []bool{true, false} {
		code, _, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: dry})
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, "tls cannot change while core runs") {
			t.Errorf("dry %v: %d %s", dry, code, body)
		}
		bodies = append(bodies, body)
	}
	if bodies[0] != bodies[1] {
		t.Errorf("the dry run and the import refuse differently:\ndry  %s\nreal %s", bodies[0], bodies[1])
	}
	if after, err := os.ReadFile(cfgPath); err != nil || !bytes.Equal(before, after) {
		t.Errorf("the refused import changed core's config: %v", err)
	}
}

// The file names its auth folder twice (the older auth-dir and the v8 oauth.auth-dir): the row
// of the key the translator read is the one that says its folder is not there, and the older one
// keeps its own reason.
func TestSetupImportAuthDirRow(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	older, v8 := filepath.Join(cpa, "older"), filepath.Join(cpa, "v8")
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "auth-dir: "+older+"\noauth: {auth-dir: "+v8+"}\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: true})
	if code != http.StatusOK {
		t.Fatalf("dry run: %d %s", code, body)
	}
	rows := map[string]management.ImportRow{}
	for _, r := range append(append(append([]management.ImportRow{}, rep.Brought...), rep.Changed...), rep.NotBrought...) {
		rows[r.What] = r
	}
	if r := rows["oauth.auth-dir"]; !strings.Contains(r.Why, "the folder "+v8+" is not there") {
		t.Errorf("oauth.auth-dir row %+v: the folder it names is not reported", r)
	}
	if r := rows["auth-dir"]; !strings.Contains(r.Why, "the file sets oauth.auth-dir too") {
		t.Errorf("auth-dir row %+v: it lost its own reason", r)
	}
	if !slices.ContainsFunc(rep.NotBrought, func(r management.ImportRow) bool { return r.What == "oauth.auth-dir" }) {
		t.Errorf("oauth.auth-dir is not under not_brought: %s", body)
	}
}

// A pool whose models cannot become valid chains refuses the import, naming the pool, and writes
// nothing: the models are not written cut, with a row naming a chain that is not there.
func TestSetupImportRefusedPool(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), `openai-compatibility:
  - name: p
    base-url: https://compat.example.com/v1
    api-key-entries: [{api-key: fake-1}]
    models:
      - {name: y, alias: x}
      - {name: z, alias: x}
      - {name: w, alias: y}
      - {name: v, alias: y}
`)
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	code, _, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "api-keys.openai-compatibility[0]") || !strings.Contains(body, "fallback chains") {
		t.Errorf("import: %d %s", code, body)
	}
	if after, err := os.ReadFile(cfgPath); err != nil || !bytes.Equal(before, after) {
		t.Errorf("the refused import wrote core's config: %v", err)
	}
}

// The file's auth folder is read once: its row says core keeps no folder, so a later import needs
// --auth-dir again.
func TestSetupImportAuthDirNotKept(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "auths", "claude-a.json"), `{"type": "claude", "access_token": "fake"}`)
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), "auth-dir: auths\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml"), DryRun: true})
	if code != http.StatusOK {
		t.Fatalf("dry run: %d %s", code, body)
	}
	row := slices.IndexFunc(rep.Brought, func(r management.ImportRow) bool { return r.What == "auth-dir" })
	if row < 0 || !strings.Contains(rep.Brought[row].Why, "not kept") || !strings.Contains(rep.Brought[row].Why, "--auth-dir") {
		t.Errorf("the auth folder's row does not say it is not kept: %s", body)
	}
}

// An account of the file's own extensions block that core already has is named as kept, like an
// account of a provider section.
func TestSetupImportKeepsExtensionAccounts(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key",
		"extensions":{"pluxy/claude":{"accounts":[{"id":"core-a","credential":{"kind":"api_key","api_key":"fake-0"}}]}}}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), `extensions:
  pluxy/claude:
    accounts:
      - {id: core-a, credential: {kind: api_key, api_key: fake-1}}
`)
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")})
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	i := slices.IndexFunc(rep.NotBrought, func(r management.ImportRow) bool { return r.What == "extensions" })
	if i < 0 || !strings.Contains(rep.NotBrought[i].Why, "core already has an account core-a") {
		t.Errorf("the extensions block's row does not name the account core keeps: %s", body)
	}
	var have int
	for _, a := range c.Config().Accounts() {
		if a.ID == "core-a" {
			have++
		}
	}
	if have != 1 {
		t.Errorf("core has %d accounts named core-a, want 1", have)
	}
}

// The extensions row tallies the file's own block only: a new account of a provider section does
// not make the row changed while the block's own account is one core keeps.
func TestSetupImportExtensionRowCountsOwnBlock(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key",
		"extensions":{"pluxy/claude":{"accounts":[{"id":"core-a","credential":{"kind":"api_key","api_key":"fake-0"}}]}}}`)
	cpa := t.TempDir()
	writeTestFile(t, filepath.Join(cpa, "config.yaml"), `extensions:
  pluxy/claude:
    accounts:
      - {id: core-a, credential: {kind: api_key, api_key: fake-1}}
claude-api-key:
  - {api-key: fake-claude-key}
`)
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	code, rep, body := importCall(t, c, "fake-mgmt-key", management.SetupImportRequest{Config: filepath.Join(cpa, "config.yaml")})
	if code != http.StatusOK {
		t.Fatalf("import: %d %s", code, body)
	}
	i := slices.IndexFunc(rep.NotBrought, func(r management.ImportRow) bool { return r.What == "extensions" })
	if i < 0 || !strings.Contains(rep.NotBrought[i].Why, "core already has an account core-a") {
		t.Errorf("the extensions row is not not_brought with the kept account: %s", body)
	}
	if j := slices.IndexFunc(rep.Brought, func(r management.ImportRow) bool { return r.What == "claude-api-key[0]" }); j < 0 {
		t.Errorf("the provider section's new account is not under brought: %s", body)
	}
	ids := map[string]bool{}
	for _, a := range c.Config().Accounts() {
		ids[a.ID] = true
	}
	if !ids["core-a"] || !ids["claude-api-key-1"] {
		t.Errorf("core has %v, want the kept and the new account", ids)
	}
}

// A path that is not absolute, a missing file and a file that is not a config are refused, and
// the refusal holds none of the file's secrets.
func TestSetupImportRefusals(t *testing.T) {
	setTestHome(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`)
	bad := filepath.Join(dir, "bad.yaml")
	writeTestFile(t, bad, "routing: {cooldown: {quota-response-mode: fake-secret-mode}}\nremote-management: {secret-key: fake-secret-mode}\n")
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	for _, cs := range []struct {
		req  management.SetupImportRequest
		code int
	}{
		{management.SetupImportRequest{Config: "config.yaml"}, http.StatusBadRequest},
		{management.SetupImportRequest{Config: filepath.Join(dir, "missing.yaml")}, http.StatusBadRequest},
		{management.SetupImportRequest{Config: bad, DryRun: true}, http.StatusUnprocessableEntity},
	} {
		code, _, body := importCall(t, c, "fake-mgmt-key", cs.req)
		if code != cs.code || strings.Contains(body, "fake-secret-mode") {
			t.Errorf("%+v: %d %s, want %d without the secret", cs.req, code, body, cs.code)
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
