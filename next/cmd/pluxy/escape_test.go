package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runWithTerminals runs a CLI command against srv with independent terminal flags for stdout and stderr.
func runWithTerminals(srv *httptest.Server, stdoutTerm, stderrTerm bool, stdin io.Reader, args ...string) (string, string, int) {
	oldOut, oldErr := stdoutIsTerminal, stderrIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return stdoutTerm }
	stderrIsTerminal = func(io.Writer) bool { return stderrTerm }
	defer func() {
		stdoutIsTerminal = oldOut
		stderrIsTerminal = oldErr
	}()
	var out, errOut bytes.Buffer
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	code := runCLIWithInput(args, stdin, &out, &errOut, env, false)
	return out.String(), errOut.String(), code
}

// foreignSample contains ESC (CSI + OSC), newline, tab, and a C1 control (U+009B).
const foreignSample = "x\x1b]0;title\x07\x1b[31m\n\t\u009by"
const foreignCellEscaped = `x\x1b]0;title\x07\x1b[31m\x0a\x09\u009by`
const foreignMultiEscaped = "x\\x1b]0;title\\x07\\x1b[31m\n\t\\u009by"

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal(%q): %v", s, err)
	}
	return string(b)
}

func assertNoRawControls(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, "\x1b") {
		t.Errorf("%s contains raw ESC:\n%q", label, s)
	}
	if strings.Contains(s, "\u009b") {
		t.Errorf("%s contains raw C1 control:\n%q", label, s)
	}
	if strings.Contains(s, "\x07") {
		t.Errorf("%s contains raw BEL:\n%q", label, s)
	}
}

