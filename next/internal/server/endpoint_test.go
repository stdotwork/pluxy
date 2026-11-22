package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// endpointHandler returns the client API handler with accounts "a" and "b" whose endpoint
// attempts answer with reply; generate attempts fail the test. It returns the requests each
// attempt received, with the account they went to.
func endpointHandler(t *testing.T, reply func(account string, req llm.Request) []llm.Event) (http.Handler, *[]string, *[]llm.Request) {
	t.Helper()
	return endpointHandlerOver(t, []route.Account{{ID: "a"}, {ID: "b"}}, reply)
}

// endpointHandlerOver is endpointHandler over the given accounts.
func endpointHandlerOver(t *testing.T, accounts []route.Account, reply func(account string, req llm.Request) []llm.Event) (http.Handler, *[]string, *[]llm.Request) {
	t.Helper()
	var tried []string
	var seen []llm.Request
	gen := func(context.Context, string, protocol.Account, llm.Request) route.Stream {
		t.Error("an endpoint request went to generate")
		return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInternal}}}}
	}
	ep := func(_ context.Context, _ string, a protocol.Account, req llm.Request) route.Stream {
		tried = append(tried, a.ID)
		seen = append(seen, req)
		return &events{evs: reply(a.ID, req)}
	}
	router, err := route.New(route.Config{}, accounts, gen, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClientKeys: []string{"k"}}
	return NewHandler(fixedSource{&Generation{Config: cfg, Router: router, Attempt: gen, Endpoint: ep}}), &tried, &seen
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

// endpointStreamHandler returns the client API handler whose endpoint attempts answer with the
// routed request and a stream made by reply.
func endpointStreamHandler(t *testing.T, reply func(req llm.Request) route.Stream) http.Handler {
	t.Helper()
	gen := func(context.Context, string, protocol.Account, llm.Request) route.Stream {
		t.Error("an endpoint request went to generate")
		return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInternal}}}}
	}
	ep := func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream { return reply(req) }
	router, err := route.New(route.Config{}, []route.Account{{ID: "a"}}, gen, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}}, Router: router, Attempt: gen, Endpoint: ep}})
}

type endpointPause struct {
	entered chan struct{}
	release chan struct{}
}

// pausedEndpointReply is an endpoint reply whose given body pieces pause before they are written,
// after every preceding piece reached the client. It lets a test observe the quiet between events.
func pausedEndpointReply(format, contentType string, pieces []string, pauses map[int]endpointPause) route.Stream {
	raw, _ := json.Marshal(llm.EndpointReply{ContentType: contentType})
	steps := []func() llm.Event{
		func() llm.Event {
			return llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{Format: format, Raw: raw}}
		},
	}
	for i, piece := range pieces {
		i, piece := i, piece
		steps = append(steps, func() llm.Event {
			if pause, ok := pauses[i]; ok {
				close(pause.entered)
				<-pause.release
			}
			body, _ := json.Marshal(piece)
			return llm.Event{Type: llm.EventNative, Raw: body}
		})
	}
	steps = append(steps, func() llm.Event {
		return llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}
	})
	return &stepStream{steps: steps}
}

type stepStream struct {
	steps []func() llm.Event
	i     int
}

func (s *stepStream) Next() llm.Event {
	f := s.steps[s.i]
	if s.i < len(s.steps)-1 {
		s.i++
	}
	return f()
}

func (*stepStream) Close() {}

// responseWriter is an http.ResponseWriter whose body can be read while a handler still writes it.
type responseWriter struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	body    strings.Builder
	flushed bool
}

func newResponseWriter() *responseWriter { return &responseWriter{header: make(http.Header)} }

func (w *responseWriter) Header() http.Header { return w.header }

func (w *responseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = status
	}
}

func (w *responseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *responseWriter) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushed = true
	return nil
}

func (w *responseWriter) Flush() { _ = w.FlushError() }

func (w *responseWriter) snapshot() (int, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.body.String(), w.flushed
}

