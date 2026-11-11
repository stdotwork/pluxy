package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Upstream wraps an extension's upstream transport. Without a proxy it delegates to base
// unchanged, preserving its connection pool, HTTP versions and timeout settings. A call whose
// account has a proxy (Info.ProxyURL) goes through a copy of base that sends every request
// through that proxy, one copy per proxy, kept for reuse. Use the handler's context on requests
// so the transport can access UpstreamInfo and core's cancellation: a request without it uses
// no proxy. base must be non-nil and must not be changed after use.
func Upstream(base *http.Transport) http.RoundTripper {
	upstreamUsed.Store(true)
	return upstreamTransport{base: base, proxies: &proxyPool{}}
}

// upstreamUsed is set when the process creates a transport with Upstream. An extension that never
// does has no client that honours an account's proxy, so the SDK refuses a call that carries one
// (refuseProxy) instead of letting the extension send it direct.
var upstreamUsed atomic.Bool

// defaultIdleConnTimeout is the idle connection timeout of a per-proxy transport whose base has
// none, as http.DefaultTransport has.
const defaultIdleConnTimeout = 90 * time.Second

// refuseProxy returns the error for a call that carries a proxy when no client of the process
// is wrapped in Upstream; nil otherwise. The message holds no part of the proxy URL.
func refuseProxy(call protocol.Call) *llm.Error {
	if call.Account == nil || call.Account.ProxyURL == "" || upstreamUsed.Load() {
		return nil
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("this extension sends no request through sdk.Upstream, so it cannot use the proxy of account %q; the call was not sent", call.Account.ID)}
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
	return rt.RoundTrip(req)
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
// settings and proxy, never its credential or the request body. Settings are read-only.
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
}

type upstreamInfoKey struct{}

// UpstreamInfo returns the upstream information attached by the SDK dispatcher. It is
// absent on contexts created outside dispatch. Model and Format are empty without a request.
func UpstreamInfo(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(upstreamInfoKey{}).(Info)
	return info, ok
}

func withUpstreamInfo(ctx context.Context, call protocol.Call) context.Context {
	info := Info{Operation: call.Op}
	if call.Type == protocol.TypeSessionTurn {
		info.Operation = protocol.TypeSessionTurn
	}
	if call.Account != nil {
		info.AccountID, info.Settings, info.ProxyURL = call.Account.ID, call.Account.Settings, call.Account.ProxyURL
	}
	if call.Request != nil {
		info.Model = call.Request.Model
		if call.Request.Native != nil {
			info.Format = call.Request.Native.Format
		}
	}
	return context.WithValue(ctx, upstreamInfoKey{}, info)
}
