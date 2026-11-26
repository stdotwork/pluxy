package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Devin returns one candidate, so a client's candidate count above one is reported dropped under
// the field it came from, and drop_policy reject refuses the request before anything is sent.
func TestCandidateCountDropped(t *testing.T) {
	chatBody := func(n string) []byte {
		return []byte(`{"model":"swe-2","n":` + n + `,"messages":[{"role":"user","content":"hi"}]}`)
	}
	geminiBody := func(n string) []byte {
		return []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"candidateCount":` + n + `}}`)
	}
	parse := map[string]func(string) (llm.Request, error){
		"chat": func(n string) (llm.Request, error) { return openaichat.ParseRequest(chatBody(n)) },
		"gemini": func(n string) (llm.Request, error) {
			return geminigenerate.ParseRequest("swe-2", false, geminiBody(n))
		},
	}
	paths := map[string]string{"chat": "n", "gemini": "generationConfig.candidateCount"}
	for name, p := range parse {
		t.Run(name, func(t *testing.T) {
			req, err := p("2")
			if err != nil {
				t.Fatal(err)
			}
			c, err := buildRequest(&req)
			if err != nil {
				t.Fatal(err)
			}
			if got := lossPaths(c); !slices.Contains(got, paths[name]) {
				t.Errorf("dropped %v, missing %s", got, paths[name])
			}

			srv := devintest.New(t)
			req.DropPolicy = llm.DropReject
			err = chat(context.Background(), sdk.Call{Account: protocol.Account{Settings: srv.Settings(), Credential: tokenCredential(srv.Token())}, Request: req}, &recorder{})
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || !slices.Contains(e.Dropped, paths[name]) || len(srv.Requests()) != 0 {
				t.Errorf("reject policy: err %v, %d requests sent; want unsupported naming %s and none sent", err, len(srv.Requests()), paths[name])
			}

			// One candidate is what Devin returns, so asking for one drops nothing.
			one, err := p("1")
			if err != nil {
				t.Fatal(err)
			}
			c, err = buildRequest(&one)
			if err != nil {
				t.Fatal(err)
			}
			if got := lossPaths(c); slices.Contains(got, paths[name]) {
				t.Errorf("a count of one is reported dropped: %v", got)
			}
		})
	}
}
