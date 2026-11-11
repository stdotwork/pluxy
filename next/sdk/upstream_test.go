package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

type recordUpstream struct {
	request *http.Request
	body    string
}

func (r *recordUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	r.request = req
	body, err := io.ReadAll(req.Body)
	r.body = string(body)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("reply")), Request: req}, err
}

func TestUpstreamUnmodified(t *testing.T) {
	base := &http.Transport{ForceAttemptHTTP2: true}
	recorder := &recordUpstream{}
	base.RegisterProtocol("record", recorder)
	t.Cleanup(base.CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "PATCH", "record://upstream.test/v1/turn?mode=raw", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{"Authorization": {"Bearer caller-value"}, "X-Custom": {"one", "two"}}
	header := req.Header.Clone()
	resp, err := Upstream(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if recorder.request.Context() != ctx || recorder.request.Method != "PATCH" || recorder.request.URL.String() != "record://upstream.test/v1/turn?mode=raw" || !reflect.DeepEqual(recorder.request.Header, header) || recorder.body != `{"input":"hello"}` {
		t.Fatalf("upstream received a changed request: %+v, body %q", recorder.request, recorder.body)
	}
	if resp.Request != recorder.request || resp.StatusCode != 200 {
		t.Fatalf("changed response: %+v", resp)
	}
}

// Dispatch through the same socket host used by the other SDK tests. No handler may
// lose core's cancellation or inherit the preceding session turn's metadata.
func TestUpstreamInfoHandlers(t *testing.T) {
	if _, ok := UpstreamInfo(context.Background()); ok {
		t.Fatal("unrelated context has upstream info")
	}
	type observation struct {
		info Info
		ok   bool
		ctx  context.Context
	}
	seen := make(chan observation, 1)
	observe := func(ctx context.Context) {
		info, ok := UpstreamInfo(ctx)
		seen <- observation{info, ok, ctx}
		<-ctx.Done()
	}
	stream := func(ctx context.Context, _ Call, _ *Stream) error { observe(ctx); return ctx.Err() }
	ext := Extension{ID: "test/sdk", Version: "0.0.0", Generate: stream, CountTokens: stream, Endpoint: stream,
		LoginMethods: []protocol.LoginMethod{{Method: "device", Kind: protocol.LoginDevice}},
		Login: func(ctx context.Context, _ LoginCall, _ *LoginSession) (*protocol.Grant, error) {
			observe(ctx)
			return nil, ctx.Err()
		},
		Refresh: func(ctx context.Context, _ RefreshCall) (*protocol.Credential, error) {
			observe(ctx)
			return nil, ctx.Err()
		},
		Models: func(ctx context.Context, _ ModelsCall) (*protocol.ModelList, error) {
			observe(ctx)
			return nil, ctx.Err()
		},
		Check: func(ctx context.Context, _ CheckCall) (*protocol.CheckResult, error) {
			observe(ctx)
			return nil, ctx.Err()
		},
		Quota:   func(ctx context.Context, _ QuotaCall) (*protocol.Quota, error) { observe(ctx); return nil, ctx.Err() },
		Session: func(ctx context.Context, _ SessionCall) (Session, error) { observe(ctx); return nil, ctx.Err() },
	}
	socket, _, _ := startServe(t, ext)
	account := protocol.Account{ID: "account-a", Settings: json.RawMessage(`{"base_url":"https://upstream.test"}`), Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: t.Name()}}
	request := &llm.Request{Model: "model-a", Native: &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(`{"input":"hi"}`)}}
	check := func(t *testing.T, want Info) context.Context {
		t.Helper()
		select {
		case got := <-seen:
			if !got.ok || !reflect.DeepEqual(got.info, want) {
				t.Fatalf("UpstreamInfo = %+v, %v; want %+v", got.info, got.ok, want)
			}
			encoded, err := json.Marshal(got.info)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), account.Credential.APIKey) {
				t.Fatal("metadata contains the credential")
			}
			// Info must not retain the credential via an embedded account.
			typ := reflect.TypeOf(got.info)
			for i := 0; i < typ.NumField(); i++ {
				if typ.Field(i).Type == reflect.TypeOf(account) || typ.Field(i).Type == reflect.TypeOf(account.Credential) {
					t.Fatal("metadata can retain a credential")
				}
			}
			return got.ctx
		case <-time.After(5 * time.Second):
			t.Fatal("handler was not called")
			return nil
		}
	}
	waitCancelled := func(t *testing.T, ctx context.Context) {
		t.Helper()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("core disconnect did not cancel handler")
		}
	}
	for _, op := range []string{protocol.OpGenerate, protocol.OpCountTokens, protocol.OpModels, protocol.OpCheck, protocol.OpQuota, protocol.OpEndpoint, protocol.OpLogin, protocol.OpRefresh, protocol.OpSession} {
		t.Run(op, func(t *testing.T) {
			conn := dial(t, socket)
			defer conn.Close()
			call := protocol.Call{Type: protocol.TypeCall, ID: "c", Op: op, Account: &account}
			want := Info{AccountID: account.ID, Settings: account.Settings, Operation: op}
			if op == protocol.OpGenerate || op == protocol.OpCountTokens || op == protocol.OpEndpoint {
				call.Request = request
				want.Model, want.Format = "model-a", llm.FormatOpenAIResponses
			}
			if op == protocol.OpLogin {
				call.Login = &protocol.Login{Method: "device"}
			}
			if op == protocol.OpSession {
				call.Session = &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}
			}
			if err := protocol.WriteFrame(conn, call); err != nil {
				t.Fatal(err)
			}
			ctx := check(t, want)
			conn.Close()
			waitCancelled(t, ctx)
		})
	}
	// Use a second host for turns, whose opening returns immediately.
	ext.Session = func(ctx context.Context, _ SessionCall) (Session, error) {
		info, ok := UpstreamInfo(ctx)
		seen <- observation{info, ok, ctx}
		return testSession{turn: func(ctx context.Context, _ Call, out *Stream) error {
			info, ok := UpstreamInfo(ctx)
			seen <- observation{info, ok, ctx}
			if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
				return err
			}
			return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
		}}, nil
	}
	socket, _, _ = startServe(t, ext)
	conn := dial(t, socket)
	defer conn.Close()
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession, Account: &account, Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}}); err != nil {
		t.Fatal(err)
	}
	opening := check(t, Info{AccountID: account.ID, Settings: account.Settings, Operation: protocol.OpSession})
	var reply protocol.Reply
	if err := protocol.ReadFrame(conn, &reply); err != nil || reply.Type != protocol.TypeSessionReady {
		t.Fatalf("session ready: %+v, %v", reply, err)
	}
	for _, turn := range []struct{ id, model, format string }{{"turn-a", "model-a", llm.FormatOpenAIResponses}, {"turn-b", "model-b", ""}} {
		account.ID = turn.id
		r := &llm.Request{Model: turn.model}
		if turn.format != "" {
			r.Native = &llm.Native{Format: turn.format, Body: json.RawMessage("{}")}
		}
		if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &account, Request: r}); err != nil {
			t.Fatal(err)
		}
		ctx := check(t, Info{AccountID: turn.id, Settings: account.Settings, Model: turn.model, Format: turn.format, Operation: protocol.TypeSessionTurn})
		for {
			if err := protocol.ReadFrame(conn, &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Event.Terminal() {
				if reply.Type != llm.EventFinish {
					t.Fatalf("turn: %+v", reply)
				}
				break
			}
		}
		if ctx.Err() != nil {
			t.Fatal("turn context cancelled before disconnect")
		}
	}
	conn.Close()
	waitCancelled(t, opening)
}

