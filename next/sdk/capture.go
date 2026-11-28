package sdk

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

type captureLog struct {
	mu          sync.Mutex
	first, last *exchange
	between     int
	sent        bool
}

type exchange struct {
	req     protocol.UpstreamRequest
	reqBody *bodyCapture
	resp    *protocol.UpstreamResponse
	body    *bodyCapture
	err     string
}

func (c *captureLog) roundTrip(rt http.RoundTripper, req *http.Request) (*http.Response, error) {
	x := &exchange{req: protocol.UpstreamRequest{TimeMs: time.Now().UnixMilli(), Method: req.Method,
		URL: capturedURL(req.URL)}}
	x.req.Headers, x.req.HeadersLeftOut = capturedHeaders(req.Header)
	hasBody := req.Body != nil && req.Body != http.NoBody
	if hasBody {
		x.reqBody = &bodyCapture{size: req.ContentLength}
	}
	if !c.add(x) {
		return rt.RoundTrip(req)
	}
	if hasBody {
		use := func(bc *bodyCapture) func() {
			return func() {
				c.mu.Lock()
				x.reqBody = bc
				c.mu.Unlock()
			}
		}
		r := *req
		r.Body = &sentBody{teeBody: teeBody{req.Body, x.reqBody}, use: use(x.reqBody)}
		if get := req.GetBody; get != nil {
			r.GetBody = func() (io.ReadCloser, error) {
				b, err := get()
				if err != nil {
					return nil, err
				}
				bc := &bodyCapture{size: req.ContentLength}
				return &sentBody{teeBody: teeBody{b, bc}, use: use(bc)}, nil
			}
		}
		req = &r
	}
	resp, err := rt.RoundTrip(req)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		x.err = err.Error()
		return resp, err
	}
	x.resp = &protocol.UpstreamResponse{TimeMs: time.Now().UnixMilli(), Status: resp.StatusCode}
	x.resp.Headers, x.resp.HeadersLeftOut = capturedHeaders(resp.Header)
	// A 101's body is the switched connection (a WebSocket's), which its reader needs as it is.
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.Body != nil {
		x.body = &bodyCapture{eof: resp.Body == http.NoBody, size: resp.ContentLength}
		resp.Body = teeBody{resp.Body, x.body}
	}
	return resp, nil
}

func (c *captureLog) add(x *exchange) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.sent:
		return false
	case c.first == nil:
		c.first = x
	default:
		if c.last != nil {
			c.between++
		}
		c.last = x
	}
	return true
}

func (c *captureLog) take() *protocol.Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sent {
		return nil
	}
	c.sent = true
	if c.first == nil {
		return nil
	}
	d := &protocol.Diagnostic{UpstreamLeftOut: c.between}
	for _, x := range []*exchange{c.first, c.last} {
		if x == nil {
			continue
		}
		e := protocol.UpstreamExchange{Request: x.req, Error: x.err}
		e.Request.CapturedBody = x.reqBody.captured()
		if x.resp != nil {
			r := *x.resp
			r.CapturedBody = x.body.captured()
			e.Response = &r
		}
		d.Upstream = append(d.Upstream, e)
	}
	return d
}

func capturedURL(u *url.URL) string {
	c := *u
	c.User = nil
	if c.RawQuery != "" {
		q, err := url.ParseQuery(c.RawQuery)
		if err != nil {
			c.RawQuery = protocol.Redacted
			return c.String()
		}
		secret := false
		for k := range q {
			if protocol.SecretName(k) {
				q[k] = []string{protocol.Redacted}
				secret = true
			}
		}
		if secret {
			c.RawQuery = q.Encode()
		}
	}
	return c.String()
}

func capturedHeaders(h http.Header) (headers map[string][]string, leftOut int) {
	if len(h) == 0 {
		return nil, 0
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	slices.Sort(names)
	out := make(map[string][]string, len(h))
	size := 0
	for _, k := range names {
		secret := protocol.SecretName(k)
		for _, v := range h[k] {
			if secret {
				if scheme, _, ok := strings.Cut(v, " "); ok && scheme != "" && strings.Contains(strings.ToLower(k), "authorization") {
					v = scheme + " " + protocol.Redacted
				} else {
					v = protocol.Redacted
				}
			}
			n := len(k) + len(v) + 3
			if size+n > protocol.CapturedHeaderBytes {
				leftOut++
				continue
			}
			size += n
			out[k] = append(out[k], v)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return out, leftOut
}

type teeBody struct {
	io.ReadCloser
	c *bodyCapture
}

func (b teeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.c.Write(p[:n])
	}
	if err == io.EOF {
		b.c.mu.Lock()
		b.c.eof = true
		b.c.mu.Unlock()
	}
	return n, err
}

type sentBody struct {
	teeBody
	use func()
}

func (b *sentBody) Read(p []byte) (int, error) {
	b.use()
	return b.teeBody.Read(p)
}

type bodyCapture struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	at      int
	written int64
	eof     bool
	size    int64
}

func (c *bodyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.written += int64(n)
	if room := protocol.CapturedHead - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	for len(p) > 0 {
		if len(c.tail) < protocol.CapturedEnd {
			k := min(protocol.CapturedEnd-len(c.tail), len(p))
			c.tail = append(c.tail, p[:k]...)
			p = p[k:]
			continue
		}
		k := copy(c.tail[c.at:], p)
		c.at = (c.at + k) % protocol.CapturedEnd
		p = p[k:]
	}
	return n, nil
}

func (c *bodyCapture) captured() protocol.CapturedBody {
	if c == nil {
		return protocol.CapturedBody{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := string(c.tail[c.at:]) + string(c.tail[:c.at])
	stopped := !c.eof && (c.size <= 0 || c.written < c.size)
	if c.written <= protocol.CapturedHead+protocol.CapturedEnd {
		return protocol.CapturedBody{Body: string(c.head) + tail, BodySize: c.written, BodyStopped: stopped}
	}
	return protocol.CapturedBody{Body: string(c.head), BodyEnd: tail, BodySize: c.written, BodyStopped: stopped}
}
