package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

// Upstream wraps an extension's upstream transport. Without a proxy it delegates to base
// unchanged, preserving its connection pool, HTTP versions and timeout settings, except for a
// tracked request (see below). A call whose account has a proxy (Info.ProxyURL) goes through a
// copy of base that sends every request through that proxy, one copy per proxy, kept for reuse. A call whose account has headers
// (Info.Headers) sends each of them on every request, in place of any value the extension set
// for the same name. A call with payload rules (Info.Payload, protocol minor version 14) has them
// applied to each request whose body is JSON, after everything else changed it (see
// WithPayloadRoot); a session turn's upstream message is not such a request, so the extension
// passes it to ApplyPayload (protocol minor version 15). A call core asks to capture (protocol
// minor version 13) records each request and what came back, without its secrets, and the SDK
// reports them to core before the call's terminal event; a response body is recorded as it is
// read, its start and end only. Use the handler's context on requests so the transport can
// access UpstreamInfo and core's cancellation: a request without it uses no proxy, no headers
// and no payload rules and is not recorded. A request tracked with TrackSend or TrackSendContext
// goes through a copy of base made for it alone, through the account's proxy if it has one, with
// keep-alives off: it dials its own connection, which no other request uses, and the copy closes
// what it opened once the request failed or its response body is closed. The copies use
// base.Clone, which does not carry protocols registered on base with RegisterProtocol: proxied
// and tracked requests go without them.
// base must be non-nil and must not be changed after use.
func Upstream(base *http.Transport) http.RoundTripper {
	upstreamUsed.Store(true)
	return upstreamTransport{base: base, proxies: &proxyPool{}}
}

// upstreamUsed is set when the process creates a transport with Upstream. An extension that never
// does has no client that honours an account's proxy or headers or a call's payload rules, so the
// SDK refuses a call that carries any of them (refuseUpstream) instead of letting the extension
// send it without them.
var upstreamUsed atomic.Bool

// defaultIdleConnTimeout is the idle connection timeout of a per-proxy transport whose base has
// none, as http.DefaultTransport has.
const defaultIdleConnTimeout = 90 * time.Second

// refuseUpstream returns the error for a call that carries a proxy, headers or payload rules
// when no client of the process is wrapped in Upstream; nil otherwise. The message holds no part
// of the proxy URL, no header value and no rule.
func refuseUpstream(call protocol.Call) *llm.Error {
	if upstreamUsed.Load() {
		return nil
	}
	a := call.Account
	if a == nil {
		a = &protocol.Account{}
	}
	what := ""
	switch {
	case a.ProxyURL != "":
		what = "the proxy of"
	case len(a.Headers) > 0:
		what = "the headers of"
	case call.Payload != nil:
		what = "the payload rules for"
	default:
		return nil
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
		Message: fmt.Sprintf("this extension sends no request through sdk.Upstream, so it cannot use %s the account; the call was not sent", what)}
}

type upstreamTransport struct {
	base    *http.Transport
	proxies *proxyPool
}

func (t upstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	info, _ := UpstreamInfo(req.Context())
	var rt http.RoundTripper
	var err error
	if wantsNewConn(req.Context()) {
		rt, err = t.oneShot(info.ProxyURL)
	} else {
		rt, err = t.transport(info.ProxyURL)
	}
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	if sink := aliveFrom(req.Context()); sink != nil {
		rt = aliveTransport{next: rt, sink: sink}
	}
	if len(info.Headers) > 0 {
		req = withHeaders(req, info.Headers)
	}
	if info.Payload != nil {
		if req, err = withPayload(req); err != nil {
			return nil, err
		}
	}
	if hasTimeouts(info) {
		rt = timeoutTransport{next: rt, info: info}
	}
	if info.capture != nil {
		return info.capture.roundTrip(rt, req)
	}
	return rt.RoundTrip(req)
}

// withHeaders returns a copy of req (a RoundTripper must not change the request it is given)
// with each of headers set over every value req has for that name, in any letter case. On a
// redirect away from the first request's scheme and host, a header whose name is secret
// (upheader.Secret) is removed instead, whoever set it, as net/http leaves Authorization and
// Cookie out there: the redirect target must not receive it.
func withHeaders(req *http.Request, headers map[string]string) *http.Request {
	r := req.Clone(req.Context())
	if r.Header == nil {
		r.Header = http.Header{}
	}
	away := redirectedAway(req)
	for name, value := range headers {
		for k := range r.Header {
			if strings.EqualFold(k, name) {
				delete(r.Header, k)
			}
		}
		if away && upheader.Secret(name) {
			continue
		}
		r.Header.Set(name, value)
	}
	return r
}