// A call's headers replace every value the extension set for the same name, whatever its letter
// case in the header map, keep its other headers, and leave the extension's request as it was.
func TestUpstreamSetsCallHeaders(t *testing.T) {
	base := &http.Transport{}
	recorder := &recordUpstream{}
	base.RegisterProtocol("record", recorder)
	call := protocol.Call{Op: protocol.OpGenerate, Account: &protocol.Account{ID: "a",
		Headers: map[string]string{"anthropic-version": "configured", "X-Gateway-Key": "fake-gw"}}}
	ctx, _ := withUpstreamInfo(context.Background(), call)
	req, err := http.NewRequestWithContext(ctx, "POST", "record://upstream.test/v1/messages", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = http.Header{"anthropic-version": {"own"}, "Anthropic-Version": {"own-too"}, "X-Own": {"kept"}}
	header := req.Header.Clone()
	resp, err := Upstream(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := http.Header{"Anthropic-Version": {"configured"}, "X-Gateway-Key": {"fake-gw"}, "X-Own": {"kept"}}
	if got := recorder.request.Header; !reflect.DeepEqual(got, want) {
		t.Errorf("upstream got headers %v, want %v", got, want)
	}
	if !reflect.DeepEqual(req.Header, header) {
		t.Errorf("the extension's request changed: %v", req.Header)
	}
}

// An extension that has not wrapped any client in Upstream cannot send through a proxy, with an
// account's headers or with a call's payload rules, so a call that carries any of them is
// answered with an error and never reaches its handler or the upstream; a call without a proxy
// still does, and once the process wraps a client the proxied call goes through too.
func TestProxiedCallRefusedWithoutUpstream(t *testing.T) {
	was := upstreamUsed.Swap(false)
	t.Cleanup(func() { upstreamUsed.Store(was) })
	var hits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer provider.Close()
	var ran atomic.Int32
	send := func(ctx context.Context) error { // what an extension does with a client it never wrapped
		ran.Add(1)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, provider.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	finish := func(out *Stream) error {
		if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
			return err
		}
		return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
	}
	ext := Extension{ID: "test/sdk", Version: "0.0.0",
		Generate: func(ctx context.Context, _ Call, out *Stream) error {
			if err := send(ctx); err != nil {
				return err
			}
			return finish(out)
		},
		Check: func(ctx context.Context, _ CheckCall) (*protocol.CheckResult, error) {
			return nil, send(ctx)
		},
		Session: func(context.Context, SessionCall) (Session, error) {
			return testSession{turn: func(ctx context.Context, _ Call, out *Stream) error {
				if err := send(ctx); err != nil {
					return err
				}
				return finish(out)
			}}, nil
		},
	}
	socket, _, _ := startServe(t, ext)
	const proxy = "http://alice:Fake-Proxy-Secret-1@127.0.0.1:1"
	request := &llm.Request{Model: "m"}
	// reply returns the first terminal frame the call gets.
	reply := func(t *testing.T, conn net.Conn) protocol.Reply {
		t.Helper()
		for {
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatal(err)
			}
			if r.Event.Terminal() {
				return r
			}
		}
	}
	refused := func(t *testing.T, r protocol.Reply) {
		t.Helper()
		if r.Type != llm.EventError || r.Error == nil || r.Error.Code != llm.CodeUnsupported || r.Error.Scope != llm.ScopeCredential || !r.Error.Retryable {
			t.Fatalf("reply %+v, want a retryable unsupported error for the account", r)
		}
		if strings.Contains(r.Error.Message, "Fake-Proxy-Secret") || strings.Contains(r.Error.Message, "alice") {
			t.Fatalf("the error shows the proxy: %q", r.Error.Message)
		}
	}
	generate := func(t *testing.T, proxy string) protocol.Reply {
		conn := dial(t, socket)
		defer conn.Close()
		call := protocol.Call{Type: protocol.TypeCall, ID: "g", Op: protocol.OpGenerate, Account: &protocol.Account{ID: "a", ProxyURL: proxy}, Request: request}
		if err := protocol.WriteFrame(conn, call); err != nil {
			t.Fatal(err)
		}
		return reply(t, conn)
	}

	refused(t, generate(t, proxy))
	// An account's headers take the same refusal, and the error holds none of their values.
	const headerSecret = "Fake-Header-Secret-1"
	conn := dial(t, socket)
	hcall := protocol.Call{Type: protocol.TypeCall, ID: "h", Op: protocol.OpGenerate, Request: request,
		Account: &protocol.Account{ID: "a", Headers: map[string]string{"X-Gateway-Key": headerSecret}}}
	if err := protocol.WriteFrame(conn, hcall); err != nil {
		t.Fatal(err)
	}
	hr := reply(t, conn)
	refused(t, hr)
	if strings.Contains(hr.Error.Message, headerSecret) {
		t.Fatalf("the error shows a header value: %q", hr.Error.Message)
	}
	conn.Close()
	// So do a call's payload rules, which only a wrapped client applies.
	conn = dial(t, socket)
	pcall := protocol.Call{Type: protocol.TypeCall, ID: "p", Op: protocol.OpGenerate, Request: request, Account: &protocol.Account{ID: "a"},
		Payload: &payload.Rules{Override: []payload.Rule{{Models: []payload.Model{{}}, Params: map[string]any{"service_tier": "priority"}}}}}
	if err := protocol.WriteFrame(conn, pcall); err != nil {
		t.Fatal(err)
	}
	refused(t, reply(t, conn))
	conn.Close()
	conn = dial(t, socket)
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "c", Op: protocol.OpCheck, Account: &protocol.Account{ID: "a", ProxyURL: proxy}}); err != nil {
		t.Fatal(err)
	}
	refused(t, reply(t, conn))
	conn.Close()
	if ran.Load() != 0 || hits.Load() != 0 {
		t.Fatalf("a refused call ran %d handlers and made %d upstream requests", ran.Load(), hits.Load())
	}

	// A session opened without a proxy refuses a turn that has one; a turn without one runs.
	conn = dial(t, socket)
	defer conn.Close()
	account := &protocol.Account{ID: "a"}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession, Account: account, Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}}); err != nil {
		t.Fatal(err)
	}
	var ready protocol.Reply
	if err := protocol.ReadFrame(conn, &ready); err != nil || ready.Type != protocol.TypeSessionReady {
		t.Fatalf("session open: %+v, %v", ready, err)
	}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: &protocol.Account{ID: "a", ProxyURL: proxy}, Request: request}); err != nil {
		t.Fatal(err)
	}
	refused(t, reply(t, conn))
	if ran.Load() != 0 || hits.Load() != 0 {
		t.Fatalf("a refused turn ran %d handlers and made %d upstream requests", ran.Load(), hits.Load())
	}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Account: account, Request: request}); err != nil {
		t.Fatal(err)
	}
	if r := reply(t, conn); r.Type != llm.EventFinish || ran.Load() != 1 || hits.Load() != 1 {
		t.Fatalf("a turn without a proxy: %+v, %d handlers, %d upstream requests", r, ran.Load(), hits.Load())
	}

	// A direct call reaches the handler; so does a proxied one once a client is wrapped.
	if r := generate(t, ""); r.Type != llm.EventFinish || ran.Load() != 2 {
		t.Fatalf("a direct call: %+v, %d handlers", r, ran.Load())
	}
	_ = Upstream(&http.Transport{})
	if r := generate(t, proxy); r.Type != llm.EventFinish || ran.Load() != 3 {
		t.Fatalf("a proxied call after Upstream: %+v, %d handlers", r, ran.Load())
	}
}

