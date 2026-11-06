package corpus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
)

// ClientKey is the key the corpus client sends; both cores are configured to accept it.
const ClientKey = "corpus-client-key"

// UpstreamKey is the API key both cores send to the fake upstream. Not a secret.
const UpstreamKey = "sk-corpus-upstream"

// UpstreamKeys returns the keys of n accounts: UpstreamKey for the first, then
// UpstreamKey-2, UpstreamKey-3 and so on. Not secrets.
func UpstreamKeys(n int) []string {
	keys := []string{UpstreamKey}
	for i := 2; i <= n; i++ {
		keys = append(keys, fmt.Sprintf("%s-%d", UpstreamKey, i))
	}
	return keys
}

// ClientUserAgent is the User-Agent the corpus client sends unless a case sets one.
const ClientUserAgent = "pluxy-corpus/0"

// Headers left out of observations: they describe the connection, not the behaviour, or
// they change with the port (Host).
var skipHeaders = map[string]bool{
	"Connection":     true,
	"Content-Length": true,
	"Date":           true,
	"Host":           true,
	"Keep-Alive":     true,
	// The WebSocket handshake's nonce and its answer change on every run.
	"Sec-Websocket-Key":    true,
	"Sec-Websocket-Accept": true,
	"Transfer-Encoding":    true,
}

// client sends requests without asking for compression, so replies are observed as sent.
var client = &http.Client{Transport: &http.Transport{DisableCompression: true}}

// Observation is what one run of a case shows: the client's reply and every request the fake
// upstream received. It is plain JSON data so it can be recorded and compared path by path.
type Observation map[string]any

// Run sends the case's client request to a core at base and returns the observation. The
// fake upstream must be the one the core points at.
func Run(ctx context.Context, c *Case, base string, up *FakeUpstream) (Observation, error) {
	var reply map[string]any
	if len(c.Client.WebSocket) > 0 {
		var err error
		if reply, err = runClientWebSocket(ctx, c, base); err != nil {
			return nil, err
		}
		up.waitWebSockets(closeWait)
	} else {
		req, err := buildRequest(ctx, c, base)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		reply = observeReply(resp)
		resp.Body.Close()
	}

	upstream := []any{}
	for _, r := range up.Requests() {
		o := map[string]any{
			"method":  r.Method,
			"path":    r.Path,
			"headers": headerMap(r.Header),
			"body":    bodyValue(r.Body),
		}
		if r.ws != nil {
			delete(o, "body")
			o["websocket"] = r.ws.observation()
		}
		upstream = append(upstream, o)
	}
	return normalize(Observation{"client": reply, "upstream": upstream})
}

func buildRequest(ctx context.Context, c *Case, base string) (*http.Request, error) {
	method := c.Client.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	switch {
	case c.Client.BodyText != nil:
		body = strings.NewReader(*c.Client.BodyText)
	case len(c.Client.Body) > 0:
		var buf bytes.Buffer
		if err := json.Compact(&buf, c.Client.Body); err != nil {
			return nil, err
		}
		body = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, base+c.Client.Path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", ClientUserAgent)
	switch c.Client.Auth {
	case "", "client-key":
		req.Header.Set("Authorization", "Bearer "+ClientKey)
	case "none":
	default:
		req.Header.Set("Authorization", "Bearer "+c.Client.Auth)
	}
	for k, v := range c.Client.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func observeReply(resp *http.Response) map[string]any {
	out := map[string]any{
		"status":  resp.StatusCode,
		"headers": headerMap(resp.Header),
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt == "text/event-stream" {
		events, err := readSSE(resp.Body)
		out["events"] = events
		if err != nil {
			out["body_error"] = bodyError(err)
		}
		return out
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		out["body_error"] = bodyError(err)
	}
	if len(b) > 0 {
		out["body"] = bodyValue(b)
	}
	return out
}

func bodyError(err error) string {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected EOF"
	}
	return err.Error()
}

// readSSE parses a server-sent event stream into a list of events. Each event is an object
// with "data" (parsed as JSON when it is JSON, else the text), and "event", "id" or
// "comment" when present. Its own parser, not the new core's, so the corpus does not judge the
// new core with the new core's code.
func readSSE(r io.Reader) ([]any, error) {
	events := []any{}
	br := bufio.NewReader(r)
	cur := map[string]any{}
	var data []string
	var comments []string
	flush := func() {
		if len(comments) > 0 {
			cur["comment"] = strings.Join(comments, "\n")
		}
		if data != nil {
			cur["data"] = textOrJSON(strings.Join(data, "\n"))
		}
		if len(cur) > 0 {
			events = append(events, cur)
		}
		cur, data, comments = map[string]any{}, nil, nil
	}
	for {
		line, err := br.ReadString('\n')
		if line != "" || err == nil {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			switch {
			case line == "":
				if err == nil {
					flush()
				}
			case strings.HasPrefix(line, ":"):
				comments = append(comments, strings.TrimPrefix(strings.TrimPrefix(line, ":"), " "))
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "data":
					data = append(data, value)
				case "event", "id", "retry":
					cur[field] = value
				default:
					cur["unknown_field"] = line
				}
			}
		}
		if err != nil {
			// An event not ended by a blank line is still shown, marked as unterminated.
			if len(cur) > 0 || data != nil || comments != nil {
				cur["unterminated"] = true
				flush()
			}
			if err == io.EOF {
				return events, nil
			}
			return events, err
		}
	}
}

func textOrJSON(s string) any {
	if !json.Valid([]byte(s)) {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return s
	}
	return v
}

func bodyValue(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return textOrJSON(string(b))
	}
	return string(b)
}

func headerMap(h http.Header) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ck := http.CanonicalHeaderKey(k)
		if skipHeaders[ck] {
			continue
		}
		out[ck] = strings.Join(h[k], ", ")
	}
	return out
}

// normalize turns an observation into plain JSON values (maps, slices, strings, json.Number,
// bools, nil), the same shape a recorded observation has after loading.
func normalize(o Observation) (Observation, error) {
	b, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	return decodeObservation(b)
}

func decodeObservation(b []byte) (Observation, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var o Observation
	if err := dec.Decode(&o); err != nil {
		return nil, err
	}
	return o, nil
}
