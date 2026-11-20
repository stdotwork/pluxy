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

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// toolChoiceEcho is an extension that reports protocol minor version minor and answers a generate
// call or a session turn with the request's tool choice in response.start's raw, and a translation
// report with one loss of its own.
func toolChoiceEcho(minor int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	reply := func(conn net.Conn, req *llm.Request) {
		raw, _ := json.Marshal(map[string]any{"tool_choice": req.Options.ToolChoice})
		tr := &llm.Translation{From: llm.FormatOpenAIChat, To: llm.FormatOpenAIResponses,
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

// allowedChoice is a request whose tool choice names the tools the model may call, as core reads
// one from a Chat client.
func allowedChoice(policy string) llm.Request {
	return llm.Request{Model: "m", DropPolicy: policy, Items: []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Weather in Oslo?"}}},
	}, Options: llm.Options{ToolChoice: &llm.ToolChoice{
		Mode: llm.ToolChoiceRequired, Allowed: []string{"get_weather", "get_time"},
	}}}
}

// An extension before protocol minor 26 gets the tool choice without the allowed list, and the
// reply's translation report lists it as dropped beside the extension's own losses; under the
// reject drop policy the request is refused before it is sent. From minor 26 the extension gets
// the list. Generate calls and session turns alike.
func TestAllowedToolsDowngrade(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	for _, c := range []struct {
		minor      int
		wantLosses []string
	}{
		{25, []string{"metadata", "options.tool_choice.allowed"}},
		{26, []string{"metadata"}},
	} {
		ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("allowed-tools-%d", c.minor)), ManifestFile), Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		defer ext.Stop()
		sess, e := ext.OpenSession(ctx, acct, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
		if e != nil {
			t.Fatalf("OpenSession: %+v", e)
		}
		defer sess.Close()
		// first returns a call's first event, having read it to its end, and fails the test if
		// an event does not come within 10 seconds: the fake extension answers one session turn
		// only, so a change that lets the allowed list through leaves the second turn
		// unanswered, and the test must fail there rather than hang.
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
			req := allowedChoice(llm.DropReport)
			ev := call.next(req, name)
			if ev.Type != llm.EventResponseStart {
				t.Fatalf("%s: first event %+v, want response.start", name, ev)
			}
			var got struct {
				ToolChoice *llm.ToolChoice `json:"tool_choice"`
			}
			if err := json.Unmarshal(ev.Response.Raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.ToolChoice == nil || got.ToolChoice.Mode != llm.ToolChoiceRequired {
				t.Errorf("%s: extension got %+v, want the required choice", name, got.ToolChoice)
			}
			if c.minor < 26 && len(got.ToolChoice.Allowed) != 0 {
				t.Errorf("%s: extension got allowed %q, want the choice without a list", name, got.ToolChoice.Allowed)
			}
			if c.minor >= 26 && !reflect.DeepEqual(got.ToolChoice.Allowed, []string{"get_weather", "get_time"}) {
				t.Errorf("%s: extension got allowed %q, want both names", name, got.ToolChoice.Allowed)
			}
			var paths []string
			for _, l := range ev.Response.Translation.Losses {
				paths = append(paths, l.Path)
			}
			if !reflect.DeepEqual(paths, c.wantLosses) {
				t.Errorf("%s: losses %q, want %q", name, paths, c.wantLosses)
			}
			if len(req.Options.ToolChoice.Allowed) != 2 {
				t.Errorf("%s: the caller's request was changed: %+v", name, req.Options.ToolChoice)
			}
			if c.minor < 26 {
				ev := call.next(allowedChoice(llm.DropReject), name)
				if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || !ev.Error.Retryable ||
					!reflect.DeepEqual(ev.Error.Dropped, c.wantLosses[1:]) {
					t.Errorf("%s, reject policy: %+v, want unsupported naming the dropped path", name, ev)
				}
			}
		}
	}
}
