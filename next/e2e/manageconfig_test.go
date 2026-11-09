package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The management calls for the config file, the client keys, the management key and the
// settings, driven over HTTP against the real core binary.

const secondClientKey = "second-client-key-e2e"

var placeholderText = regexp.MustCompile(`pluxy-redacted-[0-9a-f]{24}`)

// mcore is a running core whose config file the management API edits.
type mcore struct {
	base, path string
	yaml       bool
}

func formats() []struct {
	name string
	yaml bool
} {
	return []struct {
		name string
		yaml bool
	}{{"json", false}, {"yaml", true}}
}

// startManaged runs core with the config file in the given format, one account on a fake
// upstream, the management API on, and these client keys (default: clientKey and
// secondClientKey). The file watcher is off, so only the test's calls publish revisions.
func startManaged(t *testing.T, isYAML bool, keys ...string) *mcore {
	t.Helper()
	u := answeringUpstream(t)
	if len(keys) == 0 {
		keys = []string{clientKey, secondClientKey}
	}
	manifest := filepath.Join(binDir, "pluxy-extension.json")
	dir := t.TempDir()
	c := &mcore{yaml: isYAML}
	var content []byte
	if isYAML {
		c.path = filepath.Join(dir, "config.yaml")
		var b strings.Builder
		b.WriteString("# core config for the e2e test\nserver:\n  host: 127.0.0.1\n  port: " + strconv.Itoa(freePort(t)) + "\naccess:\n  api-keys:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "    - %q\n", k)
		}
		fmt.Fprintf(&b, "management:\n  secret-key: %q\n", managementKey)
		b.WriteString("routing:\n  strategy: round-robin # how accounts are picked\n")
		fmt.Fprintf(&b, "extension:\n  manifest: %q\n  accounts:\n    - id: upstream-1\n      settings:\n        base_url: %q\n      credential:\n        kind: api_key\n        api_key: %q\n", manifest, u.URL+"/v1", upstreamKey)
		content = []byte(b.String())
	} else {
		c.path = filepath.Join(dir, "config.json")
		cfg := map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    keys,
			"management_key": managementKey,
			"routing":        map[string]any{"strategy": "round-robin"},
			"extension": map[string]any{
				"manifest": manifest,
				"accounts": []any{map[string]any{
					"id":         "upstream-1",
					"settings":   map[string]any{"base_url": u.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
				}},
			},
		}
		content, _ = json.MarshalIndent(cfg, "", "  ")
		content = append(content, '\n')
	}
	if err := os.WriteFile(c.path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	c.base, _ = runCoreFile(t, c.path, noWatch...)
	return c
}

// mcall sends a management request with key as the bearer token (none when empty). A body
// that is a string is sent as it is, else as JSON.
func mcall(t *testing.T, base, key, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+"/v0/management"+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if r != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c *mcore) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	return mcall(t, c.base, managementKey, method, path, body)
}

// expect sends a request and fails unless the status is want; it returns the reply body.
func (c *mcore) expect(t *testing.T, want int, method, path string, body any) []byte {
	t.Helper()
	code, b := c.call(t, method, path, body)
	if code != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, code, want, b)
	}
	return b
}

func (c *mcore) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type configReply struct {
	Format  string `json:"format"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

func (c *mcore) config(t *testing.T) configReply {
	t.Helper()
	var r configReply
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/config", nil), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Error.Code == "" {
		t.Fatalf("not an error reply: %s (%v)", body, err)
	}
	return e.Error.Code
}

// chatStatus sends a chat request with key and returns the status.
func chatStatus(t *testing.T, base, key string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp := post(t, ctx, base, key, `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func wantNoSecrets(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s shows %q:\n%s", what, s, text)
		}
	}
}

// unchanged checks, after a call that must change nothing, that the file and the revisions
// are as they were.
type snapshot struct {
	file string
	st   coreStatus
}

func (c *mcore) snap(t *testing.T) snapshot {
	t.Helper()
	return snapshot{c.file(t), status(t, c.base)}
}

func (c *mcore) wantSame(t *testing.T, before snapshot, what string) {
	t.Helper()
	if got := c.file(t); got != before.file {
		t.Errorf("%s changed the config file:\n%s\nwas:\n%s", what, got, before.file)
	}
	st := status(t, c.base)
	if st.Desired != before.st.Desired || st.Active != before.st.Active {
		t.Errorf("%s changed the revisions: desired %+v active %+v, were %+v and %+v", what, st.Desired, st.Active, before.st.Desired, before.st.Active)
	}
}

