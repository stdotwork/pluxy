package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// nativeOnlyFrame is what the fake extension saw of a call frame.
type nativeOnlyFrame struct {
	NativeOnly bool `json:"native_only"`
	Items      int  `json:"items"`
	Tools      int  `json:"tools"`
	Origin     bool `json:"origin"`
	Native     bool `json:"native"`
}

// nativeOnlyExtension answers describe with the given minor and native_parser, and
// every generate and count_tokens call with an internal error whose message is the JSON of what
// the call frame held.
func nativeOnlyExtension(minor, parser int) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	exitWhenStdinCloses()
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
			if call.Op == protocol.OpDescribe {
				_ = protocol.WriteFrame(conn, protocol.Reply{
					Event: llm.Event{Type: protocol.TypeDescribeResult},
					Info: &protocol.Info{
						ID: os.Getenv(protocol.EnvExtensionID), Version: "0.0.0",
						Protocol: protocol.Version, ProtocolMinor: minor, NativeParser: parser,
						Operations: []string{protocol.OpGenerate, protocol.OpCountTokens},
					},
				})
				return
			}
			f := nativeOnlyFrame{NativeOnly: call.NativeOnly}
			if r := call.Request; r != nil {
				f.Items, f.Tools, f.Origin, f.Native = len(r.Items), len(r.Tools), r.Origin != nil, r.Native != nil
			}
			msg, _ := json.Marshal(f)
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{
				Type:  llm.EventError,
				Error: &llm.Error{Code: llm.CodeInternal, Message: string(msg)},
			}})
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
}

// nativeOnlyBody has items, a tool and an origin once parsed.
const nativeOnlyBody = `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"user":"u"}`

func nativeOnlyRequest(t *testing.T, parsed bool) llm.Request {
	t.Helper()
	req, err := openaichat.ParseRequest([]byte(nativeOnlyBody))
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Items) == 0 || len(req.Tools) == 0 || req.Origin == nil || req.Native == nil {
		t.Fatalf("test request lacks items, tools, origin or native: %+v", req)
	}
	if parsed {
		req.Native.Parsed = req.Native.Body
	}
	return req
}

// Core leaves a generate request's items, tools and origin out of the frame only for an
// extension at minor 27 or later whose native_parser is core's, and only when the request's native
// body is the one it was parsed from; count_tokens always gets the full request.
func TestNativeOnlyFrame(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   string
		parsed bool
		count  bool
		want   bool // native-only
	}{
		{"minor 27 parses native", "native-only-27-parses", true, false, true},
		{"minor 26 parses native", "native-only-26-parses", true, false, false},
		{"minor 27 without parses native", "native-only-27", true, false, false},
		{"minor 27 parses with another parser version", "native-only-27-skew", true, false, false},
		{"parsed unset", "native-only-27-parses", false, false, false},
		{"count tokens", "native-only-27-parses", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ext, err := Start(filepath.Join(fakePackage(t, tc.mode), ManifestFile), Options{Log: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			acct := protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}}
			req := nativeOnlyRequest(t, tc.parsed)
			var s *Stream
			if tc.count {
				s = ext.CountTokens(context.Background(), acct, req)
			} else {
				s = ext.Generate(context.Background(), acct, req)
			}
			ev := s.Next()
			s.Close()
			if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeInternal {
				t.Fatalf("event = %+v, want the fake's error", ev)
			}
			var got nativeOnlyFrame
			if err := json.Unmarshal([]byte(ev.Error.Message), &got); err != nil {
				t.Fatalf("message %q: %v", ev.Error.Message, err)
			}
			want := nativeOnlyFrame{NativeOnly: false, Items: len(req.Items), Tools: len(req.Tools), Origin: true, Native: true}
			if tc.want {
				want = nativeOnlyFrame{NativeOnly: true, Native: true}
			}
			if got != want {
				t.Errorf("frame = %+v, want %+v", got, want)
			}
		})
	}
}
