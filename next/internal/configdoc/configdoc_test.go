package configdoc

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/sdk/payload"
	"gopkg.in/yaml.v3"
)

var placeholderRE = regexp.MustCompile(`^pluxy-redacted-[0-9a-f]{24}$`)

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func mustParse(t *testing.T, src string, isYAML bool) *Doc {
	t.Helper()
	d, err := Parse([]byte(src), isYAML)
	if err != nil {
		t.Fatalf("Parse: %v\n%s", err, src)
	}
	return d
}

// scalarValues parses b and returns every scalar in it, keys included. Comments are not
// values, so a secret repeated in a comment does not show up here.
func scalarValues(t *testing.T, b []byte, isYAML bool) []string {
	t.Helper()
	d := mustParse(t, string(b), isYAML)
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			out = append(out, n.Value)
		}
		if n.Kind == yaml.AliasNode && n.Alias != nil {
			walk(n.Alias)
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(d.root)
	return out
}

// loads checks that core's config reader accepts b and returns what it read.
func loads(t *testing.T, b string, isYAML bool) *server.Config {
	t.Helper()
	path := "config.json"
	if isYAML {
		path = "config.yaml"
	}
	cfg, err := server.ParseConfig([]byte(b), path)
	if err != nil {
		t.Fatalf("server.ParseConfig: %v\n%s", err, b)
	}
	return cfg
}

// secretNodes returns the nodes of the document that hold secrets, and the header text of those
// that are header values of a provider section.
func (d *Doc) secretNodes() ([]*yaml.Node, map[*yaml.Node]string) { return found(d.root, false) }

// redactChecked redacts src and checks the properties every redaction must have: every secret
// the document held is gone as a value (checked by re-parsing, so a comment that repeats it
// does not count), every placeholder has the documented shape, and Restore puts it all back.
// It returns the redacted text and the restored text.
func redactChecked(t *testing.T, src string, isYAML bool, wantSecrets []string) (red, restored string) {
	t.Helper()
	r := NewRedactor()
	secrets := mustParse(t, src, isYAML).Secrets()
	if !slices.Equal(sorted(secrets), sorted(wantSecrets)) {
		t.Fatalf("Secrets() = %q, want %q", sorted(secrets), sorted(wantSecrets))
	}
	out, err := r.Redact([]byte(src), isYAML)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	vals := scalarValues(t, out, isYAML)
	nodes, texts := mustParse(t, src, isYAML).secretNodes()
	for _, n := range nodes {
		if slices.Contains(vals, n.Value) {
			t.Errorf("secret %q is still a value in the redacted document:\n%s", n.Value, out)
		}
		// A secret that is not a string has its type in its placeholder.
		if !slices.Contains(vals, r.placeholderOf(n, texts)) {
			t.Errorf("no placeholder for %q in the redacted document:\n%s", n.Value, out)
		}
	}
	for _, s := range mustParse(t, string(out), isYAML).Secrets() {
		if !placeholderRE.MatchString(s) {
			t.Errorf("redacted document still holds %q where a secret goes", s)
		}
	}
	back, err := r.Restore(out, []byte(src), isYAML)
	if err != nil {
		t.Fatalf("Restore: %v\nredacted:\n%s", err, out)
	}
	return string(out), string(back)
}

