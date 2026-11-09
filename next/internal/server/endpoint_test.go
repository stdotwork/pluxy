package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// endpointHandler returns the client API handler with accounts "a" and "b" whose endpoint
// attempts answer with reply; generate attempts fail the test. It returns the requests each
// attempt received, with the account they went to.
func endpointHandler(t *testing.T, reply func(account string, req llm.Request) []llm.Event) (http.Handler, *[]string, *[]llm.Request) {
	t.Helper()
	var accounts []string
	var seen []llm.Request
	gen := func(context.Context, string, protocol.Account, llm.Request) route.Stream {
		t.Error("an endpoint request went to generate")
		return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInternal}}}}
	}
	ep := func(_ context.Context, _ string, a protocol.Account, req llm.Request) route.Stream {
		accounts = append(accounts, a.ID)
		seen = append(seen, req)
		return &events{evs: reply(a.ID, req)}
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "a"}, {ID: "b"}}, gen, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClientKeys: []string{"k"}}
	return NewHandler(fixedSource{&Generation{Config: cfg, Router: router, Attempt: gen, Endpoint: ep}}), &accounts, &seen
}

func postEndpoint(h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer k")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(w, r)
	return w
}

func bodyReply(contentType string, pieces ...string) []llm.Event {
	raw, _ := json.Marshal(llm.EndpointReply{ContentType: contentType})
	evs := []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: llm.FormatOpenAIResponsesCompact, Raw: raw}}}
	for _, p := range pieces {
		b, _ := json.Marshal(p)
		evs = append(evs, llm.Event{Type: llm.EventNative, Raw: b})
	}
	return append(evs, llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}})
}

// A compaction request goes to an account as an endpoint call carrying the client's body, model,
// thinking suffix and Codex headers; the reply's body pieces reach the client as written, under
// the extension's content type, on both routes.
func TestEndpointCompactPassesBodyThrough(t *testing.T) {
	h, _, seen := endpointHandler(t, func(string, llm.Request) []llm.Event {
		return bodyReply("application/json", `{"object":"response.compaction",`, `"output":[]}`)
	})
	for _, path := range []string{"/v1/responses/compact", "/backend-api/codex/responses/compact"} {
		*seen = nil
		w := postEndpoint(h, path, `{"model":"gpt-x(high)","input":"hi"}`, map[string]string{"Session-Id": "s1"})
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"object":"response.compaction","output":[]}` {
			t.Fatalf("%s: got %d %q %s", path, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
		req := (*seen)[0]
		if req.Model != "gpt-x" || req.Options.ThinkingSuffix != "high" || req.Native == nil || req.Native.Format != llm.FormatOpenAIResponsesCompact {
			t.Fatalf("%s: request = %+v", path, req)
		}
		if req.Native.Headers["session-id"] != "s1" || !strings.Contains(string(req.Native.Body), `"model":"gpt-x"`) {
			t.Fatalf("%s: native = %+v %s", path, req.Native.Headers, req.Native.Body)
		}
	}
}

// A compaction request that asks for a stream, and a body that is not a JSON object, are refused
// before they reach an account.
func TestEndpointCompactRefusals(t *testing.T) {
	h, accounts, _ := endpointHandler(t, func(string, llm.Request) []llm.Event {
		return bodyReply("application/json", `{}`)
	})
	for body, want := range map[string]string{
		`{"model":"m","stream":true}`: "Streaming not supported for compact responses",
		`[1]`:                         "not a JSON object",
	} {
		w := postEndpoint(h, "/v1/responses/compact", body, nil)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: got %d %s", body, w.Code, w.Body)
		}
	}
	if len(*accounts) != 0 {
		t.Fatalf("attempts went to %v", *accounts)
	}
}

// An error before the reply starts moves the request to the next account, as for a generate
// request; when every account fails the client gets an OpenAI-style error.
func TestEndpointErrorsRoute(t *testing.T) {
	n := 0
	h, accounts, _ := endpointHandler(t, func(string, llm.Request) []llm.Event {
		n++
		if n == 1 {
			return []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "no"}}}
		}
		return bodyReply("application/json", `{"ok":true}`)
	})
	w := postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` || len(*accounts) != 2 || (*accounts)[0] == (*accounts)[1] {
		t.Fatalf("got %d %s after %v", w.Code, w.Body, *accounts)
	}

	h, _, _ = endpointHandler(t, func(string, llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeRateLimit, Status: 429, Scope: llm.ScopeCredential, Retryable: true, Message: "slow down", RetryAfterMS: 2000}}}
	})
	w = postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" || !strings.Contains(w.Body.String(), `"rate_limit_error"`) {
		t.Fatalf("got %d %v %s", w.Code, w.Header(), w.Body)
	}
}

// A streamed endpoint reply is flushed piece by piece, and an error after it started ends it with
// an error event; a reply that carries items breaks the protocol and ends the body.
func TestEndpointStreamAndBreaches(t *testing.T) {
	h, _, _ := endpointHandler(t, func(string, llm.Request) []llm.Event {
		evs := bodyReply("text/event-stream", "data: 1\n\n")
		evs[len(evs)-1] = llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUpstream, Message: "broke"}}
		return evs
	})
	w := postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(w.Body.String(), "data: 1\n\nevent: error\ndata: {") || !strings.Contains(w.Body.String(), "broke") {
		t.Fatalf("got %v %q", w.Header(), w.Body)
	}

	h, _, _ = endpointHandler(t, func(string, llm.Request) []llm.Event {
		evs := bodyReply("application/json", `{"a":`)
		return append(evs[:2], llm.Event{Type: llm.EventItemStart, Item: &llm.ItemStart{Kind: llm.ItemMessage}})
	})
	w = postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Body.String() != `{"a":` {
		t.Fatalf("got %q", w.Body)
	}

	h, _, _ = endpointHandler(t, func(string, llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{}}, {Type: llm.EventFinish, Finish: &llm.Finish{}}}
	})
	w = postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "content type") {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