// TestTableCommandsTerminalEscaping checks that every table command printing foreign text escapes
// ESC, newline, tab, and C1 controls on a terminal without adding rows or columns, while keeping
// piped and --json output exact.
func TestTableCommandsTerminalEscaping(t *testing.T) {
	q := mustJSONString(t, foreignSample)

	cases := []struct {
		name     string
		args     []string
		body     string
		wantRows int // total lines in terminal output (header + data rows + any preamble lines)
	}{
		{
			name: "usage by model",
			args: []string{"usage"},
			body: `{"enabled":true,"by":"model","requests":1,"failure":0,"tokens":{"input_tokens":1,"output_tokens":2},"breakdown":[{"name":` + q + `,"requests":1,"failure":0,"tokens":{"input_tokens":1,"output_tokens":2}}]}` + "\n",
			// summary line + header + 1 row
			wantRows: 3,
		},
		{
			name: "usage by account",
			args: []string{"usage", "--by", "account"},
			body: `{"enabled":true,"by":"account","requests":1,"failure":0,"tokens":{"input_tokens":1,"output_tokens":2},"breakdown":[{"name":` + q + `,"provider":` + q + `,"requests":1,"failure":0,"tokens":{"input_tokens":1,"output_tokens":2}}]}` + "\n",
			// summary line + header + 1 row
			wantRows: 3,
		},
		{
			name:     "account list",
			args:     []string{"account", "list"},
			body:     `{"accounts":[{"id":"acc-1","public_id":"pub-1","label":` + q + `,"extension":"pluxy/codex","source":"oauth","health":{"state":"cooling","why":` + q + `}}]}` + "\n",
			wantRows: 2,
		},
		{
			name: "account models",
			args: []string{"account", "models", "acc-1"},
			body: `{"from":` + q + `,"models":[{"id":` + q + `,"names":[` + q + `],"owned_by":` + q + `}]}` + "\n",
			// "from: ..." + header + 1 row
			wantRows: 3,
		},
		{
			name:     "model list",
			args:     []string{"model", "list"},
			body:     `{"models":[{"id":` + q + `,"extension":"pluxy/codex","upstream":` + q + `,"from":"live","accounts":["acc-1"]}]}` + "\n",
			wantRows: 2,
		},
		{
			name:     "model refresh",
			args:     []string{"model", "refresh"},
			body:     `{"accounts":[{"account":"acc-1","models":0,"error":` + q + `,"kept":false},{"account":"acc-2","models":2,"error":` + q + `,"kept":true}],"models":[]}` + "\n",
			wantRows: 3,
		},
		{
			name:     "ext list",
			args:     []string{"ext", "list"},
			body:     `{"packages":[{"id":"pluxy/codex","version":` + q + `,"enabled":true,"state":"crashed","error":` + q + `,"registry":` + q + `}]}` + "\n",
			wantRows: 2,
		},
		{
			name: "status",
			args: []string{"status"},
			body: `{"version":"0.1","active":{"revision":1},"desired":{"revision":2,"error":` + q + `},"extensions":[{"id":"pluxy/codex","version":` + q + `,"state":"crashed","last_error":` + q + `,"generation":1,"current":true,"active_requests":0}]}` + "\n",
			// buildinfo + config revision line + header + 1 row
			wantRows: 4,
		},
		{
			name:     "ext available",
			args:     []string{"ext", "available"},
			body:     `{"registries":[{"name":` + q + `,"error":` + q + `,"packages":[{"id":"pluxy/codex","title":` + q + `,"kind":` + q + `,"tier":` + q + `,"releases":[{"version":` + q + `}]}]}]}` + "\n",
			wantRows: 3, // header + error row + package row
		},
		{
			name: "theme list",
			args: []string{"theme", "list"},
			body: `{"current":` + q + `,"themes":[{"id":"my/t","name":` + q + `,"bytes":42}]}` + "\n",
			// "current: ..." + header + 1 row
			wantRows: 3,
		},
		{
			name: "account quota",
			args: []string{"account", "quota", "acc-1"},
			body: `{"quota":{"plan":` + q + `,"limits":[{"id":"req","label":` + q + `,"unit":` + q + `,"used":1,"limit":10}]}}` + "\n",
			// "plan: ..." + blank line + header + 1 row
			wantRows: 4,
		},
		{
			name:     "account methods",
			args:     []string{"account", "methods"},
			body:     `{"extensions":[{"extension":"pluxy/codex","methods":[{"method":` + q + `,"kind":"input","label":` + q + `,"inputs":[{"name":` + q + `}]}]}]}` + "\n",
			wantRows: 2,
		},
		{
			name: "setup",
			args: []string{"setup"},
			body: `{"first_run":false,"found":{"core":{"listen":"127.0.0.1:8317"},"clients":[{"id":"pi","installed":true,"version":` + q + `,"state":"ok","config":` + q + `}],"sign_ins":[{"provider":"codex","from":"local","label":` + q + `}]},"picks":[{"id":"pluxy/codex","kind":"provider","title":` + q + `,"installed":false,"suggested":false,"because":` + q + `}]}` + "\n",
			// summary + blank + "Clients:" + header + row + blank + "Sign-ins:" + header + row + blank + "Picks:" + header + row
			wantRows: 13,
		},
		{
			name:     "setup apply",
			args:     []string{"setup", "apply", "--pick", "pluxy/codex"},
			body:     `{"results":[{"id":"pluxy/codex","status":"ok","package":{"version":` + q + `,"state":"running"}}]}` + "\n",
			wantRows: 2,
		},
		{
			name: "import cpa",
			args: []string{"import", "cpa", "/tmp/cpa.yaml"},
			body: `{"brought":[{"what":"account","name":` + q + `,"as":"acc-1"}],"changed":[{"what":"setting","name":` + q + `,"from":` + q + `,"to":` + q + `,"why":` + q + `}],"not_brought":[{"what":"key","name":` + q + `,"why":` + q + `}],"needs":[{"pick":"pluxy/codex","why":` + q + `}]}` + "\n",
			// blank + Brought + header + row + blank + Changed + header + row + blank + Not brought + header + row + blank + Needs + header + row
			wantRows: 16,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == mgmt+"/setup" && tc.name == "setup apply" {
					w.Write([]byte(`{"first_run":false}`))
					return
				}
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			// Terminal: escaped, no raw controls, and embedded newline/tab cannot add a row.
			termOut, termErr, code := runWithTerminals(srv, true, false, nil, tc.args...)
			if code != 0 || termErr != "" {
				t.Fatalf("terminal: code=%d err=%q out=%q", code, termErr, termOut)
			}
			assertNoRawControls(t, tc.name+" terminal", termOut)
			if !strings.Contains(termOut, foreignCellEscaped) {
				t.Errorf("terminal output missing escaped cell %q:\n%q", foreignCellEscaped, termOut)
			}
			gotLines := strings.Split(strings.TrimSuffix(termOut, "\n"), "\n")
			if len(gotLines) != tc.wantRows {
				t.Errorf("terminal row count = %d, want %d (embedded newline added a row?)\n%s", len(gotLines), tc.wantRows, termOut)
			}

			// Piped: exact unescaped bytes reach stdout.
			pipeOut, pipeErr, code := runWithTerminals(srv, false, false, nil, tc.args...)
			if code != 0 || pipeErr != "" {
				t.Fatalf("piped: code=%d err=%q out=%q", code, pipeErr, pipeOut)
			}
			if !strings.Contains(pipeOut, "\x1b]0;title\x07\x1b[31m") || !strings.Contains(pipeOut, "\u009b") {
				t.Errorf("piped output should keep raw bytes unchanged:\n%q", pipeOut)
			}

			// --json: exact reply body on both terminal and pipe.
			jsonArgs := append(append([]string{}, tc.args...), "--json")
			for _, term := range []bool{false, true} {
				jOut, jErr, code := runWithTerminals(srv, term, term, nil, jsonArgs...)
				if code != 0 || jErr != "" || jOut != tc.body {
					t.Errorf("--json (term=%v): code=%d err=%q\n got %q\nwant %q", term, code, jErr, jOut, tc.body)
				}
			}
		})
	}
}

