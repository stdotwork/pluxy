package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func toolResultEcho(minor int, passthrough bool) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	exitWhenStdinCloses()
	reply := func(conn net.Conn, req *llm.Request) {
		raw, _ := json.Marshal(map[string]any{"items": req.Items, "options": req.Options})
		tr := &llm.Translation{From: llm.FormatAnthropicMessages, To: llm.FormatOpenAIChat, Passthrough: passthrough,
			Losses: []llm.Loss{{Kind: llm.LossDropped, Path: "metadata"}}}
		_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventResponseStart,
			Response: &llm.ResponseInfo{ID: "r", Model: "m", Raw: raw, Translation: tr}}})
		_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}})
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
			case protocol.OpGenerate:
				reply(conn, call.Request)
			case protocol.OpSession:
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeSessionReady}})
				var turn protocol.Call
				if protocol.ReadFrame(conn, &turn) != nil {
					return
				}
				reply(conn, turn.Request)
				_, _ = io.Copy(io.Discard, conn)
			}
		}()
	}
}

func richResult(policy string) llm.Request {
	return llm.Request{Model: "m", DropPolicy: policy, Items: []llm.Item{
		{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "shot", Arguments: "{}"},
		{Type: llm.ItemTypeToolResult, CallID: "c1", Output: "Saved.", IsError: true,
			Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[1].content[0]"},
			Content: []llm.Part{
				{Type: llm.PartText, Text: "Saved.", Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[1].content[0].content[0]"}},
				{Type: llm.PartImage, URL: "data:image/png;base64,AAAA", Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[1].content[0].content[1]"}},
			}},
	}}
}

func nextWithin(t *testing.T, what string, next func() llm.Event) llm.Event {
	t.Helper()
	ch := make(chan llm.Event, 1)
	go func() { ch <- next() }()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: no event within 10s", what)
		return llm.Event{}
	}
}

func TestToolResultPartsDowngrade(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	for _, c := range []struct {
		minor      int
		wantLosses []string
	}{
		{24, []string{"metadata", "messages[1].content[0].content[1]", "messages[1].content[0].is_error"}},
		{25, []string{"metadata"}},
	} {
		ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("tool-result-%d", c.minor)), ManifestFile), Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		defer ext.Stop()
		sess, e := ext.OpenSession(ctx, acct, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
		if e != nil {
			t.Fatalf("OpenSession: %+v", e)
		}
		defer sess.Close()
		first := func(name string, next func() llm.Event, close func()) llm.Event {
			defer close()
			ev := nextWithin(t, name, next)
			for e := ev; !e.Terminal(); e = nextWithin(t, name, next) {
			}
			return ev
		}
		for _, call := range []struct {
			name string
			next func(llm.Request, string) llm.Event
		}{
			{"generate", func(r llm.Request, name string) llm.Event {
				s := ext.Generate(ctx, acct, r)
				return first(name, s.Next, s.Close)
			}},
			{"session", func(r llm.Request, name string) llm.Event {
				s := sess.Turn(ctx, acct, r)
				return first(name, s.Next, s.Close)
			}},
		} {
			name := fmt.Sprintf("%s at minor %d", call.name, c.minor)
			req := richResult(llm.DropReport)
			ev := call.next(req, name)
			if ev.Type != llm.EventResponseStart {
				t.Fatalf("%s: first event %+v, want response.start", name, ev)
			}
			var got struct{ Items []llm.Item }
			if err := json.Unmarshal(ev.Response.Raw, &got); err != nil {
				t.Fatal(err)
			}
			res := got.Items[1]
			if c.minor < 25 && (len(res.Content) != 0 || res.IsError || res.Output != "Saved.") {
				t.Errorf("%s: extension got %+v, want the text alone", name, res)
			}
			if c.minor >= 25 && (len(res.Content) != 2 || !res.IsError) {
				t.Errorf("%s: extension got %+v, want the parts and the error flag", name, res)
			}
			var paths []string
			for _, l := range ev.Response.Translation.Losses {
				paths = append(paths, l.Path)
			}
			if !reflect.DeepEqual(paths, c.wantLosses) {
				t.Errorf("%s: losses %q, want %q", name, paths, c.wantLosses)
			}
			if len(req.Items[1].Content) != 2 || !req.Items[1].IsError {
				t.Errorf("%s: the caller's request was changed: %+v", name, req.Items[1])
			}
			if c.minor < 25 {
				ev := call.next(richResult(llm.DropReject), name)
				if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || !ev.Error.Retryable ||
					!reflect.DeepEqual(ev.Error.Dropped, c.wantLosses[1:]) {
					t.Errorf("%s, reject policy: %+v, want unsupported naming the dropped paths", name, ev)
				}
			}
		}
	}
}

func TestPassthroughKeepsCoreLosses(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	ext, err := Start(filepath.Join(fakePackage(t, "tool-result-passthrough-24"), ManifestFile), Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	s := ext.Generate(ctx, acct, richResult(llm.DropReport))
	defer s.Close()
	ev := nextWithin(t, "passthrough", s.Next)
	if ev.Type != llm.EventResponseStart || ev.Response == nil || ev.Response.Translation == nil {
		t.Fatalf("first event %+v, want response.start with the translation report", ev)
	}
	if !ev.Response.Translation.Passthrough {
		t.Errorf("translation %+v, want the extension's passthrough report", ev.Response.Translation)
	}
	var paths []string
	for _, l := range ev.Response.Translation.Losses {
		paths = append(paths, l.Path)
	}
	want := []string{"metadata", "messages[1].content[0].content[1]", "messages[1].content[0].is_error"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("losses %q, want %q", paths, want)
	}
}
