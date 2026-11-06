//go:build unix

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// fakeSession is the session the "session" fake extension holds. Each turn's reply has the ID
// "turn-<n>", n counting the session's turns, so a test sees its turns share one session. A
// turn for model "wait" waits for an interrupt and finishes with the interrupt's message as its
// raw reason. The account "no-session" takes no sessions.
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
