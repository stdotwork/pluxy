package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runAgainst runs the command against srv with the management key "k"; terminal says whether
// standard output is a terminal.
func runAgainst(srv *httptest.Server, terminal bool, stdout io.Writer, args ...string) (string, int) {
	old := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return terminal }
	defer func() { stdoutIsTerminal = old }()
	var errOut bytes.Buffer
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	code := runCLI(args, stdout, &errOut, env)
	return errOut.String(), code
}

func runLogs(srv *httptest.Server, terminal bool, args ...string) (string, string, int) {
	var out bytes.Buffer
	errOut, code := runAgainst(srv, terminal, &out, args...)
	return out.String(), errOut, code
}

const mgmt = "/v0/management"

func TestLogsErrorsList(t *testing.T) {
	files := `{"files":[{"name":"error-v1-chat-2026-11-20T010203-aaaa.log","size":1234,"modified":1795222923},{"name":"error-v1-chat-2026-11-19T010203-bbbb.log","size":7,"modified":1795136523}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mgmt+"/request-error-logs" || r.URL.RawQuery != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(files + "\n"))
	}))
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "logs", "errors")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if code != 0 || errOut != "" || len(lines) != 3 || strings.Fields(lines[0])[0] != "NAME" ||
		len(strings.Fields(lines[0])) != 3 || !strings.HasPrefix(lines[1], "error-v1-chat-2026-11-20T010203-aaaa.log") ||
		strings.Fields(lines[1])[1] != "1234" || strings.Fields(lines[2])[1] != "7" {
		t.Errorf("logs errors: %d %q %q, want a table of two files with their sizes", code, out, errOut)
	}
	// 1795222923 is this moment, in seconds; the table shows it in local time.
	if want := time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC).Local().Format(time.DateTime); len(lines) != 3 || !strings.Contains(lines[1], want) {
		t.Errorf("logs errors: %q, want the first file's time %s", out, want)
	}
	if out, _, code := runLogs(srv, false, "logs", "errors", "--json"); code != 0 || out != files+"\n" {
		t.Errorf("logs errors --json: %d %q, want the body unchanged", code, out)
	}
}

func TestLogsErrorsListEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"files":[]}`)) }))
	defer srv.Close()
	if out, errOut, code := runLogs(srv, false, "logs", "errors"); code != 0 || out != "no error logs\n" || errOut != "" {
		t.Errorf("logs errors with none: %d %q %q, want the line \"no error logs\"", code, out, errOut)
	}
}

// logServer answers the two download routes with body, and records the queries it saw.
func logServer(t *testing.T, body func(w http.ResponseWriter), queries *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*queries = append(*queries, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == mgmt+"/request-error-logs/error-a.log", r.URL.Path == mgmt+"/request-log-by-id/trace-1":
			if r.URL.Query().Get("format") == "json" {
				w.Write([]byte(`{"name":"x","size":2,"content":"hi"}` + "\n"))
				return
			}
			body(w)
		case r.URL.Path == mgmt+"/request-error-logs/error-big.log":
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			w.Write([]byte(`{"error":{"code":"log_too_large","message":"JSON log content exceeds 2 MiB; use the plain download"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"no log file of that name"}}`))
		}
	}))
}

func TestLogsDownloads(t *testing.T) {
	var queries []string
	srv := logServer(t, func(w http.ResponseWriter) { w.Write([]byte("line one\nline two\n")) }, &queries)
	defer srv.Close()
	for _, args := range [][]string{{"logs", "errors", "error-a.log"}, {"logs", "request", "trace-1"}} {
		queries = nil
		if out, errOut, code := runLogs(srv, false, args...); code != 0 || out != "line one\nline two\n" || errOut != "" {
			t.Errorf("%v: %d %q %q, want the file's text", args, code, out, errOut)
		}
		if len(queries) != 1 || strings.Contains(queries[0], "format") {
			t.Errorf("%v asked %v, want the plain download", args, queries)
		}
		queries = nil
		if out, _, code := runLogs(srv, false, append(args, "--json")...); code != 0 || out != `{"name":"x","size":2,"content":"hi"}`+"\n" {
			t.Errorf("%v --json: %d %q, want the JSON reply unchanged", args, code, out)
		}
		if len(queries) != 1 || !strings.HasSuffix(queries[0], "?format=json") {
			t.Errorf("%v --json asked %v, want format=json", args, queries)
		}
	}
	// 413 reaches the person, with core's words, in text and in JSON.
	if out, errOut, code := runLogs(srv, false, "logs", "errors", "error-big.log", "--json"); code != 1 || out != "" || !strings.Contains(errOut, "log_too_large") || !strings.Contains(errOut, "2 MiB") {
		t.Errorf("a log over the JSON limit: %d %q %q, want core's error", code, out, errOut)
	}
	if out, errOut, code := runLogs(srv, false, "logs", "request", "nope"); code != 5 || out != "" || !strings.Contains(errOut, "no log file of that name") {
		t.Errorf("an unknown trace ID: %d %q %q, want 5 and core's message", code, out, errOut)
	}
	// A name or ID that needs escaping stays one path part.
	queries = nil
	runLogs(srv, false, "logs", "request", "a/b c")
	if len(queries) != 1 || !strings.HasPrefix(queries[0], mgmt+"/request-log-by-id/a%2Fb%20c?") {
		t.Errorf("an ID with a slash and a space asked %v, want it escaped", queries)
	}
}

