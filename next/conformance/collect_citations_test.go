package conformance

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// serveFakeCitedReply is an extension at the current protocol minor whose every generate call
// answers with one cited message, the citation's URL a bad one when badURL is set.
func serveFakeCitedReply(badURL bool) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = ln.Close()
		os.Exit(0)
	}()
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	url := "https://example.com/a"
	if badURL {
		url = "javascript:alert(1)"
	}
	s, e := 0, 5
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			var call protocol.Call
			if protocol.ReadFrame(conn, &call) != nil {
				return
			}
			switch call.Op {
			case protocol.OpDescribe:
				_ = protocol.WriteFrame(conn, protocol.Reply{
					Event: llm.Event{Type: protocol.TypeDescribeResult},
					Info: &protocol.Info{ID: os.Getenv(protocol.EnvExtensionID), Version: "0.1.0", Protocol: protocol.Version,
						ProtocolMinor: protocol.Minor, Operations: []string{protocol.OpGenerate}},
				})
			case protocol.OpGenerate:
				for _, ev := range []llm.Event{
					{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
					{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
					{Type: llm.EventTextDelta, Index: 0, Delta: "Alpha beta."},
					{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: url, Title: "A", StartIndex: &s, EndIndex: &e}}},
					{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
				} {
					if protocol.WriteFrame(conn, protocol.Reply{Event: ev}) != nil {
						return
					}
				}
			}
		}()
	}
}

// recordT is a testing.TB that records what a check reports, so a test can ask whether the check
// reported it.
type recordT struct {
	testing.TB
	errs []string
}

func (r *recordT) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recordT) Fatalf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
	r.TB.FailNow()
}

// The kit's collect checks the citations of every reply it reads, so an extension that sends one
// the protocol does not allow fails the kit, and one that sends a good one does not.
func TestCollectChecksReplyCitations(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		wantErr string
	}{
		{"reply-cited", ""},
		{"reply-cited-badurl", "reply citations"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ext, err := host.Start(fakeCapabilitiesManifest(t, tc.mode), host.Options{Log: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			s := ext.Generate(context.Background(), protocol.Account{ID: "a"}, request(false))
			defer s.Close()
			rec := &recordT{TB: t}
			evs := collect(rec, s)
			if last := evs[len(evs)-1]; last.Type != llm.EventFinish {
				t.Fatalf("ended with %s: %v", last.Type, last.Error)
			}
			switch {
			case tc.wantErr == "" && len(rec.errs) != 0:
				t.Errorf("collect reported %q for a good reply", rec.errs)
			case tc.wantErr != "" && !strings.Contains(strings.Join(rec.errs, "\n"), tc.wantErr):
				t.Errorf("collect reported %q, want %q", rec.errs, tc.wantErr)
			}
		})
	}
}
