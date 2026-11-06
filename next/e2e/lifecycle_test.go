package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// testPackage writes an extension package for testext at version into a new folder and returns
// the folder.
func testPackage(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	prog, err := os.ReadFile(filepath.Join(binDir, "testext"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testext"), prog, 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(map[string]any{
		"manifest_version": 0,
		"id":               "test/lifecycle",
		"version":          version,
		"protocol":         []int{0},
		"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "testext"},
	})
	if err := os.WriteFile(filepath.Join(dir, "pluxy-extension.json"), m, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// gate is an HTTP endpoint that testext's "gated" requests wait on until release is called.
type gate struct {
	*httptest.Server
	once    sync.Once
	release chan struct{}
}

func newGate(t *testing.T) *gate {
	g := &gate{release: make(chan struct{})}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-g.release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { g.open(); g.Close() })
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

// lifecycleConfig is a config for testext in pkg with one account.
func lifecycleConfig(pkg string, g *gate, extension map[string]any) map[string]any {
	ext := map[string]any{
		"manifest": filepath.Join(pkg, "pluxy-extension.json"),
		"accounts": []any{map[string]any{"id": "acct", "settings": map[string]any{"gate": g.URL}}},
	}
	for k, v := range extension {
		ext[k] = v
	}
	return map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension":      ext,
	}
}

// noWatch turns off core's config file watcher, so only the test's explicit reloads publish
// revisions.
var noWatch = []string{"-watch-interval", "0"}

func writeConfig(t *testing.T, path string, cfg map[string]any) {
	t.Helper()
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

type extensionStatus struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	Generation     int    `json:"generation"`
	State          string `json:"state"`
	Current        bool   `json:"current"`
	PID            int    `json:"pid"`
	Restarts       int    `json:"restarts"`
	LastError      string `json:"last_error"`
	ActiveRequests int    `json:"active_requests"`
}

type revisionStatus struct {
	Revision int    `json:"revision"`
	Hash     string `json:"hash"`
	Error    string `json:"error"`
}

type coreStatus struct {
	SafeMode   bool              `json:"safe_mode"`
	Desired    revisionStatus    `json:"desired"`
	Active     revisionStatus    `json:"active"`
	Extensions []extensionStatus `json:"extensions"`
}

// current returns the extension generation that serves new requests.
func (s coreStatus) current() extensionStatus {
	for _, e := range s.Extensions {
		if e.Current {
			return e
		}
	}
	return extensionStatus{}
}

func management(t *testing.T, method, base, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+managementKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func status(t *testing.T, base string) coreStatus {
	t.Helper()
	code, b := management(t, http.MethodGet, base, "/v0/management/status")
	if code != http.StatusOK {
		t.Fatalf("GET /v0/management/status: %d %s", code, b)
	}
	var s coreStatus
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("status %s: %v", b, err)
	}
	return s
}

// reload asks core to reload its config and returns the reply's status code and document.
func reload(t *testing.T, base string) (int, coreStatus) {
	t.Helper()
	code, b := management(t, http.MethodPost, base, "/v0/management/reload")
	var s coreStatus
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("POST /v0/management/reload: %d %s", code, b)
	}
	return code, s
}