func TestRedactExampleConfig(t *testing.T) {
	b, err := os.ReadFile("../legacy/testdata/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	want := []string{"your-api-key-1", "your-api-key-2", "your-api-key-3"}
	red, back := redactChecked(t, src, true, want)
	if back != src {
		t.Error("Restore(Redact(x), x) is not x byte for byte")
	}
	in, out := strings.Split(src, "\n"), strings.Split(red, "\n")
	if len(in) != len(out) {
		t.Fatalf("line count changed: %d to %d", len(in), len(out))
	}
	changed := 0
	for i := range in {
		if in[i] == out[i] {
			continue
		}
		changed++
		// Only a line that held a secret may change, and it changes to a placeholder.
		hit := false
		for _, s := range want {
			hit = hit || strings.Contains(in[i], s)
		}
		if !hit {
			t.Errorf("line %d changed but holds no secret:\n- %s\n+ %s", i+1, in[i], out[i])
		}
		if !strings.Contains(out[i], "pluxy-redacted-") {
			t.Errorf("line %d changed but holds no placeholder: %s", i+1, out[i])
		}
	}
	if changed != len(want) {
		t.Errorf("%d lines changed, want %d", changed, len(want))
	}
	// The same secret has the same placeholder within one Redactor, so the three differ.
	if strings.Count(red, "pluxy-redacted-") != len(want) {
		t.Errorf("redacted example holds %d placeholders, want %d", strings.Count(red, "pluxy-redacted-"), len(want))
	}
}

const jsonSecrets = `{
  "listen": "127.0.0.1:8400",
  "client_keys": ["fake-client-key-one", "fake-client-key-two"],
  "management_key": "fake-management-key",
  "packages": {"registries": [
    {"name": "private", "url": "https://fakeuser:fakepass@registry.example.com/index.json?token=fake-query-token"},
    {"name": "public", "url": "https://registry.example.com/index.json"}
  ]},
  "extension": {
    "manifest": "/opt/ext/pluxy-extension.json",
    "accounts": [{
      "id": "acct-1",
      "weight": 3,
      "settings": {
        "base_url": "https://api.example.com/v1",
        "headers": {"Authorization": "Bearer fake-header-token", "X-Trace": "trace-visible"}
      },
      "credential": {"kind": "api_key", "api_key": "fake-upstream-key", "expires_at": 1893456000}
    }]
  }
}
`

func TestRedactJSONConfig(t *testing.T) {
	want := []string{
		"fake-client-key-one", "fake-client-key-two", "fake-management-key",
		"https://fakeuser:fakepass@registry.example.com/index.json?token=fake-query-token",
		"Bearer fake-header-token", "fake-upstream-key", "1893456000",
	}
	red, back := redactChecked(t, jsonSecrets, false, want)
	if back != jsonSecrets {
		t.Errorf("round trip is not byte exact:\n%s", back)
	}
	for _, leak := range []string{"fake-", "fakeuser", "fakepass", "1893456000"} {
		if strings.Contains(red, leak) {
			t.Errorf("redacted JSON still contains %q:\n%s", leak, red)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(red), &doc); err != nil {
		t.Fatalf("redacted JSON does not parse: %v\n%s", err, red)
	}
	// What must stay visible, and what is hidden, by name.
	if doc["listen"] != "127.0.0.1:8400" {
		t.Errorf("listen = %v", doc["listen"])
	}
	regs := doc["packages"].(map[string]any)["registries"].([]any)
	if u := regs[0].(map[string]any)["url"].(string); !placeholderRE.MatchString(u) {
		t.Errorf("registry URL with credentials = %q, want a placeholder", u)
	}
	if u := regs[1].(map[string]any)["url"]; u != "https://registry.example.com/index.json" {
		t.Errorf("plain registry URL = %v, want unchanged", u)
	}
	acct := doc["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	headers := acct["settings"].(map[string]any)["headers"].(map[string]any)
	if !placeholderRE.MatchString(headers["Authorization"].(string)) {
		t.Errorf("Authorization header = %v, want a placeholder", headers["Authorization"])
	}
	if headers["X-Trace"] != "trace-visible" {
		t.Errorf("X-Trace header = %v, want unchanged", headers["X-Trace"])
	}
	if acct["settings"].(map[string]any)["base_url"] != "https://api.example.com/v1" {
		t.Errorf("base_url changed: %v", acct["settings"])
	}
	if acct["id"] != "acct-1" || acct["weight"] != float64(3) {
		t.Errorf("id/weight changed: %v", acct)
	}
	cred := acct["credential"].(map[string]any)
	if cred["kind"] != "api_key" {
		t.Errorf("credential kind = %v, want it visible", cred["kind"])
	}
	for _, k := range []string{"api_key", "expires_at"} {
		if s, ok := cred[k].(string); !ok || !placeholderRE.MatchString(s) {
			t.Errorf("credential %s = %v, want a placeholder", k, cred[k])
		}
	}
	for _, k := range []string{"client_keys", "management_key"} {
		for _, v := range flatStrings(doc[k]) {
			if !placeholderRE.MatchString(v) {
				t.Errorf("%s holds %q, want placeholders", k, v)
			}
		}
	}

	// The number comes back as a number, not as the quoted text of one.
	var restored struct {
		Extension struct {
			Accounts []struct {
				Credential struct {
					ExpiresAt json.Number `json:"expires_at"`
				} `json:"credential"`
			} `json:"accounts"`
		} `json:"extension"`
	}
	dec := json.NewDecoder(strings.NewReader(back))
	dec.UseNumber()
	if err := dec.Decode(&restored); err != nil {
		t.Fatalf("restored JSON: %v", err)
	}
	if got := restored.Extension.Accounts[0].Credential.ExpiresAt; got != "1893456000" {
		t.Errorf("expires_at = %q, want the number 1893456000", got)
	}
	if strings.Contains(back, `"1893456000"`) {
		t.Errorf("expires_at came back as a string:\n%s", back)
	}
}

func flatStrings(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, flatStrings(e)...)
		}
		return out
	}
	return nil
}

const yamlV8 = `# v8 layout
server:
  host: 127.0.0.1
  port: 8400
access:
  api-keys:
    - "fake-v8-double" # first key
    - 'fake-v8-single'
    - fake-v8-plain
management:
  secret-key: "fake-v8-management"
api-keys:
  codex:
    - base-url: https://codex.example.com
      keys:
        - api-key: "fake-v8-codex"
  openai-compatibility:
    - name: local
      base-url: https://compat.example.com/v1
      keys:
        - api-key: 'fake-v8-compat'
        - api-key: fake-v8-compat-plain
`

const yamlOld = `# older layout, mentions fake-old-plain in a comment
api-keys: [fake-old-plain, "fake-old-double", 'fake-old-single']
remote-management:
  secret-key: "fake-old-management"
codex-api-key:
  - api-key: "fake-old-codex"
    base-url: https://codex.example.com
openai-compatibility:
  - name: local
    base-url: https://compat.example.com/v1
    api-key-entries:
      - api-key: "fake-old-compat"
      - api-key: fake-old-compat-plain
`

func TestRedactYAMLLayouts(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		secrets   []string
	}{
		{"v8", yamlV8, []string{"fake-v8-double", "fake-v8-single", "fake-v8-plain", "fake-v8-management", "fake-v8-codex", "fake-v8-compat", "fake-v8-compat-plain"}},
		{"older", yamlOld, []string{"fake-old-plain", "fake-old-double", "fake-old-single", "fake-old-management", "fake-old-codex", "fake-old-compat", "fake-old-compat-plain"}},
		{"non-ascii before the secret", "# ключ\nserver:\n  host: \"хост-ключ\"\nmanagement:\n  secret-key: \"fake-ключ-secret\"\naccess:\n  api-keys: [ключ-plain-ключ]\n", []string{"fake-ключ-secret", "ключ-plain-ключ"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			red, back := redactChecked(t, tc.src, true, tc.secrets)
			if back != tc.src {
				t.Errorf("round trip is not byte exact:\n%s", back)
			}
			if len(strings.Split(red, "\n")) != len(strings.Split(tc.src, "\n")) {
				t.Errorf("line count changed:\n%s", red)
			}
			// Every line without a secret on it is as it was, comments included.
			in, out := strings.Split(tc.src, "\n"), strings.Split(red, "\n")
			for i := range in {
				hit := false
				for _, s := range tc.secrets {
					hit = hit || strings.Contains(in[i], s)
				}
				if !hit && in[i] != out[i] {
					t.Errorf("line %d changed:\n- %s\n+ %s", i+1, in[i], out[i])
				}
			}
		})
	}
	// The comment in the older layout is the user's text and stays as written.
	red, _ := redactChecked(t, yamlOld, true, []string{"fake-old-plain", "fake-old-double", "fake-old-single", "fake-old-management", "fake-old-codex", "fake-old-compat", "fake-old-compat-plain"})
	if !strings.HasPrefix(red, "# older layout, mentions fake-old-plain in a comment\n") {
		t.Errorf("the comment changed:\n%s", red)
	}
}

