package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// expandForeign fills the placeholders of an expected text: {cell} is foreignSample as a table cell
// shows it, {multi} as a multi-line text shows it, {key} is the escape sequence "ESC c" the stored
// key hint starts with. On a pipe each is the bytes as they came.
func expandForeign(s string, terminal bool) string {
	cell, multi, key := foreignSample, foreignSample, "\x1bc"
	if terminal {
		cell, multi, key = foreignCellEscaped, foreignMultiEscaped, `\x1bc`
	}
	return strings.NewReplacer("{cell}", cell, "{multi}", multi, "{key}", key).Replace(s)
}

// TestForeignTextSitesTerminalEscaping covers the places a command shows text a client, an upstream
// or a stored file gave outside the table commands: the lines and prompts of setup, client show,
// ext, route and account import. Each shows the text escaped on a terminal and as it came on a pipe.
// Not covered here: the Vertex import line, the "could not read the sign-in" retry line and the
// route chain's own line, which write through the same helpers (x.cell, writeEscaped).
func TestForeignTextSitesTerminalEscaping(t *testing.T) {
	q := mustJSONString(t, foreignSample)
	setup := `{"first_run":true,"scanning":false,"found":{"clients":[],"sign_ins":[],"imports":[],"core":{"listen":` + q +
		`,"reachable_from_network":false}},"picks":[{"id":` + q + `,"kind":"provider","title":"T","summary":"S","installed":false,"suggested":true}]}`
	paste := `{"tool":"pi","name":` + q + `,"base_url":` + q + `,"key":"<client-key:1d2e (\u001bc******89)>","files":[{"path":` + q +
		`,"content":` + q + `}],"note":` + q + `}`
	pkg := `{"package":{"id":` + q + `,"version":` + q + `,"state":` + q + `}}`

	cases := []struct {
		name    string
		args    []string // "{file}" is a credential file the test writes
		stdin   string
		routes  map[string]string // "METHOD /path" under /v0/management
		wantOut []string
		wantErr []string
	}{
		{
			name:   "setup: listen address, confirm prompt and next step",
			args:   []string{"setup"},
			stdin:  "n\n",
			routes: map[string]string{"GET /setup": setup},
			wantOut: []string{
				"core: {cell} (network: false)",
			},
			wantErr: []string{
				"Apply suggested picks ({cell})? [y/N]: ",
				"Next: pluxy setup apply --yes installs what core suggests ({multi}).\n",
			},
		},
		{
			name:   "setup apply: confirm prompt and next step",
			args:   []string{"setup", "apply"},
			stdin:  "n\n",
			routes: map[string]string{"GET /setup": setup},
			wantErr: []string{
				"Apply suggested picks ({cell})? [y/N]: ",
				"Next: pluxy setup apply --yes installs what core suggests ({multi}).\n",
			},
		},
		{
			name:   "client show: name, address, path, file content, note and stored key hint",
			args:   []string{"client", "show", "pi", "--key-id", "1d2e"},
			routes: map[string]string{"GET /setup/clients/pi/paste": paste},
			wantOut: []string{
				"Client: {cell}\nAddress: {cell}\n",
				"\n{cell}:\n{multi}\n",
				"{multi}\n\n{multi}\n", // the file's content, then the note
			},
			wantErr: []string{
				"Replace <client-key:1d2e ({key}******89)> with that key's full value",
			},
		},
		{
			name:    "ext install",
			args:    []string{"ext", "install", "pluxy/x"},
			routes:  map[string]string{"POST /packages": pkg},
			wantOut: []string{"{cell} {cell} installed: {cell}\n"},
		},
		{
			name:    "ext enable",
			args:    []string{"ext", "enable", "pluxy/x"},
			routes:  map[string]string{"POST /packages/pluxy/x/enable": pkg},
			wantOut: []string{"{cell} enabled: {cell}\n"},
		},
		{
			name:    "route with no account to try",
			args:    []string{"route", "m"},
			routes:  map[string]string{"GET /route": `{"model":` + q + `,"order":[],"left_out":[]}`},
			wantOut: []string{"no account would be tried for {cell}\n"},
		},
		{
			name:    "account import: skipped line",
			args:    []string{"account", "import", "{file}"},
			routes:  map[string]string{"POST /accounts/import": `{"imported":[],"skipped":[` + q + `]}`},
			wantOut: []string{"skipped: {cell}\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "cred.json")
			if err := os.WriteFile(file, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string(nil), tc.args...)
			for i, a := range args {
				if a == "{file}" {
					args[i] = file
				}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, ok := tc.routes[r.Method+" "+strings.TrimPrefix(r.URL.Path, "/v0/management")]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Write([]byte(body + "\n"))
			}))
			defer srv.Close()

			for _, terminal := range []bool{true, false} {
				out, errOut, code := runWithTerminalsTTY(srv, terminal, terminal, true, strings.NewReader(tc.stdin), args...)
				if code != 0 {
					t.Fatalf("terminal=%v: code=%d out=%q err=%q", terminal, code, out, errOut)
				}
				if terminal {
					assertNoRawControls(t, "stdout", out)
					assertNoRawControls(t, "stderr", errOut)
				}
				for _, want := range tc.wantOut {
					if want = expandForeign(want, terminal); !strings.Contains(out, want) {
						t.Errorf("terminal=%v: stdout lacks %q:\n%q", terminal, want, out)
					}
				}
				for _, want := range tc.wantErr {
					if want = expandForeign(want, terminal); !strings.Contains(errOut, want) {
						t.Errorf("terminal=%v: stderr lacks %q:\n%q", terminal, want, errOut)
					}
				}
			}
		})
	}
}