func TestLogsArguments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"files":[]}`)) }))
	defer srv.Close()
	for _, args := range [][]string{{"logs", "request"}, {"logs", "request", "a", "b"}, {"logs", "errors", "a", "b"}, {"logs", "clear", "x"}, {"logs", "bogus"}, {"usage", "export", "x"}} {
		if out, errOut, code := runLogs(srv, false, args...); code != 2 || out != "" || !strings.Contains(errOut, "usage") {
			t.Errorf("%v: %d %q %q, want a usage error", args, code, out, errOut)
		}
	}
	var help bytes.Buffer
	if code := runCLI([]string{"help"}, &help, io.Discard, func(string) string { return "" }); code != 0 {
		t.Fatalf("help: %d", code)
	}
	for _, want := range []string{"logs errors [name]", "logs request <id>", "logs clear", "usage export"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("help does not list %q:\n%s", want, help.String())
		}
	}
}

// What a logged request holds comes from clients and upstreams: on a terminal its control bytes
// are shown, not sent; piped, the bytes are exact.
func TestLogsTerminalEscaping(t *testing.T) {
	raw := "ok\tcol\nred \x1b[31mx\x1b]0;title\x07 cr\r bs\b nul\x00 del\x7f c1\u009b \xff bad é \\x1b end"
	var queries []string
	// The file arrives in pieces that cut the two-byte é and the C1 character in half.
	srv := logServer(t, func(w http.ResponseWriter) {
		b := []byte(raw)
		for len(b) > 0 {
			n := min(3, len(b))
			w.Write(b[:n])
			w.(http.Flusher).Flush()
			b = b[n:]
		}
	}, &queries)
	defer srv.Close()
	want := "ok\tcol\nred \\x1b[31mx\\x1b]0;title\\x07 cr\\x0d bs\\x08 nul\\x00 del\\x7f c1\\u009b \\xff bad é \\x1b end"
	for _, args := range [][]string{{"logs", "errors", "error-a.log"}, {"logs", "request", "trace-1"}} {
		if out, _, code := runLogs(srv, true, args...); code != 0 || out != want {
			t.Errorf("%v on a terminal: %d\n got %q\nwant %q", args, code, out, want)
		}
		if out, _, code := runLogs(srv, false, args...); code != 0 || out != raw {
			t.Errorf("%v piped: %d\n got %q\nwant the exact bytes %q", args, code, out, raw)
		}
	}
	// A file that ends inside a character still shows every byte.
	srv2 := logServer(t, func(w http.ResponseWriter) { w.Write([]byte("a\xc3")) }, &queries)
	defer srv2.Close()
	if out, _, code := runLogs(srv2, true, "logs", "request", "trace-1"); code != 0 || out != "a\\xc3" {
		t.Errorf("a file ending inside a character on a terminal: %d %q, want a\\xc3", code, out)
	}
}

// A file above the 64 MiB limit of the client's ordinary calls arrives whole.
func TestLogsDownloadAbove64MiB(t *testing.T) {
	const size = 64<<20 + 3<<20 + 1
	var queries []string
	srv := logServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		chunk := bytes.Repeat([]byte("0123456789abcdef"), 4096)
		for n := 0; n < size; n += len(chunk) {
			w.Write(chunk[:min(len(chunk), size-n)])
		}
	}, &queries)
	defer srv.Close()
	var n countingWriter
	errOut, code := runAgainst(srv, false, &n, "logs", "request", "trace-1")
	if code != 0 || errOut != "" || n.n != size {
		t.Errorf("a %d byte log: exit %d, %d bytes written, stderr %q; want all of it", size, code, n.n, errOut)
	}
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// A download that breaks off is an error, however much of it arrived.
func TestLogsDownloadBrokenMidway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("first part\n"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "logs", "request", "trace-1")
	if code == 0 || !strings.HasPrefix(errOut, "pluxy: ") || out != "first part\n" {
		t.Errorf("a download cut short: exit %d, stdout %q, stderr %q; want a non-zero exit and an error line", code, out, errOut)
	}
}

// A closed pipe stops the command with an error, not a hang or a success.
func TestLogsDownloadWriteError(t *testing.T) {
	var queries []string
	srv := logServer(t, func(w http.ResponseWriter) { w.Write([]byte("x")) }, &queries)
	defer srv.Close()
	errOut, code := runAgainst(srv, false, errWriter{errors.New("broken pipe")}, "logs", "request", "trace-1")
	if code != 1 || !strings.Contains(errOut, "broken pipe") {
		t.Errorf("stdout failing: exit %d, stderr %q, want 1 and the writer's error", code, errOut)
	}
}

type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLogsClear(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == mgmt+"/logs":
			w.Write([]byte(`{"removed":2}` + "\n"))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "logs", "clear")
	if code != 0 || errOut != "" || !strings.Contains(out, "main.log emptied") || !strings.Contains(out, "2 rotated log files deleted") ||
		!strings.Contains(out, "request logs and error logs are kept") {
		t.Errorf("logs clear: %d %q %q, want what was done and that request logs stay", code, out, errOut)
	}
	if out, _, code := runLogs(srv, false, "logs", "clear", "--json"); code != 0 || out != `{"removed":2}`+"\n" {
		t.Errorf("logs clear --json: %d %q, want the body unchanged", code, out)
	}
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"removed":1}`)) }))
	defer one.Close()
	if out, _, _ := runLogs(one, false, "logs", "clear"); !strings.Contains(out, "1 rotated log file deleted") {
		t.Errorf("logs clear with one rotated file: %q, want the singular", out)
	}
	none := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"removed":0}`)) }))
	defer none.Close()
	if out, _, _ := runLogs(none, false, "logs", "clear"); !strings.Contains(out, "0 rotated log files deleted") || strings.Contains(out, "no rotated") {
		t.Errorf("logs clear that deleted none: %q, want the count: core gives 0 also when a delete failed", out)
	}
	off := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"invalid_request","message":"logging to file is off"}}`))
	}))
	defer off.Close()
	if out, errOut, code := runLogs(off, false, "logs", "clear"); code != 1 || out != "" || !strings.Contains(errOut, "logging to file is off") {
		t.Errorf("logs clear while logging is off: %d %q %q, want 1 and core's reason", code, out, errOut)
	}
}