// A secret behind an anchor, a tag or in a block scalar cannot be replaced where it stands:
// redaction writes the document out again, which must still hide it.
// A secret defined by an anchor and used again through an alias must still be hidden, and the
// redacted document must still parse.
func TestRedactAnchorWithAlias(t *testing.T) {
	src := "access:\n  api-keys:\n    - &k fake-anchored\nmanagement:\n  secret-key: *k\n"
	red, back := redactChecked(t, src, true, []string{"fake-anchored"})
	if strings.Contains(red, "fake-anchored") {
		t.Errorf("redacted document shows the secret:\n%s", red)
	}
	var a, b any
	if err := yaml.Unmarshal([]byte(src), &a); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(back), &b); err != nil || !reflect.DeepEqual(a, b) {
		t.Errorf("restored document differs (%v):\n%s", err, back)
	}
}

func TestRedactFallsBackToReencoding(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		secrets   []string
	}{
		{"anchor", "access:\n  api-keys:\n    - &k fake-anchored # kept\n    - other-key\n", []string{"fake-anchored", "other-key"}},
		{"block scalar", "management:\n  secret-key: |-\n    fake-block-secret\n", []string{"fake-block-secret"}},
		{"tag", "management:\n  secret-key: !!str fake-tagged\n", []string{"fake-tagged"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			red, back := redactChecked(t, tc.src, true, tc.secrets)
			if strings.Contains(red, "fake-anchored") || strings.Contains(red, "fake-block-secret") || strings.Contains(red, "fake-tagged") {
				t.Errorf("redacted document still shows a secret as text:\n%s", red)
			}
			// The restored document need not be the same text, but it must mean the same.
			var a, b any
			if err := yaml.Unmarshal([]byte(tc.src), &a); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal([]byte(back), &b); err != nil {
				t.Fatalf("restored document does not parse: %v\n%s", err, back)
			}
			if !reflect.DeepEqual(a, b) {
				t.Errorf("restored document means something else:\n%s\nwant\n%s", back, tc.src)
			}
		})
	}
}

const restoreJSON = `{
  "listen": "127.0.0.1:8400",
  "client_keys": ["fake-client-key"],
  "management_key": "fake-management-key",
  "packages": {"registries": [{"name": "private", "url": "https://fakeuser:fakepass@registry.example.com/index.json"}]}
}
`

