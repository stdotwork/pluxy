package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCountTokensRefusesAsGenerating(t *testing.T) {
	var sent int
	old := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"upstream says no"}}`)), Request: r}, nil
	})
	t.Cleanup(func() { client.Transport = old })

	gemini := func(body, policy string) llm.Request {
		t.Helper()
		req, err := geminigenerate.ParseRequest("kimi-k2", false, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		req.DropPolicy = policy
		return req
	}
	as := func(req llm.Request, format, body string) llm.Request {
		req.Native = &llm.Native{Format: format, Body: json.RawMessage(body)}
		return req
	}
	count := func(req llm.Request) (*llm.Error, int) {
		t.Helper()
		sent = 0
		call := sdk.Call{Account: protocol.Account{ID: "a", Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-kimi-test"}}, Request: req}
		var e *llm.Error
		if err := countTokens(context.Background(), call, nil); !errors.As(err, &e) {
			t.Fatalf("err = %v, want an *llm.Error", err)
		}
		return e, sent
	}
	const thought = `{"contents":[{"role":"user","parts":[{"text":"Hi"}]},{"role":"model","parts":[{"text":"secret","thought":true},{"text":"Hello."}]},{"role":"user","parts":[{"text":"Again."}]}]}`
	const jsonOut = `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"responseMimeType":"application/json"}}`

	refused := gemini(thought, llm.DropReject)
	e, n := count(refused)
	if e.Code != llm.CodeUnsupported || !slices.Equal(e.Dropped, []string{"contents[1].parts[0]"}) || n != 0 {
		t.Fatalf("thought under reject: error %+v after %d requests, want unsupported with dropped [contents[1].parts[0]] and nothing sent", e, n)
	}
	if _, _, gen := buildBody(refused, upstreamFor(refused), nil); gen == nil || gen.Error() != e.Error() {
		t.Errorf("generating refuses with %v, counting with %v; want the same refusal", gen, e)
	}
	for name, req := range map[string]llm.Request{
		"thought under report":    gemini(thought, llm.DropReport),
		"JSON output, reject":     gemini(jsonOut, llm.DropReject),
		"Chat client, reject":     as(refused, llm.FormatOpenAIChat, `{"model":"kimi-k2","messages":[{"role":"user","content":"Hi"}]}`),
		"Responses, reject":       as(refused, llm.FormatOpenAIResponses, `{"model":"kimi-k2","input":"Hi"}`),
		"Messages client, reject": as(refused, llm.FormatAnthropicMessages, `{"model":"kimi-k2","max_tokens":10,"messages":[{"role":"user","content":"Hi"}]}`),
	} {
		if e, n := count(req); e.Dropped != nil || !strings.Contains(e.Message, "upstream says no") || n != 1 {
			t.Errorf("%s: error %+v after %d requests, want the count sent once and the upstream's refusal", name, e, n)
		}
	}
	noKey := sdk.Call{Account: protocol.Account{ID: "a"}, Request: refused}
	var auth *llm.Error
	if err := countTokens(context.Background(), noKey, nil); !errors.As(err, &auth) || auth.Code != llm.CodeAuth {
		t.Errorf("no credential: err = %v, want auth", err)
	}
}
