package configdoc

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The secrets of the placement documents. In a document below, <name> stands for the secret
// written as a quoted string: the config file as it is now holds the secret there, and a document
// sent back holds its placeholder.
var placementSecrets = map[string]string{
	"c1":     "fake-client-1",
	"c2":     "fake-client-2",
	"mgmt":   "fake-management",
	"reg1":   "https://registry.example/one.json?token=fake-reg-1",
	"reg2":   "https://registry.example/two.json?token=fake-reg-2",
	"proxyA": "http://user:fake-proxy-a@proxy.example:8080",
	"a":      "fake-key-a",
	"b":      "fake-key-b",
	"authB":  "Bearer fake-auth-b",
	"k1":     "fake-entry-1",
	"k2":     "fake-entry-2",
	"k3":     "fake-entry-3",
}

// placement returns doc with each <name> replaced by its secret, or by its placeholder as a
// redacted document shows it: in YAML, without the quotes.
func placement(r *Redactor, doc string, placeholders, isYAML bool) string {
	for name, s := range placementSecrets {
		if placeholders && isYAML {
			doc = strings.ReplaceAll(doc, `"<`+name+`>"`, r.Placeholder(s))
		} else if placeholders {
			s = r.Placeholder(s)
		}
		doc = strings.ReplaceAll(doc, "<"+name+">", s)
	}
	return doc
}

// sameDoc reports whether two documents are the same text or, in YAML, read the same to core's
// loader: a document with an anchored secret is written out again, not edited in place (see
// Doc.span), which may change its quotes.
func sameDoc(got, want string, isYAML bool) bool {
	if got == want {
		return true
	}
	var g, w any
	return isYAML && strings.Contains(want, "&") && yaml.Unmarshal([]byte(got), &g) == nil &&
		yaml.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
}

// Config files of each layout, as they are now.
const (
	placeJSON = `{
  "client_keys": ["<c1>", "<c2>"],
  "management_key": "<mgmt>",
  "packages": {"registries": [{"name": "one", "url": "<reg1>"}, {"name": "two", "url": "<reg2>"}]},
  "extension": {"accounts": [
    {"id": "a", "proxy_url": "<proxyA>", "credential": {"kind": "api_key", "api_key": "<a>"}},
    {"id": "b", "headers": {"Authorization": "<authB>"}, "credential": {"kind": "api_key", "api_key": "<b>"}}
  ]}
}
`
	placeYAML = `management:
  secret-key: "<mgmt>"
extension:
  accounts:
    - id: a
      credential:
        kind: api_key
        api_key: "<a>"
    - id: b
      credential:
        kind: api_key
        api_key: "<b>"
`
	// Today's config.yaml, older layout: provider entries without ids.
	placeOld = `remote-management:
  secret-key: "<mgmt>"
codex-api-key:
  - api-key: "<k1>"
    base-url: "https://one.example/v1"
  - api-key: "<k2>"
    base-url: "https://two.example/v1"
claude-api-key:
  - api-key: "<k3>"
`
	// Today's config.yaml, v8 layout: named provider entries, each with a list of keys.
	placeV8 = `management:
  secret-key: "<mgmt>"
api-keys:
  codex:
    - name: one
      base-url: "https://one.example/v1"
      keys:
        - api-key: "<k1>"
        - api-key: "<k2>"
    - name: two
      base-url: "https://two.example/v1"
      keys:
        - api-key: "<k3>"
`
	// Two provider entries with the same key; only the first has a secret header.
	placeShared = `codex-api-key:
  - api-key: "<k1>"
    base-url: "https://two.example/v1"
    headers:
      Authorization: "<authB>"
  - api-key: "<k1>"
    base-url: "https://one.example/v1"
`
	// Each account also writes its key under a plain key: a copy, hidden and put back as one.
	placeCopy = `{
  "extension": {"accounts": [
    {"id": "a", "note": "<a>", "credential": {"kind": "api_key", "api_key": "<a>"}},
    {"id": "b", "note": "<b>", "credential": {"kind": "api_key", "api_key": "<b>"}}
  ]}
}
`
	placeCompat = `openai-compatibility:
  - name: one
    base-url: "https://one.example/v1"
    api-key-entries:
      - api-key: "<k1>"
  - name: two
    base-url: "https://two.example/v1"
    api-key-entries:
      - api-key: "<k2>"
`
	// Two named provider entries with the same name.
	placeSameName = `api-keys:
  codex:
    - name: one
      base-url: "https://one.example/v1"
      keys:
        - api-key: "<k1>"
    - name: one
      base-url: "https://two.example/v1"
      keys:
        - api-key: "<k2>"
`
	// Account a's key is anchored and aliased into its own header, and account c merges a in:
	// core reads a's key in each of those four places.
	placeAliased = `management:
  secret-key: "<mgmt>"
extension:
  accounts:
    - &A
      id: a
      credential:
        kind: api_key
        api_key: &ka "<a>"
      headers:
        X-Api-Key: *ka
    - <<: *A
      id: c
      settings:
        base_url: "https://c.example/v1"
    - id: b
      credential:
        kind: api_key
        api_key: "<b>"
`
)

