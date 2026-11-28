package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

func echoProxy() {
	_ = sdk.Upstream(&http.Transport{})
	err := sdk.Serve(sdk.Extension{
		ID:      "hosttest/echo-proxy",
		Version: "0.0.0",
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			msg := "cannot reach " + call.Account.ProxyURL
			for _, k := range slices.Sorted(maps.Keys(call.Account.Headers)) {
				msg += " " + k + "=" + call.Account.Headers[k]
			}
			if info, _ := sdk.UpstreamInfo(ctx); info.Payload != nil {
				b, _ := json.Marshal(info.Payload)
				msg += " payload=" + string(b)
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
	s = ext.Generate(context.Background(), protocol.Account{ID: "a", ProxyURL: "none"}, llm.Request{Model: "m"})
	ev = s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("direct call: event %+v, want the extension's hang-up", ev)
	}
}

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
			reply := c.want
			if c.name == "long password" {
				reply = strings.Replace(c.want, "reverse proxy", "reverse [redacted]", 1)
			}
			if ev.Type != llm.EventError || ev.Error.Message != reply {
				t.Errorf("reply:\n got %+v\nwant error %q", ev, reply)
			}
			log.waitFor(t, "echo: "+c.want)
		})
	}
}

func refreshOddReply() {
	socket := os.Getenv(protocol.EnvSocket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.Exit(1)
	}
	fmt.Print(protocol.HandshakeLine(protocol.Version, socket))
	exitWhenStdinCloses()
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
	if _, perr := e.outbound(&protocol.Account{ID: "a", ProxyURL: "http://alice:" + pass + "@127.0.0.1:3128", Headers: map[string]string{"X-Gateway-Key": header}}, nil); perr != nil {
		t.Fatal(perr)
	}
	for _, secret := range secrets {
		if got := e.held.String("leak " + secret); got != "leak "+protocol.Redacted {
			t.Errorf("held output %q still holds %q", got, secret)
		}
	}
}

const testHeaderSecret = "header-Secret-value-47"

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
	if want := "cannot reach  " + protocol.Redacted + " X-Title=plain-title"; ev.Type != llm.EventError || ev.Error.Message != want {
		t.Errorf("event %+v, want the error %q", ev, want)
	}
	if out := log.String(); strings.Contains(out, testHeaderSecret) || !strings.Contains(out, protocol.Redacted+" X-Title=plain-title") {
		t.Errorf("the log holds the secret header value, or not the headers:\n%s", out)
	}
}

func TestCallFillsCopiedHeaders(t *testing.T) {
	const session = "client-session-value-58"
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	account := protocol.Account{ID: "c", Headers: map[string]string{"X-Title": "plain-title", "X-Trace": "$x-client-trace",
		"X-Session-Copy": "$X-Client-Session", "X-Absent": "$X-Not-Sent"}}
	client := http.Header{"X-Client-Trace": {"trace-1"}, "X-Client-Session": {session}, "X-Unnamed": {"unnamed-1"},
		"Authorization": {"Bearer client-key-1"}}
	for _, c := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"client request", WithPayloadClient(context.Background(), "m", client), "cannot reach  " + protocol.Redacted + " X-Title=plain-title X-Trace=trace-1"},
		{"no client request", context.Background(), "cannot reach  X-Title=plain-title"},
	} {
		s := ext.Generate(c.ctx, account, llm.Request{Model: "m"})
		ev := s.Next()
		s.Close()
		if ev.Type != llm.EventError || ev.Error.Message != c.want {
			t.Errorf("%s: event %+v, want the error %q", c.name, ev, c.want)
		}
	}
	if account.Headers["X-Trace"] != "$x-client-trace" {
		t.Error("the call changed the account it was given")
	}
}

func TestShortSecretHeaderValueRemovedNextToItsName(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: &syncBuffer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	for _, c := range []struct {
		name, value, want string
	}{
		{"short value", "1", "cannot reach  " + protocol.Redacted},
		{"long value", testHeaderSecret, "cannot reach  " + protocol.Redacted},
	} {
		s := ext.Generate(context.Background(), protocol.Account{ID: "s", Headers: map[string]string{"X-Gateway-Key": c.value}}, llm.Request{Model: "m"})
		ev := s.Next()
		s.Close()
		if ev.Type != llm.EventError || ev.Error.Message != c.want {
			t.Errorf("%s: event %+v, want the error %q", c.name, ev, c.want)
		}
	}
}

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
	s = ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "m"})
	ev = s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("call without headers: event %+v, want the extension's hang-up", ev)
	}
}

