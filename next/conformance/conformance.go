// Package conformance is the extension conformance kit, version 0. It starts a provider
// extension exactly as core does and checks it against the extension protocol
// (../protocol/README.md): the handshake, streamed and unstreamed replies, cancellation, a crash,
// shutdown and structured errors; for an extension that signs in to accounts, login and
// credential refresh; and, for one that lists models, its model list.
//
// The kit cannot know how to make an extension's upstream behave, so the extension's test gives
// it a fixture: for each Scenario, the account that makes the extension act it out. See
// ../protocol/TUTORIAL.md.
package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Scenario names an upstream behaviour the fixture must arrange.
type Scenario string

const (
	// ScenarioText: the upstream replies ReplyText and stops normally (finish reason stop),
	// reporting InputTokens and OutputTokens. When the request is streamed, the reply must reach
	// the extension in at least two pieces. A check call on its account must succeed (or report
	// code unsupported), so the upstream must also answer what the extension checks with.
	ScenarioText Scenario = "text"
	// ScenarioHang: the upstream starts a reply with some text and then sends nothing more until
	// the request is cancelled.
	ScenarioHang Scenario = "hang"
	// ScenarioRateLimited: the upstream refuses the request because of a rate limit and says to
	// try again in 20 seconds. The extension must report code rate_limit, retryable, scope
	// credential (or model, for a provider that limits each model on its own), retry_after_ms
	// 20000.
	ScenarioRateLimited Scenario = "rate_limited"
	// ScenarioAuthFailed: the upstream refuses the credential. The extension must report code
	// auth, retryable, scope credential; a check call on its account must report code auth (or
	// unsupported).
	ScenarioAuthFailed Scenario = "auth_failed"
)

// What ScenarioText must produce.
const (
	ReplyText    = "Hello from the conformance kit."
	InputTokens  = 11
	OutputTokens = 7
)

// LoginScenario names a login or refresh behaviour the fixture must arrange.
type LoginScenario string

const (
	// LoginApproved: the user completes the login. The extension must end it with login.done
	// and a credential.
	LoginApproved LoginScenario = "login_approved"
	// LoginDenied: the user or the provider refuses the login (for an api_key login: the key is
	// refused). The extension must end it with code auth.
	LoginDenied LoginScenario = "login_denied"
	// LoginAbandoned: the user never completes the login. The kit cancels it after its first
	// prompt; the extension must stop (with UpstreamCancelled set, the provider must see it
	// stop) and keep serving. Only for browser and device logins.
	LoginAbandoned LoginScenario = "login_abandoned"
	// RefreshRenewed: the provider renews the credential. The extension must return a
	// credential of the same kind that differs from the one sent.
	RefreshRenewed LoginScenario = "refresh_renewed"
	// RefreshRejected: the provider refuses the grant itself (OAuth invalid_grant). The
	// extension must report code auth, scope credential, not retryable.
	RefreshRejected LoginScenario = "refresh_rejected"
	// RefreshUnavailable: the provider's token endpoint answers, but with a server failure
	// (5xx). The extension must report an error that is not code auth and not retryable: the
	// request reached the provider, so it may have run.
	RefreshUnavailable LoginScenario = "refresh_unavailable"
)

// LoginSetup is what a fixture returns for a login scenario.
type LoginSetup struct {
	// Account is passed with the call: for a login, its ID and settings; for a refresh, also
	// the credential to renew.
	Account protocol.Account
	// Input is what the user entered when starting the login.
	Input map[string]string
	// Act plays the user for each prompt of an approved or denied login. It returns the input
	// to send back (login.input), or nil when it acted another way, for example by approving
	// a device code at the fake provider. Nil Act answers nothing.
	Act func(t *testing.T, p protocol.Prompt) map[string]string
	// UpstreamCancelled, if not nil, closes when the provider sees an abandoned login stop.
	UpstreamCancelled <-chan struct{}
}

