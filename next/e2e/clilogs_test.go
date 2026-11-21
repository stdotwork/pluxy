package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pluxy command reads the error logs and request logs, clears core's log and exports the
// recorded requests, against a real core.
func TestCLILogsAndUsageExport(t *testing.T) {
	c := startManaged(t, false)
	env := []string{"PLUXY_URL=" + c.base, "PLUXY_MANAGEMENT_KEY=" + managementKey}
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"logging-to-file": true}}}`)

	// One request that fails (an error log) and one that succeeds with request-log on (a request log).
	if code, _ := requestWithSecrets(t, c.base, "nobody"); code != http.StatusNotFound {
		t.Fatalf("pinned to no account: %d", code)
	}
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"request-log": true}}}`)
	code, trace := requestWithSecrets(t, c.base, "")
	if code != http.StatusOK || trace == "" {
		t.Fatalf("logged request: %d %q", code, trace)
	}

	r := runCLI(t, env, "logs", "errors")
	files := c.errorLogs(t).Files
	if r.code != 0 || len(files) != 1 || !strings.Contains(r.stdout, "NAME") || !strings.Contains(r.stdout, files[0].Name) {
		t.Fatalf("logs errors: %s", r)
	}
	onDisk, err := os.ReadFile(filepath.Join(c.logsDir(), files[0].Name))
	if err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, env, "logs", "errors", files[0].Name); r.code != 0 || r.stdout != string(onDisk) || r.stderr != "" {
		t.Fatalf("logs errors <name> is not the file's bytes: %s", r)
	}
	var j struct{ Name, Content string }
	if r := runCLI(t, env, "logs", "errors", files[0].Name, "--json"); r.code != 0 || json.Unmarshal([]byte(r.stdout), &j) != nil || j.Name != files[0].Name || j.Content != string(onDisk) {
		t.Fatalf("logs errors <name> --json: %s", r)
	}
	if r := runCLI(t, env, "logs", "request", trace); r.code != 0 || !strings.Contains(r.stdout, "Trace ID: "+trace) || !strings.Contains(r.stdout, "Status: 200") {
		t.Fatalf("logs request: %s", r)
	}
	if r := runCLI(t, env, "logs", "request", "no-such-trace"); r.code != 5 || r.stdout != "" || !strings.Contains(r.stderr, "no request log has this trace ID") {
		t.Fatalf("logs request of an unknown trace: %s", r)
	}

	// The record has the successful request, in the panel's columns.
	r = runCLI(t, env, "usage", "export", "--model", "gpt-4o")
	lines := strings.Split(strings.TrimSuffix(r.stdout, "\r\n"), "\r\n")
	if r.code != 0 || r.stderr != "" || lines[0] != "Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID" ||
		len(lines) < 2 || !strings.HasSuffix(lines[1], ","+trace) || !strings.Contains(lines[1], ",gpt-4o,") || !strings.Contains(lines[1], ",false,200,") {
		t.Fatalf("usage export: %s", r)
	}
	if r := runCLI(t, env, "usage", "export", "--json"); r.code != 2 || !strings.Contains(r.stderr, "pluxy usage events --json") {
		t.Fatalf("usage export --json: %s", r)
	}

	// Clearing empties main.log and leaves the request and error logs.
	if r := runCLI(t, env, "logs", "clear"); r.code != 0 || !strings.Contains(r.stdout, "main.log emptied") || !strings.Contains(r.stdout, "request logs and error logs are kept") {
		t.Fatalf("logs clear: %s", r)
	}
	if info, err := os.Stat(filepath.Join(c.logsDir(), "main.log")); err != nil || info.Size() != 0 {
		t.Fatalf("main.log after logs clear: %v %v", info, err)
	}
	if r := runCLI(t, env, "logs", "request", trace); r.code != 0 {
		t.Fatalf("the request log after logs clear: %s", r)
	}
	if got := c.errorLogs(t).Files; len(got) != 1 {
		t.Fatalf("error logs after logs clear: %v", got)
	}
}
