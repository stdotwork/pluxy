package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// accountHealth returns account id's health from GET /accounts.
func accountHealth(t *testing.T, base, id string) mgmt.Health {
	t.Helper()
	code, b := mcall(t, base, managementKey, http.MethodGet, "/accounts", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /accounts: %d %s", code, b)
	}
	var r mgmt.AccountsReply
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.Accounts {
		if a.ID == id {
			return a.Health
		}
	}
	t.Fatalf("GET /accounts has no account %q: %s", id, b)
	return mgmt.Health{}
}

// waitHealth polls account id's health until ok reports true for it, for up to 30 seconds.
func waitHealth(t *testing.T, base, id, what string, ok func(mgmt.Health) bool) mgmt.Health {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		h := accountHealth(t, base, id)
		if ok(h) {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: health is %+v", what, h)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An upstream that names a reset 30 days off is not believed: the account shows exhausted, from
// an unverified source, held for two check intervals and checked after one. With no usage reading
// (an API key), the check lets the next request through, and once that request is served the
// account is ok again, before the hold would have ended by itself.
func TestUnbelievedResetIsChecked(t *testing.T) {
	t.Setenv("PLUXY_TEST_CHECK_INTERVAL_MS", "5000") // held 10 s, checked at 5 s
	u := newUpstream(t)
	var calls atomic.Int32
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if calls.Add(1) > 1 {
			okReply(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", strconv.Itoa(30*24*3600))
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)
	}
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-a"},
			}},
		},
	})
	resp := post(t, context.Background(), base, clientKey, plainBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("first request: %d, want the upstream's 429", resp.StatusCode)
	}
	h := accountHealth(t, base, "acct")
	if h.State != mgmt.HealthExhausted || h.Source != mgmt.SourceUnverified || h.Until == nil || h.NextCheck == nil {
		t.Fatalf("after a 30-day reset: %+v, want exhausted, unverified, with an end and a next check", h)
	}
	if left := time.Until(*h.Until); left > 10*time.Second || !h.NextCheck.Before(*h.Until) {
		t.Fatalf("after a 30-day reset: %+v, want held at most 10 s and checked before then", h)
	}
	until := *h.Until

	// The check finds no reading to make, so the next request may pass: next_check goes.
	waitHealth(t, base, "acct", "the check", func(h mgmt.Health) bool { return h.NextCheck == nil })
	if !time.Now().Before(until) {
		t.Fatal("the check came after the hold ended, so the test cannot tell them apart; the machine is too slow for its timing")
	}
	postOK(t, base, plainBody)
	if h := accountHealth(t, base, "acct"); h.State != mgmt.HealthOK {
		t.Errorf("after the check's request was served: %+v, want ok", h)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("upstream calls = %d, want 2: the refused one and the check's", n)
	}
}

// A quota block is saved to state/routing.json and read back after a restart: the account stays
// out, no request reaches it before its check, and the check at start, a usage reading that
// finds its window still full, keeps it out. pluxy account list shows the health column, and its
// --json prints GET /accounts's body unchanged.
func TestSavedBlockKeepsAccountOutAfterRestart(t *testing.T) {
	pkg := testPackageID(t, "test/health", "1.0.0")
	logPath := filepath.Join(t.TempDir(), "generate.log")
	resetAt := time.Now().Add(3 * time.Hour).Unix()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{"id": "a", "settings": map[string]any{"exhausted_until": resetAt, "generate_log": logPath}},
				map[string]any{"id": "b", "settings": map[string]any{"generate_log": logPath}},
			},
		},
	})
	served := func() []string {
		b, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Fields(string(b))
	}
	const body = `{"model":"plain","messages":[{"role":"user","content":"hi"}]}`
	bin := filepath.Join(binDir, "pluxy")

	base, _, stop, err := startCoreStoppable(t, bin, "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	postOK(t, base, body) // a refuses, b serves
	if got := served(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("requests = %v, want a then b", got)
	}
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthExhausted || h.Source != mgmt.SourceProvider {
		t.Fatalf("a after its quota ran out: %+v", h)
	}
	stop()

	file := filepath.Join(dir, "state", "routing.json")
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %v, want 0600", file, fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), `"account": "a"`) || strings.Contains(string(b), `"account": "b"`) {
		t.Errorf("%s holds %s, want a's block alone", file, b)
	}

	started := time.Now()
	base, _, _, err = startCoreStoppable(t, bin, "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	h := accountHealth(t, base, "a")
	if h.State != mgmt.HealthExhausted || h.Until == nil || h.Until.Sub(time.Unix(resetAt, 0)).Abs() > 2*time.Second {
		t.Fatalf("a after the restart: %+v, want exhausted until %v", h, time.Unix(resetAt, 0))
	}
	for range 3 {
		postOK(t, base, body)
	}
	if got := served()[2:]; !slices.Equal(got, []string{"b", "b", "b"}) {
		t.Errorf("requests after the restart = %v, want b alone", got)
	}
	// The check at start read a's usage, found the window full and left the block: the next
	// check is an interval away.
	h = waitHealth(t, base, "a", "the check at start", func(h mgmt.Health) bool {
		return h.NextCheck != nil && h.NextCheck.After(started.Add(time.Minute))
	})
	if h.State != mgmt.HealthExhausted || h.Source != mgmt.SourceProvider || h.Until.Sub(time.Unix(resetAt, 0)).Abs() > time.Second {
		t.Errorf("a after the check at start: %+v, want exhausted until the full window's reset", h)
	}
	if got := served()[2:]; !slices.Equal(got, []string{"b", "b", "b"}) {
		t.Errorf("requests after the restart = %v, want b alone", got)
	}

	target := []string{"--url", base, "--key", managementKey}
	r := runCLI(t, nil, append([]string{"account", "list"}, target...)...)
	if r.code != 0 || !strings.Contains(r.stdout, "HEALTH") {
		t.Fatalf("account list: %s", r)
	}
	for _, line := range strings.Split(r.stdout, "\n") {
		f := strings.Fields(line)
		if len(f) > 3 && f[0] == "a" && f[3] != "exhausted" || len(f) > 3 && f[0] == "b" && f[3] != "ok" {
			t.Errorf("account list line %q: want a exhausted and b ok\n%s", line, r.stdout)
		}
	}
	r = runCLI(t, nil, append([]string{"account", "list", "--json"}, target...)...)
	code, api := mcall(t, base, managementKey, http.MethodGet, "/accounts", nil)
	if r.code != 0 || code != http.StatusOK {
		t.Fatalf("account list --json: %s; GET /accounts: %d", r, code)
	}
	var fromCLI, fromAPI any
	if err := json.Unmarshal([]byte(r.stdout), &fromCLI); err != nil {
		t.Fatalf("account list --json printed no JSON: %v\n%s", err, r)
	}
	_ = json.Unmarshal(api, &fromAPI)
	if a, b := mustJSON(fromCLI), mustJSON(fromAPI); a != b {
		t.Errorf("account list --json printed\n%s\nthe API answers\n%s", a, b)
	}
}
