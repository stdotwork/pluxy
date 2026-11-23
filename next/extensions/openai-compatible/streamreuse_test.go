package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	chunkText = `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`
	chunkStop = `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
)

type collected struct{ events []llm.Event }

func (c *collected) Send(ev llm.Event) error {
	c.events = append(c.events, ev)
	return nil
}

// A streamed reply is read to its end, so the connection goes back to the transport and the next
// request reuses it instead of dialing a new one. A read that stopped at "[DONE]" left the
// connection to be closed whenever the end of the transfer had not arrived yet, as here, where the
// upstream ends the chunked body a little after its last event.
func TestStreamedReplyKeepsItsConnection(t *testing.T) {
	var conns atomic.Int32
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunkText, chunkStop)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond) // the end of the transfer follows the last event
	}))
	up.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	up.Start()
	t.Cleanup(up.Close)

	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	for i := 0; i < 3; i++ {
		resp, err := client.Post(up.URL, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		var got collected
		if err := relayStream(context.Background(), resp.Body, &got, llm.FormatOpenAIChat); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if n := len(got.events); n == 0 || got.events[n-1].Type != llm.EventFinish {
			t.Fatalf("reply %d ended with events %+v, want a finish last", i, got.events)
		}
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("three streamed replies opened %d connections to the upstream, want 1", n)
	}
}

// An upstream that holds the stream open after its last event does not hold the reply back for
// longer than afterEndWait, and its body is left as it was.
func TestStreamedReplyEndsWhenUpstreamHoldsTheStreamOpen(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", chunkText, chunkStop)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(up.Close)
	t.Cleanup(func() { close(release) })

	resp, err := http.Post(up.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	var got collected
	if err := relayStream(context.Background(), resp.Body, &got, llm.FormatOpenAIChat); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < afterEndWait/2 || d > 3*afterEndWait {
		t.Errorf("relayStream returned after %v, want about %v", d, afterEndWait)
	}
}