// In a config file, a placeholder goes back only where its own secret is in the file as it is
// now: the same account (by its id, wherever it is in the list), the same provider entry (by its
// name, else by the secrets it holds), the same list, the same keys. An edit that keeps each
// placeholder there is taken: accounts and entries reordered or renamed, list items reordered.
func TestRestorePlacesEachSecretInItsOwnSlot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		isYAML  bool
		edits   []string // old and new text, in pairs, applied to current to make the document sent back
		line    int      // the line a refusal names; 0: the document is taken
	}{
		{"json: the management key in an account's api_key", placeJSON, false,
			[]string{`"api_key": "<b>"`, `"api_key": "<mgmt>"`}, 7},
		{"json: another account's key in an account's api_key", placeJSON, false,
			[]string{`"api_key": "<b>"`, `"api_key": "<a>"`}, 7},
		{"json: a client key in an account's Authorization header", placeJSON, false,
			[]string{`"Authorization": "<authB>"`, `"Authorization": "<c1>"`}, 7},
		{"json: another account's key in a new header", placeJSON, false,
			[]string{`"Authorization": "<authB>"}`, `"Authorization": "<authB>", "X-Api-Key": "<a>"}`}, 7},
		{"json: two accounts' keys swapped", placeJSON, false,
			[]string{`"api_key": "<a>"`, `"api_key": "<x>"`, `"api_key": "<b>"`, `"api_key": "<a>"`, `"api_key": "<x>"`, `"api_key": "<b>"`}, 6},
		{"json: an account removed, its key in the other", placeJSON, false,
			[]string{"    {\"id\": \"a\", \"proxy_url\": \"<proxyA>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}},\n", "",
				`"api_key": "<b>"}}`, `"api_key": "<a>"}}`}, 6},
		{"json: a new account with another's key", placeJSON, false,
			[]string{"\n  ]}", ",\n    {\"id\": \"c\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}}\n  ]}"}, 8},
		{"json: a renamed account with the key of one still there", placeJSON, false,
			[]string{`{"id": "b", "headers": {"Authorization": "<authB>"}, "credential": {"kind": "api_key", "api_key": "<b>"}}`,
				`{"id": "b2", "credential": {"kind": "api_key", "api_key": "<a>"}}`}, 7},
		{"json: a client key in the management key's place", placeJSON, false,
			[]string{`"management_key": "<mgmt>"`, `"management_key": "<c1>"`}, 3},
		{"json: the management key among the client keys", placeJSON, false,
			[]string{`["<c1>", "<c2>"]`, `["<c1>", "<c2>", "<mgmt>"]`}, 2},
		{"json: a registry URL in a name", placeJSON, false,
			[]string{`"name": "one"`, `"name": "<reg1>"`}, 4},
		{"json: an account's proxy URL in another account", placeJSON, false,
			[]string{`{"id": "b", `, `{"id": "b", "proxy_url": "<proxyA>", `}, 7},
		{"json: an account's proxy URL at the top", placeJSON, false,
			[]string{`"management_key"`, `"proxy_url": "<proxyA>", "management_key"`}, 3},

		{"json: accounts reordered", placeJSON, false,
			[]string{
				"    {\"id\": \"a\", \"proxy_url\": \"<proxyA>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}},\n", "",
				`"api_key": "<b>"}}`, "\"api_key\": \"<b>\"}},\n    {\"id\": \"a\", \"proxy_url\": \"<proxyA>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}}",
			}, 0},
		{"json: an account renamed", placeJSON, false, []string{`"id": "b"`, `"id": "b2"`}, 0},
		{"json: an account renamed and the other removed", placeJSON, false,
			[]string{"    {\"id\": \"a\", \"proxy_url\": \"<proxyA>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}},\n", "",
				`"id": "b"`, `"id": "b2"`}, 0},
		// An account whose secrets are not those of the account with its id is paired by its
		// secrets: it is that account, renamed.
		{"json: an account removed and the other renamed to its id", placeJSON, false,
			[]string{"    {\"id\": \"a\", \"proxy_url\": \"<proxyA>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}},\n", "",
				`"id": "b"`, `"id": "a"`}, 0},
		{"json: two accounts' ids swapped", placeJSON, false,
			[]string{`"id": "a"`, `"id": "x"`, `"id": "b"`, `"id": "a"`, `"id": "x"`, `"id": "b"`}, 0},
		// Core's JSON reader takes the last of two keys that are the same name in any case, so
		// an item with a placeholder may not have its id or name twice.
		{"json: an account with its id twice", placeJSON, false,
			[]string{`{"id": "a", `, `{"id": "a", "id": "zz", `}, 6},
		{"json: an account with its id twice, in another case", placeJSON, false,
			[]string{`{"id": "a", `, `{"id": "a", "ID": "zz", `}, 6},
		{"yaml: an account with its id twice, in another case", placeYAML, true,
			[]string{"    - id: a\n", "    - id: a\n      ID: zz\n"}, 6},
		{"json: client keys reordered", placeJSON, false, []string{`["<c1>", "<c2>"]`, `["<c2>", "<c1>"]`}, 0},
		{"json: registries reordered", placeJSON, false,
			[]string{`[{"name": "one", "url": "<reg1>"}, {"name": "two", "url": "<reg2>"}]`, `[{"name": "two", "url": "<reg2>"}, {"name": "one", "url": "<reg1>"}]`}, 0},

		{"json: accounts with copies reordered", placeCopy, false,
			[]string{
				"    {\"id\": \"a\", \"note\": \"<a>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}},\n", "",
				`"api_key": "<b>"}}`, "\"api_key\": \"<b>\"}},\n    {\"id\": \"a\", \"note\": \"<a>\", \"credential\": {\"kind\": \"api_key\", \"api_key\": \"<a>\"}}",
			}, 0},

		{"yaml: another account's key in an account's api_key", placeYAML, true,
			[]string{`api_key: "<b>"`, `api_key: "<a>"`}, 12},
		{"yaml: the management key in an account's api_key", placeYAML, true,
			[]string{`api_key: "<b>"`, `api_key: "<mgmt>"`}, 12},
		{"yaml: accounts reordered", placeYAML, true,
			[]string{"    - id: a\n      credential:\n        kind: api_key\n        api_key: \"<a>\"\n", "",
				"api_key: \"<b>\"\n", "api_key: \"<b>\"\n    - id: a\n      credential:\n        kind: api_key\n        api_key: \"<a>\"\n"}, 0},

		{"config.yaml: a provider entry's key copied to another entry", placeOld, true,
			[]string{`api-key: "<k2>"`, `api-key: "<k1>"`}, 6},
		{"config.yaml: a provider key in another section", placeOld, true,
			[]string{`api-key: "<k3>"`, `api-key: "<k1>"`}, 9},
		{"config.yaml: the management key in a provider entry", placeOld, true,
			[]string{`api-key: "<k2>"`, `api-key: "<mgmt>"`}, 6},
		{"config.yaml: provider entries reordered", placeOld, true,
			[]string{
				"  - api-key: \"<k1>\"\n    base-url: \"https://one.example/v1\"\n", "",
				"    base-url: \"https://two.example/v1\"\n", "    base-url: \"https://two.example/v1\"\n  - api-key: \"<k1>\"\n    base-url: \"https://one.example/v1\"\n",
			}, 0},

		// The entry without the header could stand for either entry; it stands for its own, so
		// the one with the header still has its own.
		{"config.yaml: entries that share a key reordered", placeShared, true,
			[]string{
				"  - api-key: \"<k1>\"\n    base-url: \"https://one.example/v1\"\n", "",
				"codex-api-key:\n", "codex-api-key:\n  - api-key: \"<k1>\"\n    base-url: \"https://one.example/v1\"\n",
			}, 0},
		{"config.yaml v8: keys swapped between named entries", placeV8, true,
			[]string{`api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k3>"`, `api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k3>"`}, 8},
		{"config.yaml v8: a key moved to another named entry", placeV8, true,
			[]string{"        - api-key: \"<k1>\"\n", "", `- api-key: "<k3>"`, "- api-key: \"<k3>\"\n        - api-key: \"<k1>\""}, 13},
		{"config.yaml v8: named entries reordered", placeV8, true,
			[]string{
				"    - name: one\n      base-url: \"https://one.example/v1\"\n      keys:\n        - api-key: \"<k1>\"\n        - api-key: \"<k2>\"\n", "",
				"        - api-key: \"<k3>\"\n", "        - api-key: \"<k3>\"\n    - name: one\n      base-url: \"https://one.example/v1\"\n      keys:\n        - api-key: \"<k1>\"\n        - api-key: \"<k2>\"\n",
			}, 0},
		{"config.yaml v8: an entry's keys reordered", placeV8, true,
			[]string{`api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k2>"`, `api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k2>"`}, 0},
		{"config.yaml v8: a named entry renamed", placeV8, true, []string{"name: two", "name: three"}, 0},
		{"config.yaml v8: two entries with the same name reordered", placeSameName, true,
			[]string{
				"    - name: one\n      base-url: \"https://one.example/v1\"\n      keys:\n        - api-key: \"<k1>\"\n", "",
				"        - api-key: \"<k2>\"\n", "        - api-key: \"<k2>\"\n    - name: one\n      base-url: \"https://one.example/v1\"\n      keys:\n        - api-key: \"<k1>\"\n",
			}, 0},

		// An item whose secrets are not those of the item with its id or name is paired by its
		// secrets, so these are taken: each is the same document as two items' ids or names
		// swapped, or an item removed and the other renamed to its id, and each secret stays in
		// the item that held it. The openai-compatibility entries' keys then go with each
		// other's base-url: the known limit of a key kept at a new address.
		{"json: two registries' URLs swapped", placeJSON, false,
			[]string{`"<reg1>"`, `"<x>"`, `"<reg2>"`, `"<reg1>"`, `"<x>"`, `"<reg2>"`}, 0},
		{"yaml: two accounts' keys swapped", placeYAML, true,
			[]string{`api_key: "<a>"`, `api_key: "<x>"`, `api_key: "<b>"`, `api_key: "<a>"`, `api_key: "<x>"`, `api_key: "<b>"`}, 0},
		{"yaml: an account removed, its key in the other", placeYAML, true,
			[]string{"    - id: a\n      credential:\n        kind: api_key\n        api_key: \"<a>\"\n", "",
				`api_key: "<b>"`, `api_key: "<a>"`}, 0},
		{"config.yaml: keys swapped between named openai-compatibility entries", placeCompat, true,
			[]string{`api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k2>"`, `api-key: "<k1>"`, `api-key: "<x>"`, `api-key: "<k2>"`}, 0},

		// In YAML, a secret is where core reads it, with aliases and merge keys resolved as core's
		// loader resolves them: a refusal names the alias or the merge key.
		{"yaml: another account's key, by an alias", placeYAML, true,
			[]string{`api_key: "<a>"`, `api_key: &ka "<a>"`, `api_key: "<b>"`, `api_key: *ka`}, 12},
		{"yaml: the management key in an account's header, by an alias", placeYAML, true,
			[]string{`secret-key: "<mgmt>"`, `secret-key: &mk "<mgmt>"`, "    - id: b\n", "    - id: b\n      headers:\n        X-Api-Key: *mk\n"}, 11},
		{"yaml: an account merged into a new account", placeYAML, true,
			[]string{"    - id: a\n", "    - &A\n      id: a\n",
				"api_key: \"<b>\"\n", "api_key: \"<b>\"\n    - <<: *A\n      id: c\n      settings:\n        base_url: \"https://c.example/v1\"\n"}, 14},
		{"config.yaml: a provider key in another section, by an alias", placeOld, true,
			[]string{`api-key: "<k1>"`, `api-key: &k1 "<k1>"`, `api-key: "<k3>"`, `api-key: *k1`}, 9},
		{"config.yaml: a provider entry in another section, by an alias", placeOld, true,
			[]string{"  - api-key: \"<k1>\"\n", "  - &E\n    api-key: \"<k1>\"\n", "  - api-key: \"<k3>\"\n", "  - *E\n"}, 10},
		{"yaml: a file that aliases a key, sent back as it is", placeAliased, true, nil, 0},
		{"yaml: a file that aliases a key, its accounts reordered", placeAliased, true,
			[]string{"    - id: b\n      credential:\n        kind: api_key\n        api_key: \"<b>\"\n", "",
				"  accounts:\n", "  accounts:\n    - id: b\n      credential:\n        kind: api_key\n        api_key: \"<b>\"\n"}, 0},
		{"yaml: a file's aliased key in another account's header", placeAliased, true,
			[]string{"    - id: b\n", "    - id: b\n      headers:\n        X-Api-Key: *ka\n"}, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRedactor()
			edited := tc.current
			for i := 0; i+1 < len(tc.edits); i += 2 {
				if !strings.Contains(edited, tc.edits[i]) {
					t.Fatalf("edit %d: no %q in\n%s", i/2, tc.edits[i], edited)
				}
				edited = strings.Replace(edited, tc.edits[i], tc.edits[i+1], 1)
			}
			current := placement(r, tc.current, false, tc.isYAML)
			// The document sent back is what GET /config shows, edited.
			if red, err := r.Redact([]byte(current), tc.isYAML); err != nil || !sameDoc(string(red), placement(r, tc.current, true, tc.isYAML), tc.isYAML) {
				t.Fatalf("redacted = %s, %v; want\n%s", red, err, placement(r, tc.current, true, tc.isYAML))
			}
			out, err := r.Restore([]byte(placement(r, edited, true, tc.isYAML)), []byte(current), tc.isYAML)
			if tc.line == 0 {
				if want := placement(r, edited, false, tc.isYAML); err != nil || !sameDoc(string(out), want, tc.isYAML) {
					t.Errorf("Restore = %s, %v; want\n%s", out, err, want)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrStale) {
				t.Fatalf("Restore = %s, %v; want ErrInvalid only", out, err)
			}
			if msg := err.Error(); !strings.Contains(msg, "line "+strconv.Itoa(tc.line)+":") || !strings.Contains(msg, "write the value itself") {
				t.Errorf("error %q: want it to name line %d and say to write the value itself", msg, tc.line)
			}
		})
	}
}
