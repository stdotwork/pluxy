package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type writeSpyListener struct {
	net.Listener
	onWrite func(p []byte) error
}

func (l writeSpyListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return writeSpyConn{c, l.onWrite}, nil
}

type writeSpyConn struct {
	net.Conn
	onWrite func(p []byte) error
}

func (c writeSpyConn) Write(p []byte) (int, error) {
	if err := c.onWrite(p); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}

type turnEndSource struct {
	g     *Generation
	armed *atomic.Bool
	ended chan struct{}
}

func (s turnEndSource) Acquire() (*Generation, func()) {
	return s.g, func() {
		if s.armed.Load() {
			s.ended <- struct{}{}
		}
	}
}

func decoded(t *testing.T, frames ...string) []llm.Event {
	t.Helper()
	d := openairesponses.NewStreamDecoder()
	var out []llm.Event
	for _, f := range frames {
		evs, err := d.Decode([]byte(f))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, evs...)
	}
	return out
}

func TestWebSocketTurnRecordedBeforeLastMessage(t *testing.T) {
	const (
		created   = `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`
		completed = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":5}}}`
		cut       = `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":7,"output_tokens":5}}}`
	)
	seven, five := int64(7), int64(5)
	translated := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "gpt-test"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "hi"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Usage: &llm.Usage{InputTokens: &seven, OutputTokens: &five}},
	}
	for _, tc := range []struct {
		name   string
		reply  func(t *testing.T) []llm.Event
		last   string
		leave  string
		events int
	}{
		{name: "native stream", reply: func(t *testing.T) []llm.Event { return decoded(t, created, completed) }, last: "response.completed", events: 1},
		{name: "translated stream", reply: func(*testing.T) []llm.Event { return translated }, last: "response.completed", events: 1},
		{name: "stream opening with response.completed", reply: func(t *testing.T) []llm.Event { return decoded(t, completed) }, last: "response.completed", events: 1},
		{name: "stream opening with response.incomplete", reply: func(t *testing.T) []llm.Event { return decoded(t, cut) }, last: "response.incomplete", events: 1},
		{name: "warmup", last: "response.completed"},
		{name: "warmup left at response.created", leave: "response.created"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := usage.Open(usage.Options{Dir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { store.Close() })
			count := func() []usage.Event { return store.Events(usage.Filter{}, 0, 100).Events }
			attempt := func(context.Context, string, protocol.Account, llm.Request) route.Stream {
				return &events{evs: tc.reply(t)}
			}
			router, err := route.New(route.Config{}, []route.Account{{ID: "a1"}}, attempt, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &Config{ClientKeys: []string{"k"}}
			if err := cfg.CORS.prepare(); err != nil {
				t.Fatal(err)
			}
			src := turnEndSource{g: &Generation{Config: cfg, Router: router, Attempt: attempt, Usage: store}, armed: &atomic.Bool{}, ended: make(chan struct{}, 1)}

			var mu sync.Mutex
			atLast := -1
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			spy := writeSpyListener{ln, func(p []byte) error {
				switch {
				case tc.leave != "" && strings.Contains(string(p), `"type":"`+tc.leave+`"`):
					return errors.New("the client went away")
				case tc.last != "" && strings.Contains(string(p), `"type":"`+tc.last+`"`):
					mu.Lock()
					atLast = len(count())
					mu.Unlock()
				}
				return nil
			}}
			srv := &http.Server{Handler: NewHandler(src)}
			go srv.Serve(spy)
			t.Cleanup(func() { srv.Close() })

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws://"+ln.Addr().String()+"/v1/responses", &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": {"Bearer k"}}, CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			src.armed.Store(true)
			msg := map[string]any{"type": "response.create", "model": "gpt-test", "input": []any{}}
			if tc.reply == nil {
				msg["generate"] = false
			}
			b, _ := json.Marshal(msg)
			if err := c.Write(ctx, websocket.MessageText, b); err != nil {
				t.Fatal(err)
			}
			select {
			case <-src.ended:
			case <-ctx.Done():
				t.Fatal("the turn did not end")
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.last != "" && atLast != tc.events {
				t.Errorf("usage events when %s was written: %d, want %d", tc.last, atLast, tc.events)
			}
			got := count()
			if len(got) != tc.events {
				t.Fatalf("usage events after the turn: %+v, want %d", got, tc.events)
			}
			for _, e := range got {
				if e.Failed || e.Status != http.StatusOK || e.Tokens.Total == nil || *e.Tokens.Total != 12 {
					t.Errorf("the turn's event: %+v", e)
				}
			}
		})
	}
}