// redirectedAway reports whether req follows a redirect to another scheme (https to http, say)
// or another host (name and port as written, letter case aside) than the first request of its
// redirect chain went to.
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
	from, to := first.Request.URL, req.URL
	return !strings.EqualFold(from.Scheme, to.Scheme) || !strings.EqualFold(from.Host, to.Host)
}

// withPayload returns req with rules applied to its body: a copy holding the new body when they
// change it. A body that is not JSON (by its Content-Type), or is encoded (Content-Encoding), is
// sent as it is. The rules apply to the body req carries, once: net/http sends the same new
// bytes again when it retries the request on another connection.
func withPayload(req *http.Request) (*http.Request, error) {
	if req.Body == nil || req.Body == http.NoBody || !jsonBody(req.Header) {
		return req, nil
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading the request body for payload rules: %w", err)
	}
	out := ApplyPayload(req.Context(), body)
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(out)), nil }
	r.ContentLength = int64(len(out))
	return r, nil
}

// ApplyPayload returns body with the payload rules of the call ctx belongs to (Info.Payload)
// applied, for an extension that sends a request upstream by a way that is not an HTTP request
// through Upstream: the message that carries a session turn on an upstream WebSocket. Call it
// once, on the final message, after everything else the extension does to it. Without rules, or
// when body is not a JSON object or array, it returns body as it is. Rule paths are relative to
// the body, or to the member WithPayloadRoot names. ctx must be the context of the turn or call.
func ApplyPayload(ctx context.Context, body []byte) []byte {
	info, _ := UpstreamInfo(ctx)
	if info.Payload == nil {
		return body
	}
	out, _ := payload.Apply(info.Payload, body, payload.Input{Root: payloadRoot(ctx), Selected: true})
	return out
}

// jsonBody reports whether h says the body is JSON (application/json or a +json type) and not
// encoded.
func jsonBody(h http.Header) bool {
	if e := strings.TrimSpace(h.Get("Content-Encoding")); e != "" && !strings.EqualFold(e, "identity") {
		return false
	}
	t, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (t == "application/json" || strings.HasSuffix(t, "+json"))
}

type payloadRootKey struct{}

// WithPayloadRoot returns ctx saying that requests made with it carry the model request inside
// the body's member root (for example "request" for a body {"project": ..., "request": {...}}):
// payload rule paths are then relative to it, as in today's core. Without it they are relative
// to the body itself.
func WithPayloadRoot(ctx context.Context, root string) context.Context {
	return context.WithValue(ctx, payloadRootKey{}, root)
}

func payloadRoot(ctx context.Context) string {
	root, _ := ctx.Value(payloadRootKey{}).(string)
	return root
}

// transport returns the transport for requests through proxy ("" for none).
func (t upstreamTransport) transport(proxy string) (*http.Transport, error) {
	if proxy == "" {
		return t.base, nil
	}
	return t.proxies.get(t.base, proxy)
}

// oneShot returns the transport for one tracked request: a copy of base, through proxy ("" for
// none), with keep-alives off. Nothing else uses it, so no request shares its connection: an
// HTTP/2 connection of a shared transport stays open to the next request when a request on it
// was cancelled before it was written.
func (t upstreamTransport) oneShot(proxy string) (http.RoundTripper, error) {
	c := t.base.Clone()
	if proxy != "" {
		u, err := parseProxy(proxy)
		if err != nil {
			return nil, err
		}
		c.Proxy = http.ProxyURL(u)
	}
	c.DisableKeepAlives = true
	// The copy gets a dialer of its own below.
	keepHTTP2(c, t.base)
	conns := &dialedConns{}
	dial := c.DialContext
	if dial == nil {
		if d := c.Dial; d != nil {
			dial = func(_ context.Context, network, addr string) (net.Conn, error) { return d(network, addr) }
		} else {
			dial = (&net.Dialer{}).DialContext
		}
	}
	c.Dial, c.DialContext = nil, conns.wrap(dial)
	if d := c.DialTLS; d != nil && c.DialTLSContext == nil {
		c.DialTLSContext = func(_ context.Context, network, addr string) (net.Conn, error) { return d(network, addr) }
	}
	if c.DialTLSContext != nil {
		c.DialTLS, c.DialTLSContext = nil, conns.wrap(c.DialTLSContext)
	}
	return oneShotTransport{c, conns}, nil
}

