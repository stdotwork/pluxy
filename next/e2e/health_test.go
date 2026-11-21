package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

	// The check finds no usage reading to make, so it lets the next request through, and that
	// request's outcome is the check's answer. The check is taken on a moment before it lets the
	// request through (next_check goes when it is taken on), so a request sent in that moment is
	// still refused with a cooldown, which reaches no upstream. Requests are sent until one is
	// served, and it must be served before the hold ends: only the check lets one through while
	// the block is live, so that is what tells the check from the hold simply ending.
	served := false
	for !served {
		resp := post(t, context.Background(), base, clientKey, plainBody)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			served = true
		case http.StatusTooManyRequests:
			time.Sleep(20 * time.Millisecond)
		default:
			t.Fatalf("status %d, want 200 or the cooldown's 429: %s", resp.StatusCode, raw)
		}
	}
	if !time.Now().Before(until) {
		t.Fatal("the request the check let through was served after the hold ended, so the test cannot tell the check from the hold ending; the machine is too slow for its timing")
	}
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
		// The columns are ID, PUBLIC ID, LABEL, EXTENSION, SOURCE, HEALTH.
		f := strings.Fields(line)
		if len(f) > 5 && f[0] == "a" && f[5] != "exhausted" || len(f) > 5 && f[0] == "b" && f[5] != "ok" {
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

// state/routing.json is written only when there is something to save, and a file core cannot
// read, or that another version wrote, is not loaded and is left in place until a block must be
// saved; it is then moved aside to routing.json.bad.
func TestRoutingFileIsLeftAloneUntilABlockMustBeSaved(t *testing.T) {
	pkg := testPackageID(t, "test/health", "1.0.0")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{"id": "a", "settings": map[string]any{"exhausted_until": time.Now().Add(3 * time.Hour).Unix()}},
				map[string]any{"id": "b"},
			},
		},
	})
	bin := filepath.Join(binDir, "pluxy")
	file := filepath.Join(dir, "state", "routing.json")
	start := func() (string, func()) {
		t.Helper()
		base, _, stop, err := startCoreStoppable(t, bin, "-config", path)
		if err != nil {
			t.Fatal(err)
		}
		return base, stop
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Nothing to save and no file: a start and a stop write none.
	_, stop := start()
	stop()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("after a start and a stop with no block, %s: %v, want no file", file, err)
	}

	// A file that cannot be read, and one another version wrote (holding a block on a), are not
	// loaded and stay as they are.
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	other := fmt.Sprintf(`{"v": 2, "blocks": [{"account": "a", "scope": "credential", "until": %q, "source": "provider"}]}`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	for _, content := range []string{"{not json", other} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		base, stop := start()
		if h := accountHealth(t, base, "a"); h.State != mgmt.HealthOK {
			t.Errorf("a with %q in %s: %+v, want ok (the file not loaded)", content, file, h)
		}
		stop()
		if got := read(file); got != content {
			t.Errorf("%s after a start and a stop: %q, want %q left as it was", file, got, content)
		}
	}

	// A block to save: the other version's file is moved aside, and routing.json holds the block.
	base, stop := start()
	postOK(t, base, `{"model":"plain","messages":[{"role":"user","content":"hi"}]}`) // a refuses, b serves
	stop()
	if got := read(file + ".bad"); got != other {
		t.Errorf("%s.bad = %q, want the file that was there, %q", file, got, other)
	}
	var doc struct {
		V      int `json:"v"`
		Blocks []struct {
			Account string `json:"account"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(read(file)), &doc); err != nil || doc.V != 1 || len(doc.Blocks) != 1 || doc.Blocks[0].Account != "a" {
		t.Errorf("%s = %s (%v), want version 1 with a's block", file, read(file), err)
	}
}

// A fake upstream that answers "account suspended" bans the account with the provider's words,
// survives a core restart as banned (getting zero requests, including startup checks), and
// serves again after pluxy account enable.
func TestBannedAccountSurvivesRestartAndRecoversOnEnable(t *testing.T) {
	var aCalls, bCalls atomic.Int32
	var aSuspended atomic.Bool
	aSuspended.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Api-Key")
		if key == "sk-ant-api-a" {
			aCalls.Add(1)
		} else if key == "sk-ant-api-b" {
			bCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude Sonnet 4.5"}],"has_more":false}`)
			return
		}
		if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
			if key == "sk-ant-api-a" && aSuspended.Load() {
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"Your account has been suspended."}}`)
				return
			}
			io.WriteString(w, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"ok from `+key+`"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":3}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(dir, "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	bin := filepath.Join(binDir, "pluxy")
	base, logs, stop, err := startCoreStoppable(t, bin, "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	addAcct := func(id, apiKey string, priority int) {
		t.Helper()
		var l loginReply
		if code := manage(t, base, http.MethodPost, "/accounts", map[string]any{
			"id":       id,
			"method":   "api_key",
			"priority": priority,
			"settings": map[string]any{"base_url": up.URL},
			"input":    map[string]string{"api_key": apiKey},
		}, &l); code != http.StatusAccepted {
			t.Fatalf("add %s: %d", id, code)
		}
		for deadline := time.Now().Add(10 * time.Second); l.Login.State == "pending" && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			manage(t, base, http.MethodGet, "/logins/"+l.Login.ID, nil, &l)
		}
		if l.Login.State != "done" {
			t.Fatalf("login %s: %+v", id, l.Login)
		}
	}
	addAcct("a", "sk-ant-api-a", 2)
	addAcct("b", "sk-ant-api-b", 1)

	const chatBody = `{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"hi"}]}`
	postOK(t, base, chatBody) // tries a (highest priority) -> suspended -> rotates to b -> 200

	h := accountHealth(t, base, "a")
	if h.State != mgmt.HealthBanned || h.Why != "Your account has been suspended." || h.Source != mgmt.SourceProvider {
		t.Fatalf("a after suspension: %+v, want banned with provider words", h)
	}
	logs.waitFor(t, `account "a" banned: Your account has been suspended.`)
	stop()

	routingFile := filepath.Join(dir, "state", "routing.json")
	if b, err := os.ReadFile(routingFile); err != nil || !strings.Contains(string(b), `"account": "a"`) {
		t.Fatalf("routing.json = %s (%v), want ban for a", b, err)
	}

	// Restart core and verify a stays banned and gets ZERO requests (not even startup check).
	aBefore := aCalls.Load()
	bBefore := bCalls.Load()
	base, _, stop, err = startCoreStoppable(t, bin, "-config", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthBanned || h.Why != "Your account has been suspended." {
		t.Fatalf("a after restart: %+v, want banned", h)
	}
	if r := runCLI(t, nil, "account", "list", "--url", base, "--key", managementKey); r.code != 0 || !strings.Contains(r.stdout, "banned") || !strings.Contains(r.stdout, "Your account has been suspended.") {
		t.Fatalf("account list while banned: %s", r)
	}
	// Clearing cooldowns must not clear a ban.
	if r := runCLI(t, nil, "account", "clear", "a", "--url", base, "--key", managementKey); r.code != 0 {
		t.Fatalf("account clear while banned: %s", r)
	}
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthBanned {
		t.Fatalf("a after account clear: %+v, want still banned", h)
	}
	for range 3 {
		postOK(t, base, chatBody)
	}
	if aCalls.Load() != aBefore {
		t.Errorf("a received %d requests after restart while banned, want 0", aCalls.Load()-aBefore)
	}
	if bCalls.Load() < bBefore+3 {
		t.Errorf("b calls = %d -> %d, want at least +3", bBefore, bCalls.Load())
	}

	// pluxy account enable unbans a and lets it serve again.
	aSuspended.Store(false)
	r := runCLI(t, nil, "account", "enable", "a", "--url", base, "--key", managementKey)
	if r.code != 0 {
		t.Fatalf("pluxy account enable a: %s", r)
	}
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthOK {
		t.Fatalf("a after pluxy account enable: %+v, want ok", h)
	}
	aBeforeEnableServe := aCalls.Load()
	postOK(t, base, chatBody) // a has priority 2 > b's priority 1, so a serves
	if aCalls.Load() <= aBeforeEnableServe {
		t.Errorf("a did not serve after pluxy account enable (calls stayed %d)", aCalls.Load())
	}
}

// Signing in to a banned account again unbans it without restarting core.
func TestBannedAccountReauthUnbans(t *testing.T) {
	var suspended atomic.Bool
	suspended.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude 4.5 Sonnet"}],"has_more":false}`)
			return
		}
		if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
			if suspended.Load() {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"This organization has been disabled."}}`)
				return
			}
			io.WriteString(w, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":3}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{},
		},
	})
	login := func(path string, body map[string]any) {
		t.Helper()
		var l loginReply
		if code := manage(t, base, http.MethodPost, path, body, &l); code != http.StatusAccepted {
			t.Fatalf("POST %s: %d", path, code)
		}
		for deadline := time.Now().Add(10 * time.Second); l.Login.State == "pending" && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			manage(t, base, http.MethodGet, "/logins/"+l.Login.ID, nil, &l)
		}
		if l.Login.State != "done" {
			t.Fatalf("POST %s login: %+v", path, l.Login)
		}
	}
	login("/accounts", map[string]any{
		"id": "a", "method": "api_key",
		"settings": map[string]any{"base_url": up.URL},
		"input":    map[string]string{"api_key": "sk-ant-api-old"},
	})
	const chatBody = `{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, context.Background(), base, clientKey, chatBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("first request: %d, want 400", resp.StatusCode)
	}
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthBanned || h.Why != "This organization has been disabled." {
		t.Fatalf("a after ban: %+v", h)
	}

	suspended.Store(false)
	login("/accounts/a/login", map[string]any{
		"method": "api_key",
		"input":  map[string]string{"api_key": "sk-ant-api-new"},
	})
	if h := accountHealth(t, base, "a"); h.State != mgmt.HealthOK {
		t.Fatalf("a after reauth: %+v, want ok", h)
	}
	postOK(t, base, chatBody)
}

