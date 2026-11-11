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

// captureLog records a captured call's upstream exchanges (protocol minor version 13): the
// first and the last, counting the ones between. Its zero value is ready; nil records nothing.
type captureLog struct {
	mu          sync.Mutex
	first, last *exchange
	between     int
	sent        bool // the diagnostic frame went out: later exchanges are not recorded
}

// exchange is one recorded upstream request and what came back.
type exchange struct {
	req     protocol.UpstreamRequest
	reqBody *bodyCapture
	resp    *protocol.UpstreamResponse
	body    *bodyCapture
	err     string
}

// roundTrip sends req through rt, recording it and its response. A request made after the call's
// diagnostic frame went out is sent unrecorded.
func (c *captureLog) roundTrip(rt http.RoundTripper, req *http.Request) (*http.Response, error) {
	x := &exchange{req: protocol.UpstreamRequest{TimeMs: time.Now().UnixMilli(), Method: req.Method,
		URL: capturedURL(req.URL)}}
	x.req.Headers, x.req.HeadersLeftOut = capturedHeaders(req.Header)
	hasBody := req.Body != nil && req.Body != http.NoBody
	if hasBody {
		x.reqBody = &bodyCapture{} // set before x is published: take reads it
	}
	if !c.add(x) {
		return rt.RoundTrip(req)
	}
	if hasBody {
		// What the transport reads is what is recorded, as it reads it: the request is not held
		// back to read the body first, and the body is read once.
		// When the transport sends the body again, each copy is recorded on its own and the
		// copy read last is the one kept, not the copies joined. A copy made but never read
		// (one kept ready for a retry that did not happen) changes nothing.
		use := func(bc *bodyCapture) func() {
			return func() {
				c.mu.Lock()
				x.reqBody = bc
				c.mu.Unlock()
			}
		}
		r := *req // a RoundTripper must not change the request it is given
		r.Body = &sentBody{teeBody: teeBody{req.Body, x.reqBody}, use: use(x.reqBody)}
		if get := req.GetBody; get != nil {
			r.GetBody = func() (io.ReadCloser, error) {
				b, err := get()
				if err != nil {
					return nil, err
				}
				bc := &bodyCapture{}
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
	// A 101's body is the switched connection (a WebSocket's), which its reader needs as it is;
	// its messages are not recorded.
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.Body != nil {
		x.body = &bodyCapture{}
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

// take returns the diagnostic of what was recorded, once: nil when nothing was or it was taken.
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

// capturedURL is u without its user name and password, with the values of query parameters
// whose names are secret (protocol.SecretName) redacted. A query that does not parse cannot be
// told apart from its secrets, so all of it is redacted.
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

// capturedHeaders is a copy of h with the values of headers whose names are secret
// (protocol.SecretName) redacted, keeping an authorization scheme such as Bearer. It keeps at
// most protocol.CapturedHeaderBytes of header lines, taking names in order, and counts the lines
// it left out.
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

// teeBody records what is read from a body as it is read.
type teeBody struct {
	io.ReadCloser
	c *bodyCapture
}

func (b teeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		_, _ = b.c.Write(p[:n])
	}
	return n, err
}

// sentBody is one copy of a request body the transport sends: its first read makes it the copy
// recorded.
type sentBody struct {
	teeBody
	once sync.Once
	use  func()
}

func (b *sentBody) Read(p []byte) (int, error) {
	b.once.Do(b.use)
	return b.teeBody.Read(p)
}

// bodyCapture keeps the start and the end of what is written to it (protocol.CapturedHead and
// protocol.CapturedEnd bytes) and counts the rest.
type bodyCapture struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte // a ring of protocol.CapturedEnd bytes once full
	at      int    // where the ring's oldest byte is
	written int64
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

// captured returns what c kept: the whole body in Body when it fits, else its start and end.
func (c *bodyCapture) captured() protocol.CapturedBody {
	if c == nil {
		return protocol.CapturedBody{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := string(c.tail[c.at:]) + string(c.tail[:c.at])
	if c.written <= protocol.CapturedHead+protocol.CapturedEnd {
		return protocol.CapturedBody{Body: string(c.head) + tail, BodySize: c.written}
	}
	return protocol.CapturedBody{Body: string(c.head), BodyEnd: tail, BodySize: c.written}
}
