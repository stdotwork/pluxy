package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
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
