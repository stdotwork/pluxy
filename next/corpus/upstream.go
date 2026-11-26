package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
)

// FakeUpstream is a scripted upstream server. It records every request it receives and
// answers the n-th one with the n-th scripted reply. A request beyond the script gets a 500
// reply, so the extra request shows up in the observation.
type FakeUpstream struct {
	*httptest.Server
	script []Reply
	aside  func(*http.Request) (Reply, bool)

	mu       sync.Mutex
	requests []RecordedRequest
}

// RecordedRequest is one request the fake upstream received.
type RecordedRequest struct {
	Method string
	Path   string // path and query
	Header http.Header
	Body   []byte
	ws     *wsRecord // set for a WebSocket the fake upstream accepted
}

// NewFakeUpstream starts a fake upstream with the given replies. aside, when not nil, answers the
// requests it takes (see Provider.Aside) without recording them or using up a reply.
func NewFakeUpstream(script []Reply, aside func(*http.Request) (Reply, bool)) *FakeUpstream {
	u := &FakeUpstream{script: script, aside: aside}
	u.Server = httptest.NewUnstartedServer(http.HandlerFunc(u.serve))
	// A WebSocket step that drops the connection needs the TCP connection under the handshake.
	u.Server.Config.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, connKey{}, c)
	}
	u.Server.Start()
	return u
}

// connKey is the request context key of the TCP connection a request came in on.
type connKey struct{}

func (u *FakeUpstream) serve(w http.ResponseWriter, r *http.Request) {
	if u.aside != nil {
		if rep, ok := u.aside(r); ok {
			io.Copy(io.Discard, r.Body)
			writeReply(w, r, rep)
			return
		}
	}
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	n := len(u.requests)
	rec := RecordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Header: r.Header.Clone(), Body: body}
	ws := n < len(u.script) && len(u.script[n].WebSocket) > 0 && isWebSocketHandshake(r)
	if ws {
		rec.ws = &wsRecord{done: make(chan struct{}), dropped: make(chan struct{})}
	}
	u.requests = append(u.requests, rec)
	u.mu.Unlock()
	if ws {
		u.serveWebSocket(w, r, u.script[n].WebSocket, rec.ws)
		return
	}
	if n < len(u.script) && len(u.script[n].WebSocket) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"corpus: upstream request %d should be a WebSocket handshake"}}`, n+1)
		return
	}
	if n >= len(u.script) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":{"message":"corpus: no scripted reply for upstream request %d"}}`, n+1)
		return
	}
	writeReply(w, r, u.script[n])
}

// Requests returns what the fake upstream has received so far.
func (u *FakeUpstream) Requests() []RecordedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]RecordedRequest(nil), u.requests...)
}

func writeReply(w http.ResponseWriter, r *http.Request, rep Reply) {
	h := w.Header()
	for k, v := range rep.Headers {
		h.Set(k, v)
	}
	if h.Get("Content-Type") == "" {
		switch {
		case len(rep.SSE) > 0:
			h.Set("Content-Type", "text/event-stream")
		case len(rep.Connect) > 0:
			h.Set("Content-Type", "application/connect+proto")
		case len(rep.Body) > 0:
			h.Set("Content-Type", "application/json")
		}
	}
	status := rep.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	switch {
	case len(rep.Body) > 0:
		var buf bytes.Buffer
		if err := json.Compact(&buf, rep.Body); err != nil {
			buf.Reset()
			buf.Write(rep.Body)
		}
		_, _ = w.Write(buf.Bytes())
	case rep.BodyText != nil:
		_, _ = io.WriteString(w, *rep.BodyText)
	case len(rep.Connect) > 0:
		// The case was checked when it was loaded, so the frames encode.
		b, _ := encodeConnect(rep.Connect)
		_, _ = w.Write(b)
	}
	for _, ev := range rep.SSE {
		_, _ = w.Write(encodeSSE(ev))
		flush()
	}
	if rep.Hold {
		flush()
		<-r.Context().Done()
		return
	}
	if rep.Abort {
		flush()
		// Ends the handler without finishing the response; net/http closes the connection
		// without the final chunk, so the reader sees a broken body.
		panic(http.ErrAbortHandler)
	}
}

func encodeSSE(ev SSEEvent) []byte {
	if ev.Raw != nil {
		return []byte(*ev.Raw)
	}
	var b bytes.Buffer
	if ev.Comment != nil {
		fmt.Fprintf(&b, ": %s\n", *ev.Comment)
	}
	if ev.Event != "" {
		fmt.Fprintf(&b, "event: %s\n", ev.Event)
	}
	if len(ev.Data) > 0 {
		var s string
		if json.Unmarshal(ev.Data, &s) == nil {
			fmt.Fprintf(&b, "data: %s\n", s)
		} else {
			var c bytes.Buffer
			if err := json.Compact(&c, ev.Data); err != nil {
				c.Reset()
				c.Write(ev.Data)
			}
			fmt.Fprintf(&b, "data: %s\n", c.Bytes())
		}
	}
	b.WriteString("\n")
	return b.Bytes()
}
