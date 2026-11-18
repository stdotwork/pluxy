//go:build unix

package host

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// pieceKey is a made-up API key; an upstream that refuses it echoes its first 16 characters.
const pieceKey = "sk-hp-Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr9"

var pieceEcho = pieceKey[:16] + "..."

// refusePieces returns the error an upstream that refuses key would make: its message and raw
// body echo the key's first 16 characters.
func refusePieces(key string) error {
	msg := "invalid x-api-key: " + key[:16] + "..."
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"type": "authentication_error", "message": msg}})
	return &llm.Error{Code: llm.CodeAuth, Scope: llm.ScopeCredential, Message: msg, Raw: raw}
}

// echoPieces is an extension that refuses every call with refusePieces of the account's key, or
// for a login, of the key entered. A generate call or a session's turn first sends one request to
// the account's "base_url" setting, through sdk.Upstream, so that the call captures the reply; a
// generate call on an account whose settings have "finish" finishes instead. A session opens on
// the account "open-session" only, and its turns are refused.
func echoPieces() {
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-pieces",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			if err := askUpstream(ctx, call.Account); err != nil {
				return err
			}
			var st struct {
				Finish bool `json:"finish"`
			}
			_ = json.Unmarshal(call.Account.Settings, &st)
			if st.Finish {
				if err := out.Start(llm.ResponseInfo{ID: "r", Model: "m"}); err != nil {
					return err
				}
				return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
			}
			return refusePieces(call.Account.Credential.APIKey)
		},
		CountTokens: func(_ context.Context, call sdk.Call, _ *sdk.Stream) error {
			return refusePieces(call.Account.Credential.APIKey)
		},
		Endpoint: func(_ context.Context, call sdk.Call, _ *sdk.Stream) error {
			return refusePieces(call.Account.Credential.APIKey)
		},
		LoginMethods: []protocol.LoginMethod{{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "API key",
			Inputs: []protocol.LoginInput{{Name: "api_key", Label: "API key", Secret: true}, {Name: "region", Label: "Region"}}}},
		Login: func(_ context.Context, call sdk.LoginCall, _ *sdk.LoginSession) (*protocol.Grant, error) {
			e := refusePieces(call.Input["api_key"]).(*llm.Error)
			e.Message += " for " + call.Input["region"][3:] // the redactor takes the region whole
			return nil, e
		},
		Check: func(_ context.Context, call sdk.CheckCall) (*protocol.CheckResult, error) {
			return nil, refusePieces(call.Account.Credential.APIKey)
		},
		Quota: func(_ context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
			return nil, refusePieces(call.Account.Credential.APIKey)
		},
		Models: func(_ context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
			return nil, refusePieces(call.Account.Credential.APIKey)
		},
		Refresh: func(_ context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
			return nil, refusePieces(call.Account.Credential.APIKey)
		},
		Session: func(_ context.Context, call sdk.SessionCall) (sdk.Session, error) {
			if call.Account.ID != "open-session" {
				return nil, refusePieces(call.Account.Credential.APIKey)
			}
			return refusingSession{}, nil
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

// askUpstream sends one request to account a's "base_url" setting, if it has one, through
// sdk.Upstream, and reads the reply.
func askUpstream(ctx context.Context, a protocol.Account) error {
	var st struct {
		BaseURL string `json:"base_url"`
	}
	_ = json.Unmarshal(a.Settings, &st)
	if st.BaseURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "POST", st.BaseURL, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Transport: sdk.Upstream(&http.Transport{})}).Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

type refusingSession struct{}

func (refusingSession) Turn(ctx context.Context, call sdk.Call, _ *sdk.Stream) error {
	if err := askUpstream(ctx, call.Account); err != nil {
		return err
	}
	return refusePieces(call.Account.Credential.APIKey)
}
func (refusingSession) Interrupt(json.RawMessage) {}
func (refusingSession) Close()                    {}

// wantNoPiece fails t when e is not the refusal with the echoed piece of pieceKey replaced, in
// its message and its raw body.
func wantNoPiece(t *testing.T, what string, e *llm.Error) {
	t.Helper()
	if e == nil {
		t.Fatalf("%s: no error", what)
	}
	const msg = "invalid x-api-key: [redacted]"
	var raw struct {
		Error struct{ Type, Message string }
	}
	_ = json.Unmarshal(e.Raw, &raw)
	if e.Message != msg || raw.Error.Message != msg || raw.Error.Type != "authentication_error" {
		t.Errorf("%s: error %q, raw %s; want the message %q in both", what, e.Message, e.Raw, msg)
	}
}

