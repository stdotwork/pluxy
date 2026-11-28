package legacy

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/cred"
)

// sample are values of the supported settings outside the provider sections, of the type each
// is read as. Every other rule's setting gets "x".
var sample = map[string]any{
	"config-version":          8,
	"server.host":             "10.9.8.7",
	"server.port":             8318,
	"access.api-keys":         []any{"fake-client-key"},
	"management.secret-key":   "fake-management-key",
	"management.allow-remote": true,
	"server.trusted-proxies":  []any{"10.0.0.1"},
	"server.tls":              map[string]any{"enable": true, "cert": "c.pem", "key": "k.pem"},

	"routing.strategy":                                  "fill-first",
	"routing.session-affinity":                          true,
	"routing.session-affinity-ttl":                      "2h",
	"routing.force-model-prefix":                        true,
	"routing.retry.max-retry-credentials":               2,
	"routing.retry.max-retry-interval":                  3,
	"routing.retry.request-retry":                       1,
	"routing.cooldown.disable-cooling":                  true,
	"routing.cooldown.transient-error-cooldown-seconds": 5,
	"routing.cooldown.quota-response-mode":              "usage-limit",

	"requests.proxy-url": "http://proxy.example.com:8080",
	"requests.payload":   map[string]any{"default": []any{map[string]any{"models": []any{map[string]any{"name": "m"}}, "params": map[string]any{"a": 1}}}},

	"upstream.codex.disable-codex-cloaking": true,
	"upstream.codex.websocket-compression":  true,
	"upstream.codex.model-level-cooling":    true,
	"upstream.claude.model-level-cooling":   true,
	"upstream.xai.inject-x-search":          true,

	"oauth.auth-dir":        "/tmp/auths",
	"oauth.model-alias":     map[string]any{"codex": []any{map[string]any{"name": "gpt-5", "alias": "fast"}}},
	"oauth.excluded-models": map[string]any{"codex": []any{"gpt-4*"}},
	"oauth.providers.codex.signin-with-chatgpt-identifier": "me",

	"multimedia.disable-image-generation":          true,
	"observability.logs.logging-to-file":           true,
	"observability.logs.logs-max-total-size-mb":    10,
	"observability.logs.request-log":               true,
	"observability.logs.error-logs-max-files":      3,
	"observability.usage.usage-statistics-enabled": true,
	"observability.pprof.enable":                   true,
	"observability.pprof.addr":                     "127.0.0.1:9999",
}

// groupSample are values of a provider group's supported fields ("api-keys.*[].<field>" and the
// section's own).
var groupSample = map[string]any{
	"name": "g", "base-url": "https://upstream.example.com/v1", "priority": 1, "api-key": "fake-upstream-key",
	"weight": 2, "prefix": "p", "excluded-models": []any{"old-*"}, "headers": map[string]any{"X-Team": "a"},
	"proxy-url": "http://proxy.example.com:8080", "disable-cooling": true, "request-retry": 1,
	"websockets": true, "disable-codex-cloaking": true, "alpha-search": true, "interactions": true,
	"disabled": false, "rebuild-mid-system-message": true,
}

