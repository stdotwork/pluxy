package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// GET /setup/clients/{id}/paste against a real core, and pluxy client show through it.
func TestClientPaste(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	t.Parallel()
	home := t.TempDir()
	env := isolatedSetupEnv(home, t.TempDir())
	base, client, _ := startSetupCore(t, env, filepath.Join(t.TempDir(), "config.json"), "")
	ctx := context.Background()
	paste := "/setup/clients/%s/paste"

	// No reply holds a client key's value or the management key.
	secretFree := func(what string, body []byte) {
		t.Helper()
		for _, secret := range []string{managementKey, clientKey} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s holds a key's value: %s", what, body)
			}
		}
	}

	for _, tool := range []string{"claude-code", "codex", "opencode", "pi", "omp"} {
		status, body := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, tool), nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %s", tool, status, body)
		}
		secretFree(tool, body)
		var p mgmt.ClientPaste
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, body)
		}
		if p.Tool != tool || p.Name == "" || p.BaseURL != base || p.Key != "<client-key>" || len(p.Files) == 0 {
			t.Errorf("%s: reply = %+v", tool, p)
		}
		for _, f := range p.Files {
			if f.Path == "" || f.Content == "" {
				t.Errorf("%s: empty file %+v", tool, f)
			}
		}
		if !strings.Contains(p.Files[0].Content, base) || !strings.Contains(p.Files[0].Content, "<client-key>") {
			t.Errorf("%s: first file does not point at core with the placeholder:\n%s", tool, p.Files[0].Content)
		}
	}

	// Other names for a tool give its canonical ID.
	for name, want := range map[string]string{"oh-my-pi": "omp", "Codex-CLI": "codex", "pluxy/pi-setup": "pi"} {
		p, err := client.ClientPaste(ctx, name, "")
		if err != nil || p.Tool != want {
			t.Errorf("ClientPaste(%q) = %q, %v; want %q", name, p.Tool, err, want)
		}
	}

	// key_id: a key core holds names it by ID and masked form; one it does not hold is a 404.
	keys, err := client.ClientKeys(ctx)
	if err != nil || len(keys) == 0 {
		t.Fatalf("client keys: %v %v", keys, err)
	}
	status, body := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, "claude-code")+"?key_id="+keys[0].ID, nil)
	secretFree("key_id", body)
	var p mgmt.ClientPaste
	want := fmt.Sprintf("<client-key:%s (%s)>", keys[0].ID, keys[0].Masked)
	if err := json.Unmarshal(body, &p); status != http.StatusOK || err != nil || p.Key != want || !strings.Contains(p.Files[0].Content, want) {
		t.Errorf("key_id of a held key: %d, key %q, want %q: %s", status, p.Key, want, body)
	}
	var apiErr *mgmt.APIError
	if _, err := client.ClientPaste(ctx, "claude-code", "no-such-key"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Errorf("key_id of a key core does not hold: %v", err)
	}
	apiErr = nil
	if _, err := client.ClientPaste(ctx, "cursor", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Errorf("unknown tool: %v", err)
	}

	// pluxy client show prints the reply's files, and with --json the reply body as it came.
	cliEnv := append(slices.Clone(env), "PLUXY_URL="+base, "PLUXY_MANAGEMENT_KEY="+managementKey)
	reply, err := client.ClientPaste(ctx, "pi", "")
	if err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, cliEnv, "client", "show", "pi")
	if r.code != 0 || r.stderr != "Replace <client-key> with a client key: the first is in client_keys in core's config.json, and \"pluxy key add\" makes another.\n" || !strings.HasPrefix(r.stdout, "Client: "+reply.Name+"\nAddress: "+reply.BaseURL+"\n") || !strings.HasSuffix(r.stdout, "\n\n"+reply.Note+"\n") {
		t.Fatalf("pluxy client show pi: %s", r)
	}
	for _, f := range reply.Files {
		if !strings.Contains(r.stdout, "\n"+f.Path+":\n"+f.Content+"\n") {
			t.Errorf("pluxy client show pi does not print %s as the call returns it:\n%s", f.Path, r.stdout)
		}
	}
	status, raw := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, "pi"), nil)
	r = runCLI(t, cliEnv, "client", "show", "pi", "--json")
	if status != http.StatusOK || r.code != 0 || r.stderr != "" || r.stdout != string(raw) {
		t.Errorf("pluxy client show pi --json is not the call's body:\n%s\nbody: %s", r, raw)
	}
	r = runCLI(t, cliEnv, "client", "show", "pi", "--key-id", "no-such-key")
	if r.code != 5 {
		t.Errorf("pluxy client show pi --key-id no-such-key: %s", r)
	}
}
