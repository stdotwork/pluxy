//go:build unix

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
	"github.com/stdotwork/pluxy/next/sdk/payload"
	"reflect"
)

// fakeSession is the session the "session" fake extension holds. Each turn's reply has the ID
// "turn-<n>", n counting the session's turns, so a test sees its turns share one session. A
// turn for model "wait" waits for an interrupt and finishes with the interrupt's message as its
// raw reason; one for model "payload" finishes with a message after sdk.ApplyPayload as its raw
// reason. The account "no-session" takes no sessions.
type fakeSession struct {
	turns      int
	interrupts chan json.RawMessage
}

func openFakeSession(_ context.Context, call sdk.SessionCall) (sdk.Session, error) {
	if call.Account.ID == "no-session" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Message: "this account takes no sessions"}
	}
	return &fakeSession{interrupts: make(chan json.RawMessage, 1)}, nil
}

func (f *fakeSession) Turn(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	f.turns++
	if err := out.Start(llm.ResponseInfo{ID: fmt.Sprintf("turn-%d", f.turns), Model: call.Request.Model}); err != nil {
		return err
	}
	if call.Request.Model == "payload" {
		// The upstream message a turn would send: the call's payload rules apply to it.
		b := sdk.ApplyPayload(ctx, []byte(`{"model":"payload","tools":[{"name":"apply_patch","n":1},{"name":"apply_patch","n":2}]}`))
		return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: string(b)}, nil)
	}
	if call.Request.Model == "wait" {
		select {
		case raw := <-f.interrupts:
			return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: string(raw)}, nil)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

func (f *fakeSession) Interrupt(raw json.RawMessage) {
	select {
	case f.interrupts <- raw:
	default:
	}
}

func (f *fakeSession) Close() {}

var responsesSession = protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}

func openSession(t *testing.T, g *Managed, account string) *Session {
	t.Helper()
	s, e := g.OpenSession(context.Background(), protocol.Account{ID: account}, responsesSession)
	if e != nil {
		t.Fatalf("OpenSession: %+v", e)
	}
	t.Cleanup(s.Close)
	return s
}

// turnEvents runs a turn to its end and returns its events.
func turnEvents(s *Session, ctx context.Context, account, model string) []llm.Event {
	t := s.Turn(ctx, protocol.Account{ID: account}, llm.Request{Model: model})
	defer t.Close()
	var evs []llm.Event
	for {
		ev := t.Next()
		evs = append(evs, ev)
		if ev.Terminal() {
			return evs
		}
	}
}

func waitClosed(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not happen", what)
	}
}

// Turns on a session reach the same session in the extension, one after another.
func TestSessionTurnsShareOneSession(t *testing.T) {
	g, _ := prepareFake(t, "session", 0)
	s := openSession(t, g, "a")
	for i := 1; i <= 2; i++ {
		evs := turnEvents(s, context.Background(), "a", "m")
		if len(evs) != 2 || evs[0].Type != llm.EventResponseStart || evs[1].Type != llm.EventFinish {
			t.Fatalf("turn %d: %+v; want start and finish", i, evs)
		}
		if want := fmt.Sprintf("turn-%d", i); evs[0].Response.ID != want {
			t.Errorf("turn %d reply ID %q, want %q", i, evs[0].Response.ID, want)
		}
	}
	if s.Ended() {
		t.Error("session ended after its turns")
	}
	s.Close()
	waitClosed(t, s.Done(), "the session's end")
}

// An extension without the session operation, and one that refuses the account, answer with
// unsupported on the credential: core then serves the account without a session.
func TestSessionRefused(t *testing.T) {
	for _, c := range []struct{ mode, account string }{{"serve", "a"}, {"session", "no-session"}} {
		g, _ := prepareFake(t, c.mode, 0)
		s, e := g.OpenSession(context.Background(), protocol.Account{ID: c.account}, responsesSession)
		if s != nil || e == nil || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
			t.Errorf("%s, account %s: session %v, error %+v; want unsupported on the credential", c.mode, c.account, s, e)
		}
	}
}

// A turn for another account is refused without ending the session.
func TestSessionTurnForAnotherAccount(t *testing.T) {
	g, _ := prepareFake(t, "session", 0)
	s := openSession(t, g, "a")
	if evs := turnEvents(s, context.Background(), "b", "m"); len(evs) != 1 || evs[0].Type != llm.EventError {
		t.Fatalf("turn for account b: %+v; want one error", evs)
	}
	if evs := turnEvents(s, context.Background(), "a", "m"); evs[len(evs)-1].Type != llm.EventFinish {
		t.Fatalf("next turn: %+v; want it to finish", evs)
	}
}

