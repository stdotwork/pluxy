package host

import (
	"context"
	"fmt"
	"maps"
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
