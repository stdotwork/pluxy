package host

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// echoProxy is an extension that writes the proxy each call carries to stderr and fails the
// call with an error that holds it; with headers, also their values.
func echoProxy() {
	_ = sdk.Upstream(&http.Transport{}) // an extension that sends through a proxy wraps its clients
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-proxy",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			msg := "cannot reach " + call.Account.ProxyURL
			for _, k := range slices.Sorted(maps.Keys(call.Account.Headers)) {
				msg += " " + k + "=" + call.Account.Headers[k]
			}
			fmt.Fprintf(os.Stderr, "proxy is %q\n%s\nproxy done %s\n", call.Account.ProxyURL, msg, call.Account.ID)
			return &llm.Error{Code: llm.CodeUnavailable, Message: msg}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

const testProxyPassword = "proxy-pass-Secret-31"

// A call carries the account's own proxy, else core's global one, and none for direct; the
// proxy's user name and password never come back in the call's error or the extension's log.
func TestCallCarriesProxyWithoutLeakingItsPassword(t *testing.T) {
	global := "socks5://user:" + testProxyPassword + "@127.0.0.1:1080"
	log := &syncBuffer{}
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: log, ProxyURL: func() string { return global }})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	for _, c := range []struct{ id, own, want string }{
		{"inherits", "", "socks5://" + protocol.Redacted + "@127.0.0.1:1080"},
		{"own", "http://127.0.0.1:3128", "http://127.0.0.1:3128"},
		{"direct", "DIRECT", ""},
	} {
		s := ext.Generate(context.Background(), protocol.Account{ID: c.id, ProxyURL: c.own}, llm.Request{Model: "m"})
		ev := s.Next()
		s.Close()
		log.waitFor(t, "proxy done "+c.id)
		if ev.Type != llm.EventError || ev.Error.Message != "cannot reach "+c.want {
			t.Errorf("account %s: event %+v, want the error naming %q", c.id, ev, c.want)
		}
	}
	if out := log.String(); strings.Contains(out, testProxyPassword) || !strings.Contains(out, `proxy is "socks5://`+protocol.Redacted+`@127.0.0.1:1080"`) {
		t.Errorf("the log holds the proxy's password, or not the proxy:\n%s", out)
	}
}

// An extension older than protocol 0.11 cannot use a proxy, so a call that has one is refused
// before it reaches it, as unsupported for the account; a direct account still reaches it.
func TestOldExtensionGetsNoProxiedCall(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: &syncBuffer{},
		ProxyURL: func() string { return "http://127.0.0.1:3128" }})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	s := ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "m"})
	ev := s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable {
		t.Fatalf("event %+v, want a retryable unsupported error for the account", ev)
	}
	// The fake extension hangs up on a generate call: a direct one reaches it.
	s = ext.Generate(context.Background(), protocol.Account{ID: "a", ProxyURL: "none"}, llm.Request{Model: "m"})
	ev = s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("direct call: event %+v, want the extension's hang-up", ev)
	}
}

// echoSecrets fails every call with an error that holds ordinary words around the forms the proxy's
// user name and password take (written, decoded, in the Basic header, alone) and writes the same
// text to stderr.
func echoSecrets() {
	_ = sdk.Upstream(&http.Transport{})
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-secrets",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			u, err := url.Parse(call.Account.ProxyURL)
			if err != nil || u.User == nil {
				return err
			}
			pass, _ := u.User.Password()
			written, _, _ := strings.Cut(strings.SplitN(call.Account.ProxyURL, "://", 2)[1], "@")
			_, writtenPass, _ := strings.Cut(written, ":")
			msg := strings.Join([]string{
				`keep: password bypass reverse proxy {"n": 1}`,
				"written " + written,
				"decoded " + u.User.Username() + ":" + pass,
				"basic " + base64.StdEncoding.EncodeToString([]byte(u.User.Username()+":"+pass)),
				"pass " + pass,
				"written-pass " + writtenPass,
				"user " + u.User.Username(),
			}, " | ")
			fmt.Fprintf(os.Stderr, "echo: %s\n", msg)
			return &llm.Error{Code: llm.CodeUnavailable, Message: msg}
		},
	})
	if err != nil {
		os.Exit(1)
	}
}

