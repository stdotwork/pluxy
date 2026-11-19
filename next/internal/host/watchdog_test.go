package host

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// watchFake is an extension without the SDK that reports protocol minor version minor and acts
// on a call's or turn's model:
//
//	"silent" sends nothing until the connection closes
//	"stall"  sends response.start, then nothing until the connection closes
//	"alive"  sends response.start, then an alive frame every 50ms for 600ms, then finish
//	"slow"   sends response.start and a text delta "alive=<the call's alive>", then nothing for
//	         600ms, then finish
func watchFake(minor int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	// Core closes stdin to stop the extension.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
	answer := func(conn net.Conn, call protocol.Call) {
		model := ""
		if call.Request != nil {
			model = call.Request.Model
		}
		start := func() {
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r"}}})
		}
		finish := func() {
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}})
		}
		switch model {
		case "silent":
		case "stall":
			start()
		case "alive":
			start()
			for range 12 {
				time.Sleep(50 * time.Millisecond)
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeAlive}})
			}
			finish()
		case "slow":
			start()
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}}})
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventTextDelta, Index: 0, Delta: fmt.Sprintf("alive=%t", call.Alive)}})
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventItemEnd, Index: 0}})
			time.Sleep(600 * time.Millisecond)
			finish()
		}
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
				return
			case protocol.OpGenerate:
				answer(conn, call)
			case protocol.OpSession:
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeSessionReady}})
				for {
					var turn protocol.Call
					if protocol.ReadFrame(conn, &turn) != nil {
						return
					}
					answer(conn, turn)
				}
			}
			// Wait for core to close the connection.
			var b [1]byte
			_, _ = conn.Read(b[:])
		}()
	}
}

// shortWatchdog makes the watchdog wait about 200ms past 1ms limits for the test.
func shortWatchdog(t *testing.T) Options {
	t.Helper()
	old := watchdogGrace
	watchdogGrace = 200 * time.Millisecond
	t.Cleanup(func() { watchdogGrace = old })
	ms := func(v int64) *int64 { return &v }
	return Options{Log: &syncBuffer{}, Timeouts: func() *protocol.Timeouts { return &protocol.Timeouts{HeaderMS: ms(1), IdleMS: ms(1), TotalMS: ms(1)} }}
}

func startWatchFake(t *testing.T, minor int, opts Options) *Extension {
	t.Helper()
	ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("watch-%d", minor)), ManifestFile), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	return ext
}

// drain reads a stream to its terminal event, failing the test if that takes longer than d.
func drain(t *testing.T, next func() llm.Event, d time.Duration) (llm.Event, time.Duration) {
	t.Helper()
	begin := time.Now()
	ch := make(chan llm.Event, 1)
	go func() {
		for {
			if ev := next(); ev.Terminal() {
				ch <- ev
				return
			}
		}
	}()
	select {
	case ev := <-ch:
		return ev, time.Since(begin)
	case <-time.After(d):
		t.Fatalf("no terminal event within %s", d)
		return llm.Event{}, 0
	}
}

// For an extension that reports protocol 0.19, core ends a call that sends nothing for the
// limits plus the grace with extension_timeout: retry-safe before response.start, not after;
// and it logs a line naming the code and the account.
func TestWatchdogEndsSilentCall(t *testing.T) {
	opts := shortWatchdog(t)
	ext := startWatchFake(t, 19, opts)
	for _, tc := range []struct {
		model     string
		retryable bool
	}{{"silent", true}, {"stall", false}} {
		t.Run(tc.model, func(t *testing.T) {
			s := ext.Generate(context.Background(), protocol.Account{ID: "acct-w"}, llm.Request{Model: tc.model, Stream: true})
			defer s.Close()
			ev, took := drain(t, s.Next, 5*time.Second)
			if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionTimeout || ev.Error.Retryable != tc.retryable || ev.Error.Status != 504 {
				t.Fatalf("event %+v, want extension_timeout, status 504, retryable %t", ev.Error, tc.retryable)
			}
			if took < 200*time.Millisecond {
				t.Errorf("ended after %s, before the limit", took)
			}
		})
	}
	log := opts.Log.(*syncBuffer).String()
	if !strings.Contains(log, "extension_timeout") || !strings.Contains(log, "\"acct-w\"") {
		t.Errorf("log %q names no extension_timeout on acct-w", log)
	}
}

// Alive frames keep a quiet call open past the watchdog's limit, and never reach the caller.
func TestWatchdogAliveKeepsCallOpen(t *testing.T) {
	ext := startWatchFake(t, 19, shortWatchdog(t))
	s := ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "alive", Stream: true})
	defer s.Close()
	var types []string
	for {
		ev := s.Next()
		types = append(types, ev.Type)
		if ev.Terminal() {
			break
		}
	}
	if len(types) != 2 || types[1] != llm.EventFinish {
		t.Fatalf("events %v, want response.start and finish", types)
	}
	if s.Alives() != 12 {
		t.Errorf("%d alive frames counted, want 12", s.Alives())
	}
}