// A session turn refused for its proxy does not leave that proxy for the next turn of the session:
// the next turn, with no account, runs with no account and no proxy, both in its call and in the
// UpstreamInfo of its context.
func TestRefusedTurnLeavesNoProxyForNextTurn(t *testing.T) {
	was := upstreamUsed.Swap(false)
	t.Cleanup(func() { upstreamUsed.Store(was) })
	type turn struct {
		call Call
		info Info
		ok   bool
	}
	var ran atomic.Int32
	seen := make(chan turn, 2)
	ext := Extension{ID: "test/sdk", Version: "0.0.0",
		Session: func(context.Context, SessionCall) (Session, error) {
			return testSession{turn: func(ctx context.Context, call Call, out *Stream) error {
				ran.Add(1)
				info, ok := UpstreamInfo(ctx)
				seen <- turn{call: call, info: info, ok: ok}
				if err := out.Start(llm.ResponseInfo{ID: "r"}); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}}, nil
		},
	}
	socket, _, _ := startServe(t, ext)
	conn := dial(t, socket)
	defer conn.Close()
	// The session opens without a proxy and with no sdk.Upstream client in the process.
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeCall, ID: "s", Op: protocol.OpSession,
		Account: &protocol.Account{ID: "a"}, Session: &protocol.SessionInfo{Format: protocol.SessionOpenAIResponses}}); err != nil {
		t.Fatal(err)
	}
	var ready protocol.Reply
	if err := protocol.ReadFrame(conn, &ready); err != nil || ready.Type != protocol.TypeSessionReady {
		t.Fatalf("session open: %+v, %v", ready, err)
	}
	// terminal returns the first terminal frame the session sends.
	terminal := func(t *testing.T) protocol.Reply {
		t.Helper()
		for {
			var r protocol.Reply
			if err := protocol.ReadFrame(conn, &r); err != nil {
				t.Fatal(err)
			}
			if r.Event.Terminal() {
				return r
			}
		}
	}
	request := &llm.Request{Model: "m"}
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn,
		Account: &protocol.Account{ID: "a", ProxyURL: "http://alice:Fake-Proxy-Secret-2@127.0.0.1:1"}, Request: request}); err != nil {
		t.Fatal(err)
	}
	if r := terminal(t); r.Type != llm.EventError || r.Error == nil || r.Error.Code != llm.CodeUnsupported {
		t.Fatalf("the turn with a proxy: %+v, want a refusal", r)
	}
	if ran.Load() != 0 {
		t.Fatalf("a refused turn ran its handler %d times", ran.Load())
	}
	// The next turn carries no account at all.
	if err := protocol.WriteFrame(conn, protocol.Call{Type: protocol.TypeSessionTurn, Request: request}); err != nil {
		t.Fatal(err)
	}
	if r := terminal(t); r.Type != llm.EventFinish || ran.Load() != 1 {
		t.Fatalf("the turn without an account: %+v, %d handlers", r, ran.Load())
	}
	got := <-seen
	if got.call.Account.ProxyURL != "" || got.call.Account.ID != "" {
		t.Fatalf("the turn's call carries the account of the refused turn (account %q)", got.call.Account.ID)
	}
	if !got.ok || got.info.ProxyURL != "" || got.info.AccountID != "" {
		t.Fatalf("UpstreamInfo of the turn: present %v, account %q", got.ok, got.info.AccountID)
	}
}

