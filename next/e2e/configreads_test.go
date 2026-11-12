package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A config whose secret below a secret key is also written in plain elsewhere (a 3 below an
// account's client_secret and a quarantine count of 3) is written by PUT /config, shown by its
// reply and by GET /config with the secret hidden and the plain 3 kept.
func TestManageConfigSecretValueAlsoPlain(t *testing.T) {
	const nested = "fake-nested-secret-1"
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			r := c.config(t)
			var content, plain3 string
			if f.yaml {
				content = r.Content + "request-retry: 3\nbedrock-api-key:\n  - api-key: " + nested + "\n    priority: 3\n"
				plain3 = "request-retry: 3\n"
			} else {
				var doc map[string]any
				if err := json.Unmarshal([]byte(r.Content), &doc); err != nil {
					t.Fatal(err)
				}
				ext := doc["extension"].(map[string]any)
				ext["restart"] = map[string]any{"quarantine_after": 3}
				account := ext["accounts"].([]any)[0].(map[string]any)
				account["settings"].(map[string]any)["client_secret"] = map[string]any{"value": nested, "rotate_after_days": 3}
				b, _ := json.MarshalIndent(doc, "", "  ")
				content, plain3 = string(b), `"quarantine_after": 3`
			}
			code, body := c.call(t, http.MethodPut, "/config", map[string]string{"content": content, "base_hash": r.Hash})
			if code != http.StatusOK {
				t.Fatalf("PUT /config: %d %s", code, body)
			}
			if !strings.Contains(c.file(t), nested) {
				t.Fatalf("the config file lacks the written secret:\n%s", c.file(t))
			}
			var reply configReply
			if err := json.Unmarshal(body, &reply); err != nil {
				t.Fatal(err)
			}
			for what, text := range map[string]string{"PUT /config reply": reply.Content, "GET /config": c.config(t).Content} {
				wantNoSecrets(t, what, text, append(managedSecrets, nested)...)
				if !strings.Contains(text, plain3) {
					t.Errorf("%s lacks %q:\n%s", what, plain3, text)
				}
			}
		})
	}
}