var managedSecrets = []string{clientKey, secondClientKey, managementKey, upstreamKey}

func TestManageConfigRead(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			r := c.config(t)
			disk := c.file(t)
			wantFormat := "json"
			if f.yaml {
				wantFormat = "yaml"
			}
			if r.Format != wantFormat {
				t.Errorf("format = %q, want %q", r.Format, wantFormat)
			}
			if r.Hash != sha256Of(disk) {
				t.Errorf("hash = %q, want %q", r.Hash, sha256Of(disk))
			}
			wantNoSecrets(t, "GET /config content", r.Content, managedSecrets...)
			if n := len(placeholderText.FindAllString(r.Content, -1)); n != 4 {
				t.Errorf("content holds %d placeholders, want 4 (two client keys, management key, upstream key):\n%s", n, r.Content)
			}
			if f.yaml {
				for _, comment := range []string{"# core config for the e2e test", "# how accounts are picked"} {
					if !strings.Contains(r.Content, comment) {
						t.Errorf("comment %q lost:\n%s", comment, r.Content)
					}
				}
				if len(strings.Split(r.Content, "\n")) != len(strings.Split(disk, "\n")) {
					t.Errorf("line count changed:\n%s", r.Content)
				}
			}
			// The raw reply must not hold a secret in any spelling either.
			_, raw := c.call(t, http.MethodGet, "/config", nil)
			wantNoSecrets(t, "GET /config reply", string(raw), managedSecrets...)
			// The same secret shows the same placeholder in a second read.
			if again := c.config(t); again.Content != r.Content {
				t.Error("two reads of an unchanged file differ")
			}
		})
	}
}

func TestManageConfigNeedsManagementKey(t *testing.T) {
	c := startManaged(t, false)
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/config"}, {http.MethodPost, "/config/validate"}, {http.MethodPut, "/config"},
		{http.MethodGet, "/client-keys"}, {http.MethodPost, "/client-keys"}, {http.MethodDelete, "/client-keys/x"},
		{http.MethodPost, "/management-key/rotate"}, {http.MethodGet, "/settings"}, {http.MethodPatch, "/settings"},
	} {
		for _, key := range []string{"", "wrong-key", clientKey} {
			if code, b := mcall(t, c.base, key, call.method, call.path, `{}`); code != http.StatusUnauthorized {
				t.Errorf("%s %s with key %q: status %d, want 401: %s", call.method, call.path, key, code, b)
			}
		}
	}
	// A rejected call changed nothing: the keys still work.
	if code := chatStatus(t, c.base, clientKey); code != http.StatusOK {
		t.Errorf("chat after rejected calls: %d", code)
	}
}

// edit helpers: the listen setting is "127.0.0.1:0" in the JSON file and a host and port in
// the config.yaml.
func (c *mcore) hostLine() string {
	if c.yaml {
		return "  host: 127.0.0.1\n"
	}
	return `"listen": "127.0.0.1:0"`
}

func (c *mcore) strategyLine(to string) (from, replacement string) {
	if c.yaml {
		return "strategy: round-robin", "strategy: " + to
	}
	return `"strategy": "round-robin"`, `"strategy": "` + to + `"`
}