// waitStatus polls core's status until ok accepts it.
func waitStatus(t *testing.T, base string, what string, ok func(coreStatus) bool) coreStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := status(t, base)
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s; last status: %+v", what, s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ask sends a non-streamed request for model and returns the status and reply body.
func ask(t *testing.T, base, model string) (int, string) {
	t.Helper()
	resp := post(t, context.Background(), base, clientKey, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func askOK(t *testing.T, base, model string) string {
	t.Helper()
	code, body := ask(t, base, model)
	if code != http.StatusOK {
		t.Fatalf("model %s: status %d: %s", model, code, body)
	}
	return body
}

// streamUntil starts a streamed request for model and reads it until a chunk contains want.
// It returns the reader for the rest of the stream.
func streamUntil(t *testing.T, ctx context.Context, base, model, want string) *bufio.Reader {
	t.Helper()
	resp := post(t, ctx, base, clientKey, `{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	r := bufio.NewReader(resp.Body)
	for {
		if d := readData(t, r); strings.Contains(d, want) {
			return r
		}
	}
}

// rest reads the remainder of an SSE stream and returns its data payloads joined by newlines.
func rest(r *bufio.Reader) string {
	var out []string
	for {
		line, err := r.ReadString('\n')
		if d, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data: "); ok {
			out = append(out, d)
		}
		if err != nil {
			return strings.Join(out, "\n")
		}
	}
}

// The slice's main check: swapping the extension while a stream runs finishes that stream on
// the old version and serves new requests from the new one; the old process then stops.
func TestHotSwapFinishesOldStreamsOnOldVersion(t *testing.T) {
	g := newGate(t)
	v1, v2 := testPackage(t, "0.1.0"), testPackage(t, "0.2.0")
	base, path, _ := runCoreArgs(t, lifecycleConfig(v1, g, nil), noWatch...)

	before := status(t, base)
	if before.Active.Revision != 1 || before.Desired.Revision != 1 {
		t.Fatalf("at start: desired %d, active %d; want 1 and 1", before.Desired.Revision, before.Active.Revision)
	}
	old := streamUntil(t, context.Background(), base, "gated", "v=0.1.0")

	writeConfig(t, path, lifecycleConfig(v2, g, nil))
	code, s := reload(t, base)
	if code != http.StatusOK || s.Active.Revision != 2 || s.Desired.Error != "" {
		t.Fatalf("reload: %d, active %d, error %q; want 200, active revision 2", code, s.Active.Revision, s.Desired.Error)
	}
	var draining extensionStatus
	for _, e := range s.Extensions {
		if e.Version == "0.1.0" {
			draining = e
		}
	}
	if draining.State != "draining" || draining.ActiveRequests != 1 {
		t.Errorf("old generation after the swap = %+v; want draining with 1 active request", draining)
	}

	if body := askOK(t, base, "plain"); !strings.Contains(body, "v=0.2.0") {
		t.Errorf("new request after the swap answered %s; want the new version 0.2.0", body)
	}

	g.open()
	tail := rest(old)
	if !strings.Contains(tail, " done") || !strings.Contains(tail, "[DONE]") || strings.Contains(tail, "v=0.2.0") {
		t.Errorf("old stream after the swap = %q; want it finished by the old version", tail)
	}
	waitStatus(t, base, "the old generation to stop", func(s coreStatus) bool {
		for _, e := range s.Extensions {
			if e.Version == "0.1.0" {
				return false
			}
		}
		return true
	})
}

// A reload that fails leaves the active config and extension in place and says why.
func TestFailedReloadKeepsActiveConfig(t *testing.T) {
	g := newGate(t)
	v1 := testPackage(t, "0.1.0")
	cfg := lifecycleConfig(v1, g, nil)
	base, path, _ := runCoreArgs(t, cfg, noWatch...)
	pid := status(t, base).current().PID

	check := func(what string, wantRevision int, wantErr string) {
		t.Helper()
		code, s := reload(t, base)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s: reload status %d, want 422", what, code)
		}
		if s.Active.Revision != 1 || s.Desired.Revision != wantRevision || !strings.Contains(s.Desired.Error, wantErr) {
			t.Errorf("%s: active %d, desired %d with error %q; want active 1, desired %d with an error naming %q",
				what, s.Active.Revision, s.Desired.Revision, s.Desired.Error, wantRevision, wantErr)
		}
		if body := askOK(t, base, "plain"); !strings.Contains(body, "v=0.1.0") {
			t.Errorf("%s: request after the failed reload answered %s; want the old extension", what, body)
		}
		if got := status(t, base).current().PID; got != pid {
			t.Errorf("%s: extension process changed from %d to %d", what, pid, got)
		}
	}

	if err := os.WriteFile(path, []byte(`{"listen": `), 0o600); err != nil {
		t.Fatal(err)
	}
	check("broken JSON", 2, "config")

	bad := testPackage(t, "0.3.0")
	if err := os.WriteFile(filepath.Join(bad, "crash-on-start"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, lifecycleConfig(bad, g, nil))
	check("extension that fails to start", 3, "exited before its handshake")

	moved := lifecycleConfig(v1, g, nil)
	moved["listen"] = "127.0.0.1:1"
	writeConfig(t, path, moved)
	check("listen address change", 4, "listen")
}

// A crashed extension is restarted after a backoff; one that keeps crashing is quarantined, and
// a reload starts it afresh.
func TestCrashedExtensionRestartsThenQuarantines(t *testing.T) {
	g := newGate(t)
	pkg := testPackage(t, "0.1.0")
	cfg := lifecycleConfig(pkg, g, map[string]any{
		"restart": map[string]any{"backoff_ms": 50, "max_backoff_ms": 100, "quarantine_after": 3, "stable_ms": 60000},
	})
	base, _, _ := runCoreArgs(t, cfg, noWatch...)
	first := status(t, base).current()

	if code, body := ask(t, base, "crash"); code == http.StatusOK {
		t.Fatalf("crashing request answered 200: %s", body)
	}
	s := waitStatus(t, base, "a restart", func(s coreStatus) bool {
		c := s.current()
		return c.State == "active" && c.PID != 0 && c.PID != first.PID
	})
	if c := s.current(); c.Restarts != 1 || !strings.Contains(c.LastError, "exit") {
		t.Errorf("after one crash: %+v; want 1 restart and the exit as last error", c)
	}
	if body := askOK(t, base, "plain"); !strings.Contains(body, "pid=") || strings.Contains(body, "pid="+itoa(first.PID)) {
		t.Errorf("request after the restart answered %s; want the new process", body)
	}

	// From now on every start fails: the restarts back off and then stop.
	if err := os.WriteFile(filepath.Join(pkg, "crash-on-start"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ask(t, base, "crash")
	s = waitStatus(t, base, "quarantine", func(s coreStatus) bool { return s.current().State == "quarantined" })
	if c := s.current(); !strings.Contains(c.LastError, "exited before its handshake") {
		t.Errorf("quarantined extension: %+v; want the failed start as last error", c)
	}
	if code, body := ask(t, base, "plain"); code != http.StatusServiceUnavailable || !strings.Contains(body, "quarantined") {
		t.Errorf("request to a quarantined extension: %d %s; want 503 naming the quarantine", code, body)
	}

	if err := os.Remove(filepath.Join(pkg, "crash-on-start")); err != nil {
		t.Fatal(err)
	}
	if code, s := reload(t, base); code != http.StatusOK || s.current().State != "active" {
		t.Fatalf("reload after fixing the extension: %d %+v; want it active again", code, s)
	}
	askOK(t, base, "plain")
}

// An extension's program stops when it has been idle for idle_stop_ms and starts again when a
// request needs it.
func TestIdleExtensionStopsAndStartsOnDemand(t *testing.T) {
	g := newGate(t)
	pkg := testPackage(t, "0.1.0")
	base, _, _ := runCoreArgs(t, lifecycleConfig(pkg, g, map[string]any{"idle_stop_ms": 200}), noWatch...)
	waitStatus(t, base, "idle stop", func(s coreStatus) bool { c := s.current(); return c.State == "idle" && c.PID == 0 })

	if body := askOK(t, base, "plain"); !strings.Contains(body, "v=0.1.0") {
		t.Errorf("request to an idle extension answered %s", body)
	}
	starts, _ := os.ReadFile(filepath.Join(pkg, "starts"))
	if n := strings.Count(string(starts), "\n"); n != 2 {
		t.Errorf("program started %d times, want 2 (verify at load, then on demand)", n)
	}
	waitStatus(t, base, "idle stop after the request", func(s coreStatus) bool { return s.current().State == "idle" })
}

// Safe mode runs core with no extensions: nothing starts, requests fail clearly and config can
// still be reloaded.
func TestSafeModeStartsNoExtensions(t *testing.T) {
	g := newGate(t)
	pkg := testPackage(t, "0.1.0")
	base, _, _ := runCoreArgs(t, lifecycleConfig(pkg, g, nil), append(noWatch, "-safe-mode")...)

	s := status(t, base)
	if !s.SafeMode || len(s.Extensions) != 0 || s.Active.Revision != 1 {
		t.Errorf("safe mode status = %+v; want safe_mode, no extensions, revision 1", s)
	}
	if code, body := ask(t, base, "plain"); code != http.StatusServiceUnavailable || !strings.Contains(body, "safe mode") {
		t.Errorf("request in safe mode: %d %s; want 503 naming safe mode", code, body)
	}
	if code, s := reload(t, base); code != http.StatusOK || s.Active.Revision != 2 || len(s.Extensions) != 0 {
		t.Errorf("reload in safe mode: %d %+v; want revision 2 and still no extensions", code, s)
	}
	if _, err := os.Stat(filepath.Join(pkg, "starts")); err == nil {
		t.Error("the extension's program ran in safe mode")
	}
}

// A draining generation is stopped at its deadline even when a stream is still running.
func TestDrainStopsAtDeadline(t *testing.T) {
	g := newGate(t)
	v1, v2 := testPackage(t, "0.1.0"), testPackage(t, "0.2.0")
	base, path, _ := runCoreArgs(t, lifecycleConfig(v1, g, map[string]any{"drain_timeout_ms": 300}), noWatch...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	old := streamUntil(t, ctx, base, "gated", "v=0.1.0")

	writeConfig(t, path, lifecycleConfig(v2, g, map[string]any{"drain_timeout_ms": 300}))
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	start := time.Now()
	tail := rest(old)
	if strings.Contains(tail, " done") || !strings.Contains(tail, "error") {
		t.Errorf("stream past the drain deadline = %q; want it ended with an error", tail)
	}
	if ctx.Err() != nil || time.Since(start) > 8*time.Second {
		t.Errorf("old stream ran %s past the swap; want it cut at the 300ms deadline", time.Since(start))
	}
	waitStatus(t, base, "the old generation to stop", func(s coreStatus) bool { return len(s.Extensions) == 1 })
}

// Routing state (here a cooldown) survives a reload while the account stays the same, and is
// dropped when the account's settings change.
func TestRoutingStateSurvivesReload(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
	}
	account := func(settings map[string]any) map[string]any {
		settings["base_url"] = u.URL + "/v1"
		return map[string]any{"id": "acct-a", "settings": settings, "credential": map[string]any{"kind": "api_key", "api_key": "sk-a"}}
	}
	config := func(keys []string, settings map[string]any) map[string]any {
		return map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    keys,
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "pluxy-extension.json"),
				"accounts": []any{account(settings)},
			},
		}
	}
	base, path, _ := runCoreArgs(t, config([]string{clientKey}, map[string]any{}), noWatch...)
	calls := func() int { b, _ := u.received(); return len(b) }

	if code, _ := ask(t, base, "gpt-test"); code != http.StatusTooManyRequests || calls() != 1 {
		t.Fatalf("first request: %d after %d upstream calls; want 429 after 1", code, calls())
	}
	// Another client key: the account is unchanged, so its cooldown still holds.
	writeConfig(t, path, config([]string{clientKey, "another-key"}, map[string]any{}))
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	if code, body := ask(t, base, "gpt-test"); code != http.StatusTooManyRequests || calls() != 1 {
		t.Errorf("after a reload that kept the account: %d %s after %d upstream calls; want 429 from the cooldown, no new call", code, body, calls())
	}
	// Changed settings make it another account as far as routing knows: the cooldown goes.
	writeConfig(t, path, config([]string{clientKey}, map[string]any{"note": "moved"}))
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	ask(t, base, "gpt-test")
	if calls() != 2 {
		t.Errorf("after a reload that changed the account's settings: %d upstream calls; want 2", calls())
	}
}

// Core watches its config file: a change is published once it settles, an unchanged or empty
// write is ignored, and a broken file leaves the last valid config active.
func TestConfigFileWatch(t *testing.T) {
	g := newGate(t)
	pkg := testPackage(t, "0.1.0")
	cfg := lifecycleConfig(pkg, g, nil)
	base, path, _ := runCoreArgs(t, cfg, "-watch-interval", "50ms")

	cfg["client_keys"] = []string{clientKey, "second-key"}
	writeConfig(t, path, cfg)
	s := waitStatus(t, base, "revision 2", func(s coreStatus) bool { return s.Active.Revision == 2 })
	resp := post(t, context.Background(), base, "second-key", `{"model":"plain","messages":[{"role":"user","content":"hi"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("new client key after the watched change: status %d", resp.StatusCode)
	}

	// An editor truncating the file, then the same content again: nothing to publish.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	writeConfig(t, path, cfg)
	time.Sleep(300 * time.Millisecond)
	if now := status(t, base); now.Desired != s.Desired || now.Active != s.Active {
		t.Errorf("after an empty write and the same content: %+v / %+v; want %+v / %+v", now.Desired, now.Active, s.Desired, s.Active)
	}

	if err := os.WriteFile(path, []byte(`{"listen": "127.0.0.1:0", `), 0o600); err != nil {
		t.Fatal(err)
	}
	s = waitStatus(t, base, "the broken file to be refused", func(s coreStatus) bool { return s.Desired.Revision == 3 && s.Desired.Error != "" })
	if s.Active.Revision != 2 || s.Desired.Error == "" {
		t.Errorf("broken file: desired %+v, active %+v; want an error and revision 2 still active", s.Desired, s.Active)
	}
	askOK(t, base, "plain")
}

// The management API needs the management key.
func TestManagementNeedsKey(t *testing.T) {
	g := newGate(t)
	base, _, _ := runCoreArgs(t, lifecycleConfig(testPackage(t, "0.1.0"), g, nil), noWatch...)
	for _, key := range []string{"", clientKey} {
		req, _ := http.NewRequest(http.MethodGet, base+"/v0/management/status", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status with key %q: %d, want 401", key, resp.StatusCode)
		}
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Round robin carries on where it was across a reload, rather than starting again at the
// first account.
func TestRotationCarriesOnAcrossReload(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	config := func(keys ...string) map[string]any {
		var accounts []any
		for _, k := range []string{"sk-a", "sk-b", "sk-c"} {
			accounts = append(accounts, map[string]any{"id": "acct-" + k, "settings": map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": k}})
		}
		return map[string]any{"listen": "127.0.0.1:0", "client_keys": keys, "management_key": managementKey,
			"extension": map[string]any{"manifest": filepath.Join(binDir, "pluxy-extension.json"), "accounts": accounts}}
	}
	base, path, _ := runCoreArgs(t, config(clientKey), noWatch...)
	postOK(t, base, plainBody)
	writeConfig(t, path, config(clientKey, "another-key"))
	if code, s := reload(t, base); code != http.StatusOK {
		t.Fatalf("reload: %d %+v", code, s)
	}
	postOK(t, base, plainBody)
	if got := usedKeys(u); !slices.Equal(got, []string{"sk-a", "sk-b"}) {
		t.Errorf("upstream keys = %v, want [sk-a sk-b]: the turn after a reload is the next account's", got)
	}
}