// A config-file API-key account that gets account_closed shows banned, and pluxy account enable
// unbans it (without modifying the config file) so it serves again.
func TestBannedConfigAccountEnable(t *testing.T) {
	var closed atomic.Bool
	closed.Store(true)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
			io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-4-5-20250929","display_name":"Claude Sonnet 4.5"}],"has_more":false}`)
			return
		}
		if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
			if closed.Load() {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"This organization has been disabled."}}`)
				return
			}
			io.WriteString(w, `{"id":"msg_ok","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"output_tokens":3}}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeConfig(t, cfgPath, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "cfg-a",
				"settings":   map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ant-api-cfg"},
			}},
		},
	})
	beforeCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "pluxy")
	base, _, stop, err := startCoreStoppable(t, bin, "-config", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	const chatBody = `{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"hi"}]}`
	resp := post(t, context.Background(), base, clientKey, chatBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("first request: %d, want 400", resp.StatusCode)
	}
	if h := accountHealth(t, base, "cfg-a"); h.State != mgmt.HealthBanned || h.Why != "This organization has been disabled." {
		t.Fatalf("cfg-a after account_closed: %+v, want banned", h)
	}

	closed.Store(false)
	// Other PATCH fields still return 409 on config accounts.
	code, _ := mcall(t, base, managementKey, http.MethodPatch, "/accounts/cfg-a", `{"disabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("PATCH disabled:true on config account: %d, want 409", code)
	}
	if r := runCLI(t, nil, "account", "enable", "cfg-a", "--url", base, "--key", managementKey); r.code != 0 {
		t.Fatalf("pluxy account enable cfg-a: %s", r)
	}
	if h := accountHealth(t, base, "cfg-a"); h.State != mgmt.HealthOK {
		t.Fatalf("cfg-a after pluxy account enable: %+v, want ok", h)
	}
	postOK(t, base, chatBody)
	afterCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCfg) != string(beforeCfg) {
		t.Errorf("config file changed after enable:\nwas:\n%s\nnow:\n%s", beforeCfg, afterCfg)
	}
}