// Setup is what a fixture returns for a scenario.
type Setup struct {
	// Account is passed to the extension with the kit's request.
	Account protocol.Account
	// UpstreamCancelled, if not nil, closes when the upstream sees the request cancelled. Only
	// used for ScenarioHang; with it the kit checks that cancellation reaches the upstream.
	UpstreamCancelled <-chan struct{}
}

// Target is the extension under test.
type Target struct {
	// Manifest is the path of the extension's manifest; its program must be built.
	Manifest string
	// Prepare returns the setup for a scenario. It is called once per check.
	Prepare func(t *testing.T, s Scenario) Setup
	// PrepareLogin returns the setup for a login scenario and login method (empty for refresh
	// scenarios). Required when the extension's describe lists login or refresh; called once
	// per check.
	PrepareLogin func(t *testing.T, s LoginScenario, method string) LoginSetup
}

// Each check waits at most this long for something that should happen at once.
const wait = 10 * time.Second

// BuildExtension builds the Go package pkg into a temporary folder, at the path the manifest
// names for this platform, copies the manifest beside it and returns the copy's path.
func BuildExtension(t *testing.T, manifest, pkg string) string {
	t.Helper()
	m, err := host.LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	prog, ok := m.Programs[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Fatalf("manifest has no program for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	dir := t.TempDir()
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, filepath.FromSlash(prog)), pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	b, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, host.ManifestFile)
	if err := os.WriteFile(copyPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return copyPath
}

// Run runs every check as a subtest of t. It starts the extension with protocol.EnvTestLoginHosts
// set to 1 (see sdk.TestLoginHost), so an extension that signs in can send its login and token
// requests to the fixture's fake provider, which its account settings name.
func Run(t *testing.T, target Target) {
	t.Helper()
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	m, err := host.LoadManifest(target.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	k := &kit{target: target, manifest: m}
	t.Run("handshake", k.handshake)
	t.Run("stream", func(t *testing.T) { k.text(t, true) })
	t.Run("no_stream", func(t *testing.T) { k.text(t, false) })
	t.Run("cancel", k.cancel)
	t.Run("errors/rate_limited", func(t *testing.T) {
		k.wantError(t, ScenarioRateLimited, llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 20000}, llm.ScopeModel)
	})
	t.Run("errors/auth_failed", func(t *testing.T) {
		k.wantError(t, ScenarioAuthFailed, llm.Error{Code: llm.CodeAuth, Retryable: true, Scope: llm.ScopeCredential})
	})
	t.Run("crash", k.crash)
	t.Run("shutdown", k.shutdown)
	t.Run("login", k.login)
	t.Run("refresh", k.refresh)
	t.Run("models", k.models)
	t.Run("check", k.check)
	t.Run("quota", k.quota)
}

type kit struct {
	target   Target
	manifest *host.Manifest
}

type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (k *kit) start(t *testing.T) *host.Extension {
	t.Helper()
	ext, err := host.Start(k.target.Manifest, host.Options{Log: logWriter{t}})
	if err != nil {
		t.Fatalf("starting the extension: %v", err)
	}
	t.Cleanup(ext.Stop)
	return ext
}

func request(stream bool) llm.Request {
	return llm.Request{
		Model:  "conformance-model",
		Stream: stream,
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, Content: []llm.Part{{Type: llm.PartText, Text: "You are a test."}}},
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Say hello."}}},
		},
	}
}

// next returns the stream's next event, failing the test if it takes longer than d.
func next(t *testing.T, s *host.Stream, d time.Duration) llm.Event {
	t.Helper()
	ch := make(chan llm.Event, 1)
	go func() { ch <- s.Next() }()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(d):
		s.Close()
		t.Fatalf("no event within %s", d)
		return llm.Event{}
	}
}

// collect reads events up to and including the terminal one.
func collect(t *testing.T, s *host.Stream) []llm.Event {
	t.Helper()
	var evs []llm.Event
	for {
		ev := next(t, s, wait)
		evs = append(evs, ev)
		if ev.Terminal() {
			return evs
		}
	}
}

