//go:build unix

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/format"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The test binary doubles as the fake extension below. Core clears an extension's environment,
// so the fake reads its behaviour from the extension ID core passes it.
func TestMain(m *testing.M) {
	if mode, ok := strings.CutPrefix(os.Getenv(protocol.EnvExtensionID), "servertest/"); ok {
		frameExtension(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// frameSeen is what the fake extension saw of a generate call's frame.
type frameSeen struct {
	NativeOnly bool `json:"native_only"`
	Items      int  `json:"items"`
	Tools      int  `json:"tools"`
	Origin     bool `json:"origin"`
	Native     bool `json:"native"`
}

// frameExtension describes itself at core's minor, with core's native_parser when mode is
// "parses" and none otherwise. It writes what each generate call's frame held to the file
// "frame" in its working folder (the package folder), answers with an empty reply, and exits
// when core closes its stdin.
func frameExtension(mode string) {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	parser := 0
	if mode == "parses" {
		parser = format.ParserVersion
	}
	os.Stdout.WriteString(protocol.HandshakeLine(protocol.Version, socket))
	go func() {
		// Core closes stdin to stop the extension.
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
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
						Protocol: protocol.Version, ProtocolMinor: protocol.Minor, NativeParser: parser,
						Operations: []string{protocol.OpGenerate},
					},
				})
				return
			}
			f := frameSeen{NativeOnly: call.NativeOnly}
			if r := call.Request; r != nil {
				f.Items, f.Tools, f.Origin, f.Native = len(r.Items), len(r.Tools), r.Origin != nil, r.Native != nil
			}
			b, _ := json.Marshal(f)
			if os.WriteFile("frame.tmp", b, 0o644) == nil {
				_ = os.Rename("frame.tmp", "frame")
			}
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}}})
			_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}})
			_, _ = io.Copy(io.Discard, conn)
		}()
	}
}

// frameServer serves core's client API with one account on a real extension that runs
// frameExtension in mode, and returns the server and the extension's package folder.
func frameServer(t *testing.T, mode string) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(dir, "ext")); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{
		"manifest_version": 0,
		"id":               "servertest/" + mode,
		"version":          "0.0.0",
		"protocol":         []int{protocol.Version},
		"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "ext"},
	})
	if err := os.WriteFile(filepath.Join(dir, host.ManifestFile), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	ext, err := host.Start(filepath.Join(dir, host.ManifestFile), host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	attempt := func(ctx context.Context, _ string, a protocol.Account, req llm.Request) route.Stream {
		a.Credential = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-1"}
		return ext.Generate(ctx, a, req)
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "a1"}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClientKeys: []string{"k"}}
	if err := cfg.CORS.prepare(); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(fixedSource{&Generation{Config: cfg, Router: router, Attempt: attempt}}))
	t.Cleanup(srv.Close)
	return srv, dir
}

// readFrame waits for the frame the fake extension saw.
func readFrame(t *testing.T, dir string) frameSeen {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "frame"))
		if err == nil {
			var f frameSeen
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("frame %q: %v", b, err)
			}
			return f
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			t.Fatalf("the extension got no generate call: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// wantFrame is the frame of a request with one item, one tool and an origin: native-only to an
// extension that parses with core's parsers, and the full request to one that does not.
func wantFrame(mode string) frameSeen {
	if mode == "parses" {
		return frameSeen{NativeOnly: true, Native: true}
	}
	return frameSeen{Items: 1, Tools: 1, Origin: true, Native: true}
}

// A request a client sends over HTTP reaches an extension that parses with core's parsers as a
// native-only call, and one that does not as the full request (serve marks what it parsed).
func TestServeSendsNativeOnlyFrame(t *testing.T) {
	for _, mode := range []string{"parses", "full"} {
		t.Run(mode, func(t *testing.T) {
			srv, dir := frameServer(t, mode)
			body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"user":"u"}`
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader([]byte(body)))
			req.Header.Set("Authorization", "Bearer k")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			reply, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, reply)
			}
			if got, want := readFrame(t, dir), wantFrame(mode); got != want {
				t.Errorf("frame = %+v, want %+v", got, want)
			}
		})
	}
}

// A Responses WebSocket turn that starts a response, on an extension without sessions, goes as
// an HTTP call of its whole conversation: native-only to an extension that parses with core's
// parsers, and the full request to one that does not (runTurn marks what it parsed).
func TestWebSocketTurnSendsNativeOnlyFrame(t *testing.T) {
	for _, mode := range []string{"parses", "full"} {
		t.Run(mode, func(t *testing.T) {
			srv, dir := frameServer(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/responses", &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": {"Bearer k"}}, CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			msg := `{"type":"response.create","model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"user":"u"}`
			if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				t.Fatal(err)
			}
			if got, want := readFrame(t, dir), wantFrame(mode); got != want {
				t.Errorf("frame = %+v, want %+v", got, want)
			}
		})
	}
}
