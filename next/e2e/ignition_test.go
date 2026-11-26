package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// With window ignition on, an account whose five-hour window resets a few seconds after core
// starts gets exactly one request right after the reset, which starts its next window; core then
// waits for that window's reset. An account ignition is not on for gets none. The request is in
// the usage record under the ignition endpoint.
func TestIgnitionStartsTheWindowAfterItsReset(t *testing.T) {
	pkg := testPackageID(t, "test/ignition", "1.0.0")
	dir := t.TempDir()
	logOn, logOff := filepath.Join(dir, "on.log"), filepath.Join(dir, "off.log")
	reset := time.Now().Add(6 * time.Second).Truncate(time.Second)
	settings := func(log string) map[string]any {
		return map[string]any{"five_hour_resets_at": reset.Unix(), "window_starts": true, "generate_log": log}
	}
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing": map[string]any{"ignition": map[string]any{
			// All day, so the test runs at any hour; the shortest hold.
			"enabled": true, "start_hour": 0, "end_hour": 0, "end_grace_ms": 0, "grace_ms": 500, "hold_ms": 15000,
			"extensions": map[string]any{"test/ignition": map[string]any{"model": "plain"}},
			"accounts":   map[string]any{"off": map[string]any{"enabled": false}},
		}},
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{"id": "on", "settings": settings(logOn)},
				map[string]any{"id": "off", "settings": settings(logOff)},
			},
		},
	})

	account := func(id string) mgmt.Account {
		t.Helper()
		code, b := mcall(t, base, managementKey, http.MethodGet, "/accounts", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /accounts: %d %s", code, b)
		}
		var r struct {
			Accounts []mgmt.Account `json:"accounts"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		for _, a := range r.Accounts {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("no account %q in %s", id, b)
		return mgmt.Account{}
	}
	lines := func(path string) []string {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Fields(string(b))
	}
	wait := func(what string, d time.Duration, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(d); !ok(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("%s: not after %s", what, d)
			}
		}
	}
	window := func(id string) *mgmt.AccountIgnition {
		ig := account(id).Ignition
		if ig == nil || len(ig.Windows) != 1 {
			return nil
		}
		return ig
	}

	// Before the reset: the window runs, and core starts it next at the reset plus the grace.
	wait("the first reading", 10*time.Second, func() bool { return window("on") != nil })
	if time.Now().Before(reset) {
		w := window("on").Windows[0]
		if w.ID != "five_hour" || w.State != "running" || w.Next == nil || !w.Next.Equal(reset.Add(500*time.Millisecond).UTC()) {
			t.Fatalf("before the reset: %+v", w)
		}
		if got := lines(logOn); len(got) != 0 {
			t.Fatalf("a request before the reset: %v", got)
		}
	}
	if ig := account("off").Ignition; ig != nil {
		t.Fatalf("ignition runs for an account it is off for: %+v", ig)
	}

	// Right after the reset, one request.
	wait("the ignition request", 15*time.Second, func() bool { return len(lines(logOn)) > 0 })
	if fi, err := os.Stat(logOn); err != nil || fi.ModTime().Before(reset) {
		t.Fatalf("the request came before the reset (%v, %v)", fi.ModTime(), err)
	}
	if w := window("on").Windows[0]; w.State != "starting" || w.Model != "plain" || w.Last == nil {
		t.Fatalf("after the request: %+v", w)
	}

	// A hold later core reads the window: it runs, to reset five hours after the request; no
	// second request was sent.
	wait("the window seen started", 25*time.Second, func() bool { return window("on").Windows[0].State == "running" })
	w := window("on").Windows[0]
	if w.Next == nil || w.Next.Before(reset.Add(5*time.Hour)) || w.Failures != 0 || w.Error != "" {
		t.Fatalf("once started: %+v", w)
	}
	if got := lines(logOn); len(got) != 1 {
		t.Fatalf("requests on the account: %v, want one", got)
	}
	if got := lines(logOff); len(got) != 0 {
		t.Fatalf("requests on the account ignition is off for: %v", got)
	}

	// The usage record has it, under the ignition endpoint, with no client key.
	code, b := mcall(t, base, managementKey, http.MethodGet, "/usage/events", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /usage/events: %d %s", code, b)
	}
	var page struct {
		Events []struct {
			Endpoint string `json:"endpoint"`
			Model    string `json:"model"`
			Account  string `json:"account"`
			Provider string `json:"provider"`
			KeyID    string `json:"key_id"`
			Failed   bool   `json:"failed"`
		} `json:"events"`
	}
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Endpoint != "ignition" || page.Events[0].Account != "on" ||
		page.Events[0].Model != "plain" || page.Events[0].Provider != "test/ignition" || page.Events[0].KeyID != "" || page.Events[0].Failed {
		t.Fatalf("usage events: %s", b)
	}
}