func (k *kit) handshake(t *testing.T) {
	t.Run("describe", func(t *testing.T) {
		info := k.start(t).Info()
		if info.ID != k.manifest.ID || info.Version != k.manifest.Version {
			t.Errorf("describe says %s %s; manifest says %s %s", info.ID, info.Version, k.manifest.ID, k.manifest.Version)
		}
		if info.Protocol != protocol.Version {
			t.Errorf("describe says protocol %d, want %d", info.Protocol, protocol.Version)
		}
		if !slices.Contains(info.Operations, protocol.OpGenerate) {
			t.Errorf("operations %v do not include %q", info.Operations, protocol.OpGenerate)
		}
	})
	t.Run("refuses_to_run_outside_pluxy", func(t *testing.T) {
		k.wantRefusal(t, nil)
	})
	t.Run("refuses_unknown_protocol", func(t *testing.T) {
		k.wantRefusal(t, []string{
			protocol.EnvProtocolVersions + "=999",
			protocol.EnvSocket + "=" + filepath.Join(t.TempDir(), "ext.sock"),
		})
	})
}

// wantRefusal runs the program directly with env and expects a non-zero exit and no handshake.
func (k *kit) wantRefusal(t *testing.T, env []string) {
	t.Helper()
	prog, err := k.manifest.Program()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, prog)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || ctx.Err() != nil {
		t.Fatalf("want a non-zero exit, got %v (stderr: %q)", err, stderr.String())
	}
	if strings.HasPrefix(stdout.String(), protocol.HandshakeMarker+"|") {
		t.Errorf("printed a handshake line: %q", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("exited without saying why on stderr")
	}
}

func (k *kit) text(t *testing.T, stream bool) {
	ext := k.start(t)
	setup := k.target.Prepare(t, ScenarioText)
	s := ext.Generate(context.Background(), setup.Account, request(stream))
	defer s.Close()
	evs := collect(t, s)
	last := evs[len(evs)-1]
	if last.Type != llm.EventFinish {
		t.Fatalf("ended with %s: %v", last.Type, last.Error)
	}
	var text strings.Builder
	deltas := 0
	for _, ev := range evs {
		if ev.Type == llm.EventTextDelta {
			text.WriteString(ev.Delta)
			deltas++
		}
	}
	if text.String() != ReplyText {
		t.Errorf("text = %q, want %q", text.String(), ReplyText)
	}
	if stream && deltas < 2 {
		t.Errorf("streamed reply came as %d text delta(s); the upstream sent it in pieces, so pass them on as they arrive", deltas)
	}
	if last.Finish.Reason != llm.FinishStop {
		t.Errorf("finish reason = %q, want %q", last.Finish.Reason, llm.FinishStop)
	}
	u := last.Usage
	if u == nil || u.InputTokens == nil || *u.InputTokens != InputTokens || u.OutputTokens == nil || *u.OutputTokens != OutputTokens {
		t.Errorf("usage = %s, want input %d and output %d", jsonString(u), InputTokens, OutputTokens)
	}
}

// startHang starts a ScenarioHang call and waits for its first text.
func (k *kit) startHang(t *testing.T, ext *host.Extension, ctx context.Context) (*host.Stream, Setup) {
	t.Helper()
	setup := k.target.Prepare(t, ScenarioHang)
	s := ext.Generate(ctx, setup.Account, request(true))
	for {
		ev := next(t, s, wait)
		if ev.Terminal() {
			t.Fatalf("hanging reply ended with %s: %v", ev.Type, ev.Error)
		}
		if ev.Type == llm.EventTextDelta {
			return s, setup
		}
	}
}

