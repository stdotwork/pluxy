package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func serveFakeEndpoint(minor int) {
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
	os.Stdout.WriteString(protocol.HandshakeLine(protocol.Version, socket))
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
					Info: &protocol.Info{
						ID:            os.Getenv(protocol.EnvExtensionID),
						Version:       "0.1.0",
						Protocol:      protocol.Version,
						ProtocolMinor: minor,
						Operations:    []string{protocol.OpGenerate, protocol.OpEndpoint},
					},
				})
			case protocol.OpEndpoint:
				format := ""
				if call.Request != nil && call.Request.Native != nil {
					format = call.Request.Native.Format
				}
				start, _ := json.Marshal(llm.EndpointReply{ContentType: "application/json"})
				piece, _ := json.Marshal(`{"embedding":{"values":[1]}}`)
				for _, ev := range []llm.Event{
					{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: format, Raw: start}},
					{Type: llm.EventNative, Raw: piece},
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

func TestConformanceGeminiEmbeddings(t *testing.T) {
	if protocol.Minor < host.GeminiEmbeddingsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.GeminiEmbeddingsMinor)
	}
	req := func(format string) llm.Request {
		return llm.Request{Model: "gemini-embedding-001", Native: &llm.Native{Format: format, Body: []byte(`{}`)}}
	}
	formats := []string{llm.FormatGeminiEmbedContent, llm.FormatGeminiBatchEmbedContents}

	t.Run("minor gate", func(t *testing.T) {
		for _, tc := range []struct {
			mode      string
			wantServe bool
		}{{"endpoint-35", false}, {"endpoint-36", true}} {
			ext, err := host.Start(fakeCapabilitiesManifest(t, tc.mode), host.Options{Log: io.Discard})
			if err != nil {
				t.Fatalf("%s start: %v", tc.mode, err)
			}
			t.Cleanup(ext.Stop)
			for _, format := range formats {
				s := ext.Endpoint(context.Background(), protocol.Account{ID: "a"}, req(format))
				ev := s.Next()
				s.Close()
				if tc.wantServe {
					if ev.Type != llm.EventResponseStart || ev.Response == nil || ev.Response.Format != format {
						t.Errorf("%s %s: first event = %+v, want response.start", tc.mode, format, ev)
					}
					continue
				}
				if ev.Type != llm.EventError || ev.Error == nil || ev.Error.Code != llm.CodeUnsupported ||
					ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable {
					t.Errorf("%s %s: first event = %+v, want a retryable unsupported refusal on the credential", tc.mode, format, ev)
				}
			}
		}
	})

	t.Run("client to upstream", func(t *testing.T) {
		replies := map[string]string{
			":embedContent":       `{"embedding":{"values":[0.25,0.5]},"usageMetadata":{"promptTokenCount":3}}`,
			":batchEmbedContents": `{"embeddings":[{"values":[1,2]},{"values":[3,4]}],"usageMetadata":{"promptTokenCount":6}}`,
		}
		var paths []string
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			for suffix, reply := range replies {
				if strings.HasSuffix(r.URL.Path, suffix) {
					io.WriteString(w, reply)
					return
				}
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(up.Close)

		manifest := BuildExtension(t, "../extensions/gemini/pluxy-extension.json", "../extensions/gemini")
		ext, err := host.Start(manifest, host.Options{Log: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(ext.Stop)
		attempt := func(ctx context.Context, _ string, a protocol.Account, r llm.Request) route.Stream {
			a.Settings = json.RawMessage(`{"base_url":"` + up.URL + `"}`)
			a.Credential = &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "fake-key"}
			return ext.Endpoint(ctx, a, r)
		}
		router, err := route.New(route.Config{}, []route.Account{{ID: "a1"}}, attempt, nil)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &server.Config{ClientKeys: []string{"k"}}
		core := httptest.NewServer(server.NewHandler(source{&server.Generation{Config: cfg, Router: router, Attempt: attempt, Endpoint: attempt}}))
		t.Cleanup(core.Close)

		for _, tc := range []struct{ path, suffix, body string }{
			{"/v1beta/models/gemini-embedding-001:embedContent", ":embedContent", `{"content":{"parts":[{"text":"hi"}]}}`},
			{"/v1beta/models/gemini-embedding-001:batchEmbedContents", ":batchEmbedContents",
				`{"requests":[{"content":{"parts":[{"text":"a"}]}},{"content":{"parts":[{"text":"b"}]}}]}`},
		} {
			r, _ := http.NewRequest(http.MethodPost, core.URL+tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Goog-Api-Key", "k")
			resp, err := http.DefaultClient.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(got) != replies[tc.suffix] {
				t.Errorf("POST %s: status %d, body %s; want 200 and %s", tc.path, resp.StatusCode, got, replies[tc.suffix])
			}
		}
		if len(paths) != 2 {
			t.Errorf("the upstream saw %v, want one :embedContent and one :batchEmbedContents call", paths)
		}
	})
}

type source struct{ g *server.Generation }

func (s source) Acquire() (*server.Generation, func()) { return s.g, func() {} }