// Every call on an account whose upstream refuses its key, echoing a piece of it, returns the
// error without the piece: generate, count_tokens, endpoint, check, quota, models, refresh,
// opening a session, a session's turn, and a login, whose error loses the piece of the key the
// user entered but keeps the region, an input the login does not mark secret.
func TestErrorPiecesRemovedFromCalls(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "echo-pieces"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	ctx := context.Background()
	account := func(id string) protocol.Account {
		return protocol.Account{ID: id, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: pieceKey}}
	}
	stream := func(s *Stream) *llm.Error {
		defer s.Close()
		return s.Next().Error
	}
	for _, c := range []struct {
		name string
		call func() *llm.Error
	}{
		{"generate", func() *llm.Error { return stream(ext.Generate(ctx, account("a"), llm.Request{Model: "m"})) }},
		{"count_tokens", func() *llm.Error { return stream(ext.CountTokens(ctx, account("a"), llm.Request{Model: "m"})) }},
		{"endpoint", func() *llm.Error { return stream(ext.Endpoint(ctx, account("a"), splitRequest("m"))) }},
		{"check", func() *llm.Error { _, e := ext.Check(ctx, account("a")); return e }},
		{"quota", func() *llm.Error { _, e := ext.Quota(ctx, account("a")); return e }},
		{"models", func() *llm.Error { _, e := ext.Models(ctx, account("a")); return e }},
		{"refresh", func() *llm.Error { _, e := ext.Refresh(ctx, account("a")); return e }},
		{"session", func() *llm.Error { _, e := ext.OpenSession(ctx, account("a"), responsesSession); return e }},
		{"session turn", func() *llm.Error {
			sess, e := ext.OpenSession(ctx, account("open-session"), responsesSession)
			if e != nil {
				t.Fatalf("OpenSession: %+v", e)
			}
			defer sess.Close()
			turn := sess.Turn(ctx, account("open-session"), llm.Request{Model: "m"})
			defer turn.Close()
			return turn.Next().Error
		}},
	} {
		t.Run(c.name, func(t *testing.T) { wantNoPiece(t, c.name, c.call()) })
	}
	t.Run("login", func(t *testing.T) {
		l := ext.Login(ctx, protocol.Account{ID: "new"}, protocol.Login{Method: "api_key",
			Input: map[string]string{"api_key": pieceKey, "region": "eu-west-Zone7"}})
		defer l.Close()
		r := l.Next()
		if r.Error == nil || r.Error.Message != "invalid x-api-key: [redacted] for west-Zone7" {
			t.Errorf("login reply %+v, want the refusal without the key's piece, with the region", r.Error)
		}
	})
}

// core takes an account's settings secrets for pieces from its own rules (Options.SettingsSecrets):
// a URL setting counts by its user name, password and secret query values, such as a sig, a
// token_url is no secret, so an error keeps "https", "oauth" and "token", and neither is
// turnkey_mode, which protocol.SecretName would take for one for holding "key".
func TestAccountPiecesUseCoreSettingsRules(t *testing.T) {
	e := &Extension{opts: Options{SettingsSecrets: configdoc.SettingsSecrets}}
	settings := []byte(`{"token_url":"https://auth.example.com/oauth/token","base_url":"https://gw.example.com/v1?sig=Sg5Tb2Nq8Vc1Xz&model=m","turnkey_mode":"standard-ordering"}`)
	p := e.accountPieces(protocol.Account{Settings: settings})
	in := "POST https://auth.example.com/oauth/token failed: token expired, sig Sg5Tb2Nq given, standard ordering"
	if got, want := protocol.RemovePieces(in, p...), "POST https://auth.example.com/oauth/token failed: token expired, sig [redacted] given, standard ordering"; got != want {
		t.Errorf("RemovePieces = %q, want %q (pieces %q)", got, want, p)
	}
}

