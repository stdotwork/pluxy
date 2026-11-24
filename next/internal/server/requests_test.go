package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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
}

func newRequestsCore(t *testing.T) *requestsCore {
	t.Helper()
	c := &requestsCore{q: NewRequests()}
	store, err := usage.Open(usage.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	c.usage = store
	attempt := func(ctx context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
		return (*c.reply.Load())(ctx, req)
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "work", Extension: "test-ext"}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClientKeys: []string{"k"}}
	if err := cfg.CORS.prepare(); err != nil {
		t.Fatal(err)
	}
	g := &Generation{Config: cfg, Router: router, Attempt: attempt, Count: attempt, Endpoint: attempt, Usage: store, Requests: c.q,
		KeyID: func(key string) string { return "id-of-" + key }}
	c.Server = httptest.NewServer(NewHandler(fixedSource{g}))
	t.Cleanup(c.Close)
	return c
}

// holding makes every attempt reply pre, wait at the hold it returns, then reply post.
func (c *requestsCore) holding(pre, post []llm.Event) *hold {
	h := newHold()
	f := func(ctx context.Context, _ llm.Request) route.Stream { return &held{ctx: ctx, h: h, pre: pre, post: post} }
	c.reply.Store(&f)
	return h
}

func (c *requestsCore) post(ctx context.Context, path, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
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