func TestManageConfigValidate(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			r := c.config(t)
			before := c.snap(t)
			type verdict struct {
				Valid bool   `json:"valid"`
				Error string `json:"error"`
			}
			validate := func(content string) (int, verdict, string) {
				code, b := c.call(t, http.MethodPost, "/config/validate", map[string]string{"content": content})
				var v verdict
				if code == http.StatusOK {
					if err := json.Unmarshal(b, &v); err != nil {
						t.Fatalf("validate reply %s: %v", b, err)
					}
				}
				return code, v, string(b)
			}
			ph := placeholderText.FindString(r.Content)

			t.Run("the document as read is valid", func(t *testing.T) {
				if code, v, raw := validate(r.Content); code != 200 || !v.Valid {
					t.Errorf("status %d: %s", code, raw)
				}
			})
			t.Run("a bad routing value", func(t *testing.T) {
				var bad string
				if f.yaml {
					// A strategy name the file does not know is only a notice there; a value of the
					// wrong type is refused.
					bad = strings.Replace(r.Content, "routing:\n", "routing:\n  retry:\n    max-retry-credentials: [not, a, number]\n", 1)
				} else {
					from, to := c.strategyLine("no-such-strategy")
					bad = replaceOnce(t, r.Content, from, to)
				}
				code, v, raw := validate(bad)
				if code != 200 || v.Valid || v.Error == "" {
					t.Errorf("status %d: %s", code, raw)
				}
			})
			t.Run("a changed listen", func(t *testing.T) {
				var bad string
				if f.yaml {
					bad = replaceOnce(t, r.Content, "  port: "+portOf(t, c.file(t)), "  port: "+strconv.Itoa(freePort(t)))
				} else {
					bad = replaceOnce(t, r.Content, c.hostLine(), `"listen": "127.0.0.1:1"`)
				}
				code, v, raw := validate(bad)
				if code != 200 || v.Valid || !strings.Contains(v.Error, "listen cannot change") {
					t.Errorf("status %d: %s", code, raw)
				}
			})
			t.Run("a placeholder on a non-secret key", func(t *testing.T) {
				to := `"listen": "` + ph + `"`
				if f.yaml {
					to = "  host: " + ph + "\n"
				}
				code, v, raw := validate(replaceOnce(t, r.Content, c.hostLine(), to))
				if code != 200 || v.Valid || v.Error == "" {
					t.Errorf("status %d: %s", code, raw)
				}
			})
			t.Run("a document that does not parse", func(t *testing.T) {
				bad := "{ not json"
				if f.yaml {
					bad = "server: [unclosed\n"
				}
				code, v, raw := validate(bad)
				if code != 200 || v.Valid || v.Error == "" {
					t.Errorf("status %d: %s", code, raw)
				}
			})
			t.Run("an unknown placeholder", func(t *testing.T) {
				unknown := strings.Replace(r.Content, ph, "pluxy-redacted-000000000000000000000000", 1)
				code, b := c.call(t, http.MethodPost, "/config/validate", map[string]string{"content": unknown})
				if code != http.StatusConflict || errorCode(t, b) != "conflict" {
					t.Errorf("status %d: %s", code, b)
				}
			})
			t.Run("the document must be sent", func(t *testing.T) {
				if code, b := c.call(t, http.MethodPost, "/config/validate", `{}`); code != http.StatusBadRequest {
					t.Errorf("status %d: %s", code, b)
				}
				if code, b := c.call(t, http.MethodPost, "/config/validate", `{"content":"x","extra":1}`); code != http.StatusBadRequest {
					t.Errorf("unknown field: status %d: %s", code, b)
				}
			})
			c.wantSame(t, before, "validating")
		})
	}
}

