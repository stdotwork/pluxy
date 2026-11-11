package e2e

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// One access line per client API request: its status, latency, client, method and path, and the
// trace ID and account core logged for it, with no query string. A reply of 400 or more is a
// warning line, which the log's level mechanism marks by the "warning: " prefix.

var accessLine = regexp.MustCompile(`^(warning: )?(\d{3}) \| (\S+) \| (\S+) \| ([A-Z]+) (\S+) \| (\S+) \| (\S+)$`)

// accessEntry is one access line, split into its fields.
type accessEntry struct {
	warn                    bool
	status, latency, client string
	method, path, trace     string
	account                 string
}

// accessLines returns the access lines core logged so far, with standard error's "pluxy: "
// prefix off.
func accessLines(l *coreLog) []accessEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []accessEntry
	for _, line := range l.lines {
		m := accessLine.FindStringSubmatch(strings.TrimPrefix(line, "pluxy: "))
		if m == nil {
			continue
		}
		out = append(out, accessEntry{warn: m[1] != "", status: m[2], latency: m[3], client: m[4],
			method: m[5], path: m[6], trace: m[7], account: m[8]})
	}
	return out
}

// accessLinesSettled waits for n access lines, then checks that a later read has the same n:
// one line per request, never two.
func accessLinesSettled(t *testing.T, l *coreLog, n int) []accessEntry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(accessLines(l)) < n {
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %d access lines, have %+v", n, accessLines(l))
		}
		time.Sleep(20 * time.Millisecond)
	}
	settle := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(settle) {
		time.Sleep(20 * time.Millisecond)
	}
	got := accessLines(l)
	if len(got) != n {
		t.Fatalf("want %d access lines, have %d: %+v", n, len(got), got)
	}
	return got
}

func TestAccessLog(t *testing.T) {
	u := answeringUpstream(t)
	base, logs := startCoreLogged(t, u, func(cfg map[string]any) { cfg["management_key"] = managementKey })
	const secret = "SECRET-query-key-e2e"

	// A request that works: its line has the reply's trace ID and the account it went to.
	code, trace, body := chatReply(t, base, clientKey, false)
	if code != http.StatusOK || trace == "" {
		t.Fatalf("chat: status %d, trace %q: %s", code, trace, body)
	}
	// A request with a wrong client key is refused before routing: no trace, no account.
	if code, replyTrace, _ := chatReply(t, base, "wrong-key", false); code != http.StatusUnauthorized || replyTrace != "" {
		t.Fatalf("wrong key: status %d, trace %q", code, replyTrace)
	}
	// The query string never reaches the log; Gemini clients put their client key in ?key=.
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions?key="+secret,
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a request with a query key: status %d", resp.StatusCode)
	}

	lines := accessLinesSettled(t, logs, 3)
	statuses := map[string]int{}
	for _, e := range lines {
		statuses[e.status]++
		if e.method != http.MethodPost || e.path != "/v1/chat/completions" {
			t.Errorf("an access line is not the chat request: %+v", e)
		}
		if e.client != "127.0.0.1" {
			t.Errorf("client in %+v", e)
		}
		if _, err := time.ParseDuration(e.latency); err != nil {
			t.Errorf("latency %q in %+v: %v", e.latency, e, err)
		}
		switch e.status {
		case "200":
			if e.warn {
				t.Errorf("a 200 line is a warning: %+v", e)
			}
			if e.account != "upstream-1" {
				t.Errorf("account in %+v", e)
			}
			if e.trace == "-" {
				t.Errorf("a 200 line has no trace ID: %+v", e)
			}
		case "401":
			if !e.warn {
				t.Errorf("a 401 line is not a warning: %+v", e)
			}
			if e.trace != "-" || e.account != "-" {
				t.Errorf("the refused line's trace and account: %+v", e)
			}
		default:
			t.Errorf("unexpected status: %+v", e)
		}
	}
	if statuses["200"] != 2 || statuses["401"] != 1 {
		t.Fatalf("statuses %v in %+v", statuses, lines)
	}
	matched := false
	for _, e := range lines {
		matched = matched || e.trace == trace
	}
	if !matched {
		t.Fatalf("no access line has the reply's trace ID %q: %+v", trace, lines)
	}

	// /healthz and the management API are not client API requests: neither leaves a line.
	hr, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, hr.Body)
	hr.Body.Close()
	if hr.StatusCode != http.StatusOK {
		t.Fatalf("/healthz: status %d", hr.StatusCode)
	}
	if code, b := mcall(t, base, managementKey, http.MethodGet, "/settings", nil); code != http.StatusOK {
		t.Fatalf("GET /settings: status %d: %s", code, b)
	}
	for _, e := range accessLinesSettled(t, logs, 3) {
		if e.path == "/healthz" || strings.HasPrefix(e.path, "/v0/management/") {
			t.Fatalf("a non-client request left an access line: %+v", e)
		}
	}

	// The query string is nowhere in the log, not even where the level prefix would not hide it.
	logs.mu.Lock()
	all := strings.Join(logs.lines, "\n")
	logs.mu.Unlock()
	if strings.Contains(all, "SECRET") {
		t.Fatalf("the log holds the query string's secret:\n%s", all)
	}
}