func TestRestoreRefusals(t *testing.T) {
	r := NewRedactor()
	red, err := r.Redact([]byte(restoreJSON), false)
	if err != nil {
		t.Fatal(err)
	}
	clientPH := r.Placeholder("fake-client-key")
	urlPH := r.Placeholder("https://fakeuser:fakepass@registry.example.com/index.json")

	t.Run("placeholder on a non-secret key", func(t *testing.T) {
		edited := strings.Replace(string(red), `"listen": "127.0.0.1:8400"`, `"listen": "`+clientPH+`"`, 1)
		if edited == string(red) {
			t.Fatal("test edit did not apply")
		}
		_, err := r.Restore([]byte(edited), []byte(restoreJSON), false)
		if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrStale) {
			t.Errorf("err = %v, want ErrInvalid only", err)
		}
		// The error says which line the placeholder is on (line 2 of restoreJSON's redaction).
		if err != nil && !strings.Contains(err.Error(), "line 2") {
			t.Errorf("err = %v, want it to name line 2", err)
		}
	})
	t.Run("unknown placeholder", func(t *testing.T) {
		edited := strings.Replace(string(red), clientPH, "pluxy-redacted-000000000000000000000000", 1)
		_, err := r.Restore([]byte(edited), []byte(restoreJSON), false)
		if !errors.Is(err, ErrStale) || errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrStale only", err)
		}
	})
	t.Run("current file lacks the secret", func(t *testing.T) {
		current := strings.Replace(restoreJSON, "fake-client-key", "fake-changed-key", 1)
		_, err := r.Restore(red, []byte(current), false)
		if !errors.Is(err, ErrStale) {
			t.Errorf("err = %v, want ErrStale", err)
		}
	})
	t.Run("another core run's placeholder", func(t *testing.T) {
		other := NewRedactor()
		_, err := other.Restore(red, []byte(restoreJSON), false)
		if !errors.Is(err, ErrStale) {
			t.Errorf("err = %v, want ErrStale", err)
		}
	})
	t.Run("current file unreadable", func(t *testing.T) {
		_, err := r.Restore(red, nil, false)
		if !errors.Is(err, ErrStale) {
			t.Errorf("err = %v, want ErrStale", err)
		}
	})
	t.Run("a URL secret may move to another key", func(t *testing.T) {
		edited := strings.Replace(string(red), `"name": "private"`, `"name": "`+urlPH+`"`, 1)
		if edited == string(red) {
			t.Fatal("test edit did not apply")
		}
		out, err := r.Restore([]byte(edited), []byte(restoreJSON), false)
		if err != nil {
			t.Fatalf("Restore: %v", err)
		}
		var doc struct {
			Packages struct {
				Registries []struct{ Name, URL string } `json:"registries"`
			} `json:"packages"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatal(err)
		}
		reg := doc.Packages.Registries[0]
		if reg.Name != "https://fakeuser:fakepass@registry.example.com/index.json" || reg.URL != reg.Name {
			t.Errorf("registry = %+v", reg)
		}
	})
	t.Run("a document with no placeholders comes back unchanged", func(t *testing.T) {
		for _, tc := range []struct {
			src   string
			isYML bool
		}{{"{\n \"listen\":   \"a:1\" }", false}, {"# c\nserver:   {host: x}\n", true}} {
			out, err := r.Restore([]byte(tc.src), []byte(restoreJSON), tc.isYML)
			if err != nil || string(out) != tc.src {
				t.Errorf("Restore(%q) = %q, %v", tc.src, out, err)
			}
		}
	})
	t.Run("a document that does not parse", func(t *testing.T) {
		_, err := r.Restore([]byte(`{"listen": `), []byte(restoreJSON), false)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

func TestRestoreKeepsEditsElsewhere(t *testing.T) {
	r := NewRedactor()
	red, err := r.Redact([]byte(yamlV8), true)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(red), "port: 8400", "port: 8500 # moved", 1)
	edited = strings.Replace(edited, "# v8 layout", "# v8 layout, edited", 1)
	want := strings.Replace(yamlV8, "port: 8400", "port: 8500 # moved", 1)
	want = strings.Replace(want, "# v8 layout", "# v8 layout, edited", 1)
	got, err := r.Restore([]byte(edited), []byte(yamlV8), true)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// Dropping a secret from the edited document drops it from the result; the others stay.
	ph := r.Placeholder("fake-v8-plain")
	dropped := strings.Replace(string(red), "    - "+ph+"\n", "", 1)
	if dropped == string(red) {
		t.Fatalf("test edit did not apply:\n%s", red)
	}
	got, err = r.Restore([]byte(dropped), []byte(yamlV8), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(yamlV8, "    - fake-v8-plain\n", "", 1); string(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// edited parses src, applies change, writes the document and checks core's config reader
// still accepts it. It returns the new text.
func edited(t *testing.T, src string, isYAML bool, change func(d *Doc) error) string {
	t.Helper()
	d := mustParse(t, src, isYAML)
	if err := change(d); err != nil {
		t.Fatalf("edit: %v", err)
	}
	b, err := d.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	loads(t, string(b), isYAML)
	return string(b)
}

func str(s string) *string { return &s }
func flag(b bool) *bool    { return &b }
func num(n int64) *int64   { return &n }

const editJSON = `{
  "listen": "127.0.0.1:8400",
  "client_keys": [
    "fake-key-one"
  ],
  "management_key": "fake-old-management",
  "extension": {
    "manifest": "m.json",
    "accounts": [
      {
        "id": "a"
      }
    ]
  }
}
`

func TestEditJSONKeys(t *testing.T) {
	got := edited(t, editJSON, false, func(d *Doc) error { return d.AddClientKey(`fake<&>"key`) })
	want := strings.Replace(editJSON, `"fake-key-one"`, `"fake-key-one",`+"\n    "+`"fake<&>\"key"`, 1)
	if got != want {
		t.Errorf("AddClientKey:\n%s\nwant:\n%s", got, want)
	}
	if keys := loads(t, got, false).ClientKeys; !slices.Equal(keys, []string{"fake-key-one", `fake<&>"key`}) {
		t.Errorf("client keys = %q", keys)
	}

	left := -1
	got = edited(t, got, false, func(d *Doc) (err error) {
		left, err = d.RemoveClientKeys(func(k string) bool { return k == "fake-key-one" })
		return
	})
	if left != 1 || !strings.Contains(got, `"fake<&>\"key"`) || strings.Contains(got, "fake-key-one") {
		t.Errorf("RemoveClientKeys left %d:\n%s", left, got)
	}

	got = edited(t, editJSON, false, func(d *Doc) error { return d.SetManagementKey("fake-new-management") })
	if want := strings.Replace(editJSON, "fake-old-management", "fake-new-management", 1); got != want {
		t.Errorf("SetManagementKey:\n%s\nwant:\n%s", got, want)
	}
	if k := loads(t, got, false).ManagementKey; k != "fake-new-management" {
		t.Errorf("management key = %q", k)
	}

	// A key that is not there is added; the list and the key come after the others.
	noKeys := `{"listen": "127.0.0.1:8400", "management_key": "fake-m"}`
	got = edited(t, noKeys, false, func(d *Doc) error { return d.AddClientKey("fake-first") })
	if keys := loads(t, got, false).ClientKeys; !slices.Equal(keys, []string{"fake-first"}) {
		t.Errorf("client keys = %q in\n%s", keys, got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("document not ended by a newline: %q", got)
	}

	// Removing nothing, or setting nothing, leaves the text as it was.
	same := edited(t, editJSON, false, func(d *Doc) error {
		_, err := d.RemoveClientKeys(func(string) bool { return false })
		return err
	})
	if same != editJSON {
		t.Errorf("a removal of nothing changed the text:\n%s", same)
	}
}

func TestEditJSONKeepsIndent(t *testing.T) {
	for _, tc := range []struct{ name, indent string }{{"two spaces", "  "}, {"four spaces", "    "}, {"tab", "\t"}} {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.ReplaceAll(editJSON, "  ", tc.indent)
			got := edited(t, src, false, func(d *Doc) error { return d.SetManagementKey("fake-new") })
			if want := strings.Replace(src, "fake-old-management", "fake-new", 1); got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
	// A document on one line stays on one line.
	got := edited(t, `{"listen":"127.0.0.1:8400","client_keys":["fake-c"],"management_key":"fake-m"}`, false, func(d *Doc) error { return d.SetManagementKey("fake-n") })
	if got != "{\"listen\":\"127.0.0.1:8400\",\"client_keys\":[\"fake-c\"],\"management_key\":\"fake-n\"}\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplySettingsJSON(t *testing.T) {
	apply := func(src string, p SettingsPatch) (string, error) {
		d := mustParse(t, src, false)
		if err := d.ApplySettings(p); err != nil {
			return "", err
		}
		b, err := d.Bytes()
		return string(b), err
	}
	t.Run("everything at once", func(t *testing.T) {
		got := edited(t, editJSON, false, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{
				Strategy:           str("weighted-round-robin"),
				SessionAffinity:    flag(true),
				SessionAffinityTTL: str("2h"),
				ForceModelPrefix:   flag(true),
				Retry:              &RetryPatch{RequestRetry: num(3), MaxRetryCredentials: num(2), MaxRetryInterval: num(5)},
				Cooldown:           &CooldownPatch{DisableCooling: flag(true), TransientErrorCooldownSeconds: num(20)},
			}})
		})
		r := loads(t, got, false).Routing
		if r.Strategy != "weighted" || r.MaxAccounts != 2 || r.MaxWaitMS != 5000 || r.SessionAffinity == nil || r.SessionAffinity.TTLMS != 7200000 || !r.ForceModelPrefix ||
			r.RequestRetry == nil || *r.RequestRetry != 3 || !r.DisableCooling || r.TransientErrorCooldownMS != 20000 {
			t.Errorf("routing = %+v (affinity %+v) in\n%s", r, r.SessionAffinity, got)
		}
		var raw struct {
			Routing map[string]any `json:"routing"`
		}
		if err := json.Unmarshal([]byte(got), &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Routing["strategy"] != "weighted" || raw.Routing["max_accounts"] != float64(2) || raw.Routing["max_wait_ms"] != float64(5000) || raw.Routing["force_model_prefix"] != true ||
			raw.Routing["request_retry"] != float64(3) || raw.Routing["disable_cooling"] != true || raw.Routing["transient_error_cooldown_ms"] != float64(20000) {
			t.Errorf("routing keys = %v, want the JSON names", raw.Routing)
		}
		if !strings.HasPrefix(got, "{\n  \"listen\"") || strings.Index(got, `"listen"`) > strings.Index(got, `"routing"`) {
			t.Errorf("indent or key order lost:\n%s", got)
		}
	})
	t.Run("no transient cooldown", func(t *testing.T) {
		got, err := apply(editJSON, SettingsPatch{Routing: &RoutingPatch{Cooldown: &CooldownPatch{TransientErrorCooldownSeconds: num(-1)}}})
		if err != nil {
			t.Fatal(err)
		}
		if ms := loads(t, got, false).Routing.TransientErrorCooldownMS; ms != -1 {
			t.Errorf("transient_error_cooldown_ms = %d, want -1", ms)
		}
	})
	t.Run("strategy names", func(t *testing.T) {
		for in, want := range map[string]string{"round-robin": "round-robin", "fill-first": "fill-first", " FF ": "fill-first", "wrr": "weighted", "Weighted": "weighted", "least-active": "least-active"} {
			got := edited(t, editJSON, false, func(d *Doc) error {
				return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{Strategy: str(in)}})
			})
			if s := loads(t, got, false).Routing.Strategy; s != want {
				t.Errorf("strategy %q gives %q, want %q", in, s, want)
			}
		}
	})
	t.Run("affinity off removes it, ttl then needs it on", func(t *testing.T) {
		on, err := apply(editJSON, SettingsPatch{Routing: &RoutingPatch{SessionAffinity: flag(true), SessionAffinityTTL: str("30m")}})
		if err != nil {
			t.Fatal(err)
		}
		if a := loads(t, on, false).Routing.SessionAffinity; a == nil || a.TTLMS != 1800000 {
			t.Errorf("affinity = %+v", a)
		}
		off, err := apply(on, SettingsPatch{Routing: &RoutingPatch{SessionAffinity: flag(false)}})
		if err != nil {
			t.Fatal(err)
		}
		if a := loads(t, off, false).Routing.SessionAffinity; a != nil {
			t.Errorf("affinity = %+v, want none", a)
		}
		if _, err := apply(off, SettingsPatch{Routing: &RoutingPatch{SessionAffinityTTL: str("1h")}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("ttl with affinity off: err = %v, want ErrInvalid", err)
		}
		if _, err := apply(on, SettingsPatch{Routing: &RoutingPatch{SessionAffinity: flag(false), SessionAffinityTTL: str("1h")}}); !errors.Is(err, ErrInvalid) {
			t.Errorf("ttl with affinity turned off in the same patch: err = %v, want ErrInvalid", err)
		}
		// A ttl shorter than a second is a second, as today's core reads it.
		short, err := apply(on, SettingsPatch{Routing: &RoutingPatch{SessionAffinityTTL: str("500ms")}})
		if err != nil {
			t.Fatal(err)
		}
		if a := loads(t, short, false).Routing.SessionAffinity; a == nil || a.TTLMS != 1000 {
			t.Errorf("affinity = %+v, want 1000 ms", a)
		}
	})
	t.Run("invalid values", func(t *testing.T) {
		for name, p := range map[string]RoutingPatch{
			"unknown strategy":     {Strategy: str("random")},
			"empty strategy":       {Strategy: str("")},
			"negative accounts":    {Retry: &RetryPatch{MaxRetryCredentials: num(-1)}},
			"negative interval":    {Retry: &RetryPatch{MaxRetryInterval: num(-1)}},
			"interval over 30 day": {Retry: &RetryPatch{MaxRetryInterval: num(30*24*60*60 + 1)}},
			"negative rounds":      {Retry: &RetryPatch{RequestRetry: num(-1)}},
			"transient below -1":   {Cooldown: &CooldownPatch{TransientErrorCooldownSeconds: num(-2)}},
			"bad duration":         {SessionAffinity: flag(true), SessionAffinityTTL: str("soon")},
			"zero duration":        {SessionAffinity: flag(true), SessionAffinityTTL: str("0s")},
			"negative duration":    {SessionAffinity: flag(true), SessionAffinityTTL: str("-5m")},
		} {
			if _, err := apply(editJSON, SettingsPatch{Routing: &p}); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
	})
	t.Run("zero limits and the largest interval are allowed", func(t *testing.T) {
		got := edited(t, editJSON, false, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{Retry: &RetryPatch{MaxRetryCredentials: num(0), MaxRetryInterval: num(30 * 24 * 60 * 60)}}})
		})
		if w := loads(t, got, false).Routing.MaxWaitMS; w != 30*24*60*60*1000 {
			t.Errorf("max_wait_ms = %d", w)
		}
	})
	t.Run("an empty patch changes nothing", func(t *testing.T) {
		got, err := apply(editJSON, SettingsPatch{})
		if err != nil || got != editJSON {
			t.Errorf("got %q, %v", got, err)
		}
		got, err = apply(editJSON, SettingsPatch{Routing: &RoutingPatch{}})
		if err != nil || got != editJSON {
			t.Errorf("got %q, %v", got, err)
		}
	})
}