// TestMultilineCommandsTerminalEscaping checks config show and theme show: multi-line output uses
// escapeWriter on a terminal (preserving newlines and tabs, escaping ESC and C1), while piped and
// --json stay exact.
func TestMultilineCommandsTerminalEscaping(t *testing.T) {
	t.Run("config show", func(t *testing.T) {
		rawCfg := "listen: 127.0.0.1:8317\n# " + foreignSample + "\r\n"
		body := `{"content":` + mustJSONString(t, rawCfg) + `}` + "\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		defer srv.Close()

		wantTerm := "listen: 127.0.0.1:8317\n# " + foreignMultiEscaped + "\\x0d\n"
		if out, errOut, code := runWithTerminals(srv, true, false, nil, "config", "show"); code != 0 || errOut != "" || out != wantTerm {
			t.Errorf("config show terminal: code=%d err=%q\n got %q\nwant %q", code, errOut, out, wantTerm)
		}
		if out, errOut, code := runWithTerminals(srv, false, false, nil, "config", "show"); code != 0 || errOut != "" || out != rawCfg {
			t.Errorf("config show piped: code=%d err=%q\n got %q\nwant %q", code, errOut, out, rawCfg)
		}
		for _, term := range []bool{false, true} {
			if out, errOut, code := runWithTerminals(srv, term, term, nil, "config", "show", "--json"); code != 0 || errOut != "" || out != body {
				t.Errorf("config show --json (term=%v): code=%d err=%q\n got %q\nwant %q", term, code, errOut, out, body)
			}
		}
	})

	t.Run("theme show", func(t *testing.T) {
		// Theme JSON with a raw C1 control and a literal ESC byte (or control chars in JSON).
		rawTheme := "{\n\t\"name\": \"" + "x\x1b[31m\u009by" + "\"\n}"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(rawTheme))
		}))
		defer srv.Close()

		wantTerm := "{\n\t\"name\": \"x\\x1b[31m\\u009by\"\n}\n"
		if out, errOut, code := runWithTerminals(srv, true, false, nil, "theme", "show", "my/t"); code != 0 || errOut != "" || out != wantTerm {
			t.Errorf("theme show terminal: code=%d err=%q\n got %q\nwant %q", code, errOut, out, wantTerm)
		}
		if out, errOut, code := runWithTerminals(srv, false, false, nil, "theme", "show", "my/t"); code != 0 || errOut != "" || out != rawTheme+"\n" {
			t.Errorf("theme show piped: code=%d err=%q\n got %q\nwant %q", code, errOut, out, rawTheme+"\n")
		}
		for _, term := range []bool{false, true} {
			if out, errOut, code := runWithTerminals(srv, term, term, nil, "theme", "show", "my/t", "--json"); code != 0 || errOut != "" || out != rawTheme {
				t.Errorf("theme show --json (term=%v): code=%d err=%q\n got %q\nwant %q", term, code, errOut, out, rawTheme)
			}
		}
	})
}