// usageServer answers GET /usage/events with pages[after], and records each call's query.
func usageServer(pages map[string]string, queries *[]url.Values) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != mgmt+"/usage/events" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		*queries = append(*queries, r.URL.Query())
		body, ok := pages[r.URL.Query().Get("after")]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(body))
	}))
}

const (
	usagePage1 = `{"enabled":true,"events":[` +
		`{"seq":1,"id":"t1","timestamp":"2026-11-20T01:02:03Z","model":"gpt-4o","provider":"pluxy/openai-compatible","account":"acc_1","key_id":"k1","failed":false,"status":200,"latency_ms":120,"tokens":{"input_tokens":10,"output_tokens":5,"total_tokens":15}},` +
		`{"seq":2,"timestamp":"2026-11-20T01:02:04.5+02:00","model":"=SUM(A1)","account":"a\"b,c","failed":true,"latency_ms":7,"tokens":{}}` +
		`],"next":2,"more":true,"lost":false}`
	usagePage2 = `{"enabled":true,"events":[` +
		`{"seq":3,"id":"t3","timestamp":"2026-11-20T01:02:05Z","model":"  -x","provider":"p","account":"line\nbreak","key_id":"+k","failed":false,"status":429,"latency_ms":1,"tokens":{"total_tokens":0}}` +
		`],"next":3,"more":false,"lost":false}`
	// Newest first, as the panel's export is; every line ends in CRLF.
	usageCSV = "Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID\r\n" +
		"2026-11-20T01:02:05Z,'  -x,p,\"line\nbreak\",'+k,false,429,1,0,t3\r\n" +
		"2026-11-20T01:02:04.5+02:00,'=SUM(A1),,\"a\"\"b,c\",,true,,7,,\r\n" +
		"2026-11-20T01:02:03Z,gpt-4o,pluxy/openai-compatible,acc_1,k1,false,200,120,15,t1\r\n"
)