// A call's capture loses the pieces of the account's key from the body of an upstream reply with
// an error status, and from a 200 reply's body when the call or session turn ended in an error (a
// stream that ends in an error event); a call that succeeded keeps its reply's body as it came,
// words that share 4 characters with the key included.
func TestErrorPiecesRemovedFromCapture(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  int
		session bool // the call is a session's turn
		finish  bool // the call succeeds
		body    string
		want    string
	}{
		{"refused", http.StatusUnauthorized, false, false, `{"error":"invalid x-api-key: ` + pieceEcho + `"}`, `{"error":"invalid x-api-key: [redacted]"}`},
		{"failed after a 200", http.StatusOK, false, false, `data: {"error":"invalid x-api-key: ` + pieceEcho + `"}`, `data: {"error":"invalid x-api-key: [redacted]"}`},
		{"session turn failed after a 200", http.StatusOK, true, false, `data: {"error":"invalid x-api-key: ` + pieceEcho + `"}`, `data: {"error":"invalid x-api-key: [redacted]"}`},
		{"succeeded", http.StatusOK, false, true, `{"text":"Ab3Cd5 and Kl3Mn5Op are words"}`, `{"text":"Ab3Cd5 and Kl3Mn5Op are words"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer up.Close()
			ext, err := Start(filepath.Join(fakePackage(t, "echo-pieces"), ManifestFile), Options{Log: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			var mu sync.Mutex
			var got []protocol.Diagnostic
			ctx := WithCapture(context.Background(), func(_ string, d protocol.Diagnostic) {
				mu.Lock()
				defer mu.Unlock()
				got = append(got, d)
			})
			settings, _ := json.Marshal(map[string]any{"base_url": up.URL, "finish": c.finish})
			a := protocol.Account{ID: "a", ProxyURL: "none", Settings: settings,
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: pieceKey}}
			if c.session {
				a.ID = "open-session"
				sess, e := ext.OpenSession(context.Background(), a, responsesSession)
				if e != nil {
					t.Fatalf("OpenSession: %+v", e)
				}
				defer sess.Close()
				turn := sess.Turn(ctx, a, llm.Request{Model: "m"})
				for ev := turn.Next(); !ev.Terminal(); ev = turn.Next() {
				}
				turn.Close()
			} else {
				s := ext.Generate(ctx, a, llm.Request{Model: "m"})
				for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
				}
				s.Close()
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 1 || len(got[0].Upstream) != 1 || got[0].Upstream[0].Response == nil {
				t.Fatalf("captured %+v, want one exchange with its response", got)
			}
			b := got[0].Upstream[0].Response.CapturedBody
			if b.Body != c.want || b.BodySize != int64(len(c.want)) {
				t.Errorf("captured body %q (size %d), want %q (size %d)", b.Body, b.BodySize, c.want, len(c.want))
			}
		})
	}
}

// An error event loses the pieces of the secrets in its message and in each string of its raw
// data, escaped or not, and keeps everything else; an event that is not an error, and an error
// that holds no piece, are kept as they are.
func TestPiecesEvent(t *testing.T) {
	p := pieces{"Ab3Cd5Ef7Gh9Ij1K"}
	raw := json.RawMessage(`{"error": {"message": "bad key sk-Ab3Cd5Ef...", "hint": "key \u0041b3C\u0064 given", "n": 401}, "type": "error"}`)
	ev := p.event(llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "bad key sk-Ab3Cd5Ef... (cut)", ProviderCode: "invalid_api_key", Raw: raw}})
	want := &llm.Error{Code: llm.CodeAuth, Message: "bad key [redacted] (cut)", ProviderCode: "invalid_api_key",
		Raw: json.RawMessage(`{"error": {"message": "bad key [redacted]", "hint": "key [redacted] given", "n": 401}, "type": "error"}`)}
	if ev.Error.Message != want.Message || string(ev.Error.Raw) != string(want.Raw) || ev.Error.ProviderCode != want.ProviderCode {
		t.Errorf("event error %+v raw %s\nwant %+v raw %s", ev.Error, ev.Error.Raw, want, want.Raw)
	}
	keep := []llm.Event{
		{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "bad key Ab3 given, try again", Raw: json.RawMessage(`{"m":"Ab3 Cd5 Ef7"}`)}},
		// An object key is a name, not upstream text.
		{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "bad", Raw: json.RawMessage(`{"Ab3Cd5Ef" : "Ab3", "list": ["Ab3"]}`)}},
		{Type: llm.EventTextDelta, Delta: "Ab3Cd5Ef7Gh9 is a word here"},
	}
	for _, in := range keep {
		out := p.event(in)
		if out.Delta != in.Delta || in.Error != nil && (out.Error.Message != in.Error.Message || string(out.Error.Raw) != string(in.Error.Raw)) {
			t.Errorf("event %+v changed to %+v", in, out)
		}
	}
	if out := (pieces{}).event(llm.Event{Type: llm.EventError, Error: &llm.Error{Message: "Ab3Cd5Ef7Gh9"}}); out.Error.Message != "Ab3Cd5Ef7Gh9" {
		t.Errorf("no secrets changed the message to %q", out.Error.Message)
	}
}

// Raw data that is not valid JSON is cleaned as text and comes back as a JSON string.
func TestPiecesRawNotJSON(t *testing.T) {
	got := pieces{"Ab3Cd5Ef7Gh9Ij1K"}.raw(json.RawMessage(`bad key Ab3Cd5Ef`))
	if string(got) != `"bad key [redacted]"` {
		t.Errorf("raw = %s", got)
	}
}

// A capture loses the pieces from an error reply's body, start and end, and its size counts
// what is left; a successful reply's body and the request are kept, unless the call failed.
func TestPiecesDiagnostic(t *testing.T) {
	p := pieces{"Ab3Cd5Ef7Gh9Ij1K"}
	body := protocol.CapturedBody{Body: "bad key Ab3Cd5Ef", BodyEnd: "Ij1K end", BodySize: 100}
	d := protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{
		{Request: protocol.UpstreamRequest{CapturedBody: body}, Response: &protocol.UpstreamResponse{Status: 401, CapturedBody: body}},
		{Request: protocol.UpstreamRequest{CapturedBody: body}, Response: &protocol.UpstreamResponse{Status: 200, CapturedBody: body}},
		{Request: protocol.UpstreamRequest{CapturedBody: body}},
	}}
	cleaned := protocol.CapturedBody{Body: "bad key [redacted]", BodyEnd: "[redacted] end", BodySize: 100 + 2 + 6}
	out := p.diagnostic(d, false)
	if got := out.Upstream[0].Response.CapturedBody; got != cleaned {
		t.Errorf("error reply body %+v, want %+v", got, cleaned)
	}
	// A call that failed loses the pieces from every reply's body, its request's kept.
	failed := p.diagnostic(d, true)
	if failed.Upstream[0].Response.CapturedBody != cleaned || failed.Upstream[1].Response.CapturedBody != cleaned ||
		failed.Upstream[1].Request.CapturedBody != body {
		t.Errorf("failed call's capture: %+v", failed.Upstream)
	}
	for i, x := range out.Upstream {
		if x.Request.CapturedBody != body || i > 0 && x.Response != nil && x.Response.CapturedBody != body {
			t.Errorf("exchange %d changed: %+v", i, x)
		}
	}
	if d.Upstream[0].Response.CapturedBody != body {
		t.Error("the diagnostic passed in changed")
	}
}

// add keeps each secret once, in the order first seen.
func TestPiecesAdd(t *testing.T) {
	got := pieces{"a1", "b2"}.add(pieces{"b2", "c3"})
	if !slices.Equal(got, pieces{"a1", "b2", "c3"}) {
		t.Errorf("add = %q", got)
	}
}

// A call that sends many captures before it ends holds two, the first and the others joined into
// one, and the request log's count of exchanges stays right.
func TestHeldCaptureKeepsFirstAndLast(t *testing.T) {
	var h heldCapture
	for i := range 5 {
		h.hold(protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{Request: protocol.UpstreamRequest{URL: fmt.Sprint("u", i)}}}})
	}
	ds, failed := h.take()
	if !failed || len(ds) != 2 {
		t.Fatalf("take = %d captures, failed %v; want 2, true", len(ds), failed)
	}
	urls := func(d protocol.Diagnostic) []string {
		var out []string
		for _, x := range d.Upstream {
			out = append(out, x.Request.URL)
		}
		return out
	}
	if got := urls(ds[0]); !slices.Equal(got, []string{"u0"}) {
		t.Errorf("first capture %q, want [u0]", got)
	}
	if got := urls(ds[1]); !slices.Equal(got, []string{"u3", "u4"}) || ds[1].UpstreamLeftOut != 2 {
		t.Errorf("last capture %q, %d left out; want [u3 u4], 2", got, ds[1].UpstreamLeftOut)
	}
	if again, _ := h.take(); again != nil {
		t.Errorf("a second take returned %d captures", len(again))
	}
}

// pipeStream returns a Stream that reads the frames written to the returned end, capturing into
// got, for a call on an account whose secrets are p.
func pipeStream(t *testing.T, p pieces, got *[]protocol.Diagnostic, mu *sync.Mutex) (*Stream, net.Conn) {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	s := &Stream{conn: ours, ext: &Extension{manifest: &Manifest{ID: "hosttest/pipe"}}, redact: protocol.NewRedactor(), pieces: p, account: "a",
		capture: func(_ string, d protocol.Diagnostic) { mu.Lock(); defer mu.Unlock(); *got = append(*got, d) }}
	return s, theirs
}

// An extension that sends several diagnostic frames and then an error has at most two captures
// logged, each 200 reply in them without the pieces of the account's secrets.
func TestManyCapturesCleanedOnError(t *testing.T) {
	var mu sync.Mutex
	var got []protocol.Diagnostic
	s, ext := pipeStream(t, pieces{pieceKey}, &got, &mu)
	go func() {
		for i := range 4 {
			d := protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{
				Request:  protocol.UpstreamRequest{URL: fmt.Sprint("https://up.example/", i)},
				Response: &protocol.UpstreamResponse{Status: 200, CapturedBody: protocol.CapturedBody{Body: "echo " + pieceEcho, BodySize: int64(len("echo " + pieceEcho))}},
			}}}
			_ = protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: protocol.TypeDiagnostic}, Diagnostic: &d})
		}
		_ = protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "refused"}}})
	}()
	if ev := s.Next(); ev.Type != llm.EventError {
		t.Fatalf("event %+v, want the error", ev)
	}
	s.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 || len(got) > 2 {
		t.Fatalf("captured %d diagnostics, want 1 or 2", len(got))
	}
	exchanges := 0
	for _, d := range got {
		exchanges += len(d.Upstream) + d.UpstreamLeftOut
		for _, x := range d.Upstream {
			if b := x.Response.CapturedBody.Body; b != "echo [redacted]" {
				t.Errorf("captured body %q, want the piece removed", b)
			}
		}
	}
	if exchanges != 4 {
		t.Errorf("the captures count %d exchanges, want 4", exchanges)
	}
}

// Close on another goroutine just after Next has read the call's terminal event reads nothing
// Next writes unguarded (go test -race).
func TestStreamCloseWhileNextEnds(t *testing.T) {
	var mu sync.Mutex
	var got []protocol.Diagnostic
	s, ext := pipeStream(t, pieces{pieceKey}, &got, &mu)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Next()
	}()
	d := protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{Request: protocol.UpstreamRequest{URL: "https://up.example/"}}}}
	_ = protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: protocol.TypeDiagnostic}, Diagnostic: &d})
	_ = protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "refused"}}})
	s.Close() // as a client that goes away would, while Next ends the call
	<-done
}

// As TestStreamCloseWhileNextEnds, for a session's turn: Close reads whether the turn ended while
// Next ends it, and the turn's capture is taken once.
func TestSessionTurnCloseWhileNextEnds(t *testing.T) {
	ours, theirs := net.Pipe()
	defer theirs.Close()
	var mu sync.Mutex
	var got []protocol.Diagnostic
	sess := &Session{ext: &Extension{manifest: &Manifest{ID: "hosttest/pipe"}}, conn: ours, running: true,
		frames: make(chan sessionFrame), done: make(chan struct{}), closing: make(chan struct{})}
	turn := &SessionStream{s: sess, redact: protocol.NewRedactor(), pieces: pieces{pieceKey}, account: "a",
		capture: func(_ string, d protocol.Diagnostic) { mu.Lock(); defer mu.Unlock(); got = append(got, d) }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		turn.Next()
	}()
	d := protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{{Request: protocol.UpstreamRequest{URL: "https://up.example/"}}}}
	sess.frames <- sessionFrame{reply: protocol.Reply{Event: llm.Event{Type: protocol.TypeDiagnostic}, Diagnostic: &d}}
	sess.frames <- sessionFrame{reply: protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeAuth, Message: "refused"}}}}
	turn.Close()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Errorf("captured %d diagnostics, want 1", len(got))
	}
}