// Every transport made for a proxy closes its own idle connections after a finite time, so one the
// pool drops while a request still uses it does not keep them open for good.
func TestProxyTransportClosesIdleConnections(t *testing.T) {
	for _, c := range []struct {
		name       string
		base, want time.Duration
	}{
		{"base without a timeout", 0, 90 * time.Second},
		{"base with a timeout", 20 * time.Second, 20 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := &http.Transport{IdleConnTimeout: c.base}
			rt, err := (&proxyPool{}).get(base, "http://127.0.0.1:3128")
			if err != nil {
				t.Fatal(err)
			}
			if rt.IdleConnTimeout != c.want {
				t.Errorf("IdleConnTimeout = %v, want %v", rt.IdleConnTimeout, c.want)
			}
			if base.IdleConnTimeout != c.base {
				t.Errorf("the base transport changed: IdleConnTimeout %v", base.IdleConnTimeout)
			}
		})
	}
}

// A redirect that keeps the host but changes the scheme (https to http) is away from the first
// request too: the account's secret headers would go out unencrypted.
func TestRedirectedAwayOnSchemeChange(t *testing.T) {
	first, _ := http.NewRequest(http.MethodGet, "https://api.example.test/start", nil)
	for _, c := range []struct {
		to   string
		away bool
	}{
		{"https://api.example.test/next", false},
		{"https://API.example.test/next", false},
		{"http://api.example.test/next", true},
		{"https://api.example.test:8443/next", true},
		{"https://other.example.test/next", true},
	} {
		next, _ := http.NewRequest(http.MethodGet, c.to, nil)
		next.Response = &http.Response{Request: first}
		if got := redirectedAway(next); got != c.away {
			t.Errorf("redirect to %s: away %v, want %v", c.to, got, c.away)
		}
	}
}