// ruleDoc is a config.yaml that sets every setting of the rule table, each to a value that asks
// for something, and gives every provider section a group with each of its fields. skip, when
// not empty, is a rule left out: the setting is not in the file.
func ruleDoc(t *testing.T, skip string) tree {
	t.Helper()
	doc := tree{}
	groups := map[string]tree{}
	for _, f := range families {
		groups[f.v8] = tree{}
	}
	for _, path := range slices.Sorted(maps.Keys(rules)) {
		if path == skip {
			continue
		}
		r := rules[path]
		if rest, ok := strings.CutPrefix(path, "api-keys."); ok {
			section, field, _ := strings.Cut(rest, "[].")
			if field == "" { // a whole section: api-keys.meta
				continue
			}
			v, typed := groupSample[strings.TrimSuffix(field, "[].name")]
			if !typed {
				v = "x"
			}
			targets := []string{section}
			if section == "*" {
				targets = slices.Collect(maps.Keys(groups))
			}
			for _, s := range targets {
				g := groups[s]
				if m, isModel := strings.CutPrefix(field, "models[]."); isModel {
					models, _ := g["models"].([]any)
					if len(models) == 0 {
						models = []any{tree{}}
						g["models"] = models
					}
					models[0].(tree)[m] = "x"
					if m == "name" || m == "alias" {
						models[0].(tree)[m] = "model-" + m
					}
					continue
				}
				g[field] = v
			}
			continue
		}
		v, ok := sample[path]
		switch {
		case ok:
		case r.status == supported && !rulesBelow(path):
			t.Fatalf("no sample value for the supported setting %s: add one", path)
		case rulesBelow(path):
			v = tree{"zz-not-a-key": "x"} // its own rules set the rest
		default:
			v = "x"
		}
		if _, has := get(doc, path); has {
			if m, isMap := v.(tree); isMap {
				cur, _ := get(doc, path)
				set(doc, path, merge(cur, m))
			}
			continue
		}
		set(doc, path, v)
	}
	keys := tree{}
	for s, g := range groups {
		if s == "meta" {
			keys[s] = []any{tree{"api-key": "fake-meta-key"}}
			continue
		}
		// A key's api-key and weight are the group's, unless the rule that sets them was left out.
		key := tree{}
		for _, field := range []string{"api-key", "weight"} {
			if v, ok := g[field]; ok {
				key[field] = v
				delete(g, field)
			}
		}
		g["keys"] = []any{key}
		keys[s] = []any{g}
	}
	doc["api-keys"] = keys
	return doc
}

// noConfigEffect are the supported settings whose sample value asks nothing of core's config: the
// value is carried elsewhere, or is the default core already has. Each says why, so a setting
// added here without a reason is caught by TestImportReportCoversEveryRule.
var noConfigEffect = map[string]string{
	"oauth.auth-dir": "it names the folder of accounts to read: the folder's accounts are imported, and core keeps no folder",
	"oauth.providers.codex.signin-with-chatgpt-identifier": "it applies to accounts added by a sign-in: the import's account options carry it, not core's config",
	"upstream.codex.disable-codex-cloaking":                "it applies to accounts added by a sign-in: the import's account options carry it, not core's config",
	"upstream.codex.model-level-cooling":                   "pluxy keeps a used-up model's block on that model always, so true asks for nothing here",
	"upstream.codex.websocket-compression":                 "compression is always on, so true asks for nothing",
	"api-keys.openai-compatibility[].disabled":             "false is the default: the group's keys are imported either way",
}

// importDoc imports the config.yaml that doc makes, and returns the import's result.
func importDoc(t *testing.T, dir string, doc tree) *Result {
	t.Helper()
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ImportConfig(b, dir)
	if err != nil {
		t.Fatalf("ImportConfig: %v\n%s", err, b)
	}
	return res
}