// An extension below protocol 0.19 is not told it may send alive frames and gets no watchdog;
// one that reports 0.19 is told. With the idle limit turned off, there is no watchdog either.
func TestWatchdogOnlyForAliveExtensions(t *testing.T) {
	for _, tc := range []struct {
		minor int
		opts  func(t *testing.T) Options
		alive string
	}{
		{18, shortWatchdog, "alive=false"},
		{19, func(t *testing.T) Options {
			o := shortWatchdog(t)
			zero := int64(0)
			o.Timeouts = func() *protocol.Timeouts { return &protocol.Timeouts{IdleMS: &zero} }
			return o
		}, "alive=true"},
	} {
		t.Run(fmt.Sprint(tc.minor), func(t *testing.T) {
			ext := startWatchFake(t, tc.minor, tc.opts(t))
			s := ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "slow", Stream: true})
			defer s.Close()
			var text string
			for {
				ev := s.Next()
				text += ev.Delta
				if ev.Terminal() {
					if ev.Type != llm.EventFinish {
						t.Fatalf("ended with %+v, want finish", ev.Error)
					}
					break
				}
			}
			if text != tc.alive {
				t.Errorf("the extension saw %q, want %q", text, tc.alive)
			}
		})
	}
}

// The watchdog covers session turns too: a turn whose alive frames keep coming finishes; a
// silent turn ends with extension_timeout and the session with it.
func TestWatchdogSessionTurn(t *testing.T) {
	ext := startWatchFake(t, 19, shortWatchdog(t))
	account := protocol.Account{ID: "acct-s"}
	s, e := ext.OpenSession(context.Background(), account, responsesSession)
	if e != nil {
		t.Fatalf("OpenSession: %+v", e)
	}
	defer s.Close()
	turn := s.Turn(context.Background(), account, llm.Request{Model: "alive", Stream: true})
	if ev, _ := drain(t, turn.Next, 5*time.Second); ev.Type != llm.EventFinish {
		t.Fatalf("alive turn ended with %+v, want finish", ev.Error)
	}
	if turn.Alives() != 12 {
		t.Errorf("%d alive frames counted on the turn, want 12", turn.Alives())
	}
	turn = s.Turn(context.Background(), account, llm.Request{Model: "silent", Stream: true})
	ev, _ := drain(t, turn.Next, 5*time.Second)
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionTimeout || !ev.Error.Retryable {
		t.Fatalf("silent turn ended with %+v, want a retry-safe extension_timeout", ev.Error)
	}
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end after the turn timed out")
	}
}

// The watchdog waits as long as the SDK may still wait on the upstream, and a minute more. Nothing
// reaches core before the upstream's headers, so a streamed call may be silent for the header limit
// as well as the idle limit; a request that is not streamed may run to its total limit too. With a
// limit the SDK waits on turned off (0), core cannot tell a quiet call from a stuck one: no watchdog.
func TestWatchdogLimit(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	tm := func(header, idle, total int64) *protocol.Timeouts {
		return &protocol.Timeouts{HeaderMS: ms(header), IdleMS: ms(idle), TotalMS: ms(total)}
	}
	s := time.Second
	for _, tc := range []struct {
		name   string
		t      *protocol.Timeouts
		stream bool
		want   time.Duration
	}{
		{"streamed defaults", tm(60_000, 300_000, 600_000), true, 5*time.Minute + time.Minute},
		{"streamed header over idle", tm(600_000, 300_000, 0), true, 10*time.Minute + time.Minute},
		{"streamed header off", tm(0, 300_000, 600_000), true, 0},
		{"streamed idle off", tm(60_000, 0, 600_000), true, 0},
		{"streamed total is not waited on", tm(1_000, 2_000, 0), true, 2*s + time.Minute},
		{"not streamed defaults", tm(60_000, 300_000, 600_000), false, 10*time.Minute + time.Minute},
		{"not streamed header longest", tm(900_000, 300_000, 600_000), false, 15*time.Minute + time.Minute},
		{"not streamed idle longest", tm(1_000, 700_000, 600_000), false, 700*s + time.Minute},
		{"not streamed header off", tm(0, 300_000, 600_000), false, 0},
		{"not streamed idle off", tm(60_000, 0, 600_000), false, 0},
		{"not streamed total off", tm(60_000, 300_000, 0), false, 0},
		{"no timeouts", nil, true, 0},
		{"a limit unset", &protocol.Timeouts{IdleMS: ms(1_000)}, true, 0},
	} {
		if got := watchdog(tc.t, tc.stream); got != tc.want {
			t.Errorf("%s: watchdog = %s, want %s", tc.name, got, tc.want)
		}
	}
}
