package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Upstream wraps an extension's upstream transport. Without a proxy it delegates to base
// unchanged, preserving its connection pool, HTTP versions and timeout settings. A call whose
// account has a proxy (Info.ProxyURL) goes through a copy of base that sends every request
// through that proxy, one copy per proxy, kept for reuse. A call whose account has headers
// (Info.Headers) sends each of them on every request, in place of any value the extension set
// for the same name. A call core asks to capture (protocol minor version 13) records each request
// and what came back, without its secrets, and the SDK reports them to core before the call's
// terminal event; a response body is recorded as it is read, its start and end only. Use the
// handler's context on requests so the transport can access UpstreamInfo and core's
// cancellation: a request without it uses no proxy and no headers and is not recorded.
// base must be non-nil and must not be changed after use.
func Upstream(base *http.Transport) http.RoundTripper {
	upstreamUsed.Store(true)
	return upstreamTransport{base: base, proxies: &proxyPool{}}
}

// upstreamUsed is set when the process creates a transport with Upstream. An extension that never
// does has no client that honours an account's proxy or headers, so the SDK refuses a call that
// carries either (refuseUpstream) instead of letting the extension send it without them.
var upstreamUsed atomic.Bool

// defaultIdleConnTimeout is the idle connection timeout of a per-proxy transport whose base has
// none, as http.DefaultTransport has.
const defaultIdleConnTimeout = 90 * time.Second

// refuseUpstream returns the error for a call that carries a proxy or headers when no client of
// the process is wrapped in Upstream; nil otherwise. The message holds no part of the proxy URL
// and no header value.
func refuseUpstream(call protocol.Call) *llm.Error {
	if call.Account == nil || upstreamUsed.Load() {
		return nil
	}
	what := ""
	switch {
	case call.Account.ProxyURL != "":
		what = "the proxy"
	case len(call.Account.Headers) > 0:
		what = "the headers"
	default:
		return nil
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("this extension sends no request through sdk.Upstream, so it cannot use %s of account %q; the call was not sent", what, call.Account.ID)}
}

type upstreamTransport struct {
	base    *http.Transport
	proxies *proxyPool
}

func (t upstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	info, _ := UpstreamInfo(req.Context())
	rt, err := t.transport(info.ProxyURL)
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	if len(info.Headers) > 0 {
		req = withHeaders(req, info.Headers)
	}
	if info.capture != nil {
		return info.capture.roundTrip(rt, req)
	}
	return rt.RoundTrip(req)
}

// withHeaders returns a copy of req (a RoundTripper must not change the request it is given)
// with each of headers set over every value req has for that name, in any letter case. On a
// redirect to another host than the first request's, the headers whose names are secret
// (upheader.Secret) are left out, as net/http leaves Authorization and Cookie out there: the
// redirect target must not receive them.
func withHeaders(req *http.Request, headers map[string]string) *http.Request {
	r := req.Clone(req.Context())
	if r.Header == nil {
		r.Header = http.Header{}
	}
	crossHost := redirectedAway(req)
	for name, value := range headers {
		if crossHost && upheader.Secret(name) {
			continue
		}
		for k := range r.Header {
			if strings.EqualFold(k, name) {
				delete(r.Header, k)
			}
		}
		r.Header.Set(name, value)
	}
	return r
}

// redirectedAway reports whether req follows a redirect to a host (name and port as written,
// letter case aside) other than the one the first request of its redirect chain went to.
func redirectedAway(req *http.Request) bool {
	if req.Response == nil || req.URL == nil {
		return false
	}
	first := req.Response
	for first.Request != nil && first.Request.Response != nil {
		first = first.Request.Response
	}
	if first.Request == nil || first.Request.URL == nil {
		return true // the chain cannot be traced, so the new host is not known to be the first
	}
	return !strings.EqualFold(first.Request.URL.Host, req.URL.Host)
}

// transport returns the transport for requests through proxy ("" for none).
func (t upstreamTransport) transport(proxy string) (*http.Transport, error) {
	if proxy == "" {
		return t.base, nil
	}
	return t.proxies.get(t.base, proxy)
}