// A request that has no Header map (a RoundTripper may be given one) gets the call's headers
// instead of panicking.
func TestUpstreamHeadersOnRequestWithoutHeaderMap(t *testing.T) {
	base := &http.Transport{}
	recorder := &recordUpstream{}
	base.RegisterProtocol("record", recorder)
	call := protocol.Call{Op: protocol.OpGenerate, Account: &protocol.Account{ID: "a", Headers: map[string]string{"X-Title": "fake-title"}}}
	u, _ := url.Parse("record://upstream.test/v1/messages")
	ctx, _ := withUpstreamInfo(context.Background(), call)
	req := (&http.Request{Method: "POST", URL: u, Body: http.NoBody}).WithContext(ctx)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RoundTrip panicked: %v", r)
		}
	}()
	resp, err := Upstream(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := recorder.request.Header.Get("X-Title"); got != "fake-title" {
		t.Errorf("upstream got X-Title %q, want fake-title", got)
	}
}

// A redirect to another host never carries the account's secret headers (net/http leaves
// Authorization and Cookie out there, but the call's context still holds the configured ones), nor
// does a later hop of the same chain, even when the extension set one of those names itself (net/http
// copies other headers to the redirect); a header that is not secret does, and a redirect on the same
// host carries all of them.
func TestUpstreamRedirectKeepsSecretHeadersOnTheirHost(t *testing.T) {
	type seen struct{ secret, key, title string }
	var first, same, other, otherHop atomic.Pointer[seen]
	record := func(dst *atomic.Pointer[seen], r *http.Request) {
		dst.Store(&seen{r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("X-Title")})
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			record(&other, r)
			http.Redirect(w, r, "/final", http.StatusFound) // a second hop on the other host
			return
		}
		record(&otherHop, r)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			record(&first, r)
			http.Redirect(w, r, target.URL+"/hop", http.StatusFound)
		case "/along":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/landed":
			record(&same, r)
		}
	}))
	defer origin.Close()
	call := protocol.Call{Op: protocol.OpGenerate, Account: &protocol.Account{ID: "a", Headers: map[string]string{
		"Authorization": "Bearer fake-token-1", "X-Api-Key": "fake-key-1", "X-Title": "fake-title"}}}
	client := &http.Client{Transport: Upstream(&http.Transport{})}
	get := func(path string) {
		t.Helper()
		ctx, _ := withUpstreamInfo(context.Background(), call)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header["x-api-key"] = []string{"fake-extension-key"} // the extension's own, not canonical
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	all := seen{"Bearer fake-token-1", "fake-key-1", "fake-title"}
	get("/away")
	if got := first.Load(); got == nil || *got != all {
		t.Errorf("the first request got %+v, want all the account's headers", got)
	}
	for name, got := range map[string]*seen{"redirect to another host": other.Load(), "its second hop": otherHop.Load()} {
		if got == nil || *got != (seen{title: "fake-title"}) {
			t.Errorf("%s got %+v, want only the header that is not secret", name, got)
		}
	}
	get("/along")
	if got := same.Load(); got == nil || *got != all {
		t.Errorf("redirect on the same host got %+v, want all the account's headers", got)
	}
}

