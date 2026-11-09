package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A long reply keeps its start and its end, with the bytes left out counted in between.
func TestCaptureKeepsHeadAndTail(t *testing.T) {
	c := (&RequestLog{All: true}).newCapture()
	head := bytes.Repeat([]byte("h"), loggedHead)
	middle := bytes.Repeat([]byte("m"), 3<<20)
	tail := append(bytes.Repeat([]byte("t"), loggedTail-3), "END"...)
	for _, p := range [][]byte{head, middle, tail} {
		for len(p) > 0 { // in uneven writes, as a stream arrives
			n := min(len(p), 70001)
			c.Write(p[:n])
			p = p[n:]
		}
	}
	got := c.bytes()
	want := string(head) + "\n[... 3145728 bytes not logged ...]\n" + string(tail)
	if string(got) != want {
		t.Fatalf("got %d bytes, starting %q and ending %q", len(got), got[:10], got[len(got)-10:])
	}
	short := (&RequestLog{}).newCapture()
	short.Write([]byte("short reply"))
	if string(short.bytes()) != "short reply" {
		t.Fatalf("short: %q", short.bytes())
	}
}

func TestLogFileName(t *testing.T) {
	at := time.Date(2026, 11, 9, 12, 0, 5, 0, time.UTC)
	if got := logFileName("/v1beta/models/gemini-2.5:generateContent", at, "20261109120005-ab"); got != "v1beta-models-gemini-2.5-generateContent-2026-11-09T120005-20261109120005-ab.log" {
		t.Fatalf("got %q", got)
	}
	if got := logFileName("/"+strings.Repeat("a", 300), at, "x"); len(got) > 140 {
		t.Fatalf("long path: %d bytes", len(got))
	}
	for name, want := range map[string]bool{"error-v1-x.log": true, "v1-x.log": false, "error-x.txt": false, `error-a\b.log`: false, "error-../a.log": false} {
		if IsErrorLogName(name) != want {
			t.Errorf("IsErrorLogName(%q) != %v", name, want)
		}
	}
}

// Only a request log's name, ending in a trace ID, is found by one; other log files are not.
func TestFindRequestLog(t *testing.T) {
	dir := t.TempDir()
	const id = "20261109120000-0123456789abcdef"
	for _, n := range []string{"notes-abc.log", "main-abc.log", "v1-chat-completions-2026-11-09T120000-" + id + ".log"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := FindRequestLog(dir, "abc"); got != "" {
		t.Fatalf("abc found %q", got)
	}
	if got := FindRequestLog(dir, id); got != "v1-chat-completions-2026-11-09T120000-"+id+".log" {
		t.Fatalf("trace ID found %q", got)
	}
}