// TestSignInForeignTextTerminalEscaping covers the sign-in text an extension or core supplies on a
// stderr terminal: the label of a field asked for during the sign-in, the label of one asked for
// before it starts, and the reason core gave for refusing an answer. On a pipe each is as it came.
func TestSignInForeignTextTerminalEscaping(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		stdin   string
		setup   func(f *fakeAccountCore)
		wantErr string
	}{
		{
			name:   "label of a field the sign-in asks for",
			method: "input",
			stdin:  "1\n",
			setup: func(f *fakeAccountCore) {
				f.onCreate = func(l *fakeAccountLogin) {
					l.prompt = map[string]any{"kind": "input", "message": "Enter the code.",
						"inputs": []any{map[string]any{"name": "code", "label": foreignSample}}}
				}
			},
			wantErr: "{cell}: ",
		},
		{
			name:    "label of a field asked for before the sign-in starts",
			method:  "triple",
			stdin:   "1\n\n3\n",
			setup:   func(f *fakeAccountCore) { f.firstLabel = foreignSample },
			wantErr: "{cell}: ",
		},
		{
			name:   "reason core gave for refusing an answer",
			method: "input",
			stdin:  "1\n1\n",
			setup: func(f *fakeAccountCore) {
				f.onCreate = func(l *fakeAccountLogin) { l.failInputs, l.failMessage = 1, foreignSample }
			},
			wantErr: "the sign-in did not take that ({multi}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, terminal := range []bool{true, false} {
				f := newFakeAccountCore(t)
				tc.setup(f)
				// Standard error is the terminal that shows the sign-in; standard output stays a pipe.
				out, errOut, code := runWithTerminalsTTY(f.srv, false, terminal, true, strings.NewReader(tc.stdin),
					"account", "add", "test/ext", "--method", tc.method, "--id", "acc-1")
				if code != 0 {
					t.Fatalf("terminal=%v: code=%d out=%q err=%q", terminal, code, out, errOut)
				}
				if terminal {
					assertNoRawControls(t, "stderr", errOut)
				}
				if want := expandForeign(tc.wantErr, terminal); !strings.Contains(errOut, want) {
					t.Errorf("terminal=%v: stderr lacks %q:\n%q", terminal, want, errOut)
				}
			}
		})
	}
}