const editYAMLTail = `extension:
  manifest: m.json
  accounts:
    - id: a
`

const editYAMLV8 = `# my config
server:
  host: 127.0.0.1 # loopback
  port: 8400
access:
  api-keys:
    - "fake-key-one" # first
management:
  secret-key: "fake-old-management"
routing:
  strategy: round-robin # default
` + editYAMLTail

const editYAMLOld = `# older layout
host: 127.0.0.1
port: 8400
api-keys:
  - fake-old-one # first
remote-management:
  secret-key: fake-old-management
max-retry-credentials: 1
max-retry-interval: 10
` + editYAMLTail

func indexOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	last := -1
	for _, p := range parts {
		i := strings.Index(s, p)
		if i < 0 {
			t.Errorf("%q is missing from\n%s", p, s)
			return
		}
		if i < last {
			t.Errorf("%q is out of order in\n%s", p, s)
		}
		last = i
	}
}

func TestEditYAMLKeepsCommentsAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		keysAt    string // where client keys are
		mgmtAt    string
		comments  []string
		order     []string
	}{
		{"v8", editYAMLV8, "access:", "management:", []string{"# my config", "# loopback", "# first", "# default"}, []string{"server:", "access:", "management:", "routing:", "extension:"}},
		{"older", editYAMLOld, "api-keys:", "remote-management:", []string{"# older layout", "# first"}, []string{"host:", "port:", "api-keys:", "remote-management:", "max-retry-credentials:", "extension:"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isV8 := tc.name == "v8"
			got := edited(t, tc.src, true, func(d *Doc) error { return d.AddClientKey("fake-key-two") })
			cfg := loads(t, got, true)
			if len(cfg.ClientKeys) != 2 || cfg.ClientKeys[1] != "fake-key-two" {
				t.Errorf("client keys = %q in\n%s", cfg.ClientKeys, got)
			}
			// The new key is written the way the one before it is.
			if isV8 && !strings.Contains(got, `- "fake-key-two"`) {
				t.Errorf("new key not double quoted:\n%s", got)
			}
			for _, c := range tc.comments {
				if !strings.Contains(got, c) {
					t.Errorf("comment %q lost:\n%s", c, got)
				}
			}
			indexOrder(t, got, tc.order...)
			// The older layout is edited where it is: no v8 section appears beside it.
			if !isV8 && (strings.Contains(got, "access:") || strings.Contains(got, "\nmanagement:")) {
				t.Errorf("a v8 section was added to the older layout:\n%s", got)
			}

			got = edited(t, got, true, func(d *Doc) error { return d.SetManagementKey("fake-new-management") })
			if k := loads(t, got, true).ManagementKey; k != "fake-new-management" {
				t.Errorf("management key = %q in\n%s", k, got)
			}
			if strings.Contains(got, "fake-old-management") || strings.Count(got, "secret-key:") != 1 {
				t.Errorf("management key not replaced in place:\n%s", got)
			}
			if !isV8 && strings.Contains(got, "\nmanagement:") {
				t.Errorf("a v8 section was added to the older layout:\n%s", got)
			}

			// Not edited(): core does not load the document this leaves (no client keys).
			d := mustParse(t, got, true)
			left, err := d.RemoveClientKeys(func(k string) bool { return strings.HasPrefix(k, "fake-key-") || strings.HasPrefix(k, "fake-old-one") })
			if err != nil {
				t.Fatal(err)
			}
			b, err := d.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			got = string(b)
			if left != 0 {
				t.Errorf("left = %d", left)
			}
			// Core refuses a config without client keys, which shows they are all gone.
			if _, err := server.ParseConfig([]byte(got), "config.yaml"); err == nil || !strings.Contains(err.Error(), "client_keys is empty") {
				t.Errorf("ParseConfig = %v, want the refusal of no client keys, in\n%s", err, got)
			}
			indexOrder(t, got, tc.order...)
		})
	}
}