// Every setting of the rule table, set to something that asks for it, lands in exactly one of
// the report's lists: ImportConfig fails otherwise, naming the setting. Each brought or changed
// setting's value reaches core's config, so removing its writer fails the test: leaving the
// setting out of the file changes what the import writes. A setting whose value goes elsewhere is
// in noConfigEffect, with why.
func TestImportReportCoversEveryRule(t *testing.T) {
	dir := t.TempDir()
	base := importDoc(t, dir, ruleDoc(t, ""))
	lists, err := base.Ledger.Report()
	if err != nil {
		t.Fatal(err)
	}
	var whats []string
	for _, rows := range [][]Row{lists.Brought, lists.Changed, lists.NotBrought} {
		for _, r := range rows {
			whats = append(whats, r.What)
		}
	}
	// Each rule outside the provider sections is named by a row of its own or of a key below it.
	for path := range rules {
		if strings.HasPrefix(path, "api-keys.") {
			continue
		}
		if !slices.ContainsFunc(whats, func(w string) bool { return w == path || strings.HasPrefix(w, path+".") }) {
			t.Errorf("no row names %s", path)
		}
	}
	// Each provider section, a whole section too, has a row for its entry; the tests below check
	// what each of its fields did.
	for _, f := range families {
		if !slices.Contains(whats, "api-keys."+f.v8+"[0]") {
			t.Errorf("no row names api-keys.%s[0]", f.v8)
		}
	}
	// A provider field that is not brought is named in its group's row.
	var whys string
	for _, rows := range [][]Row{lists.Brought, lists.Changed, lists.NotBrought} {
		for _, r := range rows {
			whys += r.Why + "\n"
		}
	}
	for path, r := range rules {
		_, field, ok := strings.Cut(path, "[].")
		if !ok || r.status == supported {
			continue
		}
		if !strings.Contains(whys, strings.ReplaceAll(strings.TrimPrefix(field, "models[]."), "[]", "")) {
			t.Errorf("no row says %s is not brought", path)
		}
	}
	// A whole-section rule that is not brought says why in its entries' rows.
	for path, r := range rules {
		if r.status == supported || r.note == "" || !strings.HasPrefix(path, "api-keys.") || strings.Contains(path, "[].") {
			continue
		}
		section := strings.TrimPrefix(path, "api-keys.")
		if !slices.ContainsFunc(whats, func(w string) bool { return strings.HasPrefix(w, "api-keys."+section+"[") }) {
			t.Errorf("no row names an entry of %s", path)
		}
	}
	for _, path := range slices.Sorted(maps.Keys(rules)) {
		if rules[path].status != supported {
			continue
		}
		// A provider rule's value is part of its group's row: every entry of ruleDoc's sections
		// becomes an account, whose brought or changed row names the group.
		if !strings.HasPrefix(path, "api-keys.") {
			named := func(rows []Row) bool {
				return slices.ContainsFunc(rows, func(r Row) bool { return r.What == path || strings.HasPrefix(r.What, path+".") })
			}
			if !named(lists.Brought) && !named(lists.Changed) {
				continue
			}
		}
		if why, ok := noConfigEffect[path]; ok {
			if why == "" {
				t.Errorf("%s is exempt from reaching the config without a reason", path)
			}
			continue
		}
		if got := importDoc(t, dir, ruleDoc(t, path)); reflect.DeepEqual(got.Config, base.Config) {
			t.Errorf("leaving %s out of the file does not change the config the import writes: nothing brings the setting", path)
		}
	}
}

// Each older spelling of a setting is accounted for under its own name in the file.
func TestImportReportCoversOlderSpellings(t *testing.T) {
	for _, m := range moves {
		v, ok := sample[m[1]]
		if !ok {
			v = "x"
			if rulesBelow(m[1]) || slices.ContainsFunc(readPaths, func(p string) bool { return strings.HasPrefix(p, m[1]+".") }) {
				v = tree{"zz-not-a-key": "x"}
			}
		}
		doc := tree{}
		set(doc, m[0], v)
		b, _ := yaml.Marshal(doc)
		res, err := ImportConfig(b, t.TempDir())
		if err != nil {
			t.Errorf("%s: %v", m[0], err)
			continue
		}
		lists, _ := res.Ledger.Report()
		found := false
		for _, rows := range [][]Row{lists.Brought, lists.Changed, lists.NotBrought} {
			for _, r := range rows {
				found = found || r.What == m[0] || strings.HasPrefix(r.What, m[0]+".")
			}
		}
		if !found {
			t.Errorf("%s: no row names it by its spelling in the file: %+v", m[0], lists)
		}
	}
}

// The older and the v8 spelling of one setting: the v8 one is brought, the older one named as
// not brought.
func TestImportBothSpellings(t *testing.T) {
	res := importYAML(t, "port: 8000\nserver: {port: 8001}\n")
	if row, list := rowOf(t, res, "port"); list != NotBrought || !strings.Contains(row.Why, "server.port") {
		t.Errorf("port: %d %+v", list, row)
	}
	if _, list := rowOf(t, res, "server.port"); list != Brought {
		t.Errorf("server.port: list %d", list)
	}
	if res.Config["listen"] != "127.0.0.1:8001" {
		t.Errorf("listen %v", res.Config["listen"])
	}
}

// An item with no row, or with two, fails the report, naming the item.
func TestLedgerCheck(t *testing.T) {
	l := &Ledger{}
	l.Expect(ConfigItem("a"))
	l.Expect(ConfigItem("b"))
	l.Add(Brought, ConfigItem("a"), Row{What: "a"})
	if err := l.Check(); err == nil || !strings.Contains(err.Error(), "config.yaml: b") {
		t.Errorf("an item in no list: %v", err)
	}
	l.Add(NotBrought, ConfigItem("b"), Row{What: "b"})
	if err := l.Check(); err != nil {
		t.Errorf("every item once: %v", err)
	}
	l.Add(Changed, ConfigItem("a"), Row{What: "a"})
	if err := l.Check(); err == nil || !strings.Contains(err.Error(), "config.yaml: a") {
		t.Errorf("an item in two lists: %v", err)
	}
}

