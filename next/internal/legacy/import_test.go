package legacy

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
	"server.host":             "127.0.0.1",
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
// for something, and gives every provider section a group with each of its fields.
func ruleDoc(t *testing.T) tree {
	t.Helper()
	doc := tree{}
	groups := map[string]tree{}
	for _, f := range families {
		groups[f.v8] = tree{}
	}
	for _, path := range slices.Sorted(maps.Keys(rules)) {
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
		g["base-url"] = groupSample["base-url"]
		g["keys"] = []any{tree{"api-key": "fake-upstream-key", "weight": 1}}
		delete(g, "api-key")
		delete(g, "weight")
		keys[s] = []any{g}
	}
	doc["api-keys"] = keys
	return doc
}

// Every setting of the rule table, set to something that asks for it, lands in exactly one of
// the report's lists: ImportConfig fails otherwise, naming the setting.
func TestImportReportCoversEveryRule(t *testing.T) {
	doc := ruleDoc(t)
	b, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ImportConfig(b, t.TempDir())
	if err != nil {
		t.Fatalf("ImportConfig: %v\n%s", err, b)
	}
	lists, err := res.Ledger.Report()
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
	// Each provider section's group is one row.
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

// A bcrypt management key cannot be brought: the import makes a new key and names the change;
// a config.yaml core runs on has none.
func TestImportBcryptKey(t *testing.T) {
	const doc = "remote-management: {secret-key: \"$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234\"}\n"
	res := importYAML(t, doc)
	if len(res.NewManagementKey) < 20 || res.Config["management_key"] != res.NewManagementKey {
		t.Errorf("new key %q, config %v", res.NewManagementKey, res.Config["management_key"])
	}
	row, list := rowOf(t, res, "remote-management.secret-key")
	if list != Changed || !strings.Contains(row.Why, "bcrypt") || strings.Contains(row.To, "$2a$") {
		t.Errorf("row %d %+v", list, row)
	}
	if again := importYAML(t, doc); again.NewManagementKey == res.NewManagementKey {
		t.Error("two imports made the same key")
	}
	if r, err := Translate([]byte(doc)); err != nil || r.Config["management_key"] != nil || !r.HashedManagementKey {
		t.Errorf("Translate: %v %v", r.Config["management_key"], err)
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
	if row, list := rowOf(t, res, "codex-api-key[0]"); list != NotBrought || !strings.Contains(row.Why, "no base-url") {
		t.Errorf("codex[0]: %d %+v", list, row)
	}
	if row, list := rowOf(t, res, "openai-compatibility[0]"); list != NotBrought || !strings.Contains(row.Why, "disabled") {
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
	led := a.Ledger(a.Files, nil)
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
		"antigravity-a.json": "2  | pluxy no longer ships the Antigravity provider",
		"gemini-a.json":      "2  | today's core does not load it either",
		"notype.json":        "2  | today's core does not load it either",
		"qwen-a.json":        "2  | its type is not one the import takes",
		"empty-claude.json":  "2  | it holds no token",
		"broken.json":        "2  | not a JSON object",
		"notes.txt":          "2  | today's core reads only the .json files",
		"backup.json":        "2  | today's core reads only the .json files",
	} {
		if !strings.HasPrefix(got[name], want) && !strings.Contains(got[name], want) {
			t.Errorf("%s: %q, want %q", name, got[name], want)
		}
	}
	if len(got) != len(a.Entries) || len(a.Entries) != 17 {
		t.Errorf("%d rows for %d entries", len(got), len(a.Entries))
	}
	// An account the store already has is named as not brought.
	imported, taken, _ := Import(fakeStore{}, a.Files, func(id string) bool { return id == "xai-a" })
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
