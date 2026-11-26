package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Usage the upstream reported before a streamed reply failed reaches core with the error, so
// tokens the upstream counted are not lost when the reply does not finish.
func TestStreamFailureKeepsUsage(t *testing.T) {
	const (
		text  = `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`
		usage = `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scenario := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]
		if scenario == "broken" {
			// A chunked reply cut off without its last chunk: the read fails mid-stream.
			conn, buf, _ := w.(http.Hijacker).Hijack()
			defer conn.Close()
			fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
			for _, d := range []string{text, usage} {
				ev := "data: " + d + "\n\n"
				fmt.Fprintf(buf, "%x\r\n%s\r\n", len(ev), ev)
			}
			buf.Flush()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", text, usage)
		if scenario == "error" {
			fmt.Fprint(w, `data: {"error":{"message":"overloaded","code":"server_busy"}}`+"\n\n")
		}
		// "eof": the stream ends with neither a finish reason nor [DONE].
	}))
	t.Cleanup(up.Close)

	ext, err := host.Start(conformance.BuildExtension(t, "pluxy-extension.json", "."), host.Options{Log: testLog{t}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	for _, scenario := range []string{"error", "eof", "broken"} {
		t.Run(scenario, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]string{"base_url": up.URL + "/" + scenario + "/v1"})
			s := ext.Generate(context.Background(), protocol.Account{
				ID: "a", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-usage"},
			}, llm.Request{Model: "m", Stream: true, Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}}})
			defer s.Close()
			var ev llm.Event
			for ev = s.Next(); !ev.Terminal(); ev = s.Next() {
			}
			if ev.Type != llm.EventError {
				t.Fatalf("terminal event %+v, want an error", ev)
			}
			u := ev.Error.Usage
			if u == nil || u.InputTokens == nil || *u.InputTokens != 9 || u.OutputTokens == nil || *u.OutputTokens != 2 {
				t.Fatalf("error %+v carries usage %+v, want 9 input and 2 output tokens", ev.Error, u)
			}
		})
	}
}

type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(p)))
	for sc.Scan() {
		l.t.Log(sc.Text())
	}
	return len(p), nil
}
