package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func toolChoiceEcho(minor int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	exitWhenStdinCloses()
	reply := func(conn net.Conn, req *llm.Request) {
		raw, _ := json.Marshal(map[string]any{"tool_choice": req.Options.ToolChoice, "tools": req.Tools})
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

func allowedChoice(policy string) llm.Request {
	return llm.Request{Model: "m", DropPolicy: policy, Items: []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Weather in Oslo?"}}},
	}, Tools: []llm.Tool{
		{Name: "get_weather", Description: "Weather for a place."},
		{Name: "get_time"},
		{Name: "read_file"},
		{Type: llm.ToolNative, Name: "web_search"},
	}, Options: llm.Options{ToolChoice: &llm.ToolChoice{
		Mode: llm.ToolChoiceRequired, Allowed: []string{"get_weather", "get_time"},
	}}}
}

func TestAllowedToolsDowngrade(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	for _, c := range []struct {
		minor     int
		wantTools []string
	}{
		{25, []string{"get_weather", "get_time"}},
		{26, []string{"get_weather", "get_time", "read_file", "web_search"}},
	} {
		ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("allowed-tools-%d", c.minor)), ManifestFile), Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		defer ext.Stop()
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
				sess, e := ext.OpenSession(ctx, acct, protocol.SessionInfo{Format: protocol.SessionOpenAIResponses})
				if e != nil {
					t.Fatalf("%s: OpenSession: %+v", name, e)
				}
				defer sess.Close()
				s := sess.Turn(ctx, acct, r)
				return first(name, s.Next, s.Close)
			}},
		} {
			for _, policy := range []string{llm.DropReport, llm.DropReject} {
				name := fmt.Sprintf("%s at minor %d, %s policy", call.name, c.minor, policy)
				req := allowedChoice(policy)
				ev := call.next(req, name)
				if ev.Type != llm.EventResponseStart {
					t.Fatalf("%s: first event %+v, want response.start", name, ev)
				}
				var got struct {
					ToolChoice *llm.ToolChoice `json:"tool_choice"`
					Tools      []struct {
						Name string `json:"name"`
						Type string `json:"type"`
					} `json:"tools"`
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
				var names []string
				for _, tool := range got.Tools {
					names = append(names, tool.Name)
				}
				if !reflect.DeepEqual(names, c.wantTools) {
					t.Errorf("%s: extension got tools %q, want %q", name, names, c.wantTools)
				}
				var paths []string
				for _, l := range ev.Response.Translation.Losses {
					paths = append(paths, l.Path)
				}
				if !reflect.DeepEqual(paths, []string{"metadata"}) {
					t.Errorf("%s: losses %q, want the extension's own alone", name, paths)
				}
				if len(req.Options.ToolChoice.Allowed) != 2 || len(req.Tools) != 4 {
					t.Errorf("%s: the caller's request was changed: %+v, %+v", name, req.Options.ToolChoice, req.Tools)
				}
			}
		}
	}
}

func TestAllowedToolsDowngradeNoTool(t *testing.T) {
	ctx := context.Background()
	acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
	ghost := func(policy string) llm.Request {
		return llm.Request{Model: "m", DropPolicy: policy, Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Weather in Oslo?"}}},
		}, Tools: []llm.Tool{{Name: "get_weather"}}, Options: llm.Options{ToolChoice: &llm.ToolChoice{
			Mode: llm.ToolChoiceRequired, Allowed: []string{"ghost"},
		}}}
	}
	for _, c := range []struct {
		minor   int
		policy  string
		wantErr bool
	}{
		{25, llm.DropReport, true},
		{25, llm.DropReject, true},
		{26, llm.DropReport, false},
	} {
		ext, err := Start(filepath.Join(fakePackage(t, fmt.Sprintf("allowed-tools-%d", c.minor)), ManifestFile), Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		defer ext.Stop()
		s := ext.Generate(ctx, acct, ghost(c.policy))
		ev := nextWithin(t, "generate", s.Next)
		s.Close()
		name := fmt.Sprintf("minor %d, %s policy", c.minor, c.policy)
		if !c.wantErr {
			if ev.Type != llm.EventResponseStart {
				t.Errorf("%s: first event %+v, want the request sent with its list", name, ev)
			}
			continue
		}
		if ev.Type != llm.EventError || ev.Error.Code != llm.CodeInvalidRequest ||
			ev.Error.Scope != llm.ScopeRequest || ev.Error.Status != http.StatusBadRequest {
			t.Fatalf("%s: %+v, want an invalid request error", name, ev)
		}
		if !strings.Contains(ev.Error.Message, "ghost") {
			t.Errorf("%s: message %q, want it to name the allowed tool nothing defines", name, ev.Error.Message)
		}
	}
}
