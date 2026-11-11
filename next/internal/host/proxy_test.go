package host

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// echoProxy is an extension that writes the proxy each call carries to stderr and fails the
// call with an error that holds it.
func echoProxy() {
	_ = sdk.Upstream(&http.Transport{}) // an extension that sends through a proxy wraps its clients
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-proxy",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			fmt.Fprintf(os.Stderr, "proxy is %q\nproxy done %s\n", call.Account.ProxyURL, call.Account.ID)
			return &llm.Error{Code: llm.CodeUnavailable, Message: "cannot reach " + call.Account.ProxyURL}
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
				`keep: password bypass {"n": 1}`,
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
// not removed on its own: it would also scrub "password", "bypass" and the number 1.
func TestProxySecretRedaction(t *testing.T) {
	const keep = `keep: password bypass {"n": 1}`
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
		{"user name only", "http://alice@127.0.0.1:3128",
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

// The credential kept for an account's proxy has its own place: an account whose ID is that
// account's ID and " proxy" does not push out the proxy's secret or lose its own replaced one.
func TestHeldProxyDoesNotCollideWithAccountID(t *testing.T) {
	const (
		oldKey = "old-key-of-the-spaced-account-1"
		newKey = "new-key-of-the-spaced-account-2"
		pass   = "Held-Proxy-Secret-9"
	)
	e := &Extension{info: protocol.Info{ProtocolMinor: protocol.Minor}}
	e.held.add("a proxy", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: oldKey})
	e.held.add("a proxy", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: newKey})
	if _, perr := e.proxied(&protocol.Account{ID: "a", ProxyURL: "http://alice:" + pass + "@127.0.0.1:3128"}); perr != nil {
		t.Fatal(perr)
	}
	for _, secret := range []string{oldKey, newKey, pass} {
		if got := e.held.String("leak " + secret); got != "leak "+protocol.Redacted {
			t.Errorf("held output %q still holds %q", got, secret)
		}
	}
}