// An interrupt reaches the running turn in the extension as the client sent it; the session
// then takes the next turn.
func TestSessionInterrupt(t *testing.T) {
	g, _ := prepareFake(t, "session", 0)
	s := openSession(t, g, "a")
	turn := s.Turn(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "wait"})
	defer turn.Close()
	if ev := turn.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event %+v, want the start", ev)
	}
	const msg = `{"type":"response.interrupt","response_id":"turn-1"}`
	s.Interrupt([]byte(msg))
	if ev := turn.Next(); ev.Type != llm.EventFinish || ev.Finish.RawReason != msg {
		t.Fatalf("event after the interrupt %+v; want a finish carrying %s", ev, msg)
	}
	if evs := turnEvents(s, context.Background(), "a", "m"); evs[0].Response == nil || evs[0].Response.ID != "turn-2" {
		t.Fatalf("next turn: %+v; want turn-2", evs)
	}
}

// Cancelling a running turn ends the session.
func TestSessionCancelledTurnEndsSession(t *testing.T) {
	g, _ := prepareFake(t, "session", 0)
	s := openSession(t, g, "a")
	ctx, cancel := context.WithCancel(context.Background())
	turn := s.Turn(ctx, protocol.Account{ID: "a"}, llm.Request{Model: "wait"})
	defer turn.Close()
	if ev := turn.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event %+v, want the start", ev)
	}
	cancel()
	if ev := turn.Next(); ev.Type != llm.EventError || ev.Error.Code != llm.CodeCancelled {
		t.Fatalf("event after cancelling %+v; want cancelled", ev)
	}
	waitClosed(t, s.Done(), "the session's end")
	if evs := turnEvents(s, context.Background(), "a", "m"); len(evs) != 1 || evs[0].Type != llm.EventError {
		t.Fatalf("turn on the ended session: %+v; want one error", evs)
	}
}

// A draining generation closes its idle sessions at once and a busy one when its turn ends,
// opens no new ones, and finishes draining once they are closed.
func TestDrainClosesSessions(t *testing.T) {
	g, _ := prepareFake(t, "session", 0)
	idle := openSession(t, g, "a")
	busy := openSession(t, g, "b")
	turn := busy.Turn(context.Background(), protocol.Account{ID: "b"}, llm.Request{Model: "wait"})
	defer turn.Close()
	if ev := turn.Next(); ev.Type != llm.EventResponseStart {
		t.Fatalf("first event %+v, want the start", ev)
	}
	drained := make(chan struct{})
	g.Drain(func() { close(drained) })
	waitClosed(t, idle.Done(), "the idle session's end")
	if busy.Ended() {
		t.Fatal("the busy session ended before its turn")
	}
	if _, e := g.OpenSession(context.Background(), protocol.Account{ID: "c"}, responsesSession); e == nil {
		t.Error("a draining generation opened a session")
	}
	busy.Interrupt([]byte(`{}`))
	if ev := turn.Next(); ev.Type != llm.EventFinish {
		t.Fatalf("busy turn ended with %+v, want a finish", ev)
	}
	waitClosed(t, busy.Done(), "the busy session's end")
	waitClosed(t, drained, "the drain")
}

// sessionBadFrame is an extension that holds sessions without the SDK, reports protocol minor
// version minor, and answers every turn, and every other call on an account with a credential or
// settings, with an event of an unknown type that holds the account's API key and settings; it
// hangs up on any other call.
func sessionBadFrame(minor int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	leak := func(a *protocol.Account) protocol.Reply {
		key := ""
		if a.Credential != nil {
			key = a.Credential.APIKey
		}
		return protocol.Reply{Event: llm.Event{Type: "leak-" + key + string(a.Settings)}}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		go func() {
			defer conn.Close()
			var call protocol.Call
			if protocol.ReadFrame(conn, &call) != nil {
				return
			}
			switch call.Op {
			case protocol.OpDescribe:
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeDescribeResult},
					Info: &protocol.Info{ID: os.Getenv(protocol.EnvExtensionID), Version: "0.0.0", Protocol: protocol.Version, ProtocolMinor: minor,
						Operations: []string{protocol.OpGenerate, protocol.OpSession}}})
			case protocol.OpSession:
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeSessionReady}})
				var turn protocol.Call
				if protocol.ReadFrame(conn, &turn) != nil || turn.Account == nil || turn.Account.Credential == nil {
					return
				}
				_ = protocol.WriteFrame(conn, leak(turn.Account))
				_, _ = io.Copy(io.Discard, conn)
			case protocol.OpLogin:
				// A login with method "identity" ends with a grant whose identity has a field named
				// after what the user entered and the account's settings, with no value.
				if call.Login != nil && call.Login.Method == "identity" && call.Account != nil {
					_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginDone}, Grant: &protocol.Grant{
						Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-gr-0123"},
						Identity:   map[string]string{"id " + call.Login.Input["api_key"] + " " + string(call.Account.Settings) + " sk-gr-0123": ""}}})
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				_ = protocol.WriteFrame(conn, leak(call.Account))
				_, _ = io.Copy(io.Discard, conn)
			default:
				// An account with nothing to leak is hung up on.
				if a := call.Account; a != nil && (a.Credential != nil || len(a.Settings) > 0) {
					_ = protocol.WriteFrame(conn, leak(call.Account))
					_, _ = io.Copy(io.Discard, conn)
				}
			}
		}()
	}
}

