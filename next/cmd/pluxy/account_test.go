package main

// The account commands against a fake management API: the sign-in they start and follow, and the
// calls that change or read an account.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/management"
)

// fakeAccountCore is a management API with one extension that signs in a few ways: a key, a
// browser, a device, prompts of its own, a service account key, and methods whose inputs a flag
// or an account's settings give.
type fakeAccountCore struct {
	mu         sync.Mutex
	srv        *httptest.Server
	logins     map[string]*fakeAccountLogin
	nextLogin  int
	starts     []map[string]any
	relogins   []map[string]any
	inputs     []map[string]string
	cancelled  []string
	patched    []map[string]any
	removed    []string
	imports    []management.AuthImportRequest
	vertices   []string
	failDelete bool
	// deleteDelay makes a cancel take that long to answer, as a core that is busy does.
	deleteDelay time.Duration
	// onCreate runs on each sign-in the fake starts, so a test can make one fail or end late.
	onCreate func(*fakeAccountLogin)
	// firstLabel is the label of the "triple" method's first input, when set.
	firstLabel string
}

// fakeAccountLogin is one sign-in the fake runs.
type fakeAccountLogin struct {
	account, method, state string
	prompt                 map[string]any
	reason                 string
	polls                  int
	// failPolls: how many reads of the sign-in answer 500 before the state is read.
	failPolls int
	// failInputs: how many answers the sign-in refuses with 422 before taking one.
	failInputs int
	// failMessage is what a refused answer says, when set.
	failMessage string
	// gone makes every read of the sign-in answer 404, as core does for one it does not have.
	gone bool
	// doneAfter ends the sign-in on its own, so a test that never answers has an end.
	doneAfter int
	// expireAfter ends it as core's limit on a sign-in does: cancelled, by no call of the command.
	expireAfter int
	// late is a change made after a read has answered, as an extension's own work does: it is
	// applied on the read after the one that reports it.
	late func()
	// onPoll runs on each read of the sign-in, so a test can change the prompt mid-flight.
	onPoll func(*fakeAccountLogin)
	// onInput handles an answer when set, returning true when it handled it.
	onInput func(*fakeAccountLogin, map[string]string) bool
}

func (l *fakeAccountLogin) view(id string) map[string]any {
	view := map[string]any{"id": id, "account": l.account, "extension": "test/ext", "method": l.method, "state": l.state}
	if l.prompt != nil {
		view["prompt"] = l.prompt
	}
	if l.reason != "" {
		view["error"] = map[string]any{"code": "auth", "message": l.reason}
	}
	return map[string]any{"login": view}
}

const fakeRefusal = "the test provider refused this sign-in"