// portOf returns the port in a config.yaml's server section.
func portOf(t *testing.T, file string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^  port: (\d+)$`).FindStringSubmatch(file)
	if m == nil {
		t.Fatalf("no port in\n%s", file)
	}
	return m[1]
}

func TestManageConfigPut(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			r := c.config(t)
			disk := c.file(t)
			before := status(t, c.base)

			from, to := c.strategyLine("fill-first")
			content := replaceOnce(t, r.Content, from, to)
			want := replaceOnce(t, disk, from, to)
			if f.yaml {
				content = replaceOnce(t, content, "# how accounts are picked", "# picked by the test")
				want = replaceOnce(t, want, "# how accounts are picked", "# picked by the test")
			}
			var reply configReply
			if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodPut, "/config", map[string]string{"content": content, "base_hash": r.Hash}), &reply); err != nil {
				t.Fatal(err)
			}

			// The file holds the edit and the real secrets, never a placeholder.
			got := c.file(t)
			if got != want {
				t.Errorf("config file:\n%s\nwant:\n%s", got, want)
			}
			if strings.Contains(got, "pluxy-redacted-") {
				t.Errorf("a placeholder was written to the config file:\n%s", got)
			}
			for _, s := range managedSecrets {
				if !strings.Contains(got, s) {
					t.Errorf("secret %q is not in the config file", s)
				}
			}
			if reply.Hash != sha256Of(got) || reply.Format == "" {
				t.Errorf("reply = %+v, want the new file's hash", reply)
			}
			wantNoSecrets(t, "PUT /config reply", reply.Content, managedSecrets...)

			// A new revision is active, with the file's hash; desired caught up and has no error.
			st := status(t, c.base)
			if st.Active.Revision != before.Active.Revision+1 || st.Active.Hash != sha256Of(got) {
				t.Errorf("active = %+v, was %+v; want the next revision with hash %s", st.Active, before.Active, sha256Of(got))
			}
			if st.Desired.Revision != st.Active.Revision || st.Desired.Hash != st.Active.Hash || st.Desired.Error != "" {
				t.Errorf("desired = %+v, active = %+v", st.Desired, st.Active)
			}

			// The change is live, and the keys the placeholders stood for still work.
			for _, k := range []string{clientKey, secondClientKey} {
				if code := chatStatus(t, c.base, k); code != http.StatusOK {
					t.Errorf("chat with %s after the write: %d", k, code)
				}
			}
			var set struct {
				Routing struct {
					Strategy string `json:"strategy"`
				} `json:"routing"`
			}
			if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/settings", nil), &set); err != nil || set.Routing.Strategy != "fill-first" {
				t.Errorf("settings strategy = %q, %v", set.Routing.Strategy, err)
			}

			// The old hash is stale now.
			after := c.snap(t)
			b := c.expect(t, http.StatusConflict, http.MethodPut, "/config", map[string]string{"content": r.Content, "base_hash": r.Hash})
			if errorCode(t, b) != "conflict" {
				t.Errorf("stale hash: %s", b)
			}
			c.wantSame(t, after, "a write with a stale base_hash")

			// So is a placeholder core does not know.
			fresh := c.config(t)
			unknown := strings.Replace(fresh.Content, placeholderText.FindString(fresh.Content), "pluxy-redacted-000000000000000000000000", 1)
			c.expect(t, http.StatusConflict, http.MethodPut, "/config", map[string]string{"content": unknown, "base_hash": fresh.Hash})
			c.wantSame(t, after, "a write with an unknown placeholder")

			c.expect(t, http.StatusBadRequest, http.MethodPut, "/config", `{"base_hash":"x"}`)
			c.wantSame(t, after, "a write without content")
		})
	}
}

func TestManageConfigPutRefusedDocument(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			r := c.config(t)
			before := c.snap(t)
			manifest := filepath.Join(binDir, "pluxy-extension.json")

			bad := map[string]string{
				"a manifest that does not exist": replaceOnce(t, r.Content, manifest, filepath.Join(binDir, "no-such-extension.json")),
				"a document that does not parse": "{ not json",
			}
			if f.yaml {
				bad["a document that does not parse"] = "server: [unclosed\n"
			}
			for name, content := range bad {
				b := c.expect(t, http.StatusUnprocessableEntity, http.MethodPut, "/config", map[string]string{"content": content, "base_hash": r.Hash})
				if code := errorCode(t, b); code != "refused" {
					t.Errorf("%s: error code %q: %s", name, code, b)
				}
				wantNoSecrets(t, name+": reply", string(b), managedSecrets...)
				c.wantSame(t, before, name)
				if st := status(t, c.base); st.Desired.Error != "" {
					t.Errorf("%s left an error on desired: %+v", name, st.Desired)
				}
			}
			// Core went on serving.
			if code := chatStatus(t, c.base, clientKey); code != http.StatusOK {
				t.Errorf("chat after refused writes: %d", code)
			}
			// And a good write still works afterwards.
			from, to := c.strategyLine("fill-first")
			c.expect(t, http.StatusOK, http.MethodPut, "/config", map[string]string{"content": replaceOnce(t, r.Content, from, to), "base_hash": r.Hash})
		})
	}
}

type keyView struct {
	ID     string `json:"id"`
	Masked string `json:"masked"`
}

func (c *mcore) keys(t *testing.T) []keyView {
	t.Helper()
	var r struct {
		Keys []keyView `json:"keys"`
	}
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/client-keys", nil), &r); err != nil {
		t.Fatal(err)
	}
	return r.Keys
}

// mask is what a key of 8 or more characters shows.
func mask(k string) string { return k[:2] + "******" + k[len(k)-2:] }

func TestManageClientKeys(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml, clientKey, secondClientKey, "short")

			// Listed masked, and never whole.
			_, raw := c.call(t, http.MethodGet, "/client-keys", nil)
			wantNoSecrets(t, "GET /client-keys", string(raw), clientKey, secondClientKey, "short")
			listed := c.keys(t)
			masks := map[string]bool{}
			for _, k := range listed {
				if k.ID == "" {
					t.Errorf("key without an ID: %+v", k)
				}
				masks[k.Masked] = true
			}
			if len(listed) != 3 || !masks[mask(clientKey)] || !masks[mask(secondClientKey)] || !masks["**********"] {
				t.Errorf("keys = %+v, want masks %q, %q and a fully hidden short key", listed, mask(clientKey), mask(secondClientKey))
			}

			// Add one: the value is shown once and works at once.
			rev := status(t, c.base).Active.Revision
			var added struct {
				Key   keyView `json:"key"`
				Value string  `json:"value"`
			}
			if err := json.Unmarshal(c.expect(t, http.StatusCreated, http.MethodPost, "/client-keys", nil), &added); err != nil {
				t.Fatal(err)
			}
			if len(added.Value) < 16 || added.Key.Masked != mask(added.Value) || added.Key.ID == "" {
				t.Errorf("added = %+v", added)
			}
			if !strings.Contains(c.file(t), added.Value) {
				t.Errorf("the new key is not in the config file:\n%s", c.file(t))
			}
			if code := chatStatus(t, c.base, added.Value); code != http.StatusOK {
				t.Errorf("chat with the new key: %d", code)
			}
			if st := status(t, c.base); st.Active.Revision != rev+1 || st.Active.Hash != sha256Of(c.file(t)) {
				t.Errorf("active = %+v after adding, was revision %d", st.Active, rev)
			}
			_, raw = c.call(t, http.MethodGet, "/client-keys", nil)
			wantNoSecrets(t, "GET /client-keys after adding", string(raw), added.Value)
			if len(c.keys(t)) != 4 {
				t.Errorf("keys = %+v, want 4", c.keys(t))
			}
			if f.yaml && (!strings.Contains(c.file(t), "# core config for the e2e test") || !strings.Contains(c.file(t), "# how accounts are picked")) {
				t.Errorf("comments lost:\n%s", c.file(t))
			}
			c.expect(t, http.StatusOK, http.MethodGet, "/config", nil)

			// Remove it: 204, and the key is refused from then on; the others work.
			c.expect(t, http.StatusNoContent, http.MethodDelete, "/client-keys/"+added.Key.ID, nil)
			if code := chatStatus(t, c.base, added.Value); code != http.StatusUnauthorized {
				t.Errorf("chat with the removed key: %d, want 401", code)
			}
			for _, k := range []string{clientKey, secondClientKey, "short"} {
				if code := chatStatus(t, c.base, k); code != http.StatusOK {
					t.Errorf("chat with %s: %d", k, code)
				}
			}
			if strings.Contains(c.file(t), added.Value) {
				t.Error("the removed key is still in the config file")
			}
			if len(c.keys(t)) != 3 {
				t.Errorf("keys = %+v, want 3", c.keys(t))
			}

			// An ID no key has.
			before := c.snap(t)
			b := c.expect(t, http.StatusNotFound, http.MethodDelete, "/client-keys/"+added.Key.ID, nil)
			if errorCode(t, b) != "not_found" {
				t.Errorf("unknown ID: %s", b)
			}
			c.expect(t, http.StatusNotFound, http.MethodDelete, "/client-keys/no-such-id", nil)
			c.wantSame(t, before, "deleting an unknown ID")
		})
	}
}

func TestManageClientKeysKeepsLastKey(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml, clientKey)
			keys := c.keys(t)
			if len(keys) != 1 {
				t.Fatalf("keys = %+v", keys)
			}
			before := c.snap(t)
			b := c.expect(t, http.StatusConflict, http.MethodDelete, "/client-keys/"+keys[0].ID, nil)
			if errorCode(t, b) != "conflict" {
				t.Errorf("last key: %s", b)
			}
			c.wantSame(t, before, "deleting the last key")
			if code := chatStatus(t, c.base, clientKey); code != http.StatusOK {
				t.Errorf("chat with the last key: %d", code)
			}
		})
	}
}

func TestManageManagementKeyRotate(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			var rot struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodPost, "/management-key/rotate", nil), &rot); err != nil {
				t.Fatal(err)
			}
			if len(rot.Value) < 16 || rot.Value == managementKey {
				t.Fatalf("new key = %q", rot.Value)
			}
			if code, _ := mcall(t, c.base, managementKey, http.MethodGet, "/status", nil); code != http.StatusUnauthorized {
				t.Errorf("old key: status %d, want 401", code)
			}
			if code, b := mcall(t, c.base, rot.Value, http.MethodGet, "/status", nil); code != http.StatusOK {
				t.Errorf("new key: status %d: %s", code, b)
			}
			disk := c.file(t)
			if !strings.Contains(disk, rot.Value) || strings.Contains(disk, managementKey) {
				t.Errorf("config file after rotation:\n%s", disk)
			}
			if f.yaml && (strings.Count(disk, "secret-key:") != 1 || !strings.Contains(disk, "# how accounts are picked")) {
				t.Errorf("config file after rotation:\n%s", disk)
			}
			// Client keys are not the management key, and still work.
			if code := chatStatus(t, c.base, clientKey); code != http.StatusOK {
				t.Errorf("chat after rotation: %d", code)
			}
			// The redaction follows the new key.
			code, b := mcall(t, c.base, rot.Value, http.MethodGet, "/config", nil)
			if code != http.StatusOK {
				t.Fatalf("GET /config: %d %s", code, b)
			}
			wantNoSecrets(t, "GET /config after rotation", string(b), rot.Value)
		})
	}
}

type settingsReply struct {
	Routing struct {
		Strategy           string `json:"strategy"`
		SessionAffinity    bool   `json:"session-affinity"`
		SessionAffinityTTL string `json:"session-affinity-ttl"`
		ForceModelPrefix   bool   `json:"force-model-prefix"`
		Retry              struct {
			MaxRetryCredentials int     `json:"max-retry-credentials"`
			MaxRetryInterval    float64 `json:"max-retry-interval"`
		} `json:"retry"`
	} `json:"routing"`
}

func (c *mcore) settings(t *testing.T) settingsReply {
	t.Helper()
	var s settingsReply
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/settings", nil), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestManageSettings(t *testing.T) {
	const patch = `{"routing": {"strategy": "fill-first", "session-affinity": true, "session-affinity-ttl": "2h", "force-model-prefix": true, "retry": {"max-retry-credentials": 2, "max-retry-interval": 5}}}`
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			s := c.settings(t)
			wantWait := 0.0
			if f.yaml {
				wantWait = 30 // today's default wait, which a config.yaml gets when it sets none
			}
			if s.Routing.Strategy != "round-robin" || s.Routing.SessionAffinity || s.Routing.SessionAffinityTTL != "1h" ||
				s.Routing.Retry.MaxRetryCredentials != 0 || s.Routing.Retry.MaxRetryInterval != wantWait {
				t.Errorf("settings = %+v", s)
			}

			rev := status(t, c.base).Active.Revision
			var reply settingsReply
			if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodPatch, "/settings", patch), &reply); err != nil {
				t.Fatal(err)
			}
			for name, got := range map[string]settingsReply{"reply": reply, "a following GET": c.settings(t)} {
				r := got.Routing
				if r.Strategy != "fill-first" || !r.SessionAffinity || r.SessionAffinityTTL != "2h" || !r.ForceModelPrefix || r.Retry.MaxRetryCredentials != 2 || r.Retry.MaxRetryInterval != 5 {
					t.Errorf("%s = %+v", name, got)
				}
			}
			if st := status(t, c.base); st.Active.Revision != rev+1 || st.Active.Hash != sha256Of(c.file(t)) {
				t.Errorf("active = %+v after the patch, was revision %d", st.Active, rev)
			}

			// The file holds them under its own names.
			if f.yaml {
				var doc struct {
					Routing struct {
						Strategy string         `yaml:"strategy"`
						Affinity bool           `yaml:"session-affinity"`
						TTL      string         `yaml:"session-affinity-ttl"`
						Force    bool           `yaml:"force-model-prefix"`
						Retry    map[string]int `yaml:"retry"`
					} `yaml:"routing"`
				}
				if err := yaml.Unmarshal([]byte(c.file(t)), &doc); err != nil {
					t.Fatal(err)
				}
				r := doc.Routing
				if r.Strategy != "fill-first" || !r.Affinity || r.TTL != "2h" || !r.Force || r.Retry["max-retry-credentials"] != 2 || r.Retry["max-retry-interval"] != 5 {
					t.Errorf("config.yaml routing = %+v in\n%s", r, c.file(t))
				}
				for _, comment := range []string{"# core config for the e2e test", "# how accounts are picked"} {
					if !strings.Contains(c.file(t), comment) {
						t.Errorf("comment %q lost:\n%s", comment, c.file(t))
					}
				}
			} else {
				var doc struct {
					Routing struct {
						Strategy string `json:"strategy"`
						Affinity struct {
							TTLMS int64 `json:"ttl_ms"`
						} `json:"session_affinity"`
						MaxAccounts int   `json:"max_accounts"`
						MaxWaitMS   int64 `json:"max_wait_ms"`
						Force       bool  `json:"force_model_prefix"`
					} `json:"routing"`
				}
				if err := json.Unmarshal([]byte(c.file(t)), &doc); err != nil {
					t.Fatal(err)
				}
				r := doc.Routing
				if r.Strategy != "fill-first" || !r.Force || r.Affinity.TTLMS != 7200000 || r.MaxAccounts != 2 || r.MaxWaitMS != 5000 {
					t.Errorf("config.json routing = %+v in\n%s", r, c.file(t))
				}
			}

			// Weighted round robin is written by the file's own name for it, and read back by ours.
			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"routing": {"strategy": "weighted-round-robin"}}`)
			if got := c.settings(t).Routing.Strategy; got != "weighted-round-robin" {
				t.Errorf("strategy = %q", got)
			}
			wantInFile := `"strategy": "weighted"`
			if f.yaml {
				wantInFile = "strategy: weighted-round-robin"
			}
			if !strings.Contains(c.file(t), wantInFile) {
				t.Errorf("file lacks %q:\n%s", wantInFile, c.file(t))
			}

			// Turned off again.
			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"routing": {"session-affinity": false}}`)
			if c.settings(t).Routing.SessionAffinity {
				t.Error("session affinity is still on")
			}
		})
	}
}

func TestManageSettingsRefusals(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			before := c.snap(t)
			for name, body := range map[string]string{
				"an unknown field":            `{"nope": 1}`,
				"an unknown routing field":    `{"routing": {"nope": 1}}`,
				"an unknown retry field":      `{"routing": {"retry": {"nope": 1}}}`,
				"a value of the wrong type":   `{"routing": {"strategy": 5}}`,
				"not JSON":                    `{"routing": `,
				"an unknown strategy":         `{"routing": {"strategy": "random"}}`,
				"negative credentials":        `{"routing": {"retry": {"max-retry-credentials": -1}}}`,
				"a negative interval":         `{"routing": {"retry": {"max-retry-interval": -5}}}`,
				"an interval over 30 days":    `{"routing": {"retry": {"max-retry-interval": 2592001}}}`,
				"a bad duration":              `{"routing": {"session-affinity": true, "session-affinity-ttl": "soon"}}`,
				"a duration of zero":          `{"routing": {"session-affinity": true, "session-affinity-ttl": "0s"}}`,
				"a valid field next to a bad": `{"routing": {"strategy": "fill-first", "retry": {"max-retry-credentials": -1}}}`,
			} {
				code, b := c.call(t, http.MethodPatch, "/settings", body)
				if code != http.StatusBadRequest || errorCode(t, b) != "invalid_request" {
					t.Errorf("%s: status %d: %s", name, code, b)
				}
				c.wantSame(t, before, name)
			}
			if !f.yaml {
				// In the JSON config the ttl lives in session_affinity, so it needs affinity on.
				code, b := c.call(t, http.MethodPatch, "/settings", `{"routing": {"session-affinity-ttl": "2h"}}`)
				if code != http.StatusBadRequest {
					t.Errorf("ttl with affinity off: status %d: %s", code, b)
				}
				c.wantSame(t, before, "a ttl with affinity off")
			}
			if s := c.settings(t); s.Routing.Strategy != "round-robin" {
				t.Errorf("settings changed: %+v", s)
			}
		})
	}
}