// Every other call whose reply breaks the protocol with a frame holding the account's credential
// and the secret in its settings gets a protocol error without either in it.
func TestProtocolErrorsAreRedacted(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	ctx := context.Background()
	const key, setting = "sk-protocol-leak-test-0123456789", "sk-protocol-setting-0123456789"
	account := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key},
		Settings: json.RawMessage(`{"api_key":"` + setting + `"}`)}
	errs := map[string]*llm.Error{}
	_, errs["check"] = ext.Check(ctx, account)
	_, errs["quota"] = ext.Quota(ctx, account)
	_, errs["models"] = ext.Models(ctx, account)
	_, errs["refresh"] = ext.Refresh(ctx, account)
	login := ext.Login(ctx, account, protocol.Login{Method: "m"})
	errs["login"] = login.Next().Error
	login.Close()
	s := ext.Generate(ctx, account, llm.Request{Model: "m"})
	errs["generate"] = s.Next().Error
	s.Close()
	for op, e := range errs {
		switch {
		case e == nil || e.Code != llm.CodeExtensionProtocol:
			t.Errorf("%s: %+v; want a protocol error", op, e)
		case strings.Contains(e.Message, key) || strings.Contains(e.Message, setting):
			t.Errorf("%s: the protocol error holds the credential or the settings secret: %q", op, e.Message)
		case !strings.Contains(e.Message, protocol.Redacted):
			t.Errorf("%s: the protocol error does not say what was removed: %q", op, e.Message)
		}
	}
}

// A secret with a control byte in it is removed from a protocol error that quotes it, whether
// core quotes it (a check's reply type) or the event sequence check does (a generate's).
func TestProtocolErrorsQuoteNoControlByteSecret(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	ctx := context.Background()
	const key = "sk-bell\x07-0123456789"
	account := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}
	_, check := ext.Check(ctx, account)
	s := ext.Generate(ctx, account, llm.Request{Model: "m"})
	generate := s.Next().Error
	s.Close()
	for op, e := range map[string]*llm.Error{"check": check, "generate": generate} {
		if e == nil || strings.Contains(e.Message, "0123456789") || !strings.Contains(e.Message, protocol.Redacted) {
			t.Errorf("%s: %+v; want a protocol error without the key", op, e)
		}
	}
}

// A grant whose identity is refused is reported without what the user entered, the account's
// settings secrets or the grant's own credential, even when the identity names them.
func TestLoginIdentityErrorIsRedacted(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	const input, setting = "sk-in-0123", "sk-st-0123"
	login := ext.Login(context.Background(), protocol.Account{ID: "a", Settings: json.RawMessage(`{"api_key":"` + setting + `"}`)},
		protocol.Login{Method: "identity", Input: map[string]string{"api_key": input}})
	defer login.Close()
	e := login.Next().Error
	switch {
	case e == nil || e.Code != llm.CodeExtensionProtocol || !strings.Contains(e.Message, "identity"):
		t.Fatalf("login: %+v; want the refused identity", e)
	case strings.Contains(e.Message, input) || strings.Contains(e.Message, setting) || strings.Contains(e.Message, "sk-gr-0123"):
		t.Errorf("the identity error holds a secret: %q", e.Message)
	case !strings.Contains(e.Message, protocol.Redacted):
		t.Errorf("the identity error does not say what was removed: %q", e.Message)
	}
}

// An extension that breaks the protocol with a frame holding the account's credential and the
// secret in its settings gets a protocol error without either in it.
func TestSessionProtocolErrorIsRedacted(t *testing.T) {
	g, _ := prepareFake(t, "session-bad-frame", 0)
	const key, setting = "sk-session-leak-test-0123456789", "sk-session-setting-0123456789"
	account := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key},
		Settings: json.RawMessage(`{"api_key":"` + setting + `"}`)}
	s, e := g.OpenSession(context.Background(), account, responsesSession)
	if e != nil {
		t.Fatalf("OpenSession: %+v", e)
	}
	defer s.Close()
	turn := s.Turn(context.Background(), account, llm.Request{Model: "m"})
	defer turn.Close()
	ev := turn.Next()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionProtocol {
		t.Fatalf("event %+v; want a protocol error", ev)
	}
	if strings.Contains(ev.Error.Message, key) || strings.Contains(ev.Error.Message, setting) {
		t.Errorf("protocol error holds the credential or the settings secret: %q", ev.Error.Message)
	}
}