// A proxy's user name and password are removed from a call's replies and the extension's log as a
// whole and in every form the extension may write them in, but a short password or user name is
// not removed on its own: it would also scrub "password", "bypass", "reverse proxy" and the number
// 1. A user name with no password is removed on its own only from 8 bytes; its Basic login is
// always removed.
func TestProxySecretRedaction(t *testing.T) {
	const keep = `keep: password bypass reverse proxy {"n": 1}`
	for _, c := range []struct{ name, proxy, want string }{
		{"short password", "http://user:pass@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass pass | written-pass pass | user user"},
		{"one-digit password", "http://u:1@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass 1 | written-pass 1 | user u"},
		{"long password", "http://alice:Long-Proxy-Secret-7@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass [redacted] | written-pass [redacted] | user alice"},
		{"encoded password", "http://alice:p%40ss-Long-Secret@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass [redacted] | written-pass [redacted] | user alice"},
		{"long user name", "http://operator-account:x@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass x | written-pass x | user [redacted]"},
		{"short user name only", "http://alice@127.0.0.1:3128",
			keep + " | written alice | decoded alice: | basic [redacted] | pass  | written-pass  | user alice"},
		{"short user name only, a word of the reply", "http://proxy@127.0.0.1:3128",
			keep + " | written proxy | decoded proxy: | basic [redacted] | pass  | written-pass  | user proxy"},
		{"long user name only", "http://tok_0123456789abcdef@127.0.0.1:3128",
			keep + " | written [redacted] | decoded [redacted] | basic [redacted] | pass  | written-pass  | user [redacted]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := &syncBuffer{}
			ext, err := Start(filepath.Join(fakePackage(t, "echo-secrets"), ManifestFile), Options{Log: log})
			if err != nil {
				t.Fatal(err)
			}
			defer ext.Stop()
			s := ext.Generate(context.Background(), protocol.Account{ID: "a", ProxyURL: c.proxy}, llm.Request{Model: "m"})
			ev := s.Next()
			s.Close()
			if ev.Type != llm.EventError || ev.Error.Message != c.want {
				t.Errorf("reply:\n got %+v\nwant error %q", ev, c.want)
			}
			log.waitFor(t, "echo: "+c.want)
		})
	}
}

// refreshOddReply answers a refresh with a frame whose type holds the proxy's password.
func refreshOddReply() {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		go func() {
			defer conn.Close()
			var call protocol.Call
			if protocol.ReadFrame(conn, &call) != nil {
				return
			}
			switch call.Op {
			case protocol.OpDescribe:
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: protocol.TypeDescribeResult},
					Info: &protocol.Info{ID: os.Getenv(protocol.EnvExtensionID), Version: "0.0.0", Protocol: protocol.Version, ProtocolMinor: protocol.Minor,
						Operations: []string{protocol.OpRefresh}}})
			case protocol.OpRefresh:
				u, _ := url.Parse(call.Account.ProxyURL)
				pass, _ := u.User.Password()
				_ = protocol.WriteFrame(conn, protocol.Reply{Event: llm.Event{Type: "odd-" + pass}})
			}
		}()
	}
}

