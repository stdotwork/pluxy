package e2e

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Session affinity: a JSON config has it on without being asked, a config.yaml keeps today's
// default, off, until it names the setting.

// sessionPost sends a chat request in session (none when empty) and returns the account that
// served it, as the reply names it.
func sessionPost(t *testing.T, base, session string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(plainBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, body)
	}
	account := resp.Header.Get("X-Pluxy-Account")
	if account == "" {
		t.Fatal("the reply names no account")
	}
	return account
}

func okUpstream(t *testing.T) *upstream {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	return u
}

// With no routing section at all, requests of one session stay on one account; another session
// takes the next account in turn.
func TestSessionAffinityIsOnByDefault(t *testing.T) {
	u := okUpstream(t)
	base := startCoreAccounts(t, u, nil, "sk-a", "sk-b", "sk-c")
	first := sessionPost(t, base, "session-1")
	other := sessionPost(t, base, "session-2")
	for range 3 {
		if got := sessionPost(t, base, "session-1"); got != first {
			t.Errorf("session-1 went to %s, want %s again", got, first)
		}
	}
	if got := sessionPost(t, base, "session-2"); got != other || other == first {
		t.Errorf("session-2 went to %s, want %s, which is not session-1's %s", got, other, first)
	}
	want := []string{"sk-a", "sk-b", "sk-a", "sk-a", "sk-a", "sk-b"}
	if got := usedKeys(u); !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// "enabled": false takes the default away: the same session takes turns.
func TestSessionAffinityCanBeTurnedOff(t *testing.T) {
	u := okUpstream(t)
	base := startCoreAccounts(t, u, map[string]any{"session_affinity": map[string]any{"enabled": false}}, "sk-a", "sk-b", "sk-c")
	for range 3 {
		sessionPost(t, base, "session-1")
	}
	if got, want := usedKeys(u), []string{"sk-a", "sk-b", "sk-c"}; !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// A config.yaml that does not name session affinity rotates as today's core does, whatever the
// client's session; with routing.session-affinity on, the session stays.
func TestConfigYAMLKeepsAffinityOffUntilAsked(t *testing.T) {
	for name, c := range map[string]struct {
		routing string
		want    []string
	}{
		"no routing section":      {"", []string{"sk-a", "sk-b", "sk-c"}},
		"routing without the key": {"routing:\n  strategy: round-robin\n", []string{"sk-a", "sk-b", "sk-c"}},
		"session-affinity: false": {"routing:\n  session-affinity: false\n", []string{"sk-a", "sk-b", "sk-c"}},
		"session-affinity: true":  {"routing:\n  session-affinity: true\n", []string{"sk-a", "sk-a", "sk-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			u := okUpstream(t)
			cfg := func(port int) []byte {
				return []byte("host: 127.0.0.1\nport: " + strconv.Itoa(port) + "\nremote-management: {secret-key: " + managementKey + "}\n" + c.routing + `
access:
  api-keys:
    - "` + clientKey + `"
openai-compatibility:
  - name: "local"
    base-url: "` + u.URL + `/v1"
    api-key-entries:
      - api-key: "sk-a"
      - api-key: "sk-b"
      - api-key: "sk-c"
    models:
      - name: "gpt-test"
`)
			}
			bundled := bundledRegistry(t, map[string]string{"pluxy/openai-compatible": "openai-compatible"})
			path := filepath.Join(t.TempDir(), "config.yaml")
			base, _, logs := startOnFreePort(t, filepath.Join(binDir, "pluxy"), path, cfg, "-bundled", bundled)
			logs.waitFor(t, "config revision 1 active")
			installBundled(t, base, "pluxy/openai-compatible")
			for range 3 {
				sessionPost(t, base, "session-1")
			}
			if got := usedKeys(u); !slices.Equal(got, c.want) {
				t.Errorf("upstream keys = %v, want %v", got, c.want)
			}
		})
	}
}
