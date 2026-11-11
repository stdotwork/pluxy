package e2e

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Grant settings supply defaults on creation; explicit caller properties win. A later login
// cannot change the owner's settings. Inspect the next call as the extension receives it.
func TestLoginGrantSettings(t *testing.T) {
	pkg := testPackage(t, "0.1.0")
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension":       map[string]any{"manifest": filepath.Join(pkg, "pluxy-extension.json"), "accounts": []any{}},
	})
	finish := func(path string, body map[string]any) {
		t.Helper()
		var l loginReply
		if code := manage(t, base, "POST", path, body, &l); code != http.StatusAccepted {
			t.Fatalf("%s: status %d", path, code)
		}
		deadline := time.Now().Add(10 * time.Second)
		for l.Login.State == "pending" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			manage(t, base, "GET", "/logins/"+l.Login.ID, nil, &l)
		}
		if l.Login.State != "done" {
			t.Fatalf("login: %+v", l.Login)
		}
	}
	grant := map[string]any{"base_url": "https://example.com/v1", "wire_api": "chat", "nested": map[string]any{"grant": true}, "nullable": "default"}
	raw, _ := json.Marshal(grant)
	finish("/accounts", map[string]any{"id": "defaults", "method": "settings",
		"input":    map[string]string{"settings": string(raw)},
		"settings": map[string]any{"wire_api": "responses", "nested": map[string]any{"owner": true}, "nullable": nil},
	})
	want := map[string]any{"base_url": "https://example.com/v1", "wire_api": "responses", "nested": map[string]any{"owner": true}, "nullable": nil}
	check := func() {
		t.Helper()
		var reply struct {
			Choices []struct{ Message struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(askOK(t, base, "account-settings")), &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.Choices) != 1 {
			t.Fatal("missing reply")
		}
		var got map[string]any
		content := strings.TrimSuffix(reply.Choices[0].Message.Content, " done")
		if err := json.Unmarshal([]byte(content), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("settings = %v, want %v", got, want)
		}
	}
	check()
	grant["base_url"] = "https://other.example/v1"
	raw, _ = json.Marshal(grant)
	finish("/accounts/defaults/login", map[string]any{"method": "settings", "input": map[string]string{"settings": string(raw)}})
	check()
}

func TestLoginGrantInvalidCallerSettings(t *testing.T) {
	pkg := testPackage(t, "0.1.0")
	base := runCore(t, map[string]any{
		"listen": "127.0.0.1:0", "client_keys": []string{clientKey}, "management_key": managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension":       map[string]any{"manifest": filepath.Join(pkg, "pluxy-extension.json"), "accounts": []any{}},
	})
	for _, value := range []any{[]any{}, "value", 42, true} {
		var raw json.RawMessage
		code := manage(t, base, "POST", "/accounts", map[string]any{"id": "invalid", "method": "settings", "settings": value, "input": map[string]string{"settings": "{}"}}, &raw)
		var reply struct{ Error struct{ Code string } }
		json.Unmarshal(raw, &reply)
		if code != http.StatusBadRequest || reply.Error.Code != "invalid_request" {
			t.Errorf("settings %v: status %d, %s", value, code, raw)
		}
	}
}
