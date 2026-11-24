package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

// hold is where a test keeps attempts open: each attempt that reaches it tells entered, then
// waits until the test closes release or the attempt's context ends.
type hold struct {
	entered chan struct{}
	release chan struct{}
}

func newHold() *hold { return &hold{entered: make(chan struct{}, 4096), release: make(chan struct{})} }

// held is an attempt's reply: pre, then a wait at its hold, then post. When the context ends
// during the wait the reply ends with the cancelled error, as the extension host's does.
type held struct {
	ctx       context.Context
	h         *hold
	pre, post []llm.Event
	n         int
	// panics makes the reply panic after the wait, as a handler that crashes does.
	panics bool
}

func (s *held) Next() llm.Event {
	if s.n < len(s.pre) {
		s.n++
		return s.pre[s.n-1]
	}
	if s.n == len(s.pre) {
		s.h.entered <- struct{}{}
		select {
		case <-s.ctx.Done():
			s.post = []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}}}
		case <-s.h.release:
			if s.panics {
				panic(http.ErrAbortHandler)
			}
		}
	}
	i := min(s.n-len(s.pre), len(s.post)-1)
	s.n++
	return s.post[i]
}

func (s *held) Close() {}

var (
	replyStart = []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "gpt-test"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "first"},
	}
	replyEnd = []llm.Event{
		{Type: llm.EventTextDelta, Index: 0, Delta: " last"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	replyRefused = []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInvalidRequest, Scope: llm.ScopeRequest, Status: 400, Message: "refused by the test"}}}
)

// requestsCore is a client API served over HTTP with one account, "work", a registry and a usage
// record. Each attempt's reply is what reply returns.
type requestsCore struct {
	*httptest.Server
	q     *Requests
	usage *usage.Store
	reply atomic.Pointer[func(ctx context.Context, req llm.Request) route.Stream]
	// key is the client key, state the routing state, attempts how many attempts began, and g
	// the generation every request runs on.
	key      string
	state    *route.State
	attempts atomic.Int64
	g        *Generation
	client   *http.Client
}

func newRequestsCore(t *testing.T) *requestsCore { return newRequestsCoreWith(t, "k", false, nil) }

// newRequestsCoreWith is newRequestsCore with the client key key, over HTTP/2 when h2, and with
// accounts that take sessions when openSession is not nil.
func newRequestsCoreWith(t *testing.T, key string, h2 bool, openSession OpenSessionFunc) *requestsCore {
	t.Helper()
	c := &requestsCore{q: NewRequests(), key: key, state: route.NewState(), client: http.DefaultClient}
	store, err := usage.Open(usage.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	c.usage = store
	attempt := func(ctx context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
		c.attempts.Add(1)
		return (*c.reply.Load())(ctx, req)
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "work", Extension: "test-ext"}}, attempt, c.state)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClientKeys: []string{key}}
	if err := cfg.CORS.prepare(); err != nil {
		t.Fatal(err)
	}
	c.g = &Generation{Config: cfg, Router: router, Attempt: attempt, Count: attempt, Endpoint: attempt, OpenSession: openSession, Usage: store, Requests: c.q,
		KeyID: func(string) string { return "id-of-k" }}
	c.Server = httptest.NewUnstartedServer(NewHandler(fixedSource{c.g}))
	if h2 {
		c.EnableHTTP2 = true
		c.StartTLS()
		c.client = c.Client()
	} else {
		c.Start()
	}
	t.Cleanup(c.Close)
	return c
}

// holding makes every attempt reply pre, wait at the hold it returns, then reply post.
func (c *requestsCore) holding(pre, post []llm.Event) *hold {
	h := newHold()
	f := func(ctx context.Context, _ llm.Request) route.Stream {
		return &held{ctx: ctx, h: h, pre: pre, post: post}
	}
	c.reply.Store(&f)
	return h
}

func (c *requestsCore) post(ctx context.Context, path, body string) (*http.Response, error) {
	return c.postBody(ctx, path, strings.NewReader(body))
}