// TestAccountTestTerminalEscaping checks both OK (stdout) and failed (stderr) outputs of account test.
func TestAccountTestTerminalEscaping(t *testing.T) {
	q := mustJSONString(t, foreignSample)

	t.Run("ok served model on stdout", func(t *testing.T) {
		body := `{"result":{"ok":true,"model":"gpt-4o","served_model":` + q + `,"latency_ms":12,"reply":` + q + `}}` + "\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		defer srv.Close()

		out, errOut, code := runWithTerminals(srv, true, false, nil, "account", "test", "acc-1")
		if code != 0 || errOut != "" {
			t.Fatalf("terminal: code=%d err=%q out=%q", code, errOut, out)
		}
		assertNoRawControls(t, "account test ok terminal", out)
		if !strings.HasPrefix(out, "ok: "+foreignCellEscaped+" answered in 12 ms: ") {
			t.Errorf("terminal stdout = %q, want escaped served model", out)
		}
		if lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(lines) != 1 {
			t.Errorf("terminal stdout has %d lines, want 1: %q", len(lines), out)
		}

		pipeOut, pipeErr, code := runWithTerminals(srv, false, false, nil, "account", "test", "acc-1")
		if code != 0 || pipeErr != "" || !strings.HasPrefix(pipeOut, "ok: "+foreignSample+" answered in 12 ms: ") {
			t.Errorf("piped stdout = %q (code=%d err=%q), want raw served model", pipeOut, code, pipeErr)
		}

		for _, term := range []bool{false, true} {
			jOut, jErr, code := runWithTerminals(srv, term, term, nil, "account", "test", "acc-1", "--json")
			if code != 0 || jErr != "" || jOut != body {
				t.Errorf("--json (term=%v): code=%d err=%q got %q want %q", term, code, jErr, jOut, body)
			}
		}
	})

	t.Run("failed error on stderr respects stderr terminal independently", func(t *testing.T) {
		body := `{"result":{"ok":false,"error":{"code":"upstream","message":` + q + `}}}` + "\n"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		defer srv.Close()

		// stdout is terminal, stderr is pipe -> stderr must NOT be escaped.
		_, errPipe, code := runWithTerminals(srv, true, false, nil, "account", "test", "acc-1")
		wantRaw := "pluxy: the test request failed: " + foreignSample + "\n"
		if code != 1 || errPipe != wantRaw {
			t.Errorf("stdout=tty stderr=pipe: code=%d\n got %q\nwant %q", code, errPipe, wantRaw)
		}

		// stdout is pipe, stderr is terminal -> stderr MUST be escaped.
		_, errTerm, code := runWithTerminals(srv, false, true, nil, "account", "test", "acc-1")
		wantEsc := "pluxy: the test request failed: " + foreignMultiEscaped + "\n"
		if code != 1 || errTerm != wantEsc {
			t.Errorf("stdout=pipe stderr=tty: code=%d\n got %q\nwant %q", code, errTerm, wantEsc)
		}
		assertNoRawControls(t, "account test failed stderr terminal", errTerm)
	})
}