// A call's payload rules change each JSON request body once, inside the root the extension
// names, and leave bodies that are not JSON or are encoded as they are. The second account's
// retry and net/http's own resend get the same bytes (GetBody).
func TestUpstreamAppliesPayload(t *testing.T) {
	rules := &payload.Rules{
		Override: []payload.Rule{{Models: []payload.Model{{}}, Params: map[string]any{"service_tier": "priority"}}},
		Filter:   []payload.FilterRule{{Models: []payload.Model{{}}, Params: []string{`tools.#(name=="apply_patch")`}}},
	}
	const body = `{"model":"m","tools":[{"name":"apply_patch","n":1},{"name":"apply_patch","n":2}]}`
	// The old engine removes only the first match (testdata case "example filter codex flat
	// tools, two patches: first only").
	const want = `{"model":"m","service_tier":"priority","tools":[{"name":"apply_patch","n":2}]}`
	ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{Op: protocol.OpGenerate, Payload: rules})
	send := func(ctx context.Context, contentType, encoding, body string) (*recordUpstream, *http.Request) {
		t.Helper()
		base := &http.Transport{}
		recorder := &recordUpstream{}
		base.RegisterProtocol("record", recorder)
		req, err := http.NewRequestWithContext(ctx, "POST", "record://upstream.test/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		if encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
		}
		resp, err := Upstream(base).RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return recorder, req
	}
	sameJSON := func(got, want string) bool {
		var g, w any
		return json.Unmarshal([]byte(got), &g) == nil && json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
	}

	rec, _ := send(ctx, "application/json; charset=utf-8", "", body)
	if !sameJSON(rec.body, want) {
		t.Errorf("upstream got %s, want %s", rec.body, want)
	}
	if n := rec.request.ContentLength; n != int64(len(rec.body)) {
		t.Errorf("ContentLength %d for a body of %d bytes", n, len(rec.body))
	}
	again, err := rec.request.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(again); string(b) != rec.body {
		t.Errorf("GetBody gave %s, the request sent %s", b, rec.body)
	}

	wrapped := `{"project":"p","request":` + body + `}`
	rec, _ = send(WithPayloadRoot(ctx, "request"), "application/json", "", wrapped)
	if w := `{"project":"p","request":` + want + `}`; !sameJSON(rec.body, w) {
		t.Errorf("with root request: upstream got %s, want %s", rec.body, w)
	}

	for _, c := range []struct{ contentType, encoding string }{{"application/x-www-form-urlencoded", ""}, {"application/json", "gzip"}, {"", ""}} {
		if rec, _ = send(ctx, c.contentType, c.encoding, body); rec.body != body {
			t.Errorf("Content-Type %q, Content-Encoding %q: upstream got %s, want it unchanged", c.contentType, c.encoding, rec.body)
		}
	}
	if rec, _ = send(context.Background(), "application/json", "", body); rec.body != body {
		t.Errorf("without the call's context: upstream got %s, want it unchanged", rec.body)
	}

	// Capture comes after the rules: the request log shows the body as it went upstream.
	captured, capture := withUpstreamInfo(context.Background(), protocol.Call{Op: protocol.OpGenerate, Payload: rules, Capture: true})
	rec, _ = send(captured, "application/json", "", body)
	d := capture.take()
	if d == nil || len(d.Upstream) != 1 || d.Upstream[0].Request.Body != rec.body || !sameJSON(rec.body, want) {
		t.Errorf("captured %+v, upstream got %s", d, rec.body)
	}
}