// CloseIdleConnections preserves http.Client.CloseIdleConnections through the wrapper.
func (t upstreamTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
	t.proxies.closeIdle()
}

// maxProxies bounds the transports one client keeps: a config names a handful of proxies, and
// past this many the pool starts over rather than grow with every proxy a reload names.
const maxProxies = 32

// proxyPool holds a client's transports for each proxy.
type proxyPool struct {
	mu sync.Mutex
	by map[string]*http.Transport
}

// get returns the transport through proxy, a copy of base made the first time. A proxy that
// does not parse fails the request instead of sending it without one.
func (p *proxyPool) get(base *http.Transport, proxy string) (*http.Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.by[proxy]; t != nil {
		return t, nil
	}
	u, err := protocol.ParseProxyURL(proxy)
	if err != nil {
		return nil, fmt.Errorf("the account's proxy_url %w", err)
	}
	if len(p.by) >= maxProxies {
		for _, t := range p.by {
			t.CloseIdleConnections()
		}
		p.by = nil
	}
	if p.by == nil {
		p.by = map[string]*http.Transport{}
	}
	t := base.Clone()
	// A transport the pool drops while a request still uses it must close its own idle
	// connections, so none stays open for good.
	if t.IdleConnTimeout <= 0 {
		t.IdleConnTimeout = defaultIdleConnTimeout
	}
	// net/http speaks to http and https proxies (CONNECT for https targets) and to socks5 and
	// socks5h ones, and fills in Proxy-Authorization or SOCKS5 authentication from the user name
	// and password. It resolves target host names at a SOCKS5 proxy for both schemes.
	t.Proxy = http.ProxyURL(u)
	p.by[proxy] = t
	return t, nil
}

func (p *proxyPool) closeIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.by {
		t.CloseIdleConnections()
	}
}

// Info describes the call making an upstream request. It contains only the account's ID,
// settings, proxy and headers, never its credential or the request body. Settings are
// read-only.
type Info struct {
	AccountID string
	Settings  json.RawMessage
	Model     string
	Format    string
	// Operation is the protocol operation; a session turn uses protocol.TypeSessionTurn.
	Operation string
	// ProxyURL is the proxy the call's upstream requests go through; empty for none. Its user
	// name and password are secret: never log or show it.
	ProxyURL string
	// Headers are the account's extra headers, which Upstream sets on each request; read-only.
	// Values of headers about authentication are secret: never log or show them.
	Headers map[string]string

	capture *captureLog // the call's capture, when core asked for one
}

type upstreamInfoKey struct{}

// UpstreamInfo returns the upstream information attached by the SDK dispatcher. It is
// absent on contexts created outside dispatch. Model and Format are empty without a request.
func UpstreamInfo(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(upstreamInfoKey{}).(Info)
	return info, ok
}

// withUpstreamInfo returns ctx carrying call's upstream information, and the call's capture when
// core asked for one (nil otherwise).
func withUpstreamInfo(ctx context.Context, call protocol.Call) (context.Context, *captureLog) {
	info := Info{Operation: call.Op}
	switch {
	case !call.Capture:
	case call.Type == protocol.TypeSessionTurn, call.Op == protocol.OpGenerate, call.Op == protocol.OpCountTokens, call.Op == protocol.OpEndpoint:
		info.capture = &captureLog{}
	}
	if call.Type == protocol.TypeSessionTurn {
		info.Operation = protocol.TypeSessionTurn
	}
	if call.Account != nil {
		info.AccountID, info.Settings, info.ProxyURL = call.Account.ID, call.Account.Settings, call.Account.ProxyURL
		info.Headers = call.Account.Headers
	}
	if call.Request != nil {
		info.Model = call.Request.Model
		if call.Request.Native != nil {
			info.Format = call.Request.Native.Format
		}
	}
	return context.WithValue(ctx, upstreamInfoKey{}, info), info.capture
}
