package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"weak"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// probe is a reply that, once its first event has gone to the client, collects garbage and
// reports whether the request's body is still alive.
type probe struct {
	body  weak.Pointer[byte]
	n     int
	alive chan bool
}

func (p *probe) Next() llm.Event {
	p.n++
	switch p.n {
	case 1:
		return llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}}
	case 2:
		for range 3 {
			runtime.GC()
		}
		p.alive <- p.body.Value() != nil
		return llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}}
	case 3:
		return llm.Event{Type: llm.EventTextDelta, Index: 0, Delta: "hi"}
	case 4:
		return llm.Event{Type: llm.EventItemEnd, Index: 0}
	}
	return llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}
}

func (p *probe) Close() {}

// Once a reply has begun, nothing on its way keeps the request's body (nor the request parsed
// from it, which refers to it) unless every request is logged, whose log holds the body.
func TestStreamingReplyLetsTheRequestGo(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	for _, tc := range []struct {
		name   string
		log    *RequestLog
		stream bool
		alive  bool
	}{
		{"stream, no request log", nil, true, false},
		{"stream, error logs only", &RequestLog{All: false}, true, false},
		{"not streamed, error logs only", &RequestLog{All: false}, false, false},
		{"stream, every request logged", &RequestLog{All: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.log != nil {
				tc.log.Dir = t.TempDir()
			}
			alive := make(chan bool, 1)
			attempt := func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
				return &probe{body: weak.Make(&req.Native.Body[0]), alive: alive}
			}
			router, err := route.New(route.Config{}, []route.Account{{ID: "work"}}, attempt, nil)
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}}, Router: router, Attempt: attempt, RequestLog: tc.log}})
			body := `{"model":"m","max_tokens":5,"stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] +
				`,"messages":[{"role":"user","content":"` + big + `"}]}`
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			r.Header.Set("X-Api-Key", "k")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := <-alive; got != tc.alive {
				t.Errorf("the request body is alive while the reply streams: %v, want %v", got, tc.alive)
			}
		})
	}
}