func (c *requestsCore) postBody(ctx context.Context, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}

// waitEmpty fails unless the registry is empty within a few seconds: a handler returns a moment
// after its client has the reply.
func (c *requestsCore) waitEmpty(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l := c.q.List()
		if len(l) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the registry still lists %d request(s) after their end: %+v", len(l), l)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitEntered waits until n attempts are at h.
func waitEntered(t *testing.T, h *hold, n int) {
	t.Helper()
	for range n {
		select {
		case <-h.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("no attempt reached the hold within 5s")
		}
	}
}

// onlyRequest returns the one request the registry lists.
func (c *requestsCore) onlyRequest(t *testing.T) management.RunningRequest {
	t.Helper()
	l := c.q.List()
	if len(l) != 1 {
		t.Fatalf("the registry lists %d requests, want 1: %+v", len(l), l)
	}
	return l[0]
}

func (c *requestsCore) lastEvent(t *testing.T) usage.Event {
	t.Helper()
	evs := c.usage.Events(usage.Filter{}, 0, 100).Events
	if len(evs) == 0 {
		t.Fatal("the usage record has no event")
	}
	return evs[len(evs)-1]
}

const heldChatBody = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
const heldChatStreamBody = `{"model":"m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`

// Every client route that reaches an account is listed while its attempt is open, with what the
// request asked for and the account it went to, and is gone when it ends.
func TestRequestsListsEveryClientRoute(t *testing.T) {
	type route struct {
		path, body, model, format string
		stream                    bool
	}
	routes := []route{
		{path: "/v1/chat/completions", body: heldChatBody, model: "m1", format: llm.FormatOpenAIChat},
		{path: "/v1/chat/completions", body: heldChatStreamBody, model: "m1", format: llm.FormatOpenAIChat, stream: true},
		{path: "/v1/completions", body: `{"model":"m1","prompt":"hi"}`, model: "m1", format: llm.FormatOpenAIChat},
		{path: "/v1/responses", body: `{"model":"m1","input":"hi"}`, model: "m1", format: llm.FormatOpenAIResponses},
		{path: "/responses", body: `{"model":"m1","input":"hi"}`, model: "m1", format: llm.FormatOpenAIResponses},
		{path: "/backend-api/codex/responses", body: `{"model":"m1","input":"hi","stream":true}`, model: "m1", format: llm.FormatOpenAIResponses, stream: true},
		{path: "/v1/messages", body: `{"model":"m1","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, model: "m1", format: llm.FormatAnthropicMessages},
		{path: "/v1/messages/count_tokens", body: `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`, model: "m1", format: llm.FormatAnthropicMessages},
		{path: "/v1beta/models/m1:generateContent", body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, model: "m1", format: llm.FormatGeminiGenerate},
		{path: "/v1beta/models/m1:streamGenerateContent", body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, model: "m1", format: llm.FormatGeminiGenerate, stream: true},
		{path: "/v1beta/models/m1:countTokens", body: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, model: "m1", format: llm.FormatGeminiGenerate},
		{path: "/v1beta/models/m1:embedContent", body: `{"content":{"parts":[{"text":"hi"}]}}`, model: "m1", format: llm.FormatGeminiEmbedContent},
	}
	// The endpoints beyond generation: each names its own format.
	for path, ep := range endpointRoutes {
		if ep.form {
			continue // a form body; the JSON ones enter and leave the registry the same way
		}
		routes = append(routes, route{path: path, body: `{"model":"m1","input":"hi","prompt":"hi","query":"hi"}`, model: "m1", format: ep.format})
	}
	c := newRequestsCore(t)
	for _, rt := range routes {
		name := rt.path
		if rt.stream {
			name += " streamed"
		}
		t.Run(name, func(t *testing.T) {
			h := c.holding(nil, replyRefused)
			type result struct {
				trace string
				err   error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := c.post(context.Background(), rt.path, rt.body)
				if err != nil {
					done <- result{err: err}
					return
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				done <- result{trace: resp.Header.Get(TraceHeader)}
			}()
			select {
			case <-h.entered:
			case r := <-done:
				t.Fatalf("the request ended before it reached an account (trace %q, error %v)", r.trace, r.err)
			case <-time.After(5 * time.Second):
				t.Fatal("the request reached no account within 5s")
			}
			got := c.onlyRequest(t)
			if got.StartedAt.IsZero() || time.Since(got.StartedAt) > time.Minute {
				t.Errorf("started_at = %v, want about now", got.StartedAt)
			}
			id := got.ID
			got.ID, got.StartedAt = "", time.Time{}
			want := management.RunningRequest{Endpoint: rt.path, Format: rt.format, Stream: rt.stream, Model: rt.model, KeyID: "id-of-k", ClientIP: "127.0.0.1",
				Account: "work", Provider: "test-ext", UpstreamModel: rt.model, Attempts: 1}
			if got != want {
				t.Errorf("listed as\n %+v\nwant\n %+v", got, want)
			}
			close(h.release)
			r := <-done
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.trace != id {
				t.Errorf("listed under %q, and the reply's %s is %q", id, TraceHeader, r.trace)
			}
			c.waitEmpty(t)
		})
	}
}

// The registry is empty after a request ends, whichever way it ends.
func TestRequestsEmptyAfterEveryEnding(t *testing.T) {
	// readChunk reads the stream until it holds want.
	readChunk := func(t *testing.T, r *bufio.Reader, want string) string {
		t.Helper()
		var got strings.Builder
		for !strings.Contains(got.String(), want) {
			line, err := r.ReadString('\n')
			got.WriteString(line)
			if err != nil {
				t.Fatalf("the stream ended before %q: %v\n%s", want, err, got.String())
			}
		}
		return got.String()
	}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, c *requestsCore)
	}{
		{"a reply", func(t *testing.T, c *requestsCore) {
			h := c.holding(replyStart, replyEnd)
			close(h.release)
			resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !strings.Contains(string(b), "first last") {
				t.Fatalf("status %d, body %s", resp.StatusCode, b)
			}
		}},
		{"a streamed reply", func(t *testing.T, c *requestsCore) {
			h := c.holding(replyStart, replyEnd)
			close(h.release)
			resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatStreamBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !strings.Contains(string(b), "[DONE]") {
				t.Fatalf("status %d, body %s", resp.StatusCode, b)
			}
		}},
		{"an upstream error before the reply", func(t *testing.T, c *requestsCore) {
			h := c.holding(nil, replyRefused)
			close(h.release)
			resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("status %d, want 400", resp.StatusCode)
			}
		}},
		{"an upstream error after the reply began", func(t *testing.T, c *requestsCore) {
			h := c.holding(replyStart, replyRefused)
			close(h.release)
			resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatStreamBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !strings.Contains(string(b), "refused by the test") {
				t.Fatalf("status %d, body %s", resp.StatusCode, b)
			}
		}},
		{"a body that does not parse", func(t *testing.T, c *requestsCore) {
			resp, err := c.post(context.Background(), "/v1/chat/completions", `{"model":`)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("status %d, want 400", resp.StatusCode)
			}
		}},
		{"the client going away during the stream", func(t *testing.T, c *requestsCore) {
			h := c.holding(replyStart, replyEnd)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resp, err := c.post(ctx, "/v1/chat/completions", heldChatStreamBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			readChunk(t, bufio.NewReader(resp.Body), "first")
			waitEntered(t, h, 1)
			c.onlyRequest(t)
			cancel()
			c.waitEmpty(t)
			if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || e.Status != 200 {
				t.Errorf("usage has error_code %q status %d, want cancelled and 200", e.ErrorCode, e.Status)
			}
		}},
		{"the client going away before the reply", func(t *testing.T, c *requestsCore) {
			h := c.holding(nil, replyEnd)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failed := make(chan error, 1)
			go func() {
				_, err := c.post(ctx, "/v1/chat/completions", heldChatBody)
				failed <- err
			}()
			waitEntered(t, h, 1)
			c.onlyRequest(t)
			cancel()
			if err := <-failed; err == nil {
				t.Fatal("the request the client gave up got a reply")
			}
			c.waitEmpty(t)
			// The client got nothing, so the event has no status.
			if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || e.Status != 0 {
				t.Errorf("usage has error_code %q status %d, want cancelled and no status", e.ErrorCode, e.Status)
			}
		}},
		{"a cancel during the stream", func(t *testing.T, c *requestsCore) {
			h := c.holding(replyStart, replyEnd)
			resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatStreamBody)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body := bufio.NewReader(resp.Body)
			readChunk(t, body, "first")
			waitEntered(t, h, 1)
			id := c.onlyRequest(t).ID
			if v, ok := c.q.Cancel(id); !ok || v.ID != id {
				t.Fatalf("Cancel(%q) = %+v, %v", id, v, ok)
			}
			// The client is still there: its stream ends with the cancelled error.
			rest, err := io.ReadAll(body)
			if err != nil || !strings.Contains(string(rest), "the request was cancelled") || strings.Contains(string(rest), "last") {
				t.Fatalf("after the cancel the stream held %q (error %v), want the cancelled error and no more of the reply", rest, err)
			}
			c.waitEmpty(t)
			if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || !e.Failed || e.Status != 200 || e.ID != id {
				t.Errorf("usage has %+v, want the request %s failed as cancelled with status 200", e, id)
			}
			if _, ok := c.q.Cancel(id); ok {
				t.Error("a request that ended can still be cancelled")
			}
		}},
		{"a cancel before the reply", func(t *testing.T, c *requestsCore) {
			h := c.holding(nil, replyEnd)
			type result struct {
				status int
				body   string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatBody)
				if err != nil {
					done <- result{err: err}
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				done <- result{status: resp.StatusCode, body: string(b)}
			}()
			waitEntered(t, h, 1)
			id := c.onlyRequest(t).ID
			// Twice: a second cancel of a request still listed changes nothing.
			for range 2 {
				if _, ok := c.q.Cancel(id); !ok {
					// The first cancel may already have ended it.
					break
				}
			}
			r := <-done
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.status < 400 || !strings.Contains(r.body, "the request was cancelled") {
				t.Fatalf("the client got status %d and %q, want an error reply saying the request was cancelled", r.status, r.body)
			}
			c.waitEmpty(t)
			// Unlike a client that went away, this one got a reply: the event has its status.
			if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || e.Status != r.status {
				t.Errorf("usage has error_code %q status %d, want cancelled and the reply's %d", e.ErrorCode, e.Status, r.status)
			}
		}},
		{"a panic", func(t *testing.T, c *requestsCore) {
			h := newHold()
			f := func(ctx context.Context, _ llm.Request) route.Stream {
				return &held{ctx: ctx, h: h, post: replyEnd, panics: true}
			}
			c.reply.Store(&f)
			failed := make(chan error, 1)
			go func() {
				_, err := c.post(context.Background(), "/v1/chat/completions", heldChatBody)
				failed <- err
			}()
			waitEntered(t, h, 1)
			c.onlyRequest(t)
			close(h.release)
			if err := <-failed; err == nil {
				t.Fatal("the request whose handler panicked got a reply")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newRequestsCore(t)
			tc.run(t, c)
			c.waitEmpty(t)
		})
	}
}

// A Responses WebSocket is listed while a turn runs, under the trace ID its handshake gave, and
// not between turns. Cancelling the turn ends it as a turn whose context ends does: the socket
// closes.
func TestRequestsWebSocketTurn(t *testing.T) {
	dial := func(t *testing.T, c *requestsCore) (*websocket.Conn, string) {
		t.Helper()
		conn, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(c.URL, "http")+"/v1/responses",
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer k"}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn, resp.Header.Get(TraceHeader)
	}
	create := func(t *testing.T, conn *websocket.Conn) {
		t.Helper()
		if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"response.create","model":"m1","input":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	// readUntil reads messages until one holds want; it returns the error that ended the socket
	// when none does.
	readUntil := func(conn *websocket.Conn, want string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), want) {
				return nil
			}
		}
	}
	listed := func(t *testing.T, c *requestsCore, trace string) {
		t.Helper()
		got := c.onlyRequest(t)
		got.StartedAt = time.Time{}
		want := management.RunningRequest{ID: trace, Endpoint: "/v1/responses", Format: llm.FormatOpenAIResponses, Stream: true, Model: "m1",
			KeyID: "id-of-k", ClientIP: "127.0.0.1", Account: "work", Provider: "test-ext", UpstreamModel: "m1", Attempts: 1}
		if got != want {
			t.Errorf("listed as\n %+v\nwant\n %+v", got, want)
		}
	}

	t.Run("a turn that completes, then another", func(t *testing.T) {
		c := newRequestsCore(t)
		conn, trace := dial(t, c)
		if l := c.q.List(); len(l) != 0 {
			t.Fatalf("a socket with no turn is listed: %+v", l)
		}
		for range 2 {
			h := c.holding(replyStart, replyEnd)
			create(t, conn)
			waitEntered(t, h, 1)
			listed(t, c, trace)
			close(h.release)
			if err := readUntil(conn, "response.completed"); err != nil {
				t.Fatal(err)
			}
			c.waitEmpty(t)
		}
	})
	t.Run("a cancel", func(t *testing.T) {
		c := newRequestsCore(t)
		conn, trace := dial(t, c)
		h := c.holding(replyStart, replyEnd)
		create(t, conn)
		waitEntered(t, h, 1)
		listed(t, c, trace)
		if _, ok := c.q.Cancel(trace); !ok {
			t.Fatal("the running turn could not be cancelled")
		}
		if err := readUntil(conn, "response.completed"); err == nil {
			t.Fatal("the cancelled turn completed")
		}
		c.waitEmpty(t)
		if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || !e.Failed {
			t.Errorf("usage has %+v, want the turn failed as cancelled", e)
		}
	})
	t.Run("the client going away", func(t *testing.T) {
		c := newRequestsCore(t)
		conn, trace := dial(t, c)
		h := c.holding(replyStart, replyEnd)
		create(t, conn)
		waitEntered(t, h, 1)
		listed(t, c, trace)
		_ = conn.CloseNow()
		c.waitEmpty(t)
	})
	t.Run("a panic", func(t *testing.T) {
		c := newRequestsCore(t)
		conn, _ := dial(t, c)
		h := newHold()
		f := func(ctx context.Context, _ llm.Request) route.Stream {
			return &held{ctx: ctx, h: h, post: replyEnd, panics: true}
		}
		c.reply.Store(&f)
		create(t, conn)
		waitEntered(t, h, 1)
		c.onlyRequest(t)
		close(h.release)
		if err := readUntil(conn, "response.completed"); err == nil {
			t.Fatal("the turn whose handler panicked completed")
		}
		c.waitEmpty(t)
	})
}

// The list is oldest first, and a request nobody is serving cannot be cancelled.
func TestRequestsListOrderAndUnknownTrace(t *testing.T) {
	c := newRequestsCore(t)
	h := c.holding(nil, replyRefused)
	for _, model := range []string{"first", "second", "third"} {
		go func() {
			resp, err := c.post(context.Background(), "/v1/chat/completions", `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
			if err == nil {
				resp.Body.Close()
			}
		}()
		waitEntered(t, h, 1) // each is being served before the next starts
	}
	var models []string
	for _, r := range c.q.List() {
		models = append(models, r.Model)
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(models, want) {
		t.Errorf("listed %v, want %v", models, want)
	}
	if v, ok := c.q.Cancel("20260101000000-0000000000000000"); ok {
		t.Errorf("cancelled a request nobody is serving: %+v", v)
	}
	if n := len(c.q.List()); n != 3 {
		t.Errorf("after cancelling an unknown trace %d requests are listed, want 3", n)
	}
	close(h.release)
	c.waitEmpty(t)
}

// Requests start, end and are cancelled while the list is read: every one leaves the registry,
// and the race detector sees every access.
func TestRequestsConcurrentStartEndCancel(t *testing.T) {
	c := newRequestsCore(t)
	h := c.holding(replyStart, replyEnd)
	const n = 120
	stop := make(chan struct{})
	var readers sync.WaitGroup
	var cancelled atomic.Int64
	readers.Add(2)
	go func() { // lists, and cancels every third request it sees
		defer readers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			for j, r := range c.q.List() {
				if (i+j)%3 == 0 {
					if _, ok := c.q.Cancel(r.ID); ok {
						cancelled.Add(1)
					}
				}
			}
		}
	}()
	go func() { // only lists
		defer readers.Done()
		for {
			select {
			case <-stop:
				return
			default:
				c.q.List()
			}
		}
	}()
	var clients sync.WaitGroup
	for i := range n {
		clients.Add(1)
		go func() {
			defer clients.Done()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := heldChatStreamBody
			if i%2 == 0 {
				body = heldChatBody
			}
			resp, err := c.post(ctx, "/v1/chat/completions", body)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if i%5 == 0 {
				return // the client goes away with the reply unread
			}
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	// Let the requests that were not cancelled finish, a few at a time, while more start.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(h.release)
	}()
	clients.Wait()
	close(stop)
	readers.Wait()
	c.waitEmpty(t)
	if cancelled.Load() == 0 {
		t.Error("no request was cancelled while it ran: the test did not exercise a cancel")
	}
}

// The registry keeps the ID of a request's client key and never the key: no text an entry holds
// contains it, for an HTTP request or a WebSocket turn.
func TestRequestsKeepNoClientKey(t *testing.T) {
	const key = "client-key-the-registry-must-not-keep-7Qm2"
	c := newRequestsCoreWith(t, key, false, nil)
	h := c.holding(nil, replyRefused)
	go func() {
		if resp, err := c.post(context.Background(), "/v1/chat/completions", heldChatBody); err == nil {
			resp.Body.Close()
		}
	}()
	waitEntered(t, h, 1)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(c.URL, "http")+"/v1/responses",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"response.create","model":"m1","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, h, 1)

	c.q.mu.Lock()
	entries := 0
	for _, e := range c.q.open {
		entries++
		v := reflect.ValueOf(e).Elem()
		for i := range v.NumField() {
			if f := v.Field(i); f.Kind() == reflect.String && strings.Contains(f.String(), key) {
				t.Errorf("the entry's field %s holds the client key", v.Type().Field(i).Name)
			}
		}
		if e.keyID != "id-of-k" {
			t.Errorf("the entry's key ID is %q, want the key's ID", e.keyID)
		}
	}
	c.q.mu.Unlock()
	if entries != 2 {
		t.Fatalf("%d entries, want the HTTP request and the WebSocket turn", entries)
	}
	// Every string field is checked above; a field of another kind that could hold text fails here.
	for i := range reflect.TypeFor[running]().NumField() {
		switch f := reflect.TypeFor[running]().Field(i); f.Type.Kind() {
		case reflect.String, reflect.Bool, reflect.Int:
		default:
			switch f.Name {
			case "start", "client", "cancel", "stopped", "mu": // a time, an address, a function, a flag, a lock
			default:
				t.Errorf("the entry's field %s is a %s: say here why it cannot hold the client key", f.Name, f.Type)
			}
		}
	}
	if b, _ := json.Marshal(c.q.List()); strings.Contains(string(b), key) {
		t.Errorf("the list holds the client key: %s", b)
	}
	close(h.release)
	_ = conn.CloseNow()
	c.waitEmpty(t)
}

// A cancel while the request's body is still arriving ends the read: the client gets the
// cancelled error, the request leaves the list at once, no attempt is made, and the usage record
// has one cancelled event with no attempt. Over HTTP/1 and HTTP/2.
func TestRequestsCancelDuringBodyRead(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		name := "HTTP/1"
		if h2 {
			name = "HTTP/2"
		}
		t.Run(name, func(t *testing.T) {
			c := newRequestsCoreWith(t, "k", h2, nil)
			c.holding(replyStart, replyEnd)
			body, send := io.Pipe()
			defer send.Close()
			type result struct {
				status int
				proto  string
				body   string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				resp, err := c.postBody(context.Background(), "/v1/chat/completions", body)
				if err != nil {
					done <- result{err: err}
					return
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				done <- result{status: resp.StatusCode, proto: resp.Proto, body: string(b)}
			}()
			if _, err := send.Write([]byte(`{"model":"m1","messages":[`)); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for len(c.q.List()) != 1 {
				if time.Now().After(deadline) {
					t.Fatal("the request whose body is arriving is not listed")
				}
				time.Sleep(time.Millisecond)
			}
			got := c.onlyRequest(t)
			if got.Model != "" || got.Account != "" || got.KeyID != "id-of-k" {
				t.Errorf("listed as %+v, want no model and no account yet", got)
			}
			cancelledAt := time.Now()
			if _, ok := c.q.Cancel(got.ID); !ok {
				t.Fatal("the request could not be cancelled")
			}
			select {
			case r := <-done:
				if r.err != nil {
					t.Fatal(r.err)
				}
				if wantProto := map[bool]string{false: "HTTP/1.1", true: "HTTP/2.0"}[h2]; r.proto != wantProto {
					t.Errorf("the reply came over %s, want %s", r.proto, wantProto)
				}
				if r.status != http.StatusBadGateway || !strings.Contains(r.body, "the request was cancelled") {
					t.Errorf("the client got status %d and %q, want the cancelled error", r.status, r.body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("3s after the cancel the client has no reply: the body read did not end")
			}
			c.waitEmpty(t)
			if took := time.Since(cancelledAt); took > 2*time.Second {
				t.Errorf("the request left the list %s after the cancel", took)
			}
			if n := c.attempts.Load(); n != 0 {
				t.Errorf("%d attempts were made for a request cancelled before its body arrived", n)
			}
			evs := c.usage.Events(usage.Filter{}, 0, 100).Events
			if len(evs) != 1 || evs[0].ErrorCode != llm.CodeCancelled || !evs[0].Failed || evs[0].Attempts != 0 || evs[0].Account != "" ||
				evs[0].Status != http.StatusBadGateway || evs[0].KeyID != "id-of-k" {
				t.Errorf("usage has %+v, want one cancelled event with no attempt and the reply's status", evs)
			}
		})
	}
}

// cancelOnRead is a request body that cancels the one listed request when it is first read, then
// gives the whole body: the cancel lands after the request is listed and before it is routed.
type cancelOnRead struct {
	q    *Requests
	rest io.Reader
	done bool
}

func (b *cancelOnRead) Read(p []byte) (int, error) {
	if !b.done {
		b.done = true
		for _, r := range b.q.List() {
			b.q.Cancel(r.ID)
		}
	}
	return b.rest.Read(p)
}

func (b *cancelOnRead) Close() error { return nil }

// A request cancelled before routing never starts an attempt, though its whole body was read: the
// generate routes and an endpoint record it as cancelled with its model, a count just answers.
func TestRequestsCancelledBeforeRoutingMakesNoAttempt(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		events     int
	}{
		{"/v1/chat/completions", heldChatBody, 1},
		{"/v1beta/models/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, 1},
		{"/v1/embeddings", `{"model":"m1","input":"hi"}`, 1},
		{"/v1beta/models/m1:embedContent", `{"content":{"parts":[{"text":"hi"}]}}`, 1},
		{"/v1/messages/count_tokens", `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`, 0},
		{"/v1beta/models/m1:countTokens", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, 0},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c := newRequestsCore(t)
			c.holding(replyStart, replyEnd)
			r := httptest.NewRequest(http.MethodPost, tc.path, nil)
			r.Body = &cancelOnRead{q: c.q, rest: strings.NewReader(tc.body)}
			r.Header.Set("Authorization", "Bearer k")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NewHandler(fixedSource{c.g}).ServeHTTP(w, r)
			if w.Code < 400 || !strings.Contains(w.Body.String(), "cancelled") {
				t.Errorf("status %d, body %s; want the cancelled error", w.Code, w.Body)
			}
			if n := c.attempts.Load(); n != 0 {
				t.Errorf("%d attempts were made for a request cancelled before routing", n)
			}
			evs := c.usage.Events(usage.Filter{}, 0, 100).Events
			if len(evs) != tc.events {
				t.Fatalf("usage has %d events, want %d: %+v", len(evs), tc.events, evs)
			}
			for _, e := range evs {
				if e.ErrorCode != llm.CodeCancelled || e.Attempts != 0 || e.Model != "m1" || e.Status != w.Code {
					t.Errorf("usage has %+v, want cancelled, no attempt, model m1 and status %d", e, w.Code)
				}
			}
			c.waitEmpty(t)
		})
	}
}

// testSession is an upstream session whose turns are what reply returns.
type testSession struct {
	reply  func(ctx context.Context) Stream
	turns  atomic.Int64
	closed atomic.Bool
}

func (s *testSession) AccountID() string { return "work" }
func (s *testSession) Ended() bool       { return s.closed.Load() }
func (s *testSession) Interrupt([]byte)  {}
func (s *testSession) Close()            { s.closed.Store(true) }
func (s *testSession) Turn(ctx context.Context, _ protocol.Account, _ llm.Request) Stream {
	s.turns.Add(1)
	return s.reply(ctx)
}

// A WebSocket turn that runs on an upstream session is listed like any other, and a cancel ends
// it: the turn's context ends, the socket closes, the session is closed, the account counts no
// request, and the registry is empty.
func TestRequestsCancelWebSocketSessionTurn(t *testing.T) {
	h := newHold()
	sess := &testSession{}
	var turnCtx atomic.Pointer[context.Context]
	sess.reply = func(ctx context.Context) Stream {
		turnCtx.Store(&ctx)
		return &held{ctx: ctx, h: h, pre: replyStart, post: replyEnd}
	}
	var opened atomic.Int64
	c := newRequestsCoreWith(t, "k", false, func(context.Context, string, protocol.Account, protocol.SessionInfo) (Session, *llm.Error) {
		opened.Add(1)
		return sess, nil
	})
	active := func() int {
		return c.state.Runtime(route.Account{ID: "work", Extension: "test-ext"}, time.Now()).Active
	}
	conn, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(c.URL, "http")+"/v1/responses",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer k"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"response.create","model":"m1","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	waitEntered(t, h, 1)
	if opened.Load() != 1 || sess.turns.Load() != 1 || c.attempts.Load() != 0 {
		t.Fatalf("the turn did not run on the session: %d sessions opened, %d session turns, %d HTTP attempts", opened.Load(), sess.turns.Load(), c.attempts.Load())
	}
	got := c.onlyRequest(t)
	if got.ID != resp.Header.Get(TraceHeader) || got.Account != "work" || got.Model != "m1" || got.Attempts != 1 {
		t.Errorf("listed as %+v, want the socket's trace %s on account work", got, resp.Header.Get(TraceHeader))
	}
	if n := active(); n != 1 {
		t.Errorf("the account counts %d requests during the turn, want 1", n)
	}
	if _, ok := c.q.Cancel(got.ID); !ok {
		t.Fatal("the session turn could not be cancelled")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break // the socket closed, as it does for a turn whose context ended
		}
		if strings.Contains(string(data), "response.completed") {
			t.Fatal("the cancelled session turn completed")
		}
	}
	c.waitEmpty(t)
	if (*turnCtx.Load()).Err() == nil {
		t.Error("the session turn's context is still live after the cancel")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !sess.closed.Load() || active() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("after the cancel: session closed %v, the account counts %d requests; want closed and 0", sess.closed.Load(), active())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if e := c.lastEvent(t); e.ErrorCode != llm.CodeCancelled || !e.Failed || e.Account != "work" {
		t.Errorf("usage has %+v, want the turn failed as cancelled", e)
	}
}
