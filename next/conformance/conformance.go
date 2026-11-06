// Package conformance is the extension conformance kit, version 0. It starts a provider
// extension exactly as core does and checks it against the extension protocol
// (../protocol/README.md): the handshake, streamed and unstreamed replies, cancellation, a crash,
// shutdown and structured errors.
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
	// the extension in at least two pieces.
	ScenarioText Scenario = "text"
	// ScenarioHang: the upstream starts a reply with some text and then sends nothing more until
	// the request is cancelled.
	ScenarioHang Scenario = "hang"
	// ScenarioRateLimited: the upstream refuses the request because of a rate limit and says to
	// try again in 20 seconds. The extension must report code rate_limit, retryable, scope
	// credential, retry_after_ms 20000.
	ScenarioRateLimited Scenario = "rate_limited"
	// ScenarioAuthFailed: the upstream refuses the credential. The extension must report code
	// auth, retryable, scope credential.
	ScenarioAuthFailed Scenario = "auth_failed"
)

// What ScenarioText must produce.
const (
	ReplyText    = "Hello from the conformance kit."
	InputTokens  = 11
	OutputTokens = 7
)

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

// Run runs every check as a subtest of t.
func Run(t *testing.T, target Target) {
	t.Helper()
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
		k.wantError(t, ScenarioRateLimited, llm.Error{Code: llm.CodeRateLimit, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 20000})
	})
	t.Run("errors/auth_failed", func(t *testing.T) {
		k.wantError(t, ScenarioAuthFailed, llm.Error{Code: llm.CodeAuth, Retryable: true, Scope: llm.ScopeCredential})
	})
	t.Run("crash", k.crash)
	t.Run("shutdown", k.shutdown)
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

func (k *kit) wantError(t *testing.T, s Scenario, want llm.Error) {
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
	if e.Code != want.Code || e.Retryable != want.Retryable || e.Scope != want.Scope || e.RetryAfterMS != want.RetryAfterMS {
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