// oneShotTransport sends one request and closes every connection it dialed once the request
// failed or the response body is closed. Closing idle connections is not enough: an HTTP/2
// request cancelled before it was written gives back its hold on the connection only after
// RoundTrip has returned, and the connection is then left open.
type oneShotTransport struct {
	t     *http.Transport
	conns *dialedConns
}

func (o oneShotTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.t.RoundTrip(req)
	if err != nil {
		o.done()
		return nil, err
	}
	resp.Body = &closingBody{ReadCloser: resp.Body, done: o.done}
	return resp, nil
}

// done stops any dial still running and closes every connection dialed, now and later.
func (o oneShotTransport) done() {
	o.t.CloseIdleConnections()
	o.conns.closeAll()
}

type closingBody struct {
	io.ReadCloser
	done func()
}

func (b *closingBody) Close() error {
	err := b.ReadCloser.Close()
	b.done()
	return err
}

// dialedConns keeps the connections a transport dialed, to close them all.
type dialedConns struct {
	mu     sync.Mutex
	closed bool
	conns  []net.Conn
}

func (d *dialedConns) wrap(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.closed {
			c.Close()
			return nil, net.ErrClosed
		}
		d.conns = append(d.conns, c)
		return c, nil
	}
}

func (d *dialedConns) closeAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for _, c := range d.conns {
		c.Close()
	}
	d.conns = nil
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
	u, err := parseProxy(proxy)
	if err != nil {
		return nil, err
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
	keepHTTP2(t, base)
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

// keepHTTP2 makes c, a copy of base made with base.Clone, ask for HTTP/2 when net/http turned it
// on for base by itself. net/http does that only for a transport without a dialer or TLS config
// of its own, and a copy carries the TLS config net/http may have made for base (and oneShot gives
// its copy a dialer), so net/http would not turn HTTP/2 on for the copy, though its TLS config
// offers h2. Clone has set base up, and then base has an "h2" entry in TLSNextProto, which the
// copy does not carry. A base that chose its protocols (Protocols, its own TLSNextProto) or
// already forces HTTP/2 keeps what it chose.
func keepHTTP2(c, base *http.Transport) {
	if c.Protocols == nil && c.TLSNextProto == nil && !c.ForceAttemptHTTP2 && base.TLSNextProto["h2"] != nil {
		c.ForceAttemptHTTP2 = true
	}
}

// parseProxy parses an account's proxy URL. Its error holds no part of the URL.
func parseProxy(proxy string) (*url.URL, error) {
	u, err := protocol.ParseProxyURL(proxy)
	if err != nil {
		return nil, fmt.Errorf("the account's proxy_url %w", err)
	}
	return u, nil
}

func (p *proxyPool) closeIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, t := range p.by {
		t.CloseIdleConnections()
	}
}

// Info describes the call making an upstream request. It contains only the account's ID,
// settings, proxy and headers and the call's payload rules, never its credential or the request
// body. Settings are
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
	// Payload is the payload rules core matched to the call or session turn (protocol minor
	// version 14, 15 for a turn), which Upstream applies to each JSON request body and
	// ApplyPayload to a message; nil for none. Read-only.
	Payload *payload.Rules
	// Stream is true when the call's model request asked for streaming.
	Stream bool
	// Timeouts holds the upstream timeouts core resolved for the account (minor version 19).
	// Nil uses the SDK defaults when hasTimeouts is set.
	Timeouts *protocol.Timeouts

	capture *captureLog // the call's capture, when core asked for one
}

type upstreamInfoKey struct{}

// UpstreamInfo returns the upstream information attached by the SDK dispatcher. It is
// absent on contexts created outside dispatch. Model and Format are empty without a request.
func UpstreamInfo(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(upstreamInfoKey{}).(Info)
	return info, ok
}

// Uncaptured returns ctx without the call's upstream capture: a request made with it goes as the
// call's other requests go (through its proxy, with its headers) but is not recorded. Use it for
// credential traffic a call makes for itself, such as trading a key for an access token: core
// removes only the account's own secrets from a capture, so the token, and what was sent for it,
// would reach the request log.
func Uncaptured(ctx context.Context) context.Context {
	info, ok := UpstreamInfo(ctx)
	if !ok || info.capture == nil {
		return ctx
	}
	info.capture = nil
	return context.WithValue(ctx, upstreamInfoKey{}, info)
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
		info.Timeouts = call.Account.Timeouts
	}
	info.Payload = call.Payload
	if call.Request != nil {
		info.Model = call.Request.Model
		info.Stream = call.Request.Stream
		if call.Request.Native != nil {
			info.Format = call.Request.Native.Format
		}
	}
	return context.WithValue(ctx, upstreamInfoKey{}, info), info.capture
}