// payloadTurnRules is a rule set for model on a fake extension's channel (the last part of its
// extension ID, which is its mode).
func payloadTurnRules(model, channel string) *payload.Rules {
	models := []payload.Model{{Name: model, Protocol: channel}}
	return &payload.Rules{
		Override: []payload.Rule{{Models: models, Params: map[string]any{"service_tier": "priority"}}},
		Filter:   []payload.FilterRule{{Models: models, Params: []string{`tools.#(name=="apply_patch")`}}},
	}
}

// A session turn carries the payload rules that match it, and the SDK applies them to the message
// the extension sends upstream (sdk.ApplyPayload); a turn that no rule matches gets none, and
// rules for another model change nothing. Of two apply_patch tools the filter removes only the
// first, as in the old engine's oracle case "example filter codex flat tools, two patches: first
// only", so the second tool stays only if the rules were applied once.
func TestSessionTurnAppliesPayloadRules(t *testing.T) {
	const message = `{"model":"payload","tools":[{"name":"apply_patch","n":1},{"name":"apply_patch","n":2}]}`
	const changed = `{"model":"payload","service_tier":"priority","tools":[{"name":"apply_patch","n":2}]}`
	for _, c := range []struct {
		name  string
		rules *payload.Rules
		want  string
	}{
		{"rules match the turn", payloadTurnRules("payload", "session"), changed},
		{"rules for another model", payloadTurnRules("other", "session"), message},
		{"no rules", nil, message},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := Options{Log: &syncBuffer{}}
			if c.rules != nil {
				opts.Payload = func() *payload.Rules { return c.rules }
			}
			ext, err := Start(filepath.Join(fakePackage(t, "session"), ManifestFile), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			s, e := ext.OpenSession(context.Background(), protocol.Account{ID: "a"}, responsesSession)
			if e != nil {
				t.Fatalf("OpenSession: %+v", e)
			}
			defer s.Close()
			for turn := 1; turn <= 2; turn++ {
				evs := turnEvents(s, context.Background(), "a", "payload")
				if len(evs) != 2 || evs[1].Type != llm.EventFinish {
					t.Fatalf("turn %d: %+v; want start and finish", turn, evs)
				}
				got := evs[1].Finish.RawReason
				var g, w any
				if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(c.want), &w) != nil || !reflect.DeepEqual(g, w) {
					t.Errorf("turn %d: the message the extension sent is %s, want %s", turn, got, c.want)
				}
			}
		})
	}
}

// An extension older than protocol 0.15 cannot apply payload rules to a session turn (0.14 only
// to a call's HTTP requests), so a turn that rules match is refused before it reaches it, as
// unsupported for the account, and the session stays open; a turn no rule matches reaches it.
func TestOldExtensionGetsNoSessionTurnWithPayloadRules(t *testing.T) {
	for _, mode := range []string{"session-bad-frame", "session-bad-frame-14"} {
		t.Run(mode, func(t *testing.T) {
			rules := payloadTurnRules("payload", mode)
			ext, err := Start(filepath.Join(fakePackage(t, mode), ManifestFile), Options{Log: &syncBuffer{},
				Payload: func() *payload.Rules { return rules }})
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			account := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-fake-key"}}
			s, e := ext.OpenSession(context.Background(), account, responsesSession)
			if e != nil {
				t.Fatalf("OpenSession: %+v", e)
			}
			defer s.Close()
			turn := s.Turn(context.Background(), account, llm.Request{Model: "payload"})
			ev := turn.Next()
			turn.Close()
			if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable ||
				!strings.Contains(ev.Error.Message, "payload rules") {
				t.Fatalf("event %+v, want a retryable unsupported error for the account that names payload rules", ev)
			}
			if s.Ended() {
				t.Error("the refused turn ended the session")
			}
			// The fake extension answers any turn with an unknown frame: one that no rule
			// matches reaches it.
			turn = s.Turn(context.Background(), account, llm.Request{Model: "other"})
			ev = turn.Next()
			turn.Close()
			if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionProtocol {
				t.Fatalf("turn no rule matches: event %+v, want the extension's protocol error", ev)
			}
		})
	}
}