func TestUsageExport(t *testing.T) {
	var queries []url.Values
	srv := usageServer(map[string]string{"0": usagePage1, "2": usagePage2}, &queries)
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "usage", "export", "--since", "7d", "--until", "2026-11-21T00:00:00Z",
		"--model", "gpt-4o", "--account", "acc_1", "--provider", "p", "--client-key", "k1", "--failed", "false")
	if code != 0 || out != usageCSV {
		t.Errorf("usage export: %d\n got %q\nwant %q", code, out, usageCSV)
	}
	if errOut != "" {
		t.Errorf("usage export warned %q although core lost nothing", errOut)
	}
	if len(queries) != 2 {
		t.Fatalf("usage export made %d calls, want 2", len(queries))
	}
	for i, after := range []string{"0", "2"} {
		q := queries[i]
		if q.Get("after") != after || q.Get("limit") != "1000" || q.Get("since") != "7d" || q.Get("until") != "2026-11-21T00:00:00Z" ||
			q.Get("model") != "gpt-4o" || q.Get("account") != "acc_1" || q.Get("provider") != "p" || q.Get("key") != "k1" || q.Get("failed") != "false" {
			t.Errorf("call %d asked %v", i+1, q)
		}
	}
}

// Core's lost flag on any page means requests the export cannot include, as the panel's notice
// says: those gone before it began, and those pushed out between two of its pages.
func TestUsageExportSaysWhenOlderRequestsAreGone(t *testing.T) {
	const notice = "pluxy: some older requests are no longer available, so this export may not include them\n"
	lost := func(page string) string { return strings.Replace(page, `"lost":false`, `"lost":true`, 1) }
	for name, pages := range map[string]map[string]string{
		"first page": {"0": lost(usagePage1), "2": usagePage2},
		"later page": {"0": usagePage1, "2": lost(usagePage2)},
		"both, once": {"0": lost(usagePage1), "2": lost(usagePage2)},
	} {
		var queries []url.Values
		srv := usageServer(pages, &queries)
		out, errOut, code := runLogs(srv, false, "usage", "export")
		srv.Close()
		if code != 0 || out != usageCSV || errOut != notice {
			t.Errorf("%s: %d %q %q, want the CSV and the notice once", name, code, out, errOut)
		}
	}
}

// A recorded model or account name comes from a client: on a terminal its control bytes are
// shown, not sent, and a row ends in a plain newline. Piped, the CSV is exact.
func TestUsageExportEscapesOnATerminal(t *testing.T) {
	const page = `{"enabled":true,"events":[{"seq":1,"id":"t1","timestamp":"2026-11-20T01:02:03Z","model":"m\u001b]0;x\u0007","account":"a\r\nb","failed":false,"status":200,"latency_ms":1,"tokens":{}}],"next":1,"more":false,"lost":false}`
	const header = "Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID"
	var queries []url.Values
	srv := usageServer(map[string]string{"0": page}, &queries)
	defer srv.Close()
	want := header + "\n" + `2026-11-20T01:02:03Z,m\x1b]0;x\x07,,"a\x0d` + "\n" + `b",,false,200,1,,t1` + "\n"
	if out, errOut, code := runLogs(srv, true, "usage", "export"); code != 0 || errOut != "" || out != want {
		t.Errorf("usage export on a terminal: %d %q\n got %q\nwant %q", code, errOut, out, want)
	}
	piped := header + "\r\n2026-11-20T01:02:03Z,m\x1b]0;x\x07,,\"a\r\nb\",,false,200,1,,t1\r\n"
	if out, _, code := runLogs(srv, false, "usage", "export"); code != 0 || out != piped {
		t.Errorf("usage export piped: %d\n got %q\nwant %q", code, out, piped)
	}
}

func TestUsageExportOneEmptyPage(t *testing.T) {
	var queries []url.Values
	srv := usageServer(map[string]string{"0": `{"enabled":true,"events":[],"next":0,"more":false,"lost":false}`}, &queries)
	defer srv.Close()
	const header = "Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID\r\n"
	if out, errOut, code := runLogs(srv, false, "usage", "export"); code != 0 || out != header || errOut != "" {
		t.Errorf("usage export with no events: %d %q %q, want the header row only", code, out, errOut)
	}
}