func waitForBody(t *testing.T, w *responseWriter, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, body, _ := w.snapshot()
		if strings.Contains(body, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("body did not contain %q within %s: %q", want, timeout, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func serveEndpointAsync(h http.Handler, w http.ResponseWriter, path, body string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer k")
		h.ServeHTTP(w, r)
	}()
	return done
}

// A compaction request goes to an account as an endpoint call carrying the client's body, model,
// thinking suffix and Codex headers (the User-Agent among them, by which a Codex extension tells
// a Codex client); the reply's body pieces reach the client as written, under
// the extension's content type, on both routes.
func TestEndpointCompactPassesBodyThrough(t *testing.T) {
	h, _, seen := endpointHandler(t, func(string, llm.Request) []llm.Event {
		return bodyReply("application/json", `{"object":"response.compaction",`, `"output":[]}`)
	})
	for _, path := range []string{"/v1/responses/compact", "/backend-api/codex/responses/compact"} {
		*seen = nil
		w := postEndpoint(h, path, `{"model":"gpt-x(high)","input":"hi"}`, map[string]string{"Session-Id": "s1", "User-Agent": "codex_cli_rs/0.154.0"})
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"object":"response.compaction","output":[]}` {
			t.Fatalf("%s: got %d %q %s", path, w.Code, w.Header().Get("Content-Type"), w.Body)
		}
		req := (*seen)[0]
		if req.Model != "gpt-x" || req.Options.ThinkingSuffix != "high" || req.Native == nil || req.Native.Format != llm.FormatOpenAIResponsesCompact {
			t.Fatalf("%s: request = %+v", path, req)
		}
		if req.Native.Headers["session-id"] != "s1" || req.Native.Headers["user-agent"] != "codex_cli_rs/0.154.0" || !strings.Contains(string(req.Native.Body), `"model":"gpt-x"`) {
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

	h, _, _ = endpointHandler(t, func(string, llm.Request) []llm.Event {
		evs := bodyReply("application/json", `{}`)
		evs[0].Response.Format = llm.FormatOpenAIResponses
		return evs
	})
	w = postEndpoint(h, "/v1/responses/compact", `{"model":"m"}`, nil)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "openai.responses reply") {
		t.Fatalf("a reply in another format: got %d %s", w.Code, w.Body)
	}
}

// A quiet SSE endpoint reply gets comments only between whole events; a JSON endpoint reply gets
// none at all, however long it pauses between body pieces.
func TestEndpointKeepAlive(t *testing.T) {
	shortKeepAlive(t, 80*time.Millisecond)
	release := func(once *sync.Once, ch chan struct{}) { once.Do(func() { close(ch) }) }

	t.Run("SSE", func(t *testing.T) {
		afterFirst := endpointPause{entered: make(chan struct{}), release: make(chan struct{})}
		insideSecond := endpointPause{entered: make(chan struct{}), release: make(chan struct{})}
		var once1, once2 sync.Once
		t.Cleanup(func() {
			release(&once1, afterFirst.release)
			release(&once2, insideSecond.release)
		})
		h := endpointStreamHandler(t, func(llm.Request) route.Stream {
			return pausedEndpointReply(llm.FormatOpenAIResponsesCompact, "text/event-stream",
				[]string{"data: one\n\n", "data: two\n", "\n"}, map[int]endpointPause{1: afterFirst, 2: insideSecond})
		})
		w := newResponseWriter()
		done := serveEndpointAsync(h, w, "/v1/responses/compact", `{"model":"m"}`)
		waitForBody(t, w, "data: one\n\n", time.Second)
		waitForBody(t, w, keepAliveComment, time.Second)
		_, before, flushed := w.snapshot()
		if !flushed {
			t.Error("the comment was not flushed")
		}
		commentCount := strings.Count(before, keepAliveComment)
		release(&once1, afterFirst.release)
		waitForBody(t, w, "data: two\n", time.Second)
		time.Sleep(2 * keepAliveAfter)
		_, during, _ := w.snapshot()
		if got := strings.Count(during, keepAliveComment); got != commentCount {
			t.Fatalf("a comment went inside an event: %q", during)
		}
		release(&once2, insideSecond.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("the endpoint reply did not finish")
		}
		status, body, _ := w.snapshot()
		if status != http.StatusOK || strings.Count(body, keepAliveComment) != commentCount {
			t.Fatalf("got %d %q; want status 200 with the comments only between events", status, body)
		}
		if !strings.Contains(body, "data: one\n\n"+keepAliveComment+"data: two\n\n") {
			t.Fatalf("the comment is not between whole events: %q", body)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		between := endpointPause{entered: make(chan struct{}), release: make(chan struct{})}
		var once sync.Once
		t.Cleanup(func() { release(&once, between.release) })
		h := endpointStreamHandler(t, func(llm.Request) route.Stream {
			return pausedEndpointReply(llm.FormatOpenAIResponsesCompact, "application/json",
				[]string{`{"one":1,`, `"two":2}`}, map[int]endpointPause{1: between})
		})
		w := newResponseWriter()
		done := serveEndpointAsync(h, w, "/v1/responses/compact", `{"model":"m"}`)
		waitForBody(t, w, `{"one":1,`, time.Second)
		time.Sleep(2 * keepAliveAfter)
		release(&once, between.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("the endpoint reply did not finish")
		}
		status, body, _ := w.snapshot()
		if status != http.StatusOK || body != `{"one":1,"two":2}` || strings.Contains(body, keepAliveComment) {
			t.Fatalf("got %d %q; want the JSON body without a keep-alive", status, body)
		}
	})

	t.Run("Gemini Interactions", func(t *testing.T) {
		between := endpointPause{entered: make(chan struct{}), release: make(chan struct{})}
		var once sync.Once
		t.Cleanup(func() { release(&once, between.release) })
		h := endpointStreamHandler(t, func(llm.Request) route.Stream {
			return pausedEndpointReply(llm.FormatGeminiInteractions, "text/event-stream",
				[]string{"event: interaction.start\ndata: {}\n\n", "event: interaction.complete\ndata: {}\n\n"}, map[int]endpointPause{1: between})
		})
		w := newResponseWriter()
		done := serveEndpointAsync(h, w, "/v1beta/interactions", `{"model":"models/m","stream":true}`)
		waitForBody(t, w, "event: interaction.start", time.Second)
		time.Sleep(2 * keepAliveAfter)
		release(&once, between.release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("the Interactions reply did not finish")
		}
		status, body, _ := w.snapshot()
		if status != http.StatusOK || !strings.Contains(body, "event: interaction.complete") {
			t.Fatalf("got %d %q; want both Interactions events", status, body)
		}
		if strings.Contains(body, keepAliveComment) {
			t.Fatalf("a keep-alive comment on the Gemini Interactions route: %q", body)
		}
	})
}

// A search request goes to an account as an endpoint call on both routes, with the client's body
// and the headers today's core passes on. A request with no model is served by an account that
// lists models; one that names a model goes only to an account that lists it.
func TestEndpointAlphaSearch(t *testing.T) {
	h, accounts, seen := endpointHandlerOver(t, []route.Account{{ID: "a", Models: []route.ModelEntry{{Name: "gpt-x"}}}},
		func(string, llm.Request) []llm.Event {
			evs := bodyReply("application/json", `{"results":[]}`)
			evs[0].Response.Format = llm.FormatCodexAlphaSearch
			return evs
		})
	headers := map[string]string{"Version": "0.154.0", "User-Agent": "codex_cli_rs/0.154.0", "Session_id": "s1", "X-Client-Request-Id": "r1", "Session-Id": "ignored"}
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		for _, body := range []string{`{"query":"q"}`, `{"model":"gpt-x","query":"q"}`} {
			*seen, *accounts = nil, nil
			w := postEndpoint(h, path, body, headers)
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"results":[]}` {
				t.Fatalf("%s %s: got %d %q %s", path, body, w.Code, w.Header().Get("Content-Type"), w.Body)
			}
			req := (*seen)[0]
			if req.Native == nil || req.Native.Format != llm.FormatCodexAlphaSearch || string(req.Native.Body) != body {
				t.Fatalf("%s %s: native = %+v", path, body, req.Native)
			}
			if want := map[string]string{"version": "0.154.0", "user-agent": "codex_cli_rs/0.154.0", "session_id": "s1", "x-client-request-id": "r1"}; !reflect.DeepEqual(req.Native.Headers, want) {
				t.Fatalf("%s %s: headers = %v, want %v", path, body, req.Native.Headers, want)
			}
		}
	}
	// The model is still what routes a request that names one.
	*seen, *accounts = nil, nil
	if w := postEndpoint(h, "/v1/alpha/search", `{"model":"other"}`, nil); w.Code != http.StatusNotFound || len(*accounts) != 0 {
		t.Fatalf("unknown model: got %d %s after %v", w.Code, w.Body, *accounts)
	}
	// Compaction still needs a model that an account serves.
	if w := postEndpoint(h, "/v1/responses/compact", `{"input":"hi"}`, nil); w.Code != http.StatusNotFound || len(*accounts) != 0 {
		t.Fatalf("compact without a model: got %d %s after %v", w.Code, w.Body, *accounts)
	}
}

// An account that cannot count Responses input tokens answers unsupported, and core replaces the
// endpoint's usual no-auth reply with its input estimate in OpenAI's token-count shape. The
// client's body and Responses headers still travel with the endpoint call.
func TestEndpointInputTokensEstimatesWhenNoAccountCanCount(t *testing.T) {
	const body = `{"model":"gpt-test","instructions":"Be brief.","input":[{"role":"user","content":[{"type":"input_text","text":"Say hello"}]}],"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	h, accounts, seen := endpointHandlerOver(t, []route.Account{{ID: "chat", Models: []route.ModelEntry{{Name: "gpt-test"}}}},
		func(string, llm.Request) []llm.Event {
			return []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Message: "no count endpoint"}}}
		})
	w := postEndpoint(h, "/v1/responses/input_tokens", body, map[string]string{"User-Agent": "codex_cli_rs/0.154.0"})
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"input_tokens":27,"object":"response.input_tokens"}` {
		t.Fatalf("got %d %q %s, want 200 with the core estimate", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	if len(*accounts) != 1 || (*accounts)[0] != "chat" {
		t.Fatalf("attempts went to %v, want chat", *accounts)
	}
	req := (*seen)[0]
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIResponsesInputTokens || string(req.Native.Body) != body || req.Native.Headers["user-agent"] != "codex_cli_rs/0.154.0" {
		t.Fatalf("endpoint request = %+v, want the client's Responses body and header", req.Native)
	}
}

// A non-retryable request-scoped 404 on /v1/responses/input_tokens (a vendor without that path)
// answers with core's estimate when token_count_estimate is on and keeps the 404 when it is off.
// Side endpoints without an estimate (such as /v1/embeddings) still answer 404.
func TestEndpointInputTokensMissingEndpointEstimates(t *testing.T) {
	const body = `{"model":"gpt-test","instructions":"Be brief.","input":[{"role":"user","content":[{"type":"input_text","text":"Say hello"}]}],"tools":[{"type":"function","name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`
	noPath := func(string, llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventError, Error: &llm.Error{
			Code: llm.CodeNotFound, Status: http.StatusNotFound, Scope: llm.ScopeRequest,
			Message: "this account's upstream has no input token count endpoint (POST /responses/input_tokens answered 404)",
		}}}
	}
	h, _, _ := endpointHandlerOver(t, []route.Account{{ID: "resp", Models: []route.ModelEntry{{Name: "gpt-test"}}}}, noPath)
	w := postEndpoint(h, "/v1/responses/input_tokens", body, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != `{"input_tokens":27,"object":"response.input_tokens"}` {
		t.Fatalf("input_tokens (estimate on): got %d %q %s, want 200 with the core estimate", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	// A non-count side endpoint still returns 404.
	w = postEndpoint(h, "/v1/embeddings", `{"model":"gpt-test","input":"hi"}`, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("embeddings: got %d %s, want 404", w.Code, w.Body)
	}
	// With token_count_estimate off, /v1/responses/input_tokens keeps the 404.
	off := false
	router, err := route.New(route.Config{}, []route.Account{{ID: "resp", Models: []route.ModelEntry{{Name: "gpt-test"}}}},
		func(context.Context, string, protocol.Account, llm.Request) route.Stream {
			t.Error("an endpoint request went to generate")
			return &events{evs: []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInternal}}}}
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ep := func(_ context.Context, _ string, a protocol.Account, req llm.Request) route.Stream {
		return &events{evs: noPath(a.ID, req)}
	}
	hOff := NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}, TokenCountEstimate: &off}, Router: router, Endpoint: ep}})
	w = postEndpoint(hOff, "/v1/responses/input_tokens", body, nil)
	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "no input token count endpoint") {
		t.Fatalf("input_tokens (estimate off): got %d %s, want 404", w.Code, w.Body)
	}
}