// TestPrintErrorStderrTerminalEscaping checks printError on API errors: escapes only when stderr is
// a terminal and --json is false, independent of stdoutIsTerminal.
func TestPrintErrorStderrTerminalEscaping(t *testing.T) {
	q := mustJSONString(t, foreignSample)
	errBody := `{"error":{"code":"upstream_error","message":` + q + `}}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(errBody))
	}))
	defer srv.Close()

	// stdout=tty, stderr=pipe -> stderr stays exact raw bytes.
	_, errPipe, code := runWithTerminals(srv, true, false, nil, "status")
	wantRaw := "pluxy: " + foreignSample + " (upstream_error)\n"
	if code != 1 || errPipe != wantRaw {
		t.Errorf("stdout=tty stderr=pipe: code=%d\n got %q\nwant %q", code, errPipe, wantRaw)
	}

	// stdout=pipe, stderr=tty -> stderr is escaped.
	_, errTerm, code := runWithTerminals(srv, false, true, nil, "status")
	wantEsc := "pluxy: " + foreignMultiEscaped + " (upstream_error)\n"
	if code != 1 || errTerm != wantEsc {
		t.Errorf("stdout=pipe stderr=tty: code=%d\n got %q\nwant %q", code, errTerm, wantEsc)
	}
	assertNoRawControls(t, "printError stderr terminal", errTerm)

	// --json with stderr=tty -> exact JSON error body unchanged.
	for _, term := range []bool{false, true} {
		out, errJSON, code := runWithTerminals(srv, term, term, nil, "status", "--json")
		if code != 1 || out != "" || errJSON != errBody {
			t.Errorf("--json (term=%v): code=%d out=%q\n got %q\nwant %q", term, code, out, errJSON, errBody)
		}
	}
}

// TestLoginPromptStderrTerminalEscaping checks that sign-in prompt text from extensions is escaped
// on a stderr terminal and kept exact on a stderr pipe.
func TestLoginPromptStderrTerminalEscaping(t *testing.T) {
	q := mustJSONString(t, foreignSample)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case mgmt + "/login-methods":
			w.Write([]byte(`{"extensions":[{"extension":"pluxy/ext","methods":[{"method":"device","kind":"device"}]}]}`))
		case mgmt + "/accounts":
			w.Write([]byte(`{"login":{"id":"l1","account":"acc-1","state":"pending","prompt":{"kind":"device","message":` + q + `,"user_code":` + q + `,"verification_uri":` + q + `}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// stdout=pipe, stderr=tty -> prompt message, code, URL, and waiting error all escaped.
	_, errTerm, code := runWithTerminals(srv, false, true, nil, "account", "add", "pluxy/ext", "--no-wait")
	if code != exitWaiting {
		t.Fatalf("stderr=tty: code=%d err=%q", code, errTerm)
	}
	assertNoRawControls(t, "loginPrompt stderr terminal", errTerm)
	if !strings.Contains(errTerm, foreignMultiEscaped+"\n") ||
		!strings.Contains(errTerm, "code: "+foreignCellEscaped+"\n") ||
		!strings.Contains(errTerm, "open: "+foreignCellEscaped+"\n") {
		t.Errorf("stderr=tty prompt not escaped as expected:\n%q", errTerm)
	}

	// stdout=tty, stderr=pipe -> exact raw bytes on stderr.
	_, errPipe, code := runWithTerminals(srv, true, false, nil, "account", "add", "pluxy/ext", "--no-wait")
	if code != exitWaiting {
		t.Fatalf("stderr=pipe: code=%d err=%q", code, errPipe)
	}
	if !strings.Contains(errPipe, foreignSample+"\n") ||
		!strings.Contains(errPipe, "code: "+foreignSample+"\n") ||
		!strings.Contains(errPipe, "open: "+foreignSample+"\n") {
		t.Errorf("stderr=pipe prompt should stay raw:\n%q", errPipe)
	}
}