func newFakeAccountCore(t *testing.T) *fakeAccountCore {
	t.Helper()
	f := &fakeAccountCore{logins: map[string]*fakeAccountLogin{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// run is one command against the fake server, with no standard input to read.
func (f *fakeAccountCore) run(args ...string) (string, string, int) {
	return f.runIn("", false, args...)
}

// runIn is run with standard input, and whether it is a terminal.
func (f *fakeAccountCore) runIn(stdin string, tty bool, args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": f.srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	code := runCLIWithInput(args, strings.NewReader(stdin), &out, &errOut, env, tty)
	return out.String(), errOut.String(), code
}

func (f *fakeAccountCore) start() ([]map[string]any, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.cancelled
}

func (f *fakeAccountCore) sent() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs
}

func (f *fakeAccountCore) newLogin(account, method string, state string) string {
	f.nextLogin++
	id := fmt.Sprintf("L%d", f.nextLogin)
	l := &fakeAccountLogin{account: account, method: method, state: state, prompt: fakePrompt(method)}
	if f.onCreate != nil {
		f.onCreate(l)
	}
	f.logins[id] = l
	return id
}

func fakePrompt(method string) map[string]any {
	switch method {
	case "browser":
		return map[string]any{"kind": "browser", "message": "Open the link, sign in and paste the address the browser ends on.",
			"url":    "https://login.example.test/authorize",
			"inputs": []any{map[string]any{"name": "redirect_url", "label": "Address the browser ended on"}}}
	case "device":
		return map[string]any{"kind": "device", "message": "Open the link and enter the code.",
			"user_code": "TEST-1234", "verification_uri": "https://login.example.test/device"}
	case "input":
		return map[string]any{"kind": "input", "message": "Enter the code.",
			"inputs": []any{map[string]any{"name": "code", "label": "Code"}}}
	case "ask_later":
		return map[string]any{"kind": "input", "message": "Anything to add?",
			"inputs": []any{map[string]any{"name": "note", "label": "Note", "optional": true}}}
	case "optional":
		return map[string]any{"kind": "input", "message": "Anything to add?",
			"inputs": []any{map[string]any{"name": "note", "label": "Note", "optional": true}}}
	case "secret_prompt":
		return map[string]any{"kind": "input", "message": "Enter the key.",
			"inputs": []any{map[string]any{"name": "api_key", "label": "API key", "secret": true}}}
	case "switch":
		return map[string]any{"kind": "input", "message": "Paste the address the browser ended on.",
			"inputs": []any{map[string]any{"name": "url", "label": "Address"}}}
	case "back":
		return fakeSwitchPrompt()
	case "changing":
		return fakeChangingPrompt(0)
	case "req_opt":
		return map[string]any{"kind": "input", "message": "Two values.", "inputs": []any{
			map[string]any{"name": "req", "label": "Required"},
			map[string]any{"name": "opt", "label": "Optional", "optional": true}}}
	case "repeat":
		return map[string]any{"kind": "input", "message": "Enter the code.",
			"inputs": []any{map[string]any{"name": "code", "label": "Code"}}}
	case "opt_then_secret":
		return fakeSwitchPrompt()
	case "opt_then_browser":
		return fakePrompt("browser")
	case "opt_then_passphrase":
		return map[string]any{"kind": "input", "message": "Enter the passphrase.",
			"inputs": []any{map[string]any{"name": "passphrase", "label": "Passphrase", "secret": true, "optional": true}}}
	}
	return nil
}

// fakeSwitchPrompt is the prompt a switch sign-in asks for once the person has been asked for the
// address: another field, and a secret one.
func fakeSwitchPrompt() map[string]any {
	return map[string]any{"kind": "input", "message": "Enter the key.",
		"inputs": []any{map[string]any{"name": "api_key", "label": "API key", "secret": true}}}
}

// fakeChangingPrompt is the nth prompt of a sign-in that moves on by itself.
func fakeChangingPrompt(n int) map[string]any {
	return map[string]any{"kind": "input", "message": fmt.Sprintf("Step %d.", n),
		"inputs": []any{map[string]any{"name": fmt.Sprintf("step_%d", n), "label": fmt.Sprintf("Step %d", n)}}}
}

func fakeInput(body map[string]any) map[string]string {
	out := map[string]string{}
	in, _ := body["input"].(map[string]any)
	for name, v := range in {
		if s, ok := v.(string); ok {
			out[name] = s
		}
	}
	return out
}

func (f *fakeAccountCore) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer k" {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid management key"}}`))
		return
	}
	// A cancel the test makes core slow to take holds the caller, not the fake's state: the sleep
	// is outside the lock, so what the fake did can still be read while it waits.
	if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/logins/") {
		f.mu.Lock()
		delay := f.deleteDelay
		f.mu.Unlock()
		time.Sleep(delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/v0/management")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	decode := func() map[string]any {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		return body
	}
	switch {
	case r.Method == http.MethodGet && path == "/login-methods":
		write(map[string]any{"extensions": []any{map[string]any{"extension": "test/ext", "methods": []any{
			map[string]any{"method": "api_key", "kind": "api_key", "label": "API key",
				"inputs": []any{map[string]any{"name": "api_key", "label": "API key", "secret": true}}},
			map[string]any{"method": "browser", "kind": "browser", "label": "Browser"},
			map[string]any{"method": "device", "kind": "device", "label": "Device"},
			map[string]any{"method": "input", "kind": "input", "label": "Code"},
			map[string]any{"method": "ask_later", "kind": "input", "label": "Asks later"},
			map[string]any{"method": "triple", "kind": "api_key", "label": "Three values",
				"inputs": []any{
					map[string]any{"name": "one", "label": cmpOr(f.firstLabel, "First")},
					map[string]any{"name": "two", "label": "Second", "optional": true},
					map[string]any{"name": "three", "label": "Third"},
				}},
			map[string]any{"method": "optional", "kind": "input", "label": "Optional note"},
			map[string]any{"method": "secret_prompt", "kind": "input", "label": "Secret prompt"},
			map[string]any{"method": "switch", "kind": "input", "label": "Two prompts"},
			map[string]any{"method": "back", "kind": "input", "label": "A secret, then a plain value"},
			map[string]any{"method": "changing", "kind": "input", "label": "A prompt that keeps changing"},
			map[string]any{"method": "req_opt", "kind": "input", "label": "A required and an optional value"},
			map[string]any{"method": "repeat", "kind": "input", "label": "The same prompt after a wrong answer"},
			map[string]any{"method": "service_account", "kind": "api_key", "label": "Service account",
				"inputs": []any{map[string]any{"name": "service_account", "label": "Service account key", "secret": true}}},
			map[string]any{"method": "url_key", "kind": "api_key", "label": "Address and key",
				"inputs": []any{
					map[string]any{"name": "base_url", "label": "Base URL"},
					map[string]any{"name": "api_key", "label": "API key", "secret": true},
					map[string]any{"name": "quota", "label": "Quota", "optional": true},
				}},
			map[string]any{"method": "kept", "kind": "api_key", "label": "Base URL",
				"inputs": []any{map[string]any{"name": "base_url", "label": "Base URL"}}},
			map[string]any{"method": "opt_secret", "kind": "api_key", "label": "Optional then secret",
				"inputs": []any{
					map[string]any{"name": "note", "label": "Note", "optional": true},
					map[string]any{"name": "api_key", "label": "API key", "secret": true},
				}},
			map[string]any{"method": "opt_then_secret", "kind": "input", "label": "Optional start then secret prompt",
				"inputs": []any{
					map[string]any{"name": "note", "label": "Note", "optional": true},
				}},
			map[string]any{"method": "opt_then_browser", "kind": "browser", "label": "Optional start then browser prompt",
				"inputs": []any{
					map[string]any{"name": "note", "label": "Note", "optional": true},
				}},
			map[string]any{"method": "opt_then_passphrase", "kind": "input", "label": "Optional start then optional secret prompt",
				"inputs": []any{
					map[string]any{"name": "note", "label": "Note", "optional": true},
				}},
		}}}})
	case r.Method == http.MethodPost && path == "/accounts":
		body := decode()
		f.starts = append(f.starts, body)
		id, _ := body["id"].(string)
		method, _ := body["method"].(string)
		if id == "taken" {
			w.WriteHeader(http.StatusConflict)
			write(map[string]any{"error": map[string]any{"code": "conflict", "message": "an account with this ID already exists"}})
			return
		}
		switch method {
		case "api_key", "kept", "service_account", "triple", "opt_secret":
			login := fmt.Sprintf("A%d", len(f.starts))
			state, reason := "done", ""
			switch {
			case method == "api_key" && fakeInput(body)["api_key"] == "bad-key":
				state, reason = "failed", "the test provider refused this key"
			case method == "service_account" && !json.Valid([]byte(fakeInput(body)["service_account"])):
				state, reason = "failed", "the service account key is not JSON"
			}
			f.logins[login] = &fakeAccountLogin{account: id, method: method, state: state, reason: reason}
			_ = json.NewEncoder(w).Encode(map[string]any{"login": map[string]any{"id": login, "account": id, "extension": "test/ext",
				"method": method, "state": state}})
			return
		}
		id = f.newLogin(id, method, management.LoginPending)
		w.WriteHeader(http.StatusAccepted)
		write(f.logins[id].view(id))
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/accounts/") && strings.HasSuffix(path, "/login"):
		body := decode()
		f.relogins = append(f.relogins, body)
		account := strings.TrimSuffix(strings.TrimPrefix(path, "/accounts/"), "/login")
		method, _ := body["method"].(string)
		state := management.LoginPending
		if method == "api_key" || method == "kept" {
			state = "done"
		}
		id := f.newLogin(account, method, state)
		if state != management.LoginPending {
			f.logins[id].prompt = nil
		}
		w.WriteHeader(http.StatusAccepted)
		write(f.logins[id].view(id))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/logins/"):
		id := strings.TrimPrefix(path, "/logins/")
		l, ok := f.logins[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"error": map[string]any{"code": "not_found", "message": "no such login"}})
			return
		}
		if l.failPolls > 0 {
			l.failPolls--
			w.WriteHeader(http.StatusInternalServerError)
			write(map[string]any{"error": map[string]any{"code": "failed", "message": "the sign-in cannot be read now"}})
			return
		}
		if l.gone {
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"error": map[string]any{"code": "not_found", "message": "no such login"}})
			return
		}
		l.polls++
		if l.state == management.LoginPending && l.method == "device" && l.polls >= 1 {
			l.state, l.prompt = "done", nil
		}
		if l.state == management.LoginPending && l.method == "switch" && l.polls == 1 {
			l.prompt = fakeSwitchPrompt()
		}
		if l.state == management.LoginPending && l.method == "ask_later" && l.polls == 1 {
			l.prompt = map[string]any{"kind": "input", "message": "Enter the code.",
				"inputs": []any{map[string]any{"name": "code", "label": "Code"}}}
		}
		if l.state == management.LoginPending && l.method == "changing" {
			if l.polls >= 4 {
				l.state, l.prompt = "done", nil
			} else {
				l.prompt = fakeChangingPrompt(l.polls)
			}
		}
		if l.doneAfter > 0 && l.polls >= l.doneAfter {
			l.state, l.prompt = "done", nil
		}
		if l.expireAfter > 0 && l.polls >= l.expireAfter {
			l.state, l.prompt = "cancelled", nil
		}
		if l.onPoll != nil {
			l.onPoll(l)
		}
		view := l.view(id)
		if l.late != nil {
			late := l.late
			l.late = nil
			late()
		}
		write(view)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/logins/") && strings.HasSuffix(path, "/input"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/logins/"), "/input")
		body := decode()
		in := fakeInput(body)
		l, ok := f.logins[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if l.failInputs > 0 {
			l.failInputs--
			w.WriteHeader(http.StatusUnprocessableEntity)
			write(map[string]any{"error": map[string]any{"code": "invalid_request", "message": cmpOr(l.failMessage, "the test core did not take that value")}})
			return
		}
		f.inputs = append(f.inputs, in)
		if l.onInput != nil && l.onInput(l, in) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		switch l.method {
		case "browser":
			switch {
			case strings.Contains(in["redirect_url"], "code=ok"):
				l.state, l.prompt = "done", nil
			case strings.Contains(in["redirect_url"], "code=bad"):
				l.state, l.prompt, l.reason = "failed", nil, fakeRefusal
			default:
				l.prompt["message"] = "That address holds no sign-in code."
			}
		case "input", "optional", "secret_prompt", "req_opt":
			l.late = func() { l.state, l.prompt = "done", nil }
		case "repeat":
			if in["code"] == "right" {
				l.late = func() { l.state, l.prompt = "done", nil }
			}
		case "back":
			if in["api_key"] != "" {
				l.late = func() {
					l.prompt = map[string]any{"kind": "input", "message": "Add a note.",
						"inputs": []any{map[string]any{"name": "note", "label": "Note"}}}
				}
			} else {
				l.late = func() { l.state, l.prompt = "done", nil }
			}
		case "switch":
			if in["api_key"] != "" {
				l.late = func() { l.state, l.prompt = "done", nil }
			}
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/logins/"):
		id := strings.TrimPrefix(path, "/logins/")
		if f.failDelete {
			w.WriteHeader(http.StatusInternalServerError)
			write(map[string]any{"error": map[string]any{"code": "failed", "message": "the test core cannot cancel it"}})
			return
		}
		f.cancelled = append(f.cancelled, id)
		if l, ok := f.logins[id]; ok {
			l.state, l.prompt = "cancelled", nil
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && path == "/accounts":
		write(map[string]any{"accounts": []any{
			map[string]any{"id": "work", "public_id": "acc_1", "label": "Work account", "extension": "test/ext",
				"source": "login", "state": "active", "settings": map[string]any{"base_url": "https://example.test/v1"},
				"health": map[string]any{"state": "ok"}},
			map[string]any{"id": "spare", "public_id": "acc_2", "extension": "test/ext", "source": "login",
				"state": "active", "health": map[string]any{"state": "cooling", "until": "2026-11-21T10:00:00Z", "why": "rate limited"}},
		}})
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/accounts/"):
		f.patched = append(f.patched, decode())
		write(map[string]any{"account": map[string]any{"id": strings.TrimPrefix(path, "/accounts/"), "health": map[string]any{"state": "ok"}}})
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/accounts/"):
		id := strings.TrimPrefix(path, "/accounts/")
		f.removed = append(f.removed, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/refresh"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/accounts/"), "/refresh")
		write(map[string]any{"account": map[string]any{"id": id, "expires_at": 2_000_000_000, "health": map[string]any{"state": "ok"}}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/quota"):
		write(map[string]any{
			"fetched_at": "2026-11-21T09:00:00Z",
			"quota": map[string]any{"plan": "Test plan", "plan_ends_at": 1_924_992_000, "limits": []any{
				map[string]any{"id": "five_hour", "label": "5-hour limit", "used_percent": 42,
					"resets_at": 2_000_000_000, "window_seconds": 18_000},
				map[string]any{"id": "weekly", "label": "Weekly limit", "used_percent": 7.125,
					"used": 1425, "limit": 20000, "unit": "requests", "remaining": 18575,
					"resets_at": 2_000_086_400, "window_seconds": 604_800},
				map[string]any{"id": "reset_credits", "label": "Manual resets", "remaining": 5, "unit": "resets"},
			}},
		})
	case r.Method == http.MethodPost && path == "/accounts/import":
		var req management.AuthImportRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.imports = append(f.imports, req)
		write(map[string]any{"imported": []any{map[string]any{"id": "codex-work-example.com", "extension": "pluxy/codex"}},
			"skipped": []any{"the file's id_token is not read yet"}})
	case r.Method == http.MethodPost && path == "/vertex/import":
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		raw, _ := io.ReadAll(file)
		file.Close()
		f.vertices = append(f.vertices, string(raw))
		write(map[string]any{"status": "ok", "account": "vertex-demo", "project_id": "demo", "email": "svc@demo.test", "location": "us-central1"})
	default:
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"error": map[string]any{"code": "not_found", "message": "nothing here"}})
	}
}

// A secret a sign-in asks for is read from standard input and never printed.
func TestCLIAccountAddReadsSecretFromStdin(t *testing.T) {
	f := newFakeAccountCore(t)
	const key = "fake-key-1a2b3c"
	out, errOut, code := f.runIn(key+"\n", false, "account", "add", "test/ext", "--method", "api_key", "--id", "work-1")
	if code != 0 || !strings.Contains(out, "account work-1 added") {
		t.Fatalf("account add api_key: code=%d out=%q err=%q", code, out, errOut)
	}
	starts, _ := f.start()
	if len(starts) != 1 || fakeInput(starts[0])["api_key"] != key || starts[0]["id"] != "work-1" || starts[0]["extension"] != "test/ext" {
		t.Fatalf("core was sent %v, want the key from standard input", starts)
	}
	if strings.Contains(out+errOut, key) {
		t.Errorf("the key reached the terminal: %q %q", out, errOut)
	}
	// An ID an account already has is refused by core, and nothing is changed: exit 6.
	if _, errOut, code := f.runIn(key+"\n", false, "account", "add", "test/ext", "--method", "api_key", "--id", "taken"); code != 6 ||
		!strings.Contains(errOut, "already exists") {
		t.Errorf("account add with an ID in use: %d %q, want 6 and why", code, errOut)
	}
	// A sign-in that ended at once has nothing to wait for: --no-wait still says it is added.
	if out, errOut, code := f.runIn(key+"\n", false, "account", "add", "test/ext", "--method", "api_key", "--id", "work-10", "--no-wait"); code != 0 ||
		!strings.Contains(out, "account work-10 added") {
		t.Errorf("account add --no-wait of a sign-in that ended at once: code=%d out=%q err=%q", code, out, errOut)
	}
}

// A secret is never taken from --input, where the shell's history and the process list keep it.
func TestCLIAccountAddRefusesSecretFlag(t *testing.T) {
	f := newFakeAccountCore(t)
	const key = "fake-key-4d5e6f"
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "api_key", "--input", "api_key="+key)
	if code != 2 || !strings.Contains(errOut, "secret") {
		t.Fatalf("account add with a secret in --input: code=%d out=%q err=%q, want 2 and why", code, out, errOut)
	}
	if starts, _ := f.start(); len(starts) != 0 {
		t.Errorf("a sign-in was started anyway: %v", starts)
	}
	if strings.Contains(out+errOut, key) {
		t.Errorf("the key reached the terminal: %q %q", out, errOut)
	}
}

// A browser sign-in prints its address, takes the callback URL a flag gives, and says when it is
// done.
func TestCLIAccountAddBrowser(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "browser", "--id", "work-2",
		"--input", "redirect_url=https://localhost:1455/auth/callback?code=ok")
	if code != 0 || !strings.Contains(out, "account work-2 added") || !strings.Contains(errOut, "https://login.example.test/authorize") {
		t.Fatalf("account add browser: code=%d out=%q err=%q", code, out, errOut)
	}
	if sent := f.sent(); len(sent) != 1 || !strings.Contains(sent[0]["redirect_url"], "code=ok") {
		t.Fatalf("the callback URL core got: %v", sent)
	}
}

// A refused sign-in ends with the provider's reason, and exit 1.
func TestCLIAccountAddRefused(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "browser", "--id", "work-3",
		"--input", "redirect_url=https://localhost/callback?code=bad")
	if code != 1 || !strings.Contains(errOut, fakeRefusal) || strings.Contains(out, "added") {
		t.Fatalf("a refused sign-in: code=%d out=%q err=%q, want 1 and the reason", code, out, errOut)
	}
}

// A sign-in that asks for a code in a prompt of its own reads it from standard input.
func TestCLIAccountAddPromptFromStdin(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("1\n", false, "account", "add", "test/ext", "--method", "input", "--id", "work-4")
	if code != 0 || !strings.Contains(out, "account work-4 added") {
		t.Fatalf("account add with a prompt: code=%d out=%q err=%q", code, out, errOut)
	}
	if sent := f.sent(); len(sent) != 1 || sent[0]["code"] != "1" {
		t.Fatalf("the code core got: %v", sent)
	}
	// The prompt asked for a code and nothing gave one: the sign-in is refused, and cancelled.
	f = newFakeAccountCore(t)
	out, errOut, code = f.run("account", "add", "test/ext", "--method", "input", "--id", "work-5")
	if code != 2 || !strings.Contains(errOut, "Code") || strings.Contains(out, "added") {
		t.Fatalf("a sign-in with no input: code=%d out=%q err=%q, want 2 and what it needs", code, out, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Fatalf("the sign-in was not cancelled: %v", cancelled)
	}
}

// --no-wait prints where to go and exits 7, leaving the sign-in to core.
func TestCLIAccountAddNoWait(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "device", "--id", "work-6", "--no-wait")
	if code != 7 || !strings.Contains(errOut, "TEST-1234") || !strings.Contains(errOut, "https://login.example.test/device") ||
		!strings.Contains(errOut, "pluxy account list") {
		t.Fatalf("account add --no-wait: code=%d out=%q err=%q, want 7 and where to go", code, out, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 0 {
		t.Errorf("--no-wait cancelled the sign-in: %v", cancelled)
	}
}

// A device sign-in is followed to its end: the code is shown, and the login read again.
func TestCLIAccountAddDevice(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "device", "--id", "work-7")
	if code != 0 || !strings.Contains(out, "account work-7 added") || !strings.Contains(errOut, "TEST-1234") {
		t.Fatalf("account add device: code=%d out=%q err=%q", code, out, errOut)
	}
}

// account login signs in to an account again, and names an account core does not have.
func TestCLIAccountLogin(t *testing.T) {
	f := newFakeAccountCore(t)
	const key = "fake-key-7g8h9i"
	out, errOut, code := f.runIn(key+"\n", false, "account", "login", "work", "--method", "api_key")
	if code != 0 || !strings.Contains(out, "account work signed in again") {
		t.Fatalf("account login: code=%d out=%q err=%q", code, out, errOut)
	}
	f.mu.Lock()
	relogins := f.relogins
	f.mu.Unlock()
	if len(relogins) != 1 || relogins[0]["method"] != "api_key" {
		t.Fatalf("core was sent %v", relogins)
	}
	if strings.Contains(out+errOut, key) {
		t.Errorf("the key reached the terminal: %q %q", out, errOut)
	}
	if _, errOut, _ := f.run("account", "login", "nobody", "--method", "api_key"); errOut != "pluxy: no such account \"nobody\" (not_found)\n" {
		t.Errorf("account login for an account that is not there: %q", errOut)
	}
	if _, _, code := f.run("account", "login", "nobody", "--method", "api_key"); code != 5 {
		t.Errorf("account login of an account core has not: %d, want 5", code)
	}
	// Core keeps no record of the way an account signed in, so the command asks, and says why.
	_, errOut, code = f.run("account", "login", "work")
	if code != 2 || !strings.Contains(errOut, "core does not record which one account work used; name one with --method: api_key (API key)") {
		t.Errorf("account login with no --method: code=%d err=%q, want 2 and why it asks", code, errOut)
	}
	f.mu.Lock()
	relogins = f.relogins
	f.mu.Unlock()
	if len(relogins) != 1 {
		t.Errorf("a sign-in was started without a method: %v", relogins)
	}
}

// account remove, refresh and set send what they name.
func TestCLIAccountChangeCommands(t *testing.T) {
	f := newFakeAccountCore(t)
	if out, errOut, code := f.run("account", "remove", "work"); code != 0 || !strings.Contains(out, "account work removed") {
		t.Fatalf("account remove: code=%d out=%q err=%q", code, out, errOut)
	}
	f.mu.Lock()
	removed := f.removed
	f.removed = nil
	f.mu.Unlock()
	if len(removed) != 1 || removed[0] != "work" {
		t.Errorf("core was asked to remove %v", removed)
	}
	expires := time.Unix(2_000_000_000, 0).Local().Format(time.DateTime)
	if out, errOut, code := f.run("account", "refresh", "work"); code != 0 || !strings.Contains(out, expires) {
		t.Fatalf("account refresh: code=%d out=%q err=%q, want the new expiry", code, out, errOut)
	}
	out, errOut, code := f.run("account", "set", "work", "prefix=fast", "weight=5", `models=[{"id":"m1"}]`, "disabled=true")
	if code != 0 || !strings.Contains(out, "account work changed") {
		t.Fatalf("account set: code=%d out=%q err=%q", code, out, errOut)
	}
	f.mu.Lock()
	patched := f.patched
	f.patched = nil
	f.mu.Unlock()
	if len(patched) != 1 {
		t.Fatalf("PATCH bodies: %v", patched)
	}
	body := patched[0]
	if body["prefix"] != "fast" || body["weight"] != float64(5) || body["disabled"] != true {
		t.Errorf("PATCH body %v, want prefix, weight and disabled", body)
	}
	if models, _ := body["models"].([]any); len(models) != 1 || models[0].(map[string]any)["id"] != "m1" {
		t.Errorf("PATCH models %v, want the JSON array", body["models"])
	}
	// null sets a field back, and a name core does not take is a usage error that names them.
	if _, _, code := f.run("account", "set", "work", "weight=null"); code != 0 {
		t.Errorf("account set weight=null: %d", code)
	}
	f.mu.Lock()
	patched, f.patched = f.patched, nil
	f.mu.Unlock()
	if len(patched) != 1 {
		t.Fatalf("PATCH body after weight=null: %v", patched)
	}
	if weight, present := patched[0]["weight"]; !present || weight != nil {
		t.Errorf("PATCH body %v, want weight null", patched[0])
	}
	if _, errOut, code := f.run("account", "set", "work", "nonsense=1"); code != 2 || !strings.Contains(errOut, "prefix") {
		t.Errorf("account set of a field core does not take: %d %q, want 2 and the names", code, errOut)
	}
	if _, errOut, code := f.run("account", "set", "work", "weight=five"); code != 2 || !strings.Contains(errOut, "JSON value") {
		t.Errorf("account set weight=five: %d %q, want 2 and what it takes", code, errOut)
	}
}

// account import sends a credential file to POST /accounts/import and a service account key to
// POST /vertex/import.
func TestCLIAccountImport(t *testing.T) {
	f := newFakeAccountCore(t)
	dir := t.TempDir()
	authFile := filepath.Join(dir, "codex-work@example.com.json")
	if err := os.WriteFile(authFile, []byte(`{"type":"codex","access_token":"fake-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := f.run("account", "import", authFile)
	if code != 0 || !strings.Contains(out, "codex-work-example.com") || !strings.Contains(out, "skipped: the file's id_token") {
		t.Fatalf("account import of a credential file: code=%d out=%q err=%q", code, out, errOut)
	}
	f.mu.Lock()
	imports := f.imports
	f.mu.Unlock()
	if len(imports) != 1 || imports[0].Name != "codex-work@example.com.json" || imports[0].Content != `{"type":"codex","access_token":"fake-token"}` {
		t.Fatalf("POST /accounts/import got %+v", imports)
	}

	keyFile := filepath.Join(dir, "service-account.json")
	key := `{"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----\n","project_id":"demo"}`
	if err := os.WriteFile(keyFile, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = f.run("account", "import", keyFile)
	if code != 0 || !strings.Contains(out, "account vertex-demo imported") || !strings.Contains(out, "svc@demo.test") {
		t.Fatalf("account import of a service account key: code=%d out=%q err=%q", code, out, errOut)
	}
	f.mu.Lock()
	vertices := f.vertices
	f.mu.Unlock()
	if len(vertices) != 1 || vertices[0] != key {
		t.Fatalf("POST /vertex/import got %q", vertices)
	}
	// Standard input is a service account key too.
	out, errOut, code = f.runIn(key, false, "account", "import", "-")
	if code != 0 || !strings.Contains(out, "account vertex-demo imported") {
		t.Fatalf("account import -: code=%d out=%q err=%q", code, out, errOut)
	}
	// A file that is not a .json credential file is a usage error before anything is sent.
	plain := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(plain, []byte(`{"type":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.run("account", "import", plain); code != 2 || !strings.Contains(errOut, ".json") {
		t.Errorf("account import of a .txt file: %d %q, want 2 and why", code, errOut)
	}
}

// On a terminal a value is asked for by its label, the secret is not printed back, and --json
// asks nothing: its standard output stays the API's reply.
func TestCLIAccountAddPromptsOnTerminal(t *testing.T) {
	f := newFakeAccountCore(t)
	const key = "fake-key-9j8k7l"
	out, errOut, code := f.runIn(key+"\n", true, "account", "add", "test/ext", "--method", "api_key", "--id", "work-8")
	if code != 0 || !strings.Contains(errOut, "API key: ") || strings.Contains(out+errOut, key) {
		t.Fatalf("account add on a terminal: code=%d out=%q err=%q, want the label and not the key", code, out, errOut)
	}
	// Standard input that is not a terminal is read, and nothing is asked on it: the reply is
	// standard output's alone.
	out, errOut, code = f.runIn(key+"\n", false, "account", "add", "test/ext", "--method", "api_key", "--id", "work-9", "--json")
	if code != 0 || errOut != "" || !strings.HasPrefix(out, `{"login":`) || strings.Contains(out, key) {
		t.Fatalf("account add --json: code=%d out=%q err=%q, want the reply and no prompt", code, out, errOut)
	}
}

// A value given without = to --input is refused, and the value never reaches the terminal: the
// flag package itself would quote it, and it may be a secret.
func TestCLIAccountInputKeepsValueOutOfErrors(t *testing.T) {
	f := newFakeAccountCore(t)
	const value = "sk-LEAKME"
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "api_key", "--input", value)
	if code != 2 || !strings.Contains(errOut, "name=value") {
		t.Fatalf("account add --input with no =: code=%d out=%q err=%q, want 2 and how to write it", code, out, errOut)
	}
	if strings.Contains(out+errOut, value) {
		t.Errorf("the value reached the terminal: %q %q", out, errOut)
	}
}

// --no-wait cannot leave a sign-in that only the command can answer: it cancels that sign-in and
// says what a value it needs.
func TestCLIAccountAddNoWaitRefusesInputPrompt(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "input", "--id", "wait-1", "--no-wait")
	if code != 2 || strings.Contains(out, "added") {
		t.Fatalf("account add --no-wait on a prompt that needs a value: code=%d out=%q err=%q, want 2", code, out, errOut)
	}
	if !strings.Contains(errOut, "Code") {
		t.Errorf("the error does not say what the sign-in needs: %q", errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was left to hang in core: %v", cancelled)
	}
}

// A service account key is a JSON document, and a pretty-printed one spans several lines: the
// value is read to the end of the document, not to the end of its first line.
func TestCLIAccountAddReadsJSONAcrossLines(t *testing.T) {
	f := newFakeAccountCore(t)
	key := "{\n  \"type\": \"service_account\",\n  \"project_id\": \"demo\"\n}"
	out, errOut, code := f.runIn(key+"\n", false, "account", "add", "test/ext", "--method", "service_account", "--id", "sa-1")
	if code != 0 || !strings.Contains(out, "account sa-1 added") {
		t.Fatalf("account add of a pretty-printed key: code=%d out=%q err=%q", code, out, errOut)
	}
	starts, _ := f.start()
	if len(starts) != 1 || fakeInput(starts[0])["service_account"] != key {
		t.Fatalf("core was sent %v, want the whole document", starts)
	}
	// A document that is not whole is refused rather than sent cut short.
	f = newFakeAccountCore(t)
	out, errOut, code = f.runIn("{\n  \"type\": \"service_account\"\n", false, "account", "add", "test/ext", "--method", "service_account", "--id", "sa-2")
	if code != 2 || !strings.Contains(errOut, "JSON") || strings.Contains(out, "added") {
		t.Fatalf("account add of an unfinished document: code=%d out=%q err=%q, want 2 and why", code, out, errOut)
	}
	if starts, _ := f.start(); len(starts) != 0 {
		t.Errorf("a sign-in was started with an unfinished document: %v", starts)
	}
}

// account login leaves a non-secret input the account's settings already hold optional, as the
// panel does; account add, which has no settings, still asks for it.
func TestCLIAccountLoginKeepsSettingsOptional(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "login", "work", "--method", "kept")
	if code != 0 || !strings.Contains(out, "account work signed in again") {
		t.Fatalf("account login with a value the account keeps: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, errOut, code := f.run("account", "add", "test/ext", "--method", "kept", "--id", "work-k1"); code != 2 || !strings.Contains(errOut, "Base URL") {
		t.Errorf("account add of the same value: %d %q, want 2 and what it needs", code, errOut)
	}
}

// With --json not even a terminal is read for a value: standard output is the reply alone.
func TestCLIAccountAddJSONDoesNotAskOnTerminal(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("", true, "account", "add", "test/ext", "--method", "api_key", "--json")
	if code != 2 || !strings.Contains(errOut, "--json") || strings.Contains(errOut, "API key: ") {
		t.Fatalf("account add --json on a terminal: code=%d out=%q err=%q, want 2 and why", code, out, errOut)
	}
	if starts, _ := f.start(); len(starts) != 0 {
		t.Errorf("a sign-in was started without its value: %v", starts)
	}
}

// account import refuses what is not a regular file before reading it, and a file over the 1 MiB
// core takes.
func TestCLIAccountImportRefusesNonRegularFile(t *testing.T) {
	f := newFakeAccountCore(t)
	_, errOut, code := f.run("account", "import", t.TempDir())
	if code != 2 || !strings.Contains(errOut, "not a regular file") {
		t.Fatalf("account import of a directory: code=%d err=%q, want 2 and why", code, errOut)
	}
	big := filepath.Join(t.TempDir(), "big.json")
	if err := os.WriteFile(big, bytes.Repeat([]byte(" "), maxImportFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := f.run("account", "import", big); code == 0 || !strings.Contains(errOut, "1 MiB") {
		t.Errorf("account import of a file over 1 MiB: code=%d err=%q, want a refusal", code, errOut)
	}
}

// When core cannot be asked to cancel a sign-in, the command says so rather than claiming it was
// cancelled.
func TestFollowLoginSaysWhenTheSignInWasNotCancelled(t *testing.T) {
	f := newFakeAccountCore(t)
	f.failDelete = true
	client := &management.Client{URL: f.srv.URL, Key: "k"}
	var reply management.LoginReply
	if _, err := client.Call(context.Background(), http.MethodPost, "/accounts", nil,
		management.AddAccountRequest{ID: "work", Extension: "test/ext", Method: "browser"}, &reply); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &invocation{ctx: ctx, client: client, stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	lines := newLineReader(x.stdin, nil)
	defer lines.close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := x.followLogin(reply.Login, nil, signInInputs{}, lines, false, false)
	if err == nil || !strings.Contains(err.Error(), "not cancelled") {
		t.Fatalf("followLogin after an interrupt core would not cancel: %v, want it to say the sign-in is still running", err)
	}
}

// A prompt whose values are all optional is answered by blank lines: an empty answer is sent,
// rather than the prompt being asked again.
func TestCLIAccountAddAnswersAnAllOptionalPrompt(t *testing.T) {
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 2 }
	out, errOut, code := f.runIn("\n", false, "account", "add", "test/ext", "--method", "optional", "--id", "opt-1")
	if code != 0 || !strings.Contains(out, "account opt-1 added") {
		t.Fatalf("account add of an all-optional prompt: code=%d out=%q err=%q", code, out, errOut)
	}
	if sent := f.sent(); len(sent) != 1 || len(sent[0]) != 0 {
		t.Fatalf("core was sent %v, want one empty answer", sent)
	}
}

// A prompt answered once is not answered again while the extension has not updated it yet: the
// fake applies an answer's change on the read after the one that reports it.
func TestCLIAccountAddSendsOneAnswerPerPrompt(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("1\n1\n", false, "account", "add", "test/ext", "--method", "input", "--id", "once-1")
	if code != 0 || !strings.Contains(out, "account once-1 added") {
		t.Fatalf("account add of a prompt: code=%d out=%q err=%q", code, out, errOut)
	}
	if sent := f.sent(); len(sent) != 1 {
		t.Fatalf("the prompt was answered %d times: %v", len(sent), sent)
	}
}

// shortLoginWait shortens the wait between reads of a sign-in, so a test that makes several of
// them does not take the panel's two seconds each.
func shortLoginWait(t *testing.T) {
	t.Helper()
	old := accountLoginWait
	accountLoginWait = 10 * time.Millisecond
	t.Cleanup(func() { accountLoginWait = old })
}

// A sign-in that asks for three values and is given one line is refused at once, rather than
// waiting for a line standard input will never send: the second value may be left empty, so the
// command goes on to ask for the third after standard input has ended.
func TestCLIAccountAddThreeValuesAtEndOfInput(t *testing.T) {
	f := newFakeAccountCore(t)
	done := make(chan struct{})
	var out, errOut string
	var code int
	go func() {
		defer close(done)
		out, errOut, code = f.runIn("first\n", false, "account", "add", "test/ext", "--method", "triple", "--id", "triple-1")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("account add waited for a line after standard input ended")
	}
	if code != 2 || !strings.Contains(errOut, "Third") || strings.Contains(out, "added") {
		t.Fatalf("account add with one of three values: code=%d out=%q err=%q, want 2 and what it needs", code, out, errOut)
	}
	// A later field supplied by --input is still taken after an earlier optional field hit end of
	// input, and no label is printed for it.
	out, errOut, code = f.runIn("first\n", true, "account", "add", "test/ext", "--method", "triple", "--id", "triple-flag", "--input", "three=third")
	if code != 0 || !strings.Contains(out, "account triple-flag added") {
		t.Fatalf("account add with --input after optional EOF: code=%d out=%q err=%q", code, out, errOut)
	}
	if strings.Contains(errOut, "Third: ") {
		t.Errorf("the flag-supplied field's label was printed: %q", errOut)
	}
	if starts, _ := f.start(); len(starts) != 1 || fakeInput(starts[0])["one"] != "first" || fakeInput(starts[0])["three"] != "third" {
		t.Fatalf("core was sent %v, want one=first and three=third", starts)
	}
}

// The same where the value is asked for by a prompt that comes after standard input ended: the
// sign-in is cancelled, not waited on.
func TestFollowLoginTwoValuesAtEndOfInput(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	done := make(chan struct{})
	var out, errOut string
	var code int
	go func() {
		defer close(done)
		out, errOut, code = f.runIn("", false, "account", "add", "test/ext", "--method", "ask_later", "--id", "later-1")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in waited for a line after standard input ended")
	}
	if code != 2 || !strings.Contains(errOut, "Code") || strings.Contains(out, "added") {
		t.Fatalf("a prompt that comes after standard input ended: code=%d out=%q err=%q, want 2 and what it needs", code, out, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was not cancelled: %v", cancelled)
	}
}

// A sign-in core cannot read for a moment is read again, as the panel tries three times.
func TestFollowLoginRetriesFailedReads(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) { l.failPolls = 2 }
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "device", "--id", "retry-1")
	if code != 0 || !strings.Contains(out, "account retry-1 added") {
		t.Fatalf("account add while core cannot read the sign-in: code=%d out=%q err=%q", code, out, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 0 {
		t.Errorf("a read that could be tried again ended the sign-in: %v", cancelled)
	}
}

// After three reads in a row that core could not answer, the sign-in is given up on and cancelled.
func TestFollowLoginGivesUpAfterThreeFailedReads(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) { l.failPolls = 3 }
	_, errOut, code := f.run("account", "add", "test/ext", "--method", "device", "--id", "retry-2")
	if code == 0 || !strings.Contains(errOut, "could not be read") {
		t.Fatalf("account add with three failed reads: code=%d err=%q, want a refusal and why", code, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was not cancelled: %v", cancelled)
	}
}

// An answer core refuses is shown and asked for again, rather than ending the sign-in.
func TestFollowLoginRetriesRejectedInput(t *testing.T) {
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) { l.failInputs = 1 }
	out, errOut, code := f.runIn("1\n1\n", false, "account", "add", "test/ext", "--method", "input", "--id", "retry-3")
	if code != 0 || !strings.Contains(out, "account retry-3 added") {
		t.Fatalf("account add with an answer core refused: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "did not take") {
		t.Errorf("the refusal was not shown: %q", errOut)
	}
	if sent := f.sent(); len(sent) != 1 || sent[0]["code"] != "1" {
		t.Errorf("core was sent %v, want the answer it took", sent)
	}
}

// A prompt that changes after an answer is shown again and answered: the command waits for core to
// move the prompt on, then reads the next value.
func TestCLIAccountAddAnswersAChangedPrompt(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("https://localhost/callback?code=ok\n", false,
		"account", "add", "test/ext", "--method", "browser", "--id", "changed-1",
		"--input", "redirect_url=https://localhost/callback?code=nope")
	if code != 0 || !strings.Contains(out, "account changed-1 added") {
		t.Fatalf("account add with an answer core sent back: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "That address holds no sign-in code.") {
		t.Errorf("the changed prompt was not shown: %q", errOut)
	}
	if sent := f.sent(); len(sent) != 2 {
		t.Errorf("core was sent %v, want the refused address and the one that works", sent)
	}
}

// A sign-in core no longer has is given up on, and cancelled in case core still runs it.
func TestFollowLoginCancelsWhenTheSignInIsGone(t *testing.T) {
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) { l.gone = true }
	out, errOut, code := f.run("account", "add", "test/ext", "--method", "browser", "--id", "gone-1")
	if code != 5 || !strings.Contains(errOut, "no such login") {
		t.Fatalf("account add of a sign-in core lost: code=%d out=%q err=%q, want 5", code, out, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was not cancelled: %v", cancelled)
	}
}

// Ctrl-C cancels the sign-in in core instead of leaving it to add an account later.
func TestFollowLoginCancelsOnInterrupt(t *testing.T) {
	f := newFakeAccountCore(t)
	client := &management.Client{URL: f.srv.URL, Key: "k"}
	var reply management.LoginReply
	if _, err := client.Call(context.Background(), http.MethodPost, "/accounts", nil,
		management.AddAccountRequest{ID: "work", Extension: "test/ext", Method: "browser"}, &reply); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &invocation{ctx: ctx, client: client, stdin: strings.NewReader(""), stdout: io.Discard, stderr: io.Discard}
	lines := newLineReader(x.stdin, nil)
	defer lines.close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := x.followLogin(reply.Login, nil, signInInputs{}, lines, false, false)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("followLogin after an interrupt: %v, want a cancelled sign-in", err)
	}
	f.mu.Lock()
	cancelled := f.cancelled
	f.mu.Unlock()
	if len(cancelled) != 1 || cancelled[0] != reply.Login.ID {
		t.Errorf("core's sign-ins cancelled: %v, want %s", cancelled, reply.Login.ID)
	}
}

// runBounded is one command that must end within limit, on a standard input the test holds.
func (f *fakeAccountCore) runBounded(t *testing.T, stdin io.Reader, limit time.Duration, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := func(k string) string {
		return map[string]string{"PLUXY_URL": f.srv.URL, "PLUXY_MANAGEMENT_KEY": "k"}[k]
	}
	done := make(chan int, 1)
	go func() { done <- runCLIWithInput(args, stdin, &out, &errOut, env, false) }()
	select {
	case code := <-done:
		return out.String(), errOut.String(), code
	case <-time.After(limit):
		t.Fatalf("%v did not end within %s", args, limit)
		return "", "", 0
	}
}

// A sign-in whose prompt keeps changing is read to its end while standard input stays open and
// silent: waiting for a line never stops the polling.
func TestFollowLoginPollsThroughPromptChanges(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	read, write := io.Pipe()
	defer write.Close()
	out, errOut, code := f.runBounded(t, read, 5*time.Second, "account", "add", "test/ext", "--method", "changing", "--id", "chg-1")
	if code != 0 || !strings.Contains(out, "account chg-1 added") {
		t.Fatalf("a sign-in that changes its prompt: code=%d out=%q err=%q", code, out, errOut)
	}
	if !strings.Contains(errOut, "Step 3.") {
		t.Errorf("the prompts were not followed: %q", errOut)
	}
}

// At the end of standard input the answers collected are sent when every required field has one,
// and the sign-in is cancelled with a usage error when one does not.
func TestFollowLoginAtEndOfInput(t *testing.T) {
	shortLoginWait(t)
	t.Run("required given, optional left out", func(t *testing.T) {
		f := newFakeAccountCore(t)
		out, errOut, code := f.runBounded(t, strings.NewReader("value\n"), 5*time.Second, "account", "add", "test/ext", "--method", "req_opt", "--id", "eof-1")
		if code != 0 || !strings.Contains(out, "account eof-1 added") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if sent := f.sent(); len(sent) != 1 || len(sent[0]) != 1 || sent[0]["req"] != "value" {
			t.Fatalf("core was sent %v, want req alone", sent)
		}
	})
	t.Run("all optional on empty input", func(t *testing.T) {
		f := newFakeAccountCore(t)
		out, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "optional", "--id", "eof-2")
		if code != 0 || !strings.Contains(out, "account eof-2 added") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if sent := f.sent(); len(sent) != 1 || len(sent[0]) != 0 {
			t.Fatalf("core was sent %v, want one empty answer", sent)
		}
	})
	t.Run("required missing", func(t *testing.T) {
		f := newFakeAccountCore(t)
		_, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "req_opt", "--id", "eof-3")
		if code != 2 || !strings.Contains(errOut, "Required") {
			t.Fatalf("code=%d err=%q, want 2 and what it needs", code, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 1 {
			t.Errorf("the sign-in was not cancelled: %v", cancelled)
		}
	})
	t.Run("an empty answer core refuses is not sent for ever", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.failInputs = 1 << 30 }
		_, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "optional", "--id", "eof-4")
		if code == 0 || !strings.Contains(errOut, "did not take") {
			t.Fatalf("code=%d err=%q, want the refusal", code, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 1 {
			t.Errorf("the sign-in was not cancelled: %v", cancelled)
		}
	})
}

// Only a field that takes a JSON file reads a document: a secret that starts with a brace is the
// one line it is.
func TestCLIAccountAddBraceSecretIsOneLine(t *testing.T) {
	f := newFakeAccountCore(t)
	const key = "{not-json-key"
	out, errOut, code := f.runBounded(t, strings.NewReader(key+"\n"), 5*time.Second, "account", "add", "test/ext", "--method", "api_key", "--id", "brace-1")
	if code != 0 || !strings.Contains(out, "account brace-1 added") {
		t.Fatalf("a secret that starts with a brace: code=%d out=%q err=%q", code, out, errOut)
	}
	if starts, _ := f.start(); len(starts) != 1 || fakeInput(starts[0])["api_key"] != key {
		t.Fatalf("core was sent %v, want the one line", starts)
	}
}

// A document's reading does not grow with the square of its size, and one over 1 MiB is refused
// as soon as it passes the limit.
func TestCLIAccountAddRefusesOversizedDocumentPromptly(t *testing.T) {
	f := newFakeAccountCore(t)
	doc := "{\"a\": [\n" + strings.Repeat("1,\n", (maxInputValue/3)+10)
	began := time.Now()
	_, errOut, code := f.runBounded(t, strings.NewReader(doc), 30*time.Second, "account", "add", "test/ext", "--method", "service_account", "--id", "big-1")
	// Far above a linear read (under a second with -race) and below a quadratic one (over 30
	// seconds with -race), so a busy runner does not fail it.
	if took := time.Since(began); took > 15*time.Second {
		t.Fatalf("a document over 1 MiB took %s to refuse", took)
	}
	if code != 2 || !strings.Contains(errOut, "KiB") {
		t.Fatalf("a document over 1 MiB: code=%d err=%q, want 2 and the limit", code, errOut)
	}
	if starts, _ := f.start(); len(starts) != 0 {
		t.Errorf("a sign-in was started with it: %d", len(starts))
	}
}

// A document whose braces close but which is not JSON is refused at its end, and its text is not
// repeated in the error.
func TestCLIAccountAddRefusesBrokenDocument(t *testing.T) {
	f := newFakeAccountCore(t)
	_, errOut, code := f.runBounded(t, strings.NewReader("{\n  \"private\": oops\n}\nnever-read\n"), 5*time.Second,
		"account", "add", "test/ext", "--method", "service_account", "--id", "broken-1")
	if code != 2 || !strings.Contains(errOut, "JSON") || strings.Contains(errOut, "oops") {
		t.Fatalf("a broken document: code=%d err=%q, want 2 and no value", code, errOut)
	}
}

// The same prompt shown again after a wrong answer can be answered again.
func TestFollowLoginAnswersARepeatedPrompt(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("wrong\nright\n", true, "account", "add", "test/ext", "--method", "repeat", "--id", "again-1")
	if code != 0 || !strings.Contains(out, "account again-1 added") {
		t.Fatalf("a prompt repeated after a wrong answer: code=%d out=%q err=%q", code, out, errOut)
	}
	if sent := f.sent(); len(sent) != 2 || sent[0]["code"] != "wrong" || sent[1]["code"] != "right" {
		t.Fatalf("core was sent %v, want both answers", sent)
	}
}

// Piped input answers each prompt once. Core cannot say whether a prompt still up is being worked
// on or was answered wrongly, and the next piped line may be the value of the next prompt, perhaps
// a secret, so it is never sent to this one.
func TestFollowLoginPipedLineNeverAnswersARepeatedPrompt(t *testing.T) {
	shortLoginWait(t)
	old := accountAnswerWait
	accountAnswerWait = 50 * time.Millisecond
	t.Cleanup(func() { accountAnswerWait = old })
	f := newFakeAccountCore(t)
	_, errOut, code := f.runBounded(t, strings.NewReader("first\nsecret-for-the-next-prompt\n"), 5*time.Second,
		"account", "add", "test/ext", "--method", "repeat", "--id", "piped-1")
	if code != 2 {
		t.Fatalf("a piped sign-in whose prompt never moves on: code=%d err=%q, want 2", code, errOut)
	}
	if sent := f.sent(); len(sent) != 1 || sent[0]["code"] != "first" {
		t.Fatalf("core was sent %v, want only the first answer", sent)
	}
	if strings.Contains(errOut, "secret-for-the-next-prompt") {
		t.Fatalf("the unsent line reached the output: %q", errOut)
	}
}

// A sign-in still working on the only answer standard input had is not cancelled for showing the
// same prompt; one that never moves on is, after a bounded wait.
func TestFollowLoginRepeatedPromptAtEndOfInput(t *testing.T) {
	shortLoginWait(t)
	t.Run("finishes", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 5 }
		out, errOut, code := f.runBounded(t, strings.NewReader("wrong\n"), 5*time.Second, "account", "add", "test/ext", "--method", "repeat", "--id", "slow-1")
		if code != 0 || !strings.Contains(out, "account slow-1 added") {
			t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
		}
		if sent := f.sent(); len(sent) != 1 {
			t.Fatalf("core was sent %v, want the one answer", sent)
		}
	})
	t.Run("never moves on", func(t *testing.T) {
		old := accountAnswerWait
		accountAnswerWait = 50 * time.Millisecond
		t.Cleanup(func() { accountAnswerWait = old })
		f := newFakeAccountCore(t)
		_, errOut, code := f.runBounded(t, strings.NewReader("wrong\n"), 5*time.Second, "account", "add", "test/ext", "--method", "repeat", "--id", "slow-2")
		if code != 2 || !strings.Contains(errOut, "Code") {
			t.Fatalf("code=%d err=%q, want 2 and what it needs", code, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 1 {
			t.Errorf("the sign-in was not cancelled: %v", cancelled)
		}
	})
}

// browserWaitNotice is what the command says when a browser sign-in can get no more lines.
const browserWaitNotice = "standard input has ended, so nothing more can be pasted. The sign-in can now finish only if the browser's return reaches its provider; core gives up on it 15 minutes after it started. Ctrl-C cancels it.\n"

// A browser sign-in does not end with standard input: the browser's return may reach the extension
// by itself. The command says so once and follows the sign-in to its end.
func TestFollowLoginBrowserAtEndOfInput(t *testing.T) {
	shortLoginWait(t)
	wantWaited := func(t *testing.T, f *fakeAccountCore, out, errOut string, code, sent int, id string) {
		t.Helper()
		if code != 0 || !strings.Contains(out, "account "+id+" added") {
			t.Fatalf("code=%d out=%q err=%q, want the sign-in followed to its end", code, out, errOut)
		}
		if n := strings.Count(errOut, browserWaitNotice); n != 1 {
			t.Errorf("the notice was written %d times, want once: %q", n, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 0 {
			t.Errorf("the sign-in was cancelled: %v", cancelled)
		}
		if got := f.sent(); len(got) != sent {
			t.Errorf("core was sent %v, want %d answers", got, sent)
		}
	}
	t.Run("nothing on standard input", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 3 }
		out, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-1")
		wantWaited(t, f, out, errOut, code, 0, "br-1")
	})
	t.Run("a non-address, then the end", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 3 }
		out, errOut, code := f.runBounded(t, strings.NewReader("not an address\n"), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-2")
		wantWaited(t, f, out, errOut, code, 1, "br-2")
		if !strings.Contains(errOut, "That address holds no sign-in code.") {
			t.Errorf("the prompt's answer was not shown: %q", errOut)
		}
	})
	// The second value leaves the prompt as it was, so nothing reads standard input again: its end
	// is still noticed, and the wait that gives up on an input prompt does not apply.
	t.Run("piped values the prompt keeps refusing", func(t *testing.T) {
		old := accountAnswerWait
		accountAnswerWait = 20 * time.Millisecond
		t.Cleanup(func() { accountAnswerWait = old })
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 30 }
		out, errOut, code := f.runBounded(t, strings.NewReader("not an address\nstill not one\n"), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-3")
		wantWaited(t, f, out, errOut, code, 2, "br-3")
	})
	t.Run("core's limit ends the wait", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.expireAfter = 3 }
		_, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-4")
		if code != 1 || !strings.Contains(errOut, browserWaitNotice) || !strings.Contains(errOut, "the sign-in was cancelled") {
			t.Fatalf("code=%d err=%q, want 1, the notice and the end", code, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 0 {
			t.Errorf("the command cancelled it itself: %v", cancelled)
		}
	})
	t.Run("the browser returns while standard input stays open", func(t *testing.T) {
		f := newFakeAccountCore(t)
		f.onCreate = func(l *fakeAccountLogin) { l.doneAfter = 3 }
		stdin, held := io.Pipe()
		defer held.Close()
		out, errOut, code := f.runBounded(t, stdin, 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-5")
		if code != 0 || !strings.Contains(out, "account br-5 added") || strings.Contains(errOut, "standard input has ended") {
			t.Fatalf("code=%d out=%q err=%q, want it added and no notice", code, out, errOut)
		}
	})
	t.Run("--no-wait leaves it for the browser", func(t *testing.T) {
		f := newFakeAccountCore(t)
		_, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "br-6", "--no-wait")
		if code != 7 {
			t.Fatalf("code=%d err=%q, want 7", code, errOut)
		}
		if _, cancelled := f.start(); len(cancelled) != 0 {
			t.Errorf("--no-wait cancelled the sign-in: %v", cancelled)
		}
	})
}

// An optional input prompt that arrives after standard input has already ended is still sent
// complete, without cancelling the sign-in. It guards what stopping the ask must not break; the
// change itself is shown by the terminal test "Ctrl-D then secret".
func TestFollowLoginOptionalInputAfterEndOfInput(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	f.onCreate = func(l *fakeAccountLogin) {
		l.onPoll = func(l *fakeAccountLogin) {
			if l.state == management.LoginPending && l.polls == 2 {
				l.prompt = fakePrompt("optional")
			}
		}
		l.onInput = func(l *fakeAccountLogin, _ map[string]string) bool {
			if l.prompt != nil && l.prompt["kind"] == "input" {
				l.late = func() { l.state, l.prompt = "done", nil }
				return true
			}
			return false
		}
	}
	out, errOut, code := f.runBounded(t, strings.NewReader(""), 5*time.Second, "account", "add", "test/ext", "--method", "browser", "--id", "opt-1")
	if code != 0 || !strings.Contains(out, "account opt-1 added") {
		t.Fatalf("code=%d out=%q err=%q, want the optional prompt sent complete", code, out, errOut)
	}
	if !strings.Contains(errOut, browserWaitNotice) || !strings.Contains(errOut, "Anything to add?") {
		t.Errorf("missing expected prompt output: %q", errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 0 {
		t.Errorf("the sign-in was cancelled: %v", cancelled)
	}
	sent := f.sent()
	if len(sent) != 1 || len(sent[0]) != 0 {
		t.Errorf("core was sent %v, want one empty answer for the optional prompt", sent)
	}
}

// A command that needs no value leaves standard input alone, for whatever reads it next.
func TestCLIAccountAddLeavesUnusedInputUnread(t *testing.T) {
	f := newFakeAccountCore(t)
	stdin := strings.NewReader("for the next command\n")
	_, errOut, code := f.runBounded(t, stdin, 5*time.Second, "account", "add", "test/ext", "--method", "device", "--id", "unread-1")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if stdin.Len() != len("for the next command\n") {
		t.Errorf("the command read %d bytes it had no use for", len("for the next command\n")-stdin.Len())
	}
}

// --no-wait's refusal names only what works: --input cannot answer a prompt that a sign-in shows
// after it starts, since --no-wait cancels that sign-in either way.
func TestCLIAccountAddNoWaitGivesNoFalseHint(t *testing.T) {
	f := newFakeAccountCore(t)
	_, errOut, code := f.run("account", "add", "test/ext", "--method", "input", "--id", "hint-1", "--no-wait", "--input", "code=1")
	if code != 2 || strings.Contains(errOut, "--input") || !strings.Contains(errOut, "without --no-wait") {
		t.Fatalf("--no-wait on a prompt: code=%d err=%q, want 2, no --input hint", code, errOut)
	}
}

// account methods lists each sign-in, marks a secret input, and account list shows the label.
func TestCLIAccountMethodsAndList(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "methods")
	if code != 0 || !strings.Contains(out, "test/ext") || !strings.Contains(out, "api_key (secret)") ||
		!strings.Contains(out, "browser") || !strings.Contains(out, "device") {
		t.Fatalf("account methods: code=%d out=%q err=%q", code, out, errOut)
	}
	out, errOut, code = f.run("account", "list")
	if code != 0 || !strings.Contains(out, "LABEL") || !strings.Contains(out, "Work account") ||
		!strings.Contains(out, "cooling") || !strings.Contains(out, "rate limited") {
		t.Fatalf("account list: code=%d out=%q err=%q, want the label column", code, out, errOut)
	}
}

// account quota writes a plan and its windows as the panel does, and --json stays the reply.
func TestCLIAccountQuotaFormat(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "quota", "work")
	reset := time.Unix(2_000_000_000, 0).Local().Format("2006-01-02 15:04")
	if code != 0 || !strings.Contains(out, "plan: Test plan") || !strings.Contains(out, "42%") ||
		!strings.Contains(out, "7.13%") || !strings.Contains(out, "5 hours") || !strings.Contains(out, "7 days") ||
		!strings.Contains(out, "manual resets: 5") || !strings.Contains(out, reset) ||
		!strings.Contains(out, "1,425 / 20,000 requests") || strings.Contains(out, "limits[") {
		t.Fatalf("account quota: code=%d out=%q err=%q", code, out, errOut)
	}
	raw, errOut, code := f.run("account", "quota", "work", "--json")
	if code != 0 || !strings.Contains(raw, `"five_hour"`) || !strings.Contains(raw, `"used_percent":42`) || errOut != "" {
		t.Fatalf("account quota --json: code=%d out=%q err=%q, want the body unchanged", code, raw, errOut)
	}
}

// An unknown subcommand names itself and lists the ones the group has.
func TestCLIAccountUnknownSubcommand(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.run("account", "frobnicate")
	if code != 2 || out != "" || !strings.Contains(errOut, `unknown account command "frobnicate"`) ||
		!strings.Contains(errOut, "account add <provider>") || !strings.Contains(errOut, "account import <file>") {
		t.Fatalf("account frobnicate: code=%d out=%q err=%q, want 2, the name and the valid ones", code, out, errOut)
	}
	if _, errOut, code := f.run("account"); code != 2 || !strings.Contains(errOut, "needs a command") {
		t.Errorf("pluxy account alone: %d %q", code, errOut)
	}
}

// A short extension name with one installed package of that name is taken as that package, for an
// account to add; more than one is refused with the list.
func TestCLIAccountAddShortExtensionName(t *testing.T) {
	f := newFakeAccountCore(t)
	out, errOut, code := f.runIn("sk-short\n", false, "account", "add", "ext", "--method", "api_key", "--id", "short-1")
	if code != 0 || !strings.Contains(out, "account short-1 added") {
		t.Fatalf("account add with a short name: code=%d out=%q err=%q", code, out, errOut)
	}
	if starts, _ := f.start(); len(starts) != 1 || starts[0]["extension"] != "test/ext" {
		t.Fatalf("core was sent %v, want the extension test/ext", starts)
	}
	r := management.LoginMethodsReply{Extensions: []management.LoginMethods{{Extension: "a/ext"}, {Extension: "b/ext"}, {Extension: "b/other"}}}
	if _, err := methodsFor(r, "ext"); err == nil || !strings.Contains(err.Error(), "a/ext, b/ext") || strings.Contains(err.Error(), "other") {
		t.Errorf("a name two extensions share: %v, want an error that lists both", err)
	}
	if m, err := methodsFor(r, "other"); err != nil || m.Extension != "b/other" {
		t.Errorf("a name one extension has: %v %v", m, err)
	}
	if _, err := methodsFor(r, "x/ext"); err == nil || !strings.Contains(err.Error(), "no extension") {
		t.Errorf("a full ID that is not installed: %v, want the refusal", err)
	}
}

// When standard input runs out, the error names every value still missing and the order lines
// are read in, so a line taken for the first value is not a surprise.
func TestCLIAccountAddMissingValuesNamed(t *testing.T) {
	f := newFakeAccountCore(t)
	_, errOut, code := f.runIn("only-one\n", false, "account", "add", "test/ext", "--method", "url_key", "--id", "uk-1")
	want := "the sign-in needs API key: standard input gave 1 line, and it reads one line for each value, in this order: Base URL, API key, Quota (optional)"
	if code != 2 || !strings.Contains(errOut, want) {
		t.Fatalf("one line for two values: code=%d err=%q, want it to contain %q", code, errOut, want)
	}
	_, errOut, code = f.runIn("", false, "account", "add", "test/ext", "--method", "url_key", "--id", "uk-2")
	want = "the sign-in needs Base URL, API key: standard input gave no line, and"
	if code != 2 || !strings.Contains(errOut, want) {
		t.Fatalf("no line for two values: code=%d err=%q, want it to contain %q", code, errOut, want)
	}
}

// After standard input has ended on a terminal, the sign-in asks for nothing again: a prompt that
// stays up after its (empty) answer is not shown a second time with "give the value again", since
// nothing reads a value and a person who typed one would see it on the screen and hand it to the
// shell. The command waits for the prompt to move on, then cancels the sign-in.
func TestFollowLoginAsksNothingAgainAfterEndOfInput(t *testing.T) {
	shortLoginWait(t)
	old := accountAnswerWait
	accountAnswerWait = 200 * time.Millisecond
	t.Cleanup(func() { accountAnswerWait = old })
	f := newFakeAccountCore(t)
	_, errOut, code := f.runIn("", true, "account", "add", "test/ext", "--method", "opt_then_passphrase", "--id", "again-1")
	if code != 2 || !strings.Contains(errOut, "the sign-in still asks for Passphrase") {
		t.Fatalf("account add with a prompt that stays up after input ended: code=%d err=%q, want 2 and why", code, errOut)
	}
	if strings.Contains(errOut, "give the value again") {
		t.Errorf("the command asked for the value again after standard input ended: %q", errOut)
	}
	if n := strings.Count(errOut, "Enter the passphrase."); n != 1 {
		t.Errorf("the prompt's message was printed %d times, want once: %q", n, errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was not cancelled: %v", cancelled)
	}
}

// The error for a field that needs a value after standard input ended starts on its own line, not
// after the label of the field where input ended.
func TestCLIAccountEndOfInputErrorStartsOnItsOwnLine(t *testing.T) {
	f := newFakeAccountCore(t)
	_, errOut, code := f.runIn("", true, "account", "add", "test/ext", "--method", "opt_secret", "--id", "line-1")
	if code != 2 || !strings.Contains(errOut, "Note: \npluxy: the sign-in needs API key:") {
		t.Fatalf("account add with input ended at an optional field: code=%d err=%q, want 2 and the error on its own line", code, errOut)
	}
	// Piped, no label was written, so no empty line is.
	_, errOut, code = f.runIn("", false, "account", "add", "test/ext", "--method", "opt_secret", "--id", "line-2")
	if code != 2 || strings.HasPrefix(errOut, "\n") || strings.Contains(errOut, "\n\n") {
		t.Fatalf("piped account add with no input: code=%d err=%q, want 2 and no empty line", code, errOut)
	}
}

// A sign-in again follows the same rule: once standard input has ended at its start field, a
// prompt that comes next prints no label for a value nothing can read.
func TestCLIAccountLoginPromptAfterEndOfInput(t *testing.T) {
	shortLoginWait(t)
	f := newFakeAccountCore(t)
	_, errOut, code := f.runIn("", true, "account", "login", "work", "--method", "opt_then_secret")
	if code != 2 || !strings.Contains(errOut, "the sign-in needs API key and standard input has no more lines:") {
		t.Fatalf("account login with input ended: code=%d err=%q, want 2 and what it needs", code, errOut)
	}
	if strings.Contains(errOut, "API key: ") {
		t.Errorf("the secret field's label was printed after standard input ended: %q", errOut)
	}
	if !strings.Contains(errOut, "Enter the key.") {
		t.Errorf("the prompt's message was not printed: %q", errOut)
	}
	if _, cancelled := f.start(); len(cancelled) != 1 {
		t.Errorf("the sign-in was not cancelled: %v", cancelled)
	}
}
