package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestLoginPromptIsRedacted(t *testing.T) {
	const secret = "user-entered-secret-1234"
	core, ext := net.Pipe()
	defer core.Close()
	defer ext.Close()
	l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core}
	l.addSecrets(map[string]string{"api_key": secret})
	go protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &protocol.Prompt{
		Kind: protocol.PromptBrowser, Message: "key " + secret, URL: "https://login.invalid/?key=" + secret,
		UserCode: secret, VerificationURI: "https://v.invalid/" + secret, VerificationURIComplete: "https://v.invalid/?c=" + secret,
		Inputs: []protocol.LoginInput{{Name: secret, Label: "label " + secret}},
	}})
	r := l.Next()
	if r.Prompt == nil {
		t.Fatalf("got %+v, want a prompt", r)
	}
	b := jsonOf(r.Prompt)
	if strings.Contains(b, secret) {
		t.Errorf("prompt still holds the input: %s", b)
	}
}

func TestSendToAnEndedLoginIsAClosedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peer, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	ext := &Extension{manifest: &Manifest{ID: "test/x"}}
	l := &LoginCall{ext: ext, ctx: context.Background(), conn: conn}
	if err := l.Send(map[string]string{"x": "y"}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("send on a closed connection: %v, want a closed connection", err)
	}
	if err := (&LoginCall{ext: ext, ctx: context.Background()}).Send(map[string]string{"x": "y"}); !errors.Is(err, net.ErrClosed) {
		t.Errorf("send on a login without a connection: %v, want a closed connection", err)
	}
}

func jsonOf(v any) string {
	var sb strings.Builder
	_ = protocol.WriteFrame(&sb, v)
	return sb.String()
}

func TestLoginGrantSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw  string
		ok, absent bool
	}{
		{"object", "{}", true, false},
		{"null", "null", true, true},
		{"array", "[]", false, false},
		{"string", "\"value\"", false, false},
		{"number", "1", false, false},
		{"too large", "{\"value\":\"" + strings.Repeat("x", 8192) + "\"}", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, ext := net.Pipe()
			defer core.Close()
			defer ext.Close()
			l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core}
			go func() {
				raw := map[string]any{"type": "login.done", "grant": map[string]any{"credential": map[string]string{"kind": "api_key", "api_key": "made-up-key"}, "settings": json.RawMessage(tc.raw)}}
				protocol.WriteFrame(ext, raw)
			}()
			r := l.Next()
			if !tc.ok {
				if r.Error == nil || r.Error.Code != llm.CodeExtensionProtocol {
					t.Fatalf("invalid grant settings accepted: %s", jsonOf(r))
				}
			} else {
				if r.Grant == nil {
					t.Fatalf("valid grant refused: %s", jsonOf(r))
				}
				if tc.absent && len(r.Grant.Settings) != 0 {
					t.Fatalf("null settings not absent: %s", r.Grant.Settings)
				}
			}
		})
	}
}

func loginError(t *testing.T, input map[string]string, secret []string, message string) string {
	t.Helper()
	core, ext := net.Pipe()
	defer core.Close()
	defer ext.Close()
	l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core, secretInputs: map[string]bool{}}
	for _, name := range secret {
		l.secretInputs[name] = true
	}
	l.addSecrets(input)
	go protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeUnavailable, Message: message}}})
	r := l.Next()
	if r.Error == nil {
		t.Fatalf("got %+v, want an error", r)
	}
	return r.Error.Message
}

