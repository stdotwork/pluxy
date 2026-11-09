package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Core's log files: logging-to-file through the settings call, the lines read back with the
// logs calls, rotation of main.log and the cap on the logs folder.

type logLines struct {
	Lines  []string `json:"lines"`
	Count  int      `json:"line-count"`
	Latest float64  `json:"latest-timestamp"`
}

func (c *mcore) logs(t *testing.T, query string) logLines {
	t.Helper()
	var l logLines
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/logs"+query, nil), &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func (c *mcore) logsDir() string { return filepath.Join(filepath.Dir(c.path), "logs") }

var logLine = regexp.MustCompile(`^\[\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3}\] \[(info |warn )\] `)

func TestLogFiles(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			// Off by default: no folder, and the logs calls say why.
			if _, err := os.Stat(c.logsDir()); !os.IsNotExist(err) {
				t.Fatalf("logs folder exists with logging to file off: %v", err)
			}
			for _, m := range []string{http.MethodGet, http.MethodDelete} {
				if code, b := c.call(t, m, "/logs", nil); code != http.StatusBadRequest || !strings.Contains(string(b), "logging to file is off") {
					t.Fatalf("%s /logs while off: %d %s", m, code, b)
				}
			}

			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"logging-to-file": true, "logs-max-total-size-mb": 50}}}`)
			var s struct {
				Observability struct {
					Logs struct {
						ToFile bool `json:"logging-to-file"`
						MaxMB  int  `json:"logs-max-total-size-mb"`
					} `json:"logs"`
				} `json:"observability"`
			}
			json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/settings", nil), &s)
			if !s.Observability.Logs.ToFile || s.Observability.Logs.MaxMB != 50 {
				t.Fatalf("settings: %+v", s)
			}
			want := `"to_file": true`
			if f.yaml {
				want = "logging-to-file: true"
			}
			if file := c.file(t); !strings.Contains(file, want) {
				t.Fatalf("config file has no %q:\n%s", want, file)
			}
			if _, err := os.Stat(filepath.Join(c.logsDir(), "main.log")); err != nil {
				t.Fatalf("main.log: %v", err)
			}

			// A refused request writes a failure line, with the client key nowhere in it.
			if code, _, _ := chatReply(t, c.base, clientKey, false, "X-Pluxy-Account", "nobody"); code != http.StatusNotFound {
				t.Fatalf("pinned to no account: %d", code)
			}
			l := waitLog(t, c, "", "does not exist or does not serve model")
			for _, line := range l.Lines {
				if !logLine.MatchString(line) {
					t.Errorf("line not in today's format: %q", line)
				}
			}
			raw, _ := os.ReadFile(filepath.Join(c.logsDir(), "main.log"))
			wantNoSecrets(t, "main.log", string(raw), clientKey, secondClientKey, upstreamKey, managementKey)

			// after reads only newer lines; limit keeps the newest.
			if again := c.logs(t, "?after="+strconv.FormatFloat(l.Latest, 'f', 3, 64)); again.Count != 0 || again.Latest != l.Latest {
				t.Fatalf("after latest: %+v", again)
			}
			chatReply(t, c.base, clientKey, false, "X-Pluxy-Account", "nobody-else")
			newer := waitLog(t, c, "?after="+strconv.FormatFloat(l.Latest, 'f', 3, 64), "nobody-else")
			if slices.ContainsFunc(newer.Lines, func(s string) bool { return strings.Contains(s, `"nobody"`) }) {
				t.Fatalf("after returned an older line: %v", newer.Lines)
			}
			if one := c.logs(t, "?limit=1"); one.Count != 1 || !strings.Contains(one.Lines[0], "nobody-else") {
				t.Fatalf("limit 1: %+v", one)
			}
			for _, bad := range []string{"?after=-1", "?after=x", "?limit=0", "?limit=10001"} {
				if code, b := c.call(t, http.MethodGet, "/logs"+bad, nil); code != http.StatusBadRequest {
					t.Errorf("%s: %d %s", bad, code, b)
				}
			}

			// Clearing empties main.log and keeps logging.
			var cleared struct {
				Removed *int `json:"removed"`
			}
			json.Unmarshal(c.expect(t, http.StatusOK, http.MethodDelete, "/logs", nil), &cleared)
			if cleared.Removed == nil || *cleared.Removed != 0 {
				t.Fatalf("clear: %+v", cleared)
			}
			if after := c.logs(t, ""); after.Count != 0 {
				t.Fatalf("after clear: %v", after.Lines)
			}
			chatReply(t, c.base, clientKey, false, "X-Pluxy-Account", "third")
			waitLog(t, c, "", "third")

			// Off again: no more lines, and the calls refuse.
			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"logging-to-file": false}}}`)
			if code, _ := c.call(t, http.MethodGet, "/logs", nil); code != http.StatusBadRequest {
				t.Fatalf("GET /logs after turning off: %d", code)
			}
			if code, b := c.call(t, http.MethodPatch, "/settings", `{"observability": {"logs": {"logs-max-total-size-mb": -1}}}`); code != http.StatusBadRequest {
				t.Fatalf("negative cap: %d %s", code, b)
			}
		})
	}
}

// waitLog reads the log until a line holds s, and returns that read.
func waitLog(t *testing.T, c *mcore, query, s string) logLines {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l := c.logs(t, query)
		if slices.ContainsFunc(l.Lines, func(line string) bool { return strings.Contains(line, s) }) {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line with %q in %v", s, l.Lines)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// main.log that reaches 10 MB is rotated by the real core, and the folder's cap removes the
// oldest log files, never main.log.
func TestLogFileRotationAndCap(t *testing.T) {
	c := startManaged(t, false)
	dir := c.logsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A main.log at the size limit from an earlier run, and an old request log.
	big := strings.Repeat("[2026-11-08 10:00:00.000] [info ] filler\n", 10<<20/41+1)
	if err := os.WriteFile(filepath.Join(dir, "main.log"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "v1-chat-completions-old.log")
	os.WriteFile(old, []byte("an old request log\n"), 0o600)
	os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))

	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"logging-to-file": true}}}`)
	chatReply(t, c.base, clientKey, false, "X-Pluxy-Account", "nobody")
	waitLog(t, c, "", "does not serve model")
	var rotated []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "main-") {
			rotated = append(rotated, e.Name())
		}
	}
	if len(rotated) != 1 {
		t.Fatalf("rotated files %v in %v", rotated, entries)
	}
	if info, _ := os.Stat(filepath.Join(dir, "main.log")); info.Size() > 1<<20 {
		t.Fatalf("main.log is %d bytes after rotation", info.Size())
	}
	// The newest lines read across both files.
	if l := c.logs(t, "?limit=10000"); l.Count != 10000 || !strings.Contains(l.Lines[0], "filler") ||
		!strings.Contains(strings.Join(l.Lines[l.Count-5:], "\n"), "does not serve model") {
		t.Fatalf("across files: %d lines, first %q", l.Count, l.Lines[0])
	}

	// A 1 MB cap removes the old request log first, then the rotated file; main.log stays.
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"logs-max-total-size-mb": 1}}}`)
	entries, _ = os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if !slices.Equal(left, []string{"main.log"}) {
		t.Fatalf("after a 1 MB cap: %v", left)
	}
}
