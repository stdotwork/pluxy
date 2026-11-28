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

type probe struct {
	part  weak.Pointer[llm.Part]
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
		p.alive <- p.part.Value() != nil
		return llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}}
	case 3:
		return llm.Event{Type: llm.EventTextDelta, Index: 0, Delta: "hi"}
	case 4:
		return llm.Event{Type: llm.EventItemEnd, Index: 0}
	}
	return llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}
}

func (p *probe) Close() {}

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
				if len(req.Items) == 0 || len(req.Items[0].Content) == 0 {
					t.Fatal("the attempt got no parsed request part")
				}
				return &probe{part: weak.Make(&req.Items[0].Content[0]), alive: alive}
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