func TestUsageExportRecordingOff(t *testing.T) {
	var queries []url.Values
	srv := usageServer(map[string]string{"0": `{"enabled":false,"events":[],"next":0,"more":false,"lost":false}`}, &queries)
	defer srv.Close()
	const header = "Time,Model,Provider,Account,Client key,Failed,Status,Latency,Tokens,Trace ID\r\n"
	out, errOut, code := runLogs(srv, false, "usage", "export")
	if code != 0 || out != header || !strings.Contains(errOut, "usage is not recorded") {
		t.Errorf("usage export with recording off: %d %q %q, want the header row and the note on standard error", code, out, errOut)
	}
}

func TestUsageExportRefusesJSON(t *testing.T) {
	var queries []url.Values
	srv := usageServer(nil, &queries)
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "usage", "export", "--json")
	if code != 2 || out != "" || !strings.Contains(errOut, "pluxy usage events --json") || len(queries) != 0 {
		t.Errorf("usage export --json: %d %q %q after %d calls, want a usage error naming usage events --json and no call", code, out, errOut, len(queries))
	}
}

// A core that says there is more but gives no later place to read from would loop for ever.
func TestUsageExportStopsWhenAPageDoesNotAdvance(t *testing.T) {
	var queries []url.Values
	srv := usageServer(map[string]string{"0": `{"enabled":true,"events":[],"next":0,"more":true,"lost":false}`}, &queries)
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "usage", "export")
	if code != 1 || out != "" || !strings.Contains(errOut, "did not advance") || len(queries) != 1 {
		t.Errorf("usage export of a page that does not advance: %d %q %q after %d calls, want an error", code, out, errOut, len(queries))
	}
}

func TestUsageExportError(t *testing.T) {
	var queries []url.Values
	srv := usageServer(map[string]string{"0": usagePage1}, &queries) // page "2" is a 500
	defer srv.Close()
	out, errOut, code := runLogs(srv, false, "usage", "export")
	if code != 1 || out != "" || !strings.HasPrefix(errOut, "pluxy: ") {
		t.Errorf("usage export failing on its second page: %d %q %q, want an error and no part of the CSV", code, out, errOut)
	}
}

// The encoding is the panel's csvField (ui/kit/csv.ts): a formula is neutralized before quoting, and
// leading space is the JavaScript \s set.
func TestCSVField(t *testing.T) {
	for in, want := range map[string]string{
		"plain":               "plain",
		"":                    "",
		"=1+1":                "'=1+1",
		"+1":                  "'+1",
		"-1":                  "'-1",
		"@x":                  "'@x",
		" =x":                 "' =x",
		"\u00a0=x":            "'\u00a0=x",
		"\ufeff-x":            "'\ufeff-x",
		"\u3000@x":            "'\u3000@x",
		"\u200b=x":            "\u200b=x", // zero width space is not \s
		"a=b":                 "a=b",
		"a,b":                 "\"a,b\"",
		"say \"hi\"":          "\"say \"\"hi\"\"\"",
		"two\nlines":          "\"two\nlines\"",
		"cr\rhere":            "\"cr\rhere\"",
		"=a,b":                "\"'=a,b\"",
		"\"=\"":               "\"\"\"=\"\"\"",
		"\t=x":                "'\t=x",
		"é,ü":                 "\"é,ü\"",
		"-":                   "'-",
		"0":                   "0",
		"1e5":                 "1e5",
		"\u2028\u2029=x":      "'\u2028\u2029=x",
		"\u180e=x":            "\u180e=x", // the Mongolian vowel separator left \s in ES2016
		"\u1680\u2000-x":      "'\u1680\u2000-x",
		"\u205f\u202f+x":      "'\u205f\u202f+x",
		"\v\f=x":              "'\v\f=x",
		"x\u00a0=y":           "x\u00a0=y",
		"\u0085=x":            "\u0085=x", // NEL is not \s either
		"\u200a=x":            "'\u200a=x",
		"\u2007\u2008@":       "'\u2007\u2008@",
		"\u2009\u2006\u2005-": "'\u2009\u2006\u2005-",
	} {
		if got := csvField(in); got != want {
			t.Errorf("csvField(%q) = %q, want %q", in, got, want)
		}
	}
}