func (k *kit) cancel(t *testing.T) {
	ext := k.start(t)
	ctx, cancel := context.WithCancel(context.Background())
	s, setup := k.startHang(t, ext, ctx)
	cancel()
	if ev := next(t, s, wait); ev.Type != llm.EventError || ev.Error.Code != llm.CodeCancelled {
		t.Errorf("after cancelling, got %s %s, want error %s", ev.Type, jsonString(ev.Error), llm.CodeCancelled)
	}
	if setup.UpstreamCancelled != nil {
		select {
		case <-setup.UpstreamCancelled:
		case <-time.After(wait):
			t.Fatal("the upstream request was not cancelled")
		}
	}
	// The extension keeps serving after a cancelled call.
	setup = k.target.Prepare(t, ScenarioText)
	s2 := ext.Generate(context.Background(), setup.Account, request(true))
	defer s2.Close()
	if evs := collect(t, s2); evs[len(evs)-1].Type != llm.EventFinish {
		t.Errorf("call after a cancelled one ended with %s", jsonString(evs[len(evs)-1]))
	}
}

// wantError checks that scenario s ends with the error want, or with want in one of the other
// scopes given.
func (k *kit) wantError(t *testing.T, s Scenario, want llm.Error, otherScopes ...string) {
	ext := k.start(t)
	setup := k.target.Prepare(t, s)
	st := ext.Generate(context.Background(), setup.Account, request(true))
	defer st.Close()
	evs := collect(t, st)
	got := evs[len(evs)-1]
	if got.Type != llm.EventError {
		t.Fatalf("ended with %s, want an error", got.Type)
	}
	e := got.Error
	scopeOK := e.Scope == want.Scope || slices.Contains(otherScopes, e.Scope)
	if e.Code != want.Code || e.Retryable != want.Retryable || !scopeOK || e.RetryAfterMS != want.RetryAfterMS {
		t.Errorf("error = code %q retryable %v scope %q retry_after_ms %d; want code %q retryable %v scope %q retry_after_ms %d",
			e.Code, e.Retryable, e.Scope, e.RetryAfterMS, want.Code, want.Retryable, want.Scope, want.RetryAfterMS)
	}
	if e.Message == "" {
		t.Error("error has no message")
	}
}

func (k *kit) crash(t *testing.T) {
	ext := k.start(t)
	s, _ := k.startHang(t, ext, context.Background())
	defer s.Close()
	ext.Kill()
	if ev := next(t, s, wait); ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Errorf("after the crash, got %s, want error %s", jsonString(ev), llm.CodeExtensionCrashed)
	}
	if ev := next(t, s, wait); ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Errorf("the stream did not keep its single terminal outcome: got %s", jsonString(ev))
	}
	select {
	case <-ext.Exited():
	case <-time.After(wait):
		t.Fatal("the program did not exit after being killed")
	}
	setup := k.target.Prepare(t, ScenarioText)
	s2 := ext.Generate(context.Background(), setup.Account, request(true))
	defer s2.Close()
	if ev := next(t, s2, wait); ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionUnavailable {
		t.Errorf("a call after the crash got %s, want error %s", jsonString(ev), llm.CodeExtensionUnavailable)
	}
}