func TestLoginErrorShowsTheTypedAddress(t *testing.T) {
	const key = "mock-wrong-key-mock-wrong-key-mock-wrong-key"
	secret := []string{"api_key"}
	t.Run("address", func(t *testing.T) {
		in := map[string]string{"base_url": "http://127.0.0.1:41234/v1", "api_key": key}
		got := loginError(t, in, secret, `could not reach the model list: Get "http://127.0.0.1:41234/v1/models": connection refused`)
		if want := `could not reach the model list: Get "http://127.0.0.1:41234/v1/models": connection refused`; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("user info stays out", func(t *testing.T) {
		in := map[string]string{"base_url": "https://sam:hunter2@gateway.invalid/v1", "api_key": key}
		for _, text := range []string{
			`Get "https://sam:hunter2@gateway.invalid/v1/models": refused`,
			`Get "https://sam:***@gateway.invalid/v1/models": hunter2 refused`,
			`user sam:hunter2 refused`,
		} {
			got := loginError(t, in, secret, text)
			for _, s := range []string{"hunter2", "sam:hunter2"} {
				if strings.Contains(got, s) {
					t.Errorf("%q shows %q: %q", text, s, got)
				}
			}
		}
		if got := loginError(t, in, secret, `Get "https://sam:hunter2@gateway.invalid/v1/models"`); !strings.Contains(got, "gateway.invalid/v1/models") {
			t.Errorf("the rest of the address is hidden: %q", got)
		}
	})
	t.Run("a query keeps the whole address out", func(t *testing.T) {
		in := map[string]string{"base_url": "https://gateway.invalid/v1?api_key=s3cr3tvalue&x=1"}
		got := loginError(t, in, nil, `Get "https://gateway.invalid/v1?api_key=s3cr3tvalue&x=1": refused`)
		if strings.Contains(got, "s3cr3tvalue") || strings.Contains(got, "gateway.invalid") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a fragment keeps the whole address out", func(t *testing.T) {
		in := map[string]string{"redirect_url": "http://localhost:1455/auth/callback#access_token=abcd1234"}
		if got := loginError(t, in, nil, "bad http://localhost:1455/auth/callback#access_token=abcd1234"); strings.Contains(got, "abcd1234") || strings.Contains(got, "localhost") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a value that is not an address stays out", func(t *testing.T) {
		for _, v := range []string{"abcdefgh-code", "ftp://host/path", "host.invalid/v1", "http://host.invalid/a b"} {
			in := map[string]string{"code": v}
			if got := loginError(t, in, nil, "refused "+v+" now"); strings.Contains(got, v) {
				t.Errorf("input %q: got %q", v, got)
			}
		}
	})
	t.Run("a secret input that is a URL stays out", func(t *testing.T) {
		in := map[string]string{"api_key": "https://gateway.invalid/v1"}
		if got := loginError(t, in, secret, "refused https://gateway.invalid/v1 now"); strings.Contains(got, "gateway.invalid") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("the key stays out", func(t *testing.T) {
		in := map[string]string{"base_url": "http://127.0.0.1:41234/v1", "api_key": key}
		if got := loginError(t, in, secret, "bad key "+key+" and "+key[:20]); strings.Contains(got, "mock-wrong") {
			t.Errorf("got %q", got)
		}
	})
}

func TestLoginErrorKeepsAnAddressWithHiddenSecretsWhole(t *testing.T) {
	cases := []struct{ name, input, secret string }{
		{"password after a slash", "http://localhost:41234/Hunter2@gw.invalid/v1", "Hunter2"},
		{"digits before a slash", "https://sam:2024/Hunter2xyz@gateway.invalid/v1", "Hunter2xyz"},
		{"slash in the user name", "https://te/am:Hunter2xyz@gateway.invalid/v1", "Hunter2xyz"},
		{"two addresses", "https://a:b@one.invalid/v1,https://sam:Hunter2xyz@two.invalid/v1", "Hunter2xyz"},
		{"escaped query", "http://localhost:1455/auth/callback%3Fcode%3DAuthCode12345%26state%3Dabc", "AuthCode12345"},
		{"escaped fragment", "http://localhost:1455/auth/callback%23access_token%3DAuthCode12345", "AuthCode12345"},
		{"semicolon parameter", "http://localhost:1455/auth/callback;code=AuthCode12345", "AuthCode12345"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loginError(t, map[string]string{"redirect_url": c.input}, nil, "bad "+c.input+" now")
			if strings.Contains(got, c.secret) {
				t.Errorf("shows %q: %q", c.secret, got)
			}
		})
	}
}

func TestLoginErrorShortUserName(t *testing.T) {
	in := map[string]string{"base_url": "https://a:Pw9xQ2@gateway.invalid/v1"}
	got := loginError(t, in, nil, `could not reach the model list: Get "https://a:Pw9xQ2@gateway.invalid/v1/models": Pw9xQ2 a:Pw9xQ2`)
	if !strings.Contains(got, "could not reach the model list") || strings.Contains(got, "Pw9xQ2") || !strings.Contains(got, "gateway.invalid/v1/models") {
		t.Errorf("got %q", got)
	}
	in = map[string]string{"base_url": "https://username8:Pw9xQ2@gateway.invalid/v1"}
	if got := loginError(t, in, nil, "who username8 asked"); strings.Contains(got, "username8") {
		t.Errorf("an 8-byte user name shows: %q", got)
	}
	in = map[string]string{"base_url": "https://sam:hunter%32xyz@gateway.invalid/v1"}
	if got := loginError(t, in, nil, "Get https://sam:hunter%32xyz@gateway.invalid/v1 and hunter2xyz"); strings.Contains(got, "hunter") {
		t.Errorf("got %q", got)
	}
}

func TestLoginPromptHidesAProxyPasswordThatIsAnAddress(t *testing.T) {
	const secret = "https://vault.invalid/XXsecretXX"
	core, ext := net.Pipe()
	defer core.Close()
	defer ext.Close()
	l := &LoginCall{ext: &Extension{manifest: &Manifest{ID: "test/x"}}, ctx: context.Background(), conn: core, secretInputs: map[string]bool{}}
	l.addForms(map[string]string{"proxy 0": secret})
	go protocol.WriteFrame(ext, protocol.Reply{Event: llm.Event{Type: protocol.TypeLoginPrompt}, Prompt: &protocol.Prompt{
		Kind: protocol.PromptBrowser, Message: "via " + secret,
	}})
	r := l.Next()
	if r.Prompt == nil {
		t.Fatalf("got %+v, want a prompt", r)
	}
	if strings.Contains(r.Prompt.Message, "vault.invalid") {
		t.Errorf("prompt shows the proxy password: %q", r.Prompt.Message)
	}
}

func TestLoginErrorUserNameWithoutPassword(t *testing.T) {
	for _, addr := range []string{"http://a@gw.test/v1", "http://%61@gw.test/v1", "http://a:@gw.test/v1"} {
		got := loginError(t, map[string]string{"base_url": addr}, nil, "could not reach the address "+addr)
		if want := "could not reach the address " + addr; got != want {
			t.Errorf("%s: got %q, want %q", addr, got, want)
		}
	}
	if got := loginError(t, map[string]string{"base_url": "http://username8@gw.test/v1"}, nil, "who username8 asked"); strings.Contains(got, "username8") {
		t.Errorf("an 8-byte user name shows: %q", got)
	}
}