// ApplyPayload gives a message that is not an HTTP request (a session turn's upstream message) the
// rules of its turn: once, inside the root the extension names, and as it is when the turn has no
// rules or the message is not a JSON object or array. Expected values are the ones of
// TestUpstreamAppliesPayload (the old engine's oracle).
func TestApplyPayload(t *testing.T) {
	rules := &payload.Rules{
		Override: []payload.Rule{{Models: []payload.Model{{}}, Params: map[string]any{"service_tier": "priority"}}},
		Filter:   []payload.FilterRule{{Models: []payload.Model{{}}, Params: []string{`tools.#(name=="apply_patch")`}}},
	}
	const body = `{"model":"m","tools":[{"name":"apply_patch","n":1},{"name":"apply_patch","n":2}]}`
	const want = `{"model":"m","service_tier":"priority","tools":[{"name":"apply_patch","n":2}]}`
	turn, _ := withUpstreamInfo(context.Background(), protocol.Call{Type: protocol.TypeSessionTurn, Payload: rules})
	// The engine adds a new member at the end of its object, so compare the JSON, not the text.
	sameJSON := func(got []byte, want string) bool {
		var g, w any
		return json.Unmarshal(got, &g) == nil && json.Unmarshal([]byte(want), &w) == nil && reflect.DeepEqual(g, w)
	}
	if got := ApplyPayload(turn, []byte(body)); !sameJSON(got, want) {
		t.Errorf("ApplyPayload = %s, want %s", got, want)
	}
	wrapped := `{"project":"p","request":` + body + `}`
	if got, w := ApplyPayload(WithPayloadRoot(turn, "request"), []byte(wrapped)), `{"project":"p","request":`+want+`}`; !sameJSON(got, w) {
		t.Errorf("with root request: ApplyPayload = %s, want %s", got, w)
	}
	for name, ctx := range map[string]context.Context{
		"a turn without rules": func() context.Context {
			c, _ := withUpstreamInfo(context.Background(), protocol.Call{Type: protocol.TypeSessionTurn})
			return c
		}(),
		"a context without the call": context.Background(),
	} {
		if got := ApplyPayload(ctx, []byte(body)); string(got) != body {
			t.Errorf("%s: ApplyPayload = %s, want the message as it is", name, got)
		}
	}
	if got := ApplyPayload(turn, []byte(`"not an object"`)); string(got) != `"not an object"` {
		t.Errorf("a message that is not an object or array: ApplyPayload = %s, want it as it is", got)
	}
}
