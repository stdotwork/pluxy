package corpus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// stepTimeout bounds one step that waits for the other side: a client read or an upstream
// receive. A core that never sends what the step waits for shows up as a timeout in the
// observation rather than hanging the case.
const stepTimeout = 10 * time.Second

// closeWait is how long the runner waits, after the client's script, for the fake upstream's
// sockets to close; a socket still open then is recorded as open.
const closeWait = 5 * time.Second

// readLimit is the largest message either corpus side reads.
const readLimit = 64 << 20

// turnEnds are the message types a "turn" read stops at.
var turnEnds = map[string]bool{"response.completed": true, "response.done": true, "response.incomplete": true,
	"response.failed": true, "error": true}

// runClientWebSocket runs the case's WebSocket script against a core at base and returns the
// client's side of the observation: the handshake reply's status and headers, every message
// read, and the close the core sent, if it closed the socket.
func runClientWebSocket(ctx context.Context, c *Case, base string) (map[string]any, error) {
	h := http.Header{}
	h.Set("User-Agent", ClientUserAgent)
	switch c.Client.Auth {
	case "", "client-key":
		h.Set("Authorization", "Bearer "+ClientKey)
	case "none":
	default:
		h.Set("Authorization", "Bearer "+c.Client.Auth)
	}
	for k, v := range c.Client.Headers {
		h.Set(k, v)
	}
	url := "ws" + strings.TrimPrefix(base, "http") + c.Client.Path
	dctx, cancel := context.WithTimeout(ctx, stepTimeout)
	conn, resp, err := websocket.Dial(dctx, url, &websocket.DialOptions{HTTPClient: client, HTTPHeader: h})
	cancel()
	out := map[string]any{}
	if resp != nil {
		out["status"] = resp.StatusCode
		out["headers"] = headerMap(resp.Header)
	}
	if err != nil {
		if resp == nil {
			return nil, fmt.Errorf("websocket handshake: %w", err)
		}
		// A refused handshake: the reply is an ordinary HTTP response, of which the library
		// keeps the first 1024 bytes of the body.
		if resp.Body != nil {
			var buf [1024]byte
			n, _ := resp.Body.Read(buf[:])
			if n > 0 {
				out["body"] = bodyValue(buf[:n])
			}
		}
		return out, nil
	}
	conn.SetReadLimit(readLimit)
	messages := []any{}
	closed := false
	record := func(err error) {
		closed = true
		out["close"] = closeOf(err)
	}
steps:
	for i, s := range c.Client.WebSocket {
		switch {
		case len(s.Send) > 0 || s.SendText != nil:
			text := []byte{}
			if s.SendText != nil {
				text = []byte(*s.SendText)
			} else if text, err = compactJSON(s.Send); err != nil {
				return nil, fmt.Errorf("client.websocket[%d]: %w", i, err)
			}
			if err := conn.Write(ctx, websocket.MessageText, text); err != nil {
				record(err)
				break steps
			}
		case s.Read != "":
			for {
				rctx, cancel := context.WithTimeout(ctx, stepTimeout)
				_, data, err := conn.Read(rctx)
				timedOut := rctx.Err() != nil && ctx.Err() == nil
				cancel()
				if timedOut {
					out["timeout"] = fmt.Sprintf("client.websocket[%d]: no %q message", i, s.Read)
					break steps
				}
				if err != nil {
					record(err)
					break steps
				}
				v := bodyValue(data)
				messages = append(messages, v)
				typ := messageType(v)
				if typ == s.Read || (s.Read == "turn" && turnEnds[typ]) {
					break
				}
			}
		case s.Close != 0:
			_ = conn.Close(websocket.StatusCode(s.Close), "")
			closed = true
			break steps
		case s.Drop:
			_ = conn.CloseNow()
			closed = true
			break steps
		}
	}
	if !closed {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
	out["messages"] = messages
	return out, nil
}

// closeOf describes how a socket ended from the error a read returned: the close code and
// reason the peer sent, or 1006 when the connection ended without a close message.
func closeOf(err error) map[string]any {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		out := map[string]any{"code": int(ce.Code)}
		if ce.Reason != "" {
			out["reason"] = ce.Reason
		}
		return out
	}
	return map[string]any{"code": int(websocket.StatusAbnormalClosure)}
}

func messageType(v any) string {
	if m, ok := v.(map[string]any); ok {
		if t, ok := m["type"].(string); ok {
			return t
		}
	}
	return ""
}

func compactJSON(raw json.RawMessage) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// wsRecord is what the fake upstream saw on one WebSocket: every message the core sent, and
// how the core closed the socket.
type wsRecord struct {
	mu       sync.Mutex
	messages []any
	close    map[string]any // set when the core closed the socket or the connection ended
	timeout  string
	done     chan struct{}
}

func (r *wsRecord) observation() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{"messages": append([]any{}, r.messages...)}
	select {
	case <-r.done:
		if r.close != nil {
			out["close"] = r.close
		}
	default:
		out["close"] = "open"
	}
	if r.timeout != "" {
		out["timeout"] = r.timeout
	}
	return out
}

// serveWebSocket accepts a handshake and plays the upstream's side of the script.
func (u *FakeUpstream) serveWebSocket(w http.ResponseWriter, r *http.Request, steps []UpstreamStep, rec *wsRecord) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		close(rec.done)
		return
	}
	conn.SetReadLimit(readLimit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	incoming := make(chan struct{}, 1024)
	closedByScript := false
	var scriptMu sync.Mutex
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				scriptMu.Lock()
				byScript := closedByScript
				scriptMu.Unlock()
				rec.mu.Lock()
				if !byScript {
					rec.close = closeOf(err)
				}
				rec.mu.Unlock()
				return
			}
			rec.mu.Lock()
			rec.messages = append(rec.messages, bodyValue(data))
			rec.mu.Unlock()
			incoming <- struct{}{}
		}
	}()
	// Each Receive step takes one message; messages the script does not wait for are still
	// recorded.
steps:
	for i, s := range steps {
		switch {
		case s.Receive:
			select {
			case <-incoming:
			case <-readerDone:
				break steps
			case <-time.After(stepTimeout):
				rec.mu.Lock()
				rec.timeout = fmt.Sprintf("upstream websocket step %d: no message from the core", i)
				rec.mu.Unlock()
				break steps
			}
		case len(s.Send) > 0:
			text, err := compactJSON(s.Send)
			if err != nil {
				break steps
			}
			if conn.Write(ctx, websocket.MessageText, text) != nil {
				break steps
			}
		case s.Close != nil:
			scriptMu.Lock()
			closedByScript = true
			scriptMu.Unlock()
			_ = conn.Close(websocket.StatusCode(s.Close.Code), s.Close.Reason)
			break steps
		case s.Drop:
			scriptMu.Lock()
			closedByScript = true
			scriptMu.Unlock()
			_ = conn.CloseNow()
			break steps
		}
	}
	<-readerDone
	close(rec.done)
}

// waitWebSockets waits until every WebSocket the fake upstream accepted has closed, or until
// the wait ends.
func (u *FakeUpstream) waitWebSockets(wait time.Duration) {
	deadline := time.After(wait)
	for _, r := range u.Requests() {
		if r.ws == nil {
			continue
		}
		select {
		case <-r.ws.done:
		case <-deadline:
			return
		}
	}
}

func isWebSocketHandshake(r *http.Request) bool {
	return r.Method == http.MethodGet && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
