//go:build unix

package host

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

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

// echoPieces is an extension that refuses every call with refusePieces of the account's key. A
// generate call first sends one request to the account's "base_url" setting, through
// sdk.Upstream, so that the call captures the reply. A session opens on the account
// "open-session" only, and its turns are refused.
func echoPieces() {
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-pieces",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			var st struct {
				BaseURL string `json:"base_url"`
			}
			_ = json.Unmarshal(call.Account.Settings, &st)
			if st.BaseURL != "" {
				req, err := http.NewRequestWithContext(ctx, "POST", st.BaseURL, strings.NewReader("{}"))
				if err != nil {
					return err
				}
				resp, err := (&http.Client{Transport: sdk.Upstream(&http.Transport{})}).Do(req)
				if err != nil {
					return err
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
			return refusePieces(call.Account.Credential.APIKey)
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

type refusingSession struct{}

func (refusingSession) Turn(_ context.Context, call sdk.Call, _ *sdk.Stream) error {
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
// error without the piece: generate, check, quota, models, refresh, opening a session and a
// session's turn.
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
	s := ext.Generate(ctx, account("a"), llm.Request{Model: "m"})
	ev := s.Next()
	s.Close()
	wantNoPiece(t, "generate", ev.Error)
	_, e := ext.Check(ctx, account("a"))
	wantNoPiece(t, "check", e)
	_, e = ext.Quota(ctx, account("a"))
	wantNoPiece(t, "quota", e)
	_, e = ext.Models(ctx, account("a"))
	wantNoPiece(t, "models", e)
	_, e = ext.Refresh(ctx, account("a"))
	wantNoPiece(t, "refresh", e)
	_, e = ext.OpenSession(ctx, account("a"), responsesSession)
	wantNoPiece(t, "session", e)
	sess, e := ext.OpenSession(ctx, account("open-session"), responsesSession)
	if e != nil {
		t.Fatalf("OpenSession: %+v", e)
	}
	defer sess.Close()
	turn := sess.Turn(ctx, account("open-session"), llm.Request{Model: "m"})
	ev = turn.Next()
	turn.Close()
	wantNoPiece(t, "session turn", ev.Error)
}

// A call's capture loses the pieces of the account's key from the body of an upstream reply with
// an error status, and keeps a successful reply's body as it came, words that share 4
// characters with the key included.
func TestErrorPiecesRemovedFromCapture(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"refused", http.StatusUnauthorized, `{"error":"invalid x-api-key: ` + pieceEcho + `"}`, `{"error":"invalid x-api-key: [redacted]"}`},
		{"succeeded", http.StatusOK, `{"text":"Ab3Cd5 and Kl3Mn5Op are words"}`, `{"text":"Ab3Cd5 and Kl3Mn5Op are words"}`},
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
			settings, _ := json.Marshal(map[string]string{"base_url": up.URL})
			s := ext.Generate(ctx, protocol.Account{ID: "a", ProxyURL: "none", Settings: settings,
				Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: pieceKey}}, llm.Request{Model: "m"})
			for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
			}
			s.Close()
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
// what is left; a successful reply's body and the request are kept.
func TestPiecesDiagnostic(t *testing.T) {
	p := pieces{"Ab3Cd5Ef7Gh9Ij1K"}
	body := protocol.CapturedBody{Body: "bad key Ab3Cd5Ef", BodyEnd: "Ij1K end", BodySize: 100}
	d := protocol.Diagnostic{Upstream: []protocol.UpstreamExchange{
		{Request: protocol.UpstreamRequest{CapturedBody: body}, Response: &protocol.UpstreamResponse{Status: 401, CapturedBody: body}},
		{Request: protocol.UpstreamRequest{CapturedBody: body}, Response: &protocol.UpstreamResponse{Status: 200, CapturedBody: body}},
		{Request: protocol.UpstreamRequest{CapturedBody: body}},
	}}
	out := p.diagnostic(d)
	if got, want := out.Upstream[0].Response.CapturedBody, (protocol.CapturedBody{Body: "bad key [redacted]", BodyEnd: "[redacted] end", BodySize: 100 + 2 + 6}); got != want {
		t.Errorf("error reply body %+v, want %+v", got, want)
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