func TestEditYAMLCreatesMissingSections(t *testing.T) {
	// No client keys and a null access: the section is made.
	src := "# top\nserver:\n  host: 127.0.0.1\n  port: 8400\naccess: # none yet\nmanagement:\n  secret-key: fake-m\n" + editYAMLTail
	got := edited(t, src, true, func(d *Doc) error { return d.AddClientKey("fake-first") })
	if keys := loads(t, got, true).ClientKeys; !slices.Equal(keys, []string{"fake-first"}) {
		t.Errorf("client keys = %q in\n%s", keys, got)
	}
	if !strings.Contains(got, "# top") {
		t.Errorf("comment lost:\n%s", got)
	}
	// A v8 root api-keys mapping holds providers, not client keys: it is left alone.
	providers := "server:\n  host: 127.0.0.1\n  port: 8400\napi-keys:\n  codex:\n    - base-url: https://codex.example.com\n      keys:\n        - api-key: fake-codex\n"
	got = edited(t, providers, true, func(d *Doc) error { return d.AddClientKey("fake-first") })
	cfg := loads(t, got, true)
	if !slices.Equal(cfg.ClientKeys, []string{"fake-first"}) || !strings.Contains(got, "fake-codex") || !strings.Contains(got, "codex:") {
		t.Errorf("client keys = %q in\n%s", cfg.ClientKeys, got)
	}
	// No management key anywhere: the v8 one is added.
	got = edited(t, "server:\n  host: 127.0.0.1\n  port: 8400\naccess:\n  api-keys:\n    - fake-c\n"+editYAMLTail, true, func(d *Doc) error { return d.SetManagementKey("fake-m") })
	if k := loads(t, got, true).ManagementKey; k != "fake-m" || !strings.Contains(got, "management:") {
		t.Errorf("management key = %q in\n%s", k, got)
	}
}

