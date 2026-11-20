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
// reports whether the request parsed from the body is still alive.
type probe struct {
	parsed weak.Pointer[llm.Native]
	n      int
	alive  chan bool
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
		p.alive <- p.parsed.Value() != nil
		return llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}}
	case 3:
		return llm.Event{Type: llm.EventTextDelta, Index: 0, Delta: "hi"}
	case 4:
		return llm.Event{Type: llm.EventItemEnd, Index: 0}
	}
	return llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}
}

func (p *probe) Close() {}

// Once a reply has begun, the routed call keeps only the request's model, so the rest of its
// parsed form can go while the reply streams, whether the client asked for a stream or not.
func TestStreamingReplyLetsTheParsedRequestGo(t *testing.T) {
	big := strings.Repeat("x", 200<<10)
	for _, stream := range []bool{true, false} {
		name := "not streamed"
		if stream {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			alive := make(chan bool, 1)
			attempt := func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
				if req.Native == nil {
					t.Fatal("the attempt got no parsed native request")
				}
				return &probe{parsed: weak.Make(req.Native), alive: alive}
			}
			router, err := route.New(route.Config{}, []route.Account{{ID: "work"}}, attempt, nil)
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}}, Router: router, Attempt: attempt}})
			body := `{"model":"m","max_tokens":5,"stream":` + map[bool]string{true: "true", false: "false"}[stream] +
				`,"messages":[{"role":"user","content":"` + big + `"}]}`
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
			r.Header.Set("X-Api-Key", "k")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			if got := <-alive; got {
				t.Error("the parsed request is alive after its reply began")
			}
		})
	}
}