// The error for a refresh the extension answers with an unexpected frame has the proxy's
// password removed, as the error frame of a refresh has.
func TestRefreshUnexpectedReplyHidesProxyPassword(t *testing.T) {
	const pass = "Refresh-Proxy-Secret-42"
	ext, err := Start(filepath.Join(fakePackage(t, "refresh-odd-reply"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	account := protocol.Account{ID: "a", ProxyURL: "http://alice:" + pass + "@127.0.0.1:3128",
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, RefreshToken: "refresh-token-0123456789"}}
	_, lerr := ext.Refresh(context.Background(), account)
	if lerr == nil || lerr.Code != llm.CodeExtensionProtocol || !strings.Contains(lerr.Message, "odd-"+protocol.Redacted) {
		t.Fatalf("error %+v, want a protocol error naming the reply with the password removed", lerr)
	}
	if strings.Contains(lerr.Message, pass) {
		t.Errorf("the error holds the proxy's password: %q", lerr.Message)
	}
}

// The credential kept for an account's proxy and header secrets has its own place: an account whose
// ID is that account's ID and " proxy" or " upstream" does not push out those secrets or lose its
// own replaced one.
func TestHeldOutboundDoesNotCollideWithAccountID(t *testing.T) {
	const (
		pass   = "Held-Proxy-Secret-9"
		header = "Held-Header-Secret-9"
	)
	e := &Extension{info: protocol.Info{ProtocolMinor: protocol.Minor}}
	secrets := []string{pass, header}
	for _, id := range []string{"a proxy", "a upstream"} {
		oldKey, newKey := "old-key-of-"+id+"-account-1", "new-key-of-"+id+"-account-2"
		e.held.add(id, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: oldKey})
		e.held.add(id, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: newKey})
		secrets = append(secrets, oldKey, newKey)
	}
	if _, perr := e.outbound(&protocol.Account{ID: "a", ProxyURL: "http://alice:" + pass + "@127.0.0.1:3128", Headers: map[string]string{"X-Gateway-Key": header}}); perr != nil {
		t.Fatal(perr)
	}
	for _, secret := range secrets {
		if got := e.held.String("leak " + secret); got != "leak "+protocol.Redacted {
			t.Errorf("held output %q still holds %q", got, secret)
		}
	}
}

const testHeaderSecret = "header-Secret-value-47"

// A call carries the account's headers; the value of one about authentication never comes back
// in the call's error or the extension's log, and another's does.
func TestCallCarriesHeadersWithoutLeakingSecrets(t *testing.T) {
	log := &syncBuffer{}
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	s := ext.Generate(context.Background(), protocol.Account{ID: "h", Headers: map[string]string{"X-Gateway-Key": testHeaderSecret, "X-Title": "plain-title"}}, llm.Request{Model: "m"})
	ev := s.Next()
	s.Close()
	log.waitFor(t, "proxy done h")
	if want := "cannot reach  X-Gateway-Key=" + protocol.Redacted + " X-Title=plain-title"; ev.Type != llm.EventError || ev.Error.Message != want {
		t.Errorf("event %+v, want the error %q", ev, want)
	}
	if out := log.String(); strings.Contains(out, testHeaderSecret) || !strings.Contains(out, "X-Gateway-Key="+protocol.Redacted+" X-Title=plain-title") {
		t.Errorf("the log holds the secret header value, or not the headers:\n%s", out)
	}
}

// A secret header whose value is short ("1") is removed from a reply only inside its whole
// "Name: value" pair: its value alone would also scrub every other 1 in the reply.
func TestShortSecretHeaderValueLeavesReplyUnchanged(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	for _, c := range []struct {
		name, value, want string
	}{
		{"short value", "1", "cannot reach  X-Gateway-Key=1"},
		{"long value", testHeaderSecret, "cannot reach  X-Gateway-Key=" + protocol.Redacted},
	} {
		s := ext.Generate(context.Background(), protocol.Account{ID: "s", Headers: map[string]string{"X-Gateway-Key": c.value}}, llm.Request{Model: "m"})
		ev := s.Next()
		s.Close()
		if ev.Type != llm.EventError || ev.Error.Message != c.want {
			t.Errorf("%s: event %+v, want the error %q", c.name, ev, c.want)
		}
	}
}

// An extension older than protocol 0.12 cannot send an account's headers, so a call that has
// some is refused before it reaches it, as unsupported for the account.
func TestOldExtensionGetsNoCallWithHeaders(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	s := ext.Generate(context.Background(), protocol.Account{ID: "a", Headers: map[string]string{"X-Gateway-Key": testHeaderSecret}}, llm.Request{Model: "m"})
	ev := s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable ||
		!strings.Contains(ev.Error.Message, "headers") || strings.Contains(ev.Error.Message, testHeaderSecret) {
		t.Fatalf("event %+v, want a retryable unsupported error for the account that names headers", ev)
	}
	// The fake extension hangs up on a generate call: one without headers reaches it.
	s = ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "m"})
	ev = s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("call without headers: event %+v, want the extension's hang-up", ev)
	}
}