func TestApplySettingsYAML(t *testing.T) {
	patch := SettingsPatch{Routing: &RoutingPatch{
		Strategy:           str("weighted"),
		SessionAffinity:    flag(true),
		SessionAffinityTTL: str("2h"),
		ForceModelPrefix:   flag(true),
		Retry:              &RetryPatch{MaxRetryCredentials: num(2), MaxRetryInterval: num(5)},
	}}
	t.Run("retry and cooldown", func(t *testing.T) {
		got := edited(t, editYAMLV8, true, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{
				Retry:    &RetryPatch{RequestRetry: num(2)},
				Cooldown: &CooldownPatch{DisableCooling: flag(true), TransientErrorCooldownSeconds: num(-1)},
			}})
		})
		var raw struct {
			Routing struct {
				Retry    map[string]int `yaml:"retry"`
				Cooldown map[string]any `yaml:"cooldown"`
			} `yaml:"routing"`
		}
		if err := yaml.Unmarshal([]byte(got), &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Routing.Retry["request-retry"] != 2 || raw.Routing.Cooldown["disable-cooling"] != true || raw.Routing.Cooldown["transient-error-cooldown-seconds"] != -1 {
			t.Errorf("routing = %+v, want today's names in\n%s", raw.Routing, got)
		}
		r := loads(t, got, true).Routing
		if r.RequestRetry == nil || *r.RequestRetry != 2 || !r.DisableCooling || r.TransientErrorCooldownMS != -1 {
			t.Errorf("routing read back = %+v", r)
		}
	})
	t.Run("v8 paths", func(t *testing.T) {
		got := edited(t, editYAMLV8, true, func(d *Doc) error { return d.ApplySettings(patch) })
		r := loads(t, got, true).Routing
		if r.Strategy != "weighted" || r.MaxAccounts != 2 || r.MaxWaitMS != 5000 || r.SessionAffinity == nil || r.SessionAffinity.TTLMS != 7200000 {
			t.Errorf("routing = %+v (affinity %+v) in\n%s", r, r.SessionAffinity, got)
		}
		var raw struct {
			Routing struct {
				Strategy string         `yaml:"strategy"`
				Affinity bool           `yaml:"session-affinity"`
				TTL      string         `yaml:"session-affinity-ttl"`
				Force    bool           `yaml:"force-model-prefix"`
				Retry    map[string]int `yaml:"retry"`
			} `yaml:"routing"`
		}
		if err := yaml.Unmarshal([]byte(got), &raw); err != nil {
			t.Fatal(err)
		}
		x := raw.Routing
		if x.Strategy != "weighted-round-robin" || !x.Affinity || x.TTL != "2h" || !x.Force || x.Retry["max-retry-credentials"] != 2 || x.Retry["max-retry-interval"] != 5 {
			t.Errorf("routing keys = %+v, want today's names with seconds", x)
		}
		if !strings.Contains(got, "# default") || !strings.Contains(got, "# my config") {
			t.Errorf("comments lost:\n%s", got)
		}
		indexOrder(t, got, "server:", "access:", "management:", "routing:", "extension:")
	})
	t.Run("older spelling when only that exists", func(t *testing.T) {
		got := edited(t, editYAMLOld, true, func(d *Doc) error { return d.ApplySettings(patch) })
		r := loads(t, got, true).Routing
		if r.MaxAccounts != 2 || r.MaxWaitMS != 5000 || r.Strategy != "weighted" {
			t.Errorf("routing = %+v in\n%s", r, got)
		}
		if strings.Count(got, "max-retry-credentials:") != 1 || strings.Count(got, "max-retry-interval:") != 1 {
			t.Errorf("a retry key was added beside the older one:\n%s", got)
		}
		if strings.Contains(got, "retry:") {
			t.Errorf("a v8 retry section was added:\n%s", got)
		}
		if !strings.Contains(got, "max-retry-credentials: 2") || !strings.Contains(got, "max-retry-interval: 5") {
			t.Errorf("older keys not updated in place:\n%s", got)
		}
	})
	t.Run("older force-model-prefix at the root", func(t *testing.T) {
		got := edited(t, editYAMLOld+"force-model-prefix: false # old spelling\n", true, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{ForceModelPrefix: flag(true)}})
		})
		if !loads(t, got, true).Routing.ForceModelPrefix || strings.Count(got, "force-model-prefix:") != 1 || !strings.Contains(got, "force-model-prefix: true # old spelling") {
			t.Errorf("force-model-prefix not set in place:\n%s", got)
		}
	})
	t.Run("the v8 path wins when both exist", func(t *testing.T) {
		src := editYAMLOld + "routing:\n  retry:\n    max-retry-credentials: 7\n"
		got := edited(t, src, true, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{Retry: &RetryPatch{MaxRetryCredentials: num(3)}}})
		})
		if n := loads(t, got, true).Routing.MaxAccounts; n != 3 {
			t.Errorf("max_accounts = %d in\n%s", n, got)
		}
		if !strings.Contains(got, "max-retry-credentials: 1\n") {
			t.Errorf("the older key was touched:\n%s", got)
		}
	})
	t.Run("affinity off", func(t *testing.T) {
		on := edited(t, editYAMLV8, true, func(d *Doc) error { return d.ApplySettings(patch) })
		off := edited(t, on, true, func(d *Doc) error {
			return d.ApplySettings(SettingsPatch{Routing: &RoutingPatch{SessionAffinity: flag(false)}})
		})
		if a := loads(t, off, true).Routing.SessionAffinity; a != nil {
			t.Errorf("affinity = %+v, want none in\n%s", a, off)
		}
	})
	t.Run("invalid values", func(t *testing.T) {
		for name, p := range map[string]RoutingPatch{
			"unknown strategy":  {Strategy: str("random")},
			"negative accounts": {Retry: &RetryPatch{MaxRetryCredentials: num(-1)}},
			"huge interval":     {Retry: &RetryPatch{MaxRetryInterval: num(30*24*60*60 + 1)}},
			"too many rounds":   {Retry: &RetryPatch{RequestRetry: num(1001)}},
			"bad duration":      {SessionAffinityTTL: str("1 hour")},
			"zero duration":     {SessionAffinityTTL: str("0")},
		} {
			d := mustParse(t, editYAMLV8, true)
			if err := d.ApplySettings(SettingsPatch{Routing: &p}); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err = %v, want ErrInvalid", name, err)
			}
		}
	})
}