func (k *kit) shutdown(t *testing.T) {
	ext, err := host.Start(k.target.Manifest, host.Options{Log: logWriter{t}, StopTimeout: wait})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	// Shutdown with a call still running: the extension ends it and exits by itself.
	s, _ := k.startHang(t, ext, context.Background())
	defer s.Close()
	start := time.Now()
	ext.Stop()
	if err := ext.ExitError(); err != nil {
		t.Errorf("after stdin closed, the program exited with %v (after %s), want a clean exit", err, time.Since(start).Round(time.Millisecond))
	}
	if ev := next(t, s, wait); ev.Type != llm.EventError {
		t.Errorf("the running call got %s at shutdown, want an error", jsonString(ev))
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// login checks every login method the extension lists, or that it refuses login when it lists
// none.
func (k *kit) login(t *testing.T) {
	info := k.start(t).Info()
	if !slices.Contains(info.Operations, protocol.OpLogin) {
		t.Run("unsupported", func(t *testing.T) {
			ext := k.start(t)
			l := ext.Login(context.Background(), protocol.Account{ID: "conformance"}, protocol.Login{Method: protocol.LoginAPIKey})
			defer l.Close()
			if r := nextLogin(t, l); r.Type != llm.EventError || r.Error.Code != llm.CodeUnsupported {
				t.Errorf("login on an extension that does not list it got %s, want error %s", jsonString(r), llm.CodeUnsupported)
			}
		})
		return
	}
	if k.target.PrepareLogin == nil {
		t.Fatal("describe lists login, but the target has no PrepareLogin")
	}
	if len(info.LoginMethods) == 0 {
		t.Fatal("describe lists login but no login_methods")
	}
	seen := map[string]bool{}
	for _, m := range info.LoginMethods {
		if m.Method == "" || seen[m.Method] {
			t.Errorf("login method %s: method must be set and unique", jsonString(m))
		}
		seen[m.Method] = true
		switch m.Kind {
		case protocol.LoginAPIKey, protocol.LoginBrowser, protocol.LoginDevice:
		default:
			t.Errorf("login method %q has unknown kind %q", m.Method, m.Kind)
			continue
		}
		t.Run(m.Method+"/approved", func(t *testing.T) { k.loginOutcome(t, m, LoginApproved) })
		t.Run(m.Method+"/denied", func(t *testing.T) { k.loginOutcome(t, m, LoginDenied) })
		if m.Kind != protocol.LoginAPIKey {
			t.Run(m.Method+"/abandoned", func(t *testing.T) { k.loginAbandoned(t, m) })
		}
	}
	t.Run("unknown_method", func(t *testing.T) {
		ext := k.start(t)
		l := ext.Login(context.Background(), protocol.Account{ID: "conformance"}, protocol.Login{Method: "no-such-method"})
		defer l.Close()
		if r := nextLogin(t, l); r.Type != llm.EventError || r.Error.Code != llm.CodeUnsupported {
			t.Errorf("login with an unknown method got %s, want error %s", jsonString(r), llm.CodeUnsupported)
		}
	})
}

// nextLogin returns the login's next frame, failing the test if it takes longer than wait.
func nextLogin(t *testing.T, l *host.LoginCall) protocol.Reply {
	t.Helper()
	ch := make(chan protocol.Reply, 1)
	go func() { ch <- l.Next() }()
	select {
	case r := <-ch:
		return r
	case <-time.After(wait):
		l.Close()
		t.Fatalf("no login frame within %s", wait)
		return protocol.Reply{}
	}
}

// checkPrompt checks that a prompt carries what its kind needs.
func checkPrompt(t *testing.T, p *protocol.Prompt) {
	t.Helper()
	switch p.Kind {
	case protocol.PromptBrowser:
		if p.URL == "" {
			t.Errorf("browser prompt has no url: %s", jsonString(p))
		}
	case protocol.PromptDevice:
		if p.UserCode == "" || p.VerificationURI == "" {
			t.Errorf("device prompt needs user_code and verification_uri: %s", jsonString(p))
		}
	case protocol.PromptInput:
		if len(p.Inputs) == 0 {
			t.Errorf("input prompt asks for no inputs: %s", jsonString(p))
		}
	default:
		t.Errorf("prompt has unknown kind %q", p.Kind)
	}
}

func (k *kit) loginOutcome(t *testing.T, m protocol.LoginMethod, s LoginScenario) {
	ext := k.start(t)
	setup := k.target.PrepareLogin(t, s, m.Method)
	l := ext.Login(context.Background(), setup.Account, protocol.Login{Method: m.Method, Input: setup.Input})
	defer l.Close()
	for {
		r := nextLogin(t, l)
		switch r.Type {
		case protocol.TypeLoginPrompt:
			checkPrompt(t, r.Prompt)
			if setup.Act == nil {
				continue
			}
			if in := setup.Act(t, *r.Prompt); in != nil {
				if err := l.Send(in); err != nil {
					t.Fatalf("sending login input: %v", err)
				}
			}
			continue
		case protocol.TypeLoginDone:
			if s != LoginApproved {
				t.Fatalf("a %s login succeeded", s)
			}
			if c := r.Grant.Credential; c.Kind == "" {
				t.Error("login.done credential has no kind")
			}
		case llm.EventError:
			if s == LoginApproved {
				t.Fatalf("approved login failed: %s", jsonString(r.Error))
			}
			if r.Error.Code != llm.CodeAuth || r.Error.Message == "" {
				t.Errorf("denied login ended with %s, want code %s and a message", jsonString(r.Error), llm.CodeAuth)
			}
		default:
			t.Fatalf("unexpected login frame %s", jsonString(r))
		}
		return
	}
}

func (k *kit) loginAbandoned(t *testing.T, m protocol.LoginMethod) {
	ext := k.start(t)
	setup := k.target.PrepareLogin(t, LoginAbandoned, m.Method)
	ctx, cancel := context.WithCancel(context.Background())
	l := ext.Login(ctx, setup.Account, protocol.Login{Method: m.Method, Input: setup.Input})
	defer l.Close()
	r := nextLogin(t, l)
	if r.Type != protocol.TypeLoginPrompt {
		t.Fatalf("a %s login's first frame was %s, want a prompt", m.Kind, jsonString(r))
	}
	checkPrompt(t, r.Prompt)
	cancel()
	if setup.UpstreamCancelled != nil {
		select {
		case <-setup.UpstreamCancelled:
		case <-time.After(wait):
			t.Fatal("the provider did not see the abandoned login stop")
		}
	}
	// The extension keeps serving after an abandoned login.
	text := k.target.Prepare(t, ScenarioText)
	s := ext.Generate(context.Background(), text.Account, request(false))
	defer s.Close()
	if evs := collect(t, s); evs[len(evs)-1].Type != llm.EventFinish {
		t.Errorf("call after an abandoned login ended with %s", jsonString(evs[len(evs)-1]))
	}
}

// refresh checks credential refresh, or that the extension refuses it when describe does not
// list it.
func (k *kit) refresh(t *testing.T) {
	info := k.start(t).Info()
	if !slices.Contains(info.Operations, protocol.OpRefresh) {
		t.Run("unsupported", func(t *testing.T) {
			_, e := k.start(t).Refresh(context.Background(), protocol.Account{ID: "conformance",
				Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, RefreshToken: "conformance-refresh-token"}})
			if e == nil || e.Code != llm.CodeUnsupported {
				t.Errorf("refresh on an extension that does not list it got %s, want error %s", jsonString(e), llm.CodeUnsupported)
			}
		})
		return
	}
	if k.target.PrepareLogin == nil {
		t.Fatal("describe lists refresh, but the target has no PrepareLogin")
	}
	run := func(t *testing.T, s LoginScenario) (sent, got *protocol.Credential, e *llm.Error) {
		ext := k.start(t)
		setup := k.target.PrepareLogin(t, s, "")
		if setup.Account.Credential == nil {
			t.Fatal("the refresh setup has no credential")
		}
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		got, e = ext.Refresh(ctx, setup.Account)
		return setup.Account.Credential, got, e
	}
	t.Run("renewed", func(t *testing.T) {
		sent, got, e := run(t, RefreshRenewed)
		if e != nil {
			t.Fatalf("refresh failed: %s", jsonString(e))
		}
		if got.Kind != sent.Kind {
			t.Errorf("refresh returned kind %q for a %q credential", got.Kind, sent.Kind)
		}
		if jsonString(got) == jsonString(sent) {
			t.Error("refresh returned the credential it was sent")
		}
	})
	t.Run("rejected", func(t *testing.T) {
		_, _, e := run(t, RefreshRejected)
		if e == nil || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || e.Retryable {
			t.Errorf("rejected grant got %s, want code %s, scope %s, not retryable", jsonString(e), llm.CodeAuth, llm.ScopeCredential)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		_, _, e := run(t, RefreshUnavailable)
		if e == nil || e.Code == llm.CodeAuth || e.Retryable {
			t.Errorf("server failure on refresh got %s, want an error that is not code %s and not retryable", jsonString(e), llm.CodeAuth)
		}
	})
}

// models checks the models operation: an extension that lists it answers with a list whose models
// each have an ID, none twice (with the ScenarioText account); one that does not refuses the call
// with code unsupported.
func (k *kit) models(t *testing.T) {
	ext := k.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	account := k.target.Prepare(t, ScenarioText).Account
	list, e := ext.Models(ctx, account)
	if !host.ListsModels(ext.Info()) {
		if e == nil || e.Code != llm.CodeUnsupported {
			t.Errorf("models on an extension that does not list it got %s, want error %s", jsonString(e), llm.CodeUnsupported)
		}
		return
	}
	if e != nil {
		t.Fatalf("models failed: %s", jsonString(e))
	}
	seen := map[string]bool{}
	for i, m := range list.Models {
		if m.ID == "" || seen[m.ID] {
			t.Errorf("models[%d] has an empty or repeated id %q", i, m.ID)
		}
		seen[m.ID] = true
	}
}

// check checks the check operation: an extension that lists it reports that the ScenarioText
// account works and that the ScenarioAuthFailed account's credential is refused, or code
// unsupported for both when it cannot check without a generation; one that does not list it
// refuses the call with code unsupported.
func (k *kit) check(t *testing.T) {
	ext := k.start(t)
	run := func(s Scenario) (*protocol.CheckResult, *llm.Error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		return ext.Check(ctx, k.target.Prepare(t, s).Account)
	}
	res, e := run(ScenarioText)
	if !host.ChecksAccounts(ext.Info()) {
		if e == nil || e.Code != llm.CodeUnsupported {
			t.Errorf("check on an extension that does not list it got %s, want error %s", jsonString(e), llm.CodeUnsupported)
		}
		return
	}
	unsupported := e != nil && e.Code == llm.CodeUnsupported
	if e != nil && !unsupported {
		t.Errorf("check of a working account got %s, want a result or error %s", jsonString(e), llm.CodeUnsupported)
	} else if e == nil && res == nil {
		t.Error("check of a working account returned neither a result nor an error")
	}
	_, e = run(ScenarioAuthFailed)
	switch {
	case unsupported:
		if e == nil || e.Code != llm.CodeUnsupported {
			t.Errorf("check of a refused account got %s, want error %s as for the working one", jsonString(e), llm.CodeUnsupported)
		}
	case e == nil || e.Code != llm.CodeAuth:
		t.Errorf("check of a refused account got %s, want error %s", jsonString(e), llm.CodeAuth)
	}
}

// quota checks the quota operation: an extension that lists it answers for the ScenarioText account
// with a quota whose limits each have an ID, none twice, and a used share from 0 to 100, or with an
// error that has a code and a message (an account with no plan to report answers unsupported);
// one that does not list it refuses the call with code unsupported.
func (k *kit) quota(t *testing.T) {
	ext := k.start(t)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	account := k.target.Prepare(t, ScenarioText).Account
	q, e := ext.Quota(ctx, account)
	if !host.ReportsQuota(ext.Info()) {
		if e == nil || e.Code != llm.CodeUnsupported {
			t.Errorf("quota on an extension that does not list it got %s, want error %s", jsonString(e), llm.CodeUnsupported)
		}
		return
	}
	if e != nil {
		if e.Code == "" || e.Message == "" {
			t.Errorf("quota failed with an error without a code or message: %s", jsonString(e))
		}
		return
	}
	seen := map[string]bool{}
	for i, l := range q.Limits {
		if l.ID == "" || seen[l.ID] {
			t.Errorf("limits[%d] has an empty or repeated id %q", i, l.ID)
		}
		seen[l.ID] = true
		if l.UsedPercent != nil && (*l.UsedPercent < 0 || *l.UsedPercent > 100) {
			t.Errorf("limits[%d] (%s) used_percent %v is not from 0 to 100", i, l.ID, *l.UsedPercent)
		}
	}
}