var testRules = &payload.Rules{Override: []payload.Rule{
	{Models: []payload.Model{{Name: "alias-*"}}, Params: map[string]any{"a": 1}},
	{Models: []payload.Model{{Name: "m", Protocol: "echo-proxy", FromProtocol: "openai", Headers: map[string]string{"X-Tier": "go*"}, Exist: []string{"tools"}}},
		Params: map[string]any{"b": 1}},
	{Models: []payload.Model{{Name: "m", Protocol: "codex"}}, Params: map[string]any{"c": 1}},
	{Models: []payload.Model{{Name: "alias-x(high)"}}, Params: map[string]any{"d": 1}},
	{Models: []payload.Model{{Name: "m", FromProtocol: "claude"}}, Params: map[string]any{"e": 1}},
}}

func TestCallCarriesMatchingPayloadRules(t *testing.T) {
	ext, err := Start(filepath.Join(fakePackage(t, "echo-proxy"), ManifestFile), Options{Log: &syncBuffer{},
		Payload: func() *payload.Rules { return testRules }})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	req := llm.Request{Model: "m", Options: llm.Options{ThinkingSuffix: "high"}, Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)}}
	ctx := WithPayloadClient(context.Background(), "alias-x", http.Header{"X-Tier": {"gold"}})
	s := ext.Generate(ctx, protocol.Account{ID: "p"}, req)
	ev := s.Next()
	s.Close()
	want := `cannot reach  payload={"override":[{"models":[{}],"params":{"a":1}},{"models":[{"exist":["tools"]}],"params":{"b":1}},{"models":[{}],"params":{"d":1}}]}`
	if ev.Type != llm.EventError || ev.Error.Message != want {
		t.Errorf("event %+v, want the error %q", ev, want)
	}
	s = ext.Generate(context.Background(), protocol.Account{ID: "p"}, llm.Request{Model: "m"})
	ev = s.Next()
	s.Close()
	if want := "cannot reach "; ev.Type != llm.EventError || ev.Error.Message != want {
		t.Errorf("without the client: event %+v, want the error %q", ev, want)
	}
}

func TestOldExtensionGetsNoCallWithPayloadRules(t *testing.T) {
	rules := &payload.Rules{Filter: []payload.FilterRule{{Models: []payload.Model{{Name: "m"}}, Params: []string{"x"}}}}
	ext, err := Start(filepath.Join(fakePackage(t, "session-bad-frame"), ManifestFile), Options{Log: &syncBuffer{},
		Payload: func() *payload.Rules { return rules }})
	if err != nil {
		t.Fatal(err)
	}
	defer ext.Stop()
	s := ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "m"})
	ev := s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeUnsupported || ev.Error.Scope != llm.ScopeCredential || !ev.Error.Retryable ||
		!strings.Contains(ev.Error.Message, "payload rules") {
		t.Fatalf("event %+v, want a retryable unsupported error for the account that names payload rules", ev)
	}
	s = ext.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "other"})
	ev = s.Next()
	s.Close()
	if ev.Type != llm.EventError || ev.Error.Code != llm.CodeExtensionCrashed {
		t.Fatalf("call no rule matches: event %+v, want the extension's hang-up", ev)
	}
}

func TestResolveTimeouts(t *testing.T) {
	ms := func(v int64) *int64 { return &v }
	global := &protocol.Timeouts{HeaderMS: ms(1000), IdleMS: ms(2000)}
	e := &Extension{
		info: protocol.Info{ProtocolMinor: protocol.Minor},
		opts: Options{Timeouts: func() *protocol.Timeouts { return global }},
	}

	acct := protocol.Account{ID: "a", Timeouts: &protocol.Timeouts{HeaderMS: ms(0)}}
	e.resolveTimeouts(&acct)
	if acct.Timeouts == nil || *acct.Timeouts.HeaderMS != 0 || *acct.Timeouts.IdleMS != 2000 || *acct.Timeouts.TotalMS != defaultTotalTimeoutMS {
		t.Fatalf("resolved = %+v, want header=0 idle=2000 total=%d", acct.Timeouts, defaultTotalTimeoutMS)
	}

	old := &Extension{
		info: protocol.Info{ProtocolMinor: timeoutsMinor - 1},
		opts: Options{Timeouts: func() *protocol.Timeouts { return global }},
	}
	acctOld := protocol.Account{ID: "a", Timeouts: &protocol.Timeouts{HeaderMS: ms(100)}}
	old.resolveTimeouts(&acctOld)
	if acctOld.Timeouts != nil {
		t.Errorf("old extension got timeouts %+v, want nil", acctOld.Timeouts)
	}
}