// A setting that writes nothing is named as not brought, with why: TLS files with TLS off, an
// empty client-key list, and a provider group with no keys.
func TestImportRowsThatWriteNothing(t *testing.T) {
	res := importYAML(t, "server: {tls: {cert: c.pem, key: k.pem}}\n")
	for _, k := range []string{"server.tls.cert", "server.tls.key"} {
		if row, list := rowOf(t, res, k); list != NotBrought || !strings.Contains(row.Why, "server.tls.enable") {
			t.Errorf("%s: %d %+v", k, list, row)
		}
	}
	if res.Config["tls"] != nil {
		t.Errorf("tls %v", res.Config["tls"])
	}
	res = importYAML(t, "api-keys: []\n")
	if row, list := rowOf(t, res, "api-keys"); list != NotBrought || !strings.Contains(row.Why, "no client keys") {
		t.Errorf("api-keys: []: %d %+v", list, row)
	}
	if _, ok := res.Config["client_keys"]; ok {
		t.Errorf("api-keys: [] wrote %v", res.Config["client_keys"])
	}
	res = importYAML(t, "api-keys: {claude: [{name: a}]}\n")
	if row, list := rowOf(t, res, "api-keys.claude[0]"); list != NotBrought || !strings.Contains(row.Why, "no keys") {
		t.Errorf("a group with no keys: %d %+v", list, row)
	}
}

func importYAML(t *testing.T, doc string) *Result {
	t.Helper()
	res, err := ImportConfig([]byte(doc), t.TempDir())
	if err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}
	return res
}

// rowOf returns the row of the key at path in the file, and its list.
func rowOf(t *testing.T, res *Result, path string) (Row, int) {
	t.Helper()
	for _, e := range res.Ledger.entries {
		if e.Item == ConfigItem(path) || e.Item == "" && e.Row.What == path {
			return e.Row, e.List
		}
	}
	t.Fatalf("no row for %s", path)
	return Row{}, -1
}

// T6: a config with no host, on which today's core listened on every interface, imports as
// 127.0.0.1, with a changed row that says how to keep the old reach.
func TestImportNoHostIsLoopback(t *testing.T) {
	for _, doc := range []string{"port: 8317\n", "server: {host: \"\", port: 8317}\n"} {
		res := importYAML(t, doc)
		if res.Config["listen"] != "127.0.0.1:8317" {
			t.Errorf("%q: listen %v, want 127.0.0.1:8317", doc, res.Config["listen"])
		}
		row, list := rowOf(t, res, "server.host")
		if list != Changed || row.To != "127.0.0.1" || !strings.Contains(row.From, "every interface") ||
			!strings.Contains(row.KeepWith, `"listen": "0.0.0.0:8317"`) {
			t.Errorf("%q: server.host row %d %+v", doc, list, row)
		}
	}
	// A host is kept, and Translate (a config.yaml core runs on) keeps today's reach.
	if res := importYAML(t, "host: 0.0.0.0\nport: 9000\n"); res.Config["listen"] != "0.0.0.0:9000" {
		t.Errorf("listen %v", res.Config["listen"])
	}
	if r, err := Translate([]byte("port: 8317\n")); err != nil || r.Config["listen"] != ":8317" {
		t.Errorf("Translate: %v %v", r.Config["listen"], err)
	}
}