func TestScrub(t *testing.T) {
	r := NewRedactor()
	long, short := "zzzz-long-secret", "zzzz"
	msg := "bad " + long + " and " + short + " and tiny abc"
	got := r.Scrub(msg, []string{short, long, "abc"})
	want := "bad " + r.Placeholder(long) + " and " + r.Placeholder(short) + " and tiny " + r.Placeholder("abc")
	if got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
	if strings.Contains(got, "long-secret") {
		t.Errorf("a longer secret was left in pieces: %q", got)
	}
	if got := r.Scrub("nothing here", []string{"zzzzzz"}); got != "nothing here" {
		t.Errorf("Scrub changed a message without secrets: %q", got)
	}
	if got := r.Scrub("x", nil); got != "x" {
		t.Errorf("Scrub(nil) = %q", got)
	}
	// However short, a secret is replaced; an empty one is not a secret.
	if got := r.Scrub("is z", []string{"z", ""}); got != "is "+r.Placeholder("z") {
		t.Errorf("Scrub of a 1-character secret = %q", got)
	}
}

// A placeholder is never altered, whatever the other secrets are: a short secret may match the
// placeholder's prefix ("red", "e") or its hex digits ("abc"), and only the message's own text
// may hold a secret. The key is fixed so the hex case is not left to chance.
func TestScrubLeavesPlaceholdersAlone(t *testing.T) {
	r := &Redactor{key: []byte("fixed key for the scrub test....")}
	// A long secret whose placeholder holds "abc" in its hex digits.
	var long string
	for i := 0; ; i++ {
		long = "long-secret-" + strconv.Itoa(i)
		if strings.Contains(r.ID(long), "abc") {
			break
		}
	}
	// The words between the secrets hold none of the short ones.
	for _, shorts := range [][]string{{"abc"}, {"red"}, {"e"}, {"pluxy-redacted-"}, {"abc", "red", "e", "9"}} {
		secrets := append([]string{long}, shorts...)
		msg := "oh " + long + " is"
		want := "oh " + r.Placeholder(long) + " is"
		for _, s := range shorts {
			msg += " so " + s + " us"
			want += " so " + r.Placeholder(s) + " us"
		}
		for range 2 { // in either order
			if got := r.Scrub(msg, secrets); got != want {
				t.Errorf("secrets %q:\nScrub = %q\nwant    %q", secrets, got, want)
			}
			slices.Reverse(secrets)
		}
	}
	// A short secret that occurs many times, beside placeholders that hold it.
	got := r.Scrub("ee "+long+" ee", []string{long, "e"})
	if want := r.Placeholder("e") + r.Placeholder("e") + " " + r.Placeholder(long) + " " + r.Placeholder("e") + r.Placeholder("e"); got != want {
		t.Errorf("Scrub = %q, want %q", got, want)
	}
}

func TestPlaceholderIdentity(t *testing.T) {
	a, b := NewRedactor(), NewRedactor()
	p := a.Placeholder("fake-secret")
	if !placeholderRE.MatchString(p) {
		t.Errorf("placeholder %q has the wrong shape", p)
	}
	if p != a.Placeholder("fake-secret") {
		t.Error("the same secret has two placeholders in one Redactor")
	}
	if p == a.Placeholder("fake-secret-2") {
		t.Error("two secrets share a placeholder")
	}
	if p == b.Placeholder("fake-secret") {
		t.Error("two Redactors give the same placeholder: the ID is not keyed")
	}
	if strings.Contains(p, "fake-secret") {
		t.Errorf("placeholder %q shows the secret", p)
	}
}

// Payload rules from a settings change land in each file's own layout and read back as sent:
// in a config.yaml as block YAML under requests.payload (or the older top-level payload when
// only that is there), in a JSON config as payload with numbers kept; null removes them, and a
// path outside the supported syntax is refused.
func TestApplySettingsPayload(t *testing.T) {
	rules := json.RawMessage(`{"override":[{"models":[{"name":"gpt-*","protocol":"codex"}],"params":{"service_tier":"priority","n":128000,"on":"true"}}],` +
		`"filter":[{"models":[{"name":"gpt-*"}],"params":["tools.#(name==\"apply_patch\")"]}]}`)
	want := payload.Rules{
		Override: []payload.Rule{{Models: []payload.Model{{Name: "gpt-*", Protocol: "codex"}}, Params: map[string]any{"service_tier": "priority", "n": float64(128000), "on": "true"}}},
		Filter:   []payload.FilterRule{{Models: []payload.Model{{Name: "gpt-*"}}, Params: []string{`tools.#(name=="apply_patch")`}}},
	}
	for _, c := range []struct {
		name, src, key string
		isYAML         bool
	}{
		{"json", editJSON, `"payload": {`, false},
		{"v8", editYAMLV8, "requests:\n  payload:\n    override:", true},
		{"older", editYAMLOld + "payload:\n  filter: []\n", "\npayload:\n  override:", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := edited(t, c.src, c.isYAML, func(d *Doc) error { return d.ApplySettings(SettingsPatch{Payload: rules}) })
			if !strings.Contains(got, c.key) {
				t.Errorf("no %q in\n%s", c.key, got)
			}
			if cfg := loads(t, got, c.isYAML); cfg.Payload == nil || !reflect.DeepEqual(*cfg.Payload, want) {
				t.Errorf("payload read back %+v, want %+v, from\n%s", cfg.Payload, want, got)
			}
			removed := edited(t, got, c.isYAML, func(d *Doc) error { return d.ApplySettings(SettingsPatch{Payload: json.RawMessage(`null`)}) })
			if cfg := loads(t, removed, c.isYAML); cfg.Payload != nil || strings.Contains(removed, "payload:") || strings.Contains(removed, `"payload"`) {
				t.Errorf("null left payload rules in\n%s", removed)
			}
		})
	}
	d := mustParse(t, editJSON, false)
	err := d.ApplySettings(SettingsPatch{Payload: json.RawMessage(`{"filter":[{"models":[{"name":"m"}],"params":["tools.*"]}]}`)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `payload.filter[0].params: path "tools.*"`) {
		t.Errorf("a wildcard path: err = %v", err)
	}
	err = d.ApplySettings(SettingsPatch{Payload: json.RawMessage(`{"filters":[]}`)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), `unknown field "filters"`) {
		t.Errorf("a misspelt list: err = %v", err)
	}
}