// yes, on, no and off are true and false, as today's core's YAML decoder reads them into a
// true-or-false setting.
func TestYAML11Booleans(t *testing.T) {
	r, err := Translate([]byte(`
management: {secret-key: fake-management-key, allow-remote: yes}
routing: {session-affinity: on, force-model-prefix: "off"}
observability: {logs: {request-log: Yes, logging-to-file: no}}
server: {tls: {enable: off, cert: c.pem}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Config["management_allow_remote"] != true {
		t.Errorf("allow-remote: yes: %v", r.Config["management_allow_remote"])
	}
	routing := r.Config["routing"].(map[string]any)
	if _, on := routing["session_affinity"].(map[string]any)["ttl_ms"]; !on || routing["force_model_prefix"] != nil {
		t.Errorf("routing %v", routing)
	}
	if logs := r.Config["logs"].(tree); logs["request_log"] != true || logs["to_file"] != nil {
		t.Errorf("logs %v", logs)
	}
	if r.Config["tls"] != nil {
		t.Errorf("tls enable: off: %v", r.Config["tls"])
	}
	if _, err := Translate([]byte("management: {allow-remote: maybe}\n")); err == nil {
		t.Error("allow-remote: maybe was read as true or false")
	}
}

// A management key is never brought, plain or bcrypt: the import keeps core's own key, which
// guards every client of core, and says so with the way to change it. A config.yaml core runs
// on keeps today's reading: a plain key exactly as written, a bcrypt hash noted and left out.
func TestImportKeepsManagementKey(t *testing.T) {
	const hashed = "remote-management: {secret-key: \"$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234\"}\n"
	const plain = "management: {secret-key: fake-management-key}\n"
	for _, tc := range []struct{ name, doc, item string }{
		{"plain", plain, "management.secret-key"},
		{"bcrypt", hashed, "remote-management.secret-key"},
	} {
		res := importYAML(t, tc.doc)
		if got, ok := res.Config["management_key"]; ok {
			t.Errorf("%s: the import wrote management_key %v", tc.name, got)
		}
		row, list := rowOf(t, res, tc.item)
		if list != NotBrought || !strings.Contains(row.Why, "pluxy keeps its own management key") ||
			!strings.Contains(row.Why, "POST /v0/management/management-key/rotate") {
			t.Errorf("%s: row %d %+v", tc.name, list, row)
		}
	}
	if r, err := Translate([]byte(plain)); err != nil || r.Config["management_key"] != "fake-management-key" {
		t.Errorf("Translate plain: %v %v", r.Config["management_key"], err)
	}
	if r, err := Translate([]byte(hashed)); err != nil || r.Config["management_key"] != nil || !r.HashedManagementKey {
		t.Errorf("Translate bcrypt: %v %v", r.Config["management_key"], err)
	}
}

func TestImportDisableControlPanelReason(t *testing.T) {
	const want = "the panel is always served, and signing in takes the management key"
	res := importYAML(t, "management: {disable-control-panel: true}\n")
	row, list := rowOf(t, res, "management.disable-control-panel")
	if list != NotBrought || row.Why != want {
		t.Errorf("true: list=%d row=%+v, want NotBrought with Why %q", list, row, want)
	}
	if !slices.ContainsFunc(res.Notices, func(n string) bool {
		return strings.Contains(n, "management.disable-control-panel ("+want+")")
	}) {
		t.Errorf("notices = %v, want disable-control-panel reason %q", res.Notices, want)
	}
	off := importYAML(t, "management: {disable-control-panel: false}\n")
	offRow, offList := rowOf(t, off, "management.disable-control-panel")
	if offList != NotBrought || offRow.Why != want+"; it is off, empty or at CLIProxyAPI's default here, so nothing is lost" {
		t.Errorf("false: list=%d row=%+v, want NotBrought with CLIProxyAPI default suffix", offList, offRow)
	}
}

// A provider group is one row: brought with its accounts, changed with what was left out, or not
// brought.
func TestImportGroupRows(t *testing.T) {
	res := importYAML(t, `
claude-api-key:
  - {api-key: fake-1, cloak: {mode: always}}
  - {api-key: fake-2}
codex-api-key:
  - {api-key: fake-3}
openai-compatibility:
  - {name: off, base-url: "https://x.example.com", disabled: true, api-key-entries: [{api-key: fake-4}]}
`)
	if row, list := rowOf(t, res, "claude-api-key[0]"); list != Changed || !strings.Contains(row.Why, "cloak not brought") || !strings.Contains(row.As, "pluxy/claude account claude-api-key-1") {
		t.Errorf("claude[0]: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "claude-api-key[1]"); list != Brought || row.As != "pluxy/claude account claude-api-key-2" {
		t.Errorf("claude[1]: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "codex-api-key[0]"); list != NotBrought || row.Why != "keys[0] has no base-url: CLIProxyAPI drops a Codex API key without one too" {
		t.Errorf("codex[0]: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "openai-compatibility[0]"); list != NotBrought || row.Why != "disabled: true, so CLIProxyAPI did not use it either" {
		t.Errorf("compat[0]: %d %+v", list, row)
	}
	if res.Accounts["claude-api-key-2"] != ConfigItem("claude-api-key[1]") {
		t.Errorf("accounts %v", res.Accounts)
	}
}

// Every entry of the auth folder is one row: each file type the import knows, a file it cannot
// read, a file that is not JSON and a folder.
func TestAuthFolderRows(t *testing.T) {
	expired := time.Now().Add(240 * time.Hour).UTC().Format(time.RFC3339)
	dir := writeFiles(t, map[string]any{
		"codex-a.json":       map[string]any{"type": "codex", "access_token": "fake", "refresh_token": "fake", "expired": expired},
		"codex-siwc.json":    map[string]any{"type": "codex", "siwc": map[string]any{"access_token": "fake-siwc"}},
		"claude-a.json":      map[string]any{"type": "claude", "access_token": "fake", "headers": map[string]any{"X-S": "$CPA-SESSION-ID"}},
		"kimi-a.json":        map[string]any{"type": "kimi", "access_token": "fake"},
		"kimi-ai.json":       map[string]any{"type": "kimi-ai", "access_token": "fake"},
		"vertex-a.json":      map[string]any{"type": "vertex", "service_account": map[string]any{"client_email": "a@b.c", "private_key": "fake-key"}},
		"xai-a.json":         map[string]any{"type": "xai", "access_token": "fake"},
		"meta-a.json":        map[string]any{"type": "meta", "api_key": "fake"},
		"devin-a.json":       map[string]any{"type": "devin", "session_token": "fake"},
		"antigravity-a.json": map[string]any{"type": "antigravity", "access_token": "fake"},
		"gemini-a.json":      map[string]any{"type": "gemini", "access_token": "fake"},
		"notype.json":        map[string]any{"access_token": "fake"},
		"qwen-a.json":        map[string]any{"type": "qwen", "access_token": "fake"},
		"empty-claude.json":  map[string]any{"type": "claude"},
	})
	for name, b := range map[string]string{"broken.json": "{", "notes.txt": "hi"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "backup.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	a, err := ReadAuthFolder(dir, DefaultImportOptions, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// The report is of what Import does with the folder, not of the files read: Import adds an
	// account per file and says which it took.
	imported, taken, err := Import(fakeStore{}, a.Files, func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != len(a.Files) || len(taken) != 0 {
		t.Fatalf("Import added %d of %d files, %d taken", len(imported), len(a.Files), len(taken))
	}
	led := a.Ledger(imported, taken)
	lists, err := led.Report()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for list, rows := range [][]Row{lists.Brought, lists.Changed, lists.NotBrought} {
		for _, r := range rows {
			if _, dup := got[r.Name]; dup {
				t.Errorf("%s has two rows", r.Name)
			}
			got[r.Name] = fmt.Sprint(list) + " " + r.As + " | " + r.Why
		}
	}
	for name, want := range map[string]string{
		"codex-a.json":       "0 pluxy/codex account codex-a",
		"codex-siwc.json":    "1 pluxy/codex account codex-siwc-siwc | it holds no token",
		"claude-a.json":      "1 pluxy/claude account claude-a | headers.X-S",
		"kimi-a.json":        "0 pluxy/kimi",
		"kimi-ai.json":       "0 pluxy/kimi",
		"vertex-a.json":      "0 pluxy/gemini",
		"xai-a.json":         "0 pluxy/xai",
		"meta-a.json":        "0 pluxy/meta",
		"devin-a.json":       "0 pluxy/devin",
		"antigravity-a.json": "0 pluxy/antigravity account antigravity-a",
		"gemini-a.json":      "2  | type gemini is not supported (CLIProxyAPI does not load it either)",
		"notype.json":        "2  | it has no type field (CLIProxyAPI does not load it either)",
		"qwen-a.json":        "2  | its type is not one the import takes",
		"empty-claude.json":  "2  | it holds no token",
		"broken.json":        "2  | not a JSON object",
		"notes.txt":          "2  | CLIProxyAPI reads only the .json files at the top of its auth folder",
		"backup.json":        "2  | CLIProxyAPI reads only the .json files at the top of its auth folder",
	} {
		if !strings.HasPrefix(got[name], want) && !strings.Contains(got[name], want) {
			t.Errorf("%s: %q, want %q", name, got[name], want)
		}
	}
	if len(got) != len(a.Entries) || len(a.Entries) != 17 {
		t.Errorf("%d rows for %d entries", len(got), len(a.Entries))
	}
	// An account the store already has is named as not brought.
	imported, taken, err = Import(fakeStore{}, a.Files, func(id string) bool { return id == "xai-a" })
	if err != nil {
		t.Fatal(err)
	}
	lists, err = a.Ledger(imported, taken).Report()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(lists.NotBrought, func(r Row) bool {
		return r.Name == "xai-a.json" && strings.Contains(r.Why, "config already has")
	}) {
		t.Errorf("xai-a.json not named as taken: %+v", lists.NotBrought)
	}
}

// fakeStore takes every account.
type fakeStore struct{}

func (fakeStore) Create(r cred.Record) (cred.Record, error) { return r, nil }

func TestImportMessagesUseCLIProxyAPI(t *testing.T) {
	res := importYAML(t, `
host: "127.0.0.1"
server:
  host: "127.0.0.1"
routing:
  strategy: mystery
codex-api-key:
  - {api-key: old-codex, base-url: "https://old.example.com"}
api-keys:
  codex:
    - {name: bare, keys: [{api-key: fake-codex}]}
  openai-compatibility:
    - name: pool
      base-url: "https://pool.example.com/v1"
      keys: [{api-key: fake-pool}]
      models:
        - {name: m-a, alias: shared}
        - {name: m-b, alias: shared}
      excluded-models: [m-x]
oauth:
  model-alias:
    qwen: [{name: a, alias: b}]
  excluded-models:
    qwen: [a]
totally-unknown-key: 1
`)
	if row, list := rowOf(t, res, "host"); list != NotBrought || row.Why != "the file sets server.host too, and CLIProxyAPI reads that one" {
		t.Errorf("host: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "routing.strategy"); list != Changed || row.Why != "CLIProxyAPI does not know the name either, and uses round-robin" {
		t.Errorf("routing.strategy: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "codex-api-key[0]"); list != NotBrought || row.Why != "the file sets api-keys.codex too, and CLIProxyAPI reads that one" {
		t.Errorf("codex-api-key[0]: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "oauth.model-alias.qwen"); list != NotBrought || row.Why != "no pluxy extension serves CLIProxyAPI's qwen accounts" {
		t.Errorf("oauth.model-alias.qwen: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "oauth.excluded-models.qwen"); list != NotBrought || row.Why != "no pluxy extension serves CLIProxyAPI's qwen accounts" {
		t.Errorf("oauth.excluded-models.qwen: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "totally-unknown-key"); list != NotBrought || row.Why != "not a key CLIProxyAPI or pluxy reads" {
		t.Errorf("totally-unknown-key: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "api-keys.openai-compatibility[0]"); list != Changed || !strings.Contains(row.Why, "it did nothing in CLIProxyAPI either") || !strings.Contains(row.Why, `CLIProxyAPI shared "shared" among`) {
		t.Errorf("openai-compatibility[0]: %d %+v", list, row)
	}
	allNotices := strings.Join(res.Notices, "\n")
	for _, want := range []string{
		"dropped, as CLIProxyAPI drops a Codex API key without one",
		"which CLIProxyAPI rotates over as a pool; pluxy tries them in order as the fallback chain routing.chains.shared",
		"oauth.model-alias.qwen: no pluxy extension serves CLIProxyAPI's qwen accounts; ignored",
		"oauth.excluded-models.qwen: no pluxy extension serves CLIProxyAPI's qwen accounts; ignored",
		`CLIProxyAPI shared "shared" among`,
	} {
		if !strings.Contains(allNotices, want) {
			t.Errorf("notices lack %q:\n%s", want, allNotices)
		}
	}
	if strings.Contains(allNotices, "today's") {
		t.Errorf("notices still say today's:\n%s", allNotices)
	}

	dupKeys := importYAML(t, "api-keys: [old-key]\naccess:\n  api-keys: [new-key]\n")
	if row, list := rowOf(t, dupKeys, "api-keys"); list != NotBrought || row.Why != "the file sets access.api-keys too, and CLIProxyAPI reads that one" {
		t.Errorf("api-keys: %d %+v", list, row)
	}
}
