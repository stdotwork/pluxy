package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests drive core and the Gemini extension over HTTP against a fake Gemini API, for
// Gemini's Interactions API (POST /v1beta/interactions): core serves it as an endpoint call, and
// only an account whose settings say "interactions": true answers it.

const (
	interactionsPath     = "/v1beta/interactions"
	interactionsKey      = "AIza-e2e-interactions-key"
	interactionsKeyOther = "AIza-e2e-interactions-other"
)

// geminiManifest returns the path of the Gemini extension's manifest, which TestMain builds.
func geminiManifest(t *testing.T) string {
	t.Helper()
	return filepath.Join(binDir, "gemini", "pluxy-extension.json")
}

// seenRequest is what a fake Gemini API received.
type seenRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

// fakeGemini is a fake Gemini API that records every request and answers with handle.
type fakeGemini struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newFakeGemini(t *testing.T, handle http.HandlerFunc) *fakeGemini {
	t.Helper()
	f := &fakeGemini{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen = append(f.seen, seenRequest{r.Method, r.URL.RequestURI(), r.Header.Clone(), body})
		f.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handle(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// interactions returns the Interactions requests the fake received.
func (f *fakeGemini) interactions() []seenRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []seenRequest
	for _, s := range f.seen {
		if s.path == interactionsPath {
			out = append(out, s)
		}
	}
	return out
}

// all returns what the fake received, for a failure message.
func (f *fakeGemini) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var parts []string
	for _, s := range f.seen {
		parts = append(parts, s.method+" "+s.path)
	}
	return strings.Join(parts, ", ")
}

// jsonReply answers every request with a fixed JSON body.
func jsonReply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

// interactionsAccount is one Gemini account in core's config.
type interactionsAccount struct {
	id, key string
	baseURL string
	enabled bool // "interactions": true; false leaves the setting out
}

// startInteractionsCore runs core with the Gemini extension and the accounts, tried in order.
func startInteractionsCore(t *testing.T, accounts ...interactionsAccount) string {
	t.Helper()
	var list []any
	for _, a := range accounts {
		settings := map[string]any{"base_url": a.baseURL}
		if a.enabled {
			settings["interactions"] = true
		}
		list = append(list, map[string]any{
			"id":         a.id,
			"settings":   settings,
			"credential": map[string]any{"kind": "api_key", "api_key": a.key},
		})
	}
	return runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"routing":     map[string]any{"strategy": "fill-first"},
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": list,
		},
	})
}

// postInteractions posts body to core's Interactions route with the headers. A client key is sent
// as a bearer token unless headers name Authorization or X-Goog-Api-Key.
func postInteractions(t *testing.T, ctx context.Context, base string, headers map[string]string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+interactionsPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

var bearer = map[string]string{"Authorization": "Bearer " + clientKey}

// bodyFields reads a JSON object request body.
func bodyFields(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("upstream body %q is not a JSON object: %v", raw, err)
	}
	return m
}

// An Interactions request reaches the upstream as POST /v1beta/interactions with the account's key
// and the default API revision, and the upstream's JSON comes back to the client as it was.
func TestInteractionsNonStreaming(t *testing.T) {
	const reply = `{"id":"v1_abc","status":"completed","model":"gemini-2.5-flash","outputs":[{"type":"text","text":"Hello!"}],"usage":{"total_input_tokens":3,"total_output_tokens":2}}`

	t.Run("default revision", func(t *testing.T) {
		up := newFakeGemini(t, jsonReply(reply))
		base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
		resp := postInteractions(t, context.Background(), base, bearer, `{"model":"gemini-2.5-flash","input":"Say hello"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != reply {
			t.Fatalf("status %d: %s; want 200 with the upstream's body unchanged", resp.StatusCode, raw)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		got := up.interactions()
		if len(got) != 1 {
			t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
		}
		r := got[0]
		if r.method != http.MethodPost {
			t.Errorf("upstream method = %s, want POST", r.method)
		}
		if k := r.header.Get("X-Goog-Api-Key"); k != interactionsKey {
			t.Errorf("upstream X-Goog-Api-Key = %q, want the account's key", k)
		}
		if rev := r.header.Get("Api-Revision"); rev != "2026-05-20" {
			t.Errorf("upstream Api-Revision = %q, want 2026-05-20", rev)
		}
		if a := r.header.Get("Authorization"); a != "" {
			t.Errorf("upstream got Authorization %q: the client's key must not travel on", a)
		}
		f := bodyFields(t, r.body)
		if string(f["model"]) != `"gemini-2.5-flash"` || string(f["input"]) != `"Say hello"` {
			t.Errorf("upstream body = %s, want the model and the input", r.body)
		}
	})

	t.Run("client revision", func(t *testing.T) {
		up := newFakeGemini(t, jsonReply(reply))
		base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
		resp := postInteractions(t, context.Background(), base,
			map[string]string{"Authorization": "Bearer " + clientKey, "Api-Revision": "2026-01-15"},
			`{"model":"gemini-2.5-flash","input":"Say hello"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != reply {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		got := up.interactions()
		if len(got) != 1 {
			t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
		}
		if rev := got[0].header.Get("Api-Revision"); rev != "2026-01-15" {
			t.Errorf("upstream Api-Revision = %q, want the client's 2026-01-15", rev)
		}
	})

	t.Run("models prefix", func(t *testing.T) {
		up := newFakeGemini(t, jsonReply(reply))
		base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
		resp := postInteractions(t, context.Background(), base, bearer, `{"model":"models/gemini-2.5-flash","input":"Say hello"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		got := up.interactions()
		if resp.StatusCode != http.StatusOK || len(got) != 1 {
			t.Fatalf("status %d: %s; upstream Interactions requests: %d (%s)", resp.StatusCode, raw, len(got), up.all())
		}
		if m := string(bodyFields(t, got[0].body)["model"]); m != `"gemini-2.5-flash"` {
			t.Errorf("upstream model = %s, want the name without models/", m)
		}
	})
}

// A streamed reply reaches the client event by event, as the upstream writes them: the client reads
// the first event, with its name, before the upstream has sent the second.
func TestInteractionsStreamArrivesIncrementally(t *testing.T) {
	readFirst := make(chan struct{})
	var timedOut atomic.Bool
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		io.WriteString(w, "event: content.delta\ndata: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"Hel\"}}\n\n")
		fl.Flush()
		select {
		case <-readFirst:
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Second):
			timedOut.Store(true)
		}
		io.WriteString(w, "event: content.delta\ndata: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"lo\"}}\n\n")
		fl.Flush()
		io.WriteString(w, "event: interaction.complete\ndata: {\"event_type\":\"interaction.complete\",\"interaction\":{\"usage\":{\"total_input_tokens\":3,\"total_output_tokens\":2}}}\n\n")
		fl.Flush()
	})
	base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := postInteractions(t, ctx, base, bearer, `{"model":"gemini-2.5-flash","input":"Say hello","stream":true}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	rd := bufio.NewReader(resp.Body)
	// readEvent returns the lines of the next event, up to its blank line.
	readEvent := func() string {
		t.Helper()
		var sb strings.Builder
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("stream ended inside an event (read so far %q): %v", sb.String(), err)
			}
			sb.WriteString(line)
			if line == "\n" || line == "\r\n" {
				return sb.String()
			}
		}
	}
	first := readEvent()
	if timedOut.Load() {
		t.Fatal("the client read the first event only after the upstream gave up waiting: the reply is not relayed as it arrives")
	}
	if !strings.HasPrefix(first, "event: content.delta\n") || !strings.Contains(first, `"text":"Hel"`) {
		t.Errorf("first event = %q, want a content.delta event carrying %q", first, "Hel")
	}
	close(readFirst)

	rest, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("reading the rest of the stream: %v", err)
	}
	for _, want := range []string{"event: content.delta\ndata: ", `"text":"lo"`, "event: interaction.complete\ndata: ", `"total_input_tokens":3`} {
		if !strings.Contains(string(rest), want) {
			t.Errorf("rest of the stream = %q, want it to contain %q", rest, want)
		}
	}
	if i, j := strings.Index(string(rest), "event: content.delta"), strings.Index(string(rest), "event: interaction.complete"); i < 0 || j < i {
		t.Errorf("rest of the stream = %q, want content.delta before interaction.complete", rest)
	}

	got := up.interactions()
	if len(got) != 1 {
		t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
	}
	if string(bodyFields(t, got[0].body)["stream"]) != "true" {
		t.Errorf("upstream body = %s, want stream true", got[0].body)
	}
}

// A request that names an agent, and no model, is served by the account that opts in; the body
// goes upstream with the agent and without a model.
func TestInteractionsAgent(t *testing.T) {
	const reply = `{"id":"v1_agent","status":"in_progress"}`
	up := newFakeGemini(t, jsonReply(reply))
	base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
	resp := postInteractions(t, context.Background(), base, bearer, `{"agent":"deep-research-pro-preview-12-2025","input":"x"}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != reply {
		t.Fatalf("status %d: %s; want 200 with the upstream's body", resp.StatusCode, raw)
	}
	got := up.interactions()
	if len(got) != 1 {
		t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
	}
	if k := got[0].header.Get("X-Goog-Api-Key"); k != interactionsKey {
		t.Errorf("upstream X-Goog-Api-Key = %q, want the account's key", k)
	}
	f := bodyFields(t, got[0].body)
	if string(f["agent"]) != `"deep-research-pro-preview-12-2025"` {
		t.Errorf("upstream body = %s, want the agent", got[0].body)
	}
	if _, ok := f["model"]; ok {
		t.Errorf("upstream body = %s, want no model", got[0].body)
	}
}

// An account without "interactions": true does not serve the Interactions API: core tries the next
// account, and fails the request when there is none.
func TestInteractionsAccountWithoutTheSettingIsRefused(t *testing.T) {
	const reply = `{"id":"v1_ok","status":"completed"}`
	bodies := []struct{ name, body string }{
		{"model", `{"model":"gemini-2.5-flash","input":"hi"}`},
		{"agent", `{"agent":"deep-research-pro-preview-12-2025","input":"hi"}`},
	}

	for _, c := range bodies {
		t.Run("only a plain account/"+c.name, func(t *testing.T) {
			up := newFakeGemini(t, jsonReply(reply))
			base := startInteractionsCore(t, interactionsAccount{id: "plain", key: interactionsKey, baseURL: up.URL})
			resp := postInteractions(t, context.Background(), base, bearer, c.body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Errorf("status %d: %s; want a 4xx error", resp.StatusCode, raw)
			}
			if !strings.Contains(string(raw), `"error"`) {
				t.Errorf("body %s, want an error", raw)
			}
			if got := up.interactions(); len(got) != 0 {
				t.Errorf("upstream got %d Interactions requests, want none", len(got))
			}
		})

		t.Run("plain then native account/"+c.name, func(t *testing.T) {
			plain := newFakeGemini(t, jsonReply(`{"id":"from-plain"}`))
			native := newFakeGemini(t, jsonReply(reply))
			base := startInteractionsCore(t,
				interactionsAccount{id: "plain", key: interactionsKey, baseURL: plain.URL},
				interactionsAccount{id: "native", key: interactionsKeyOther, baseURL: native.URL, enabled: true})
			resp := postInteractions(t, context.Background(), base, bearer, c.body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusOK || string(raw) != reply {
				t.Fatalf("status %d: %s; want 200 with the native account's reply", resp.StatusCode, raw)
			}
			if got := plain.interactions(); len(got) != 0 {
				t.Errorf("the plain account's upstream got %d Interactions requests, want none", len(got))
			}
			got := native.interactions()
			if len(got) != 1 {
				t.Fatalf("the native account's upstream got %d Interactions requests (%s), want 1", len(got), native.all())
			}
			if k := got[0].header.Get("X-Goog-Api-Key"); k != interactionsKeyOther {
				t.Errorf("upstream X-Goog-Api-Key = %q, want the native account's key", k)
			}
		})
	}
}

// An upstream that drops the connection after one event leaves the client the event it got and a
// final error event; the reply had started, so its status stays 200.
func TestInteractionsStreamBreaksMidway(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: content.delta\ndata: {\"event_type\":\"content.delta\",\"delta\":{\"type\":\"text\",\"text\":\"Hel\"}}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond) // let the event leave before the connection drops
		panic(http.ErrAbortHandler)
	})
	base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp := postInteractions(t, ctx, base, bearer, `{"model":"gemini-2.5-flash","input":"hi","stream":true}`)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the client's stream: %v (read so far %q)", err, raw)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s; want 200", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body := string(raw)
	first := "event: content.delta\ndata: "
	if !strings.HasPrefix(body, first) || !strings.Contains(body, `"text":"Hel"`) {
		t.Fatalf("stream = %q, want it to start with the first event", body)
	}
	i := strings.Index(body, "\n\nevent: error\ndata: ")
	if i < 0 {
		t.Fatalf("stream = %q, want a final event: error after the first event", body)
	}
	last := body[i+len("\n\n"):]
	if !strings.HasSuffix(last, "\n\n") || strings.Count(last, "event: ") != 1 {
		t.Errorf("tail = %q, want exactly one event: error frame ending the stream", last)
	}
	data := strings.TrimSuffix(strings.TrimPrefix(last, "event: error\ndata: "), "\n\n")
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &e); err != nil || e.Error.Message == "" {
		t.Errorf("error frame data = %q, want a JSON error with a message (%v)", data, err)
	}
	if got := up.interactions(); len(got) != 1 {
		t.Errorf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
	}
}

// A body that names both a model and an agent, or neither, is refused with 400 before any upstream
// call; a client may send its key as x-goog-api-key, as Google's clients do.
func TestInteractionsValidation(t *testing.T) {
	const want = "request requires exactly one of model or agent"
	for _, c := range []struct{ name, body string }{
		{"model and agent", `{"model":"gemini-2.5-flash","agent":"deep-research-pro-preview-12-2025","input":"x"}`},
		{"neither", `{"input":"x"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			up := newFakeGemini(t, jsonReply(`{"id":"v1"}`))
			base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
			resp := postInteractions(t, context.Background(), base, bearer, c.body)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(raw, &e) != nil || e.Error.Message != want {
				t.Errorf("status %d: %s; want 400 with message %q", resp.StatusCode, raw, want)
			}
			if got := up.interactions(); len(got) != 0 {
				t.Errorf("upstream got %d Interactions requests, want none", len(got))
			}
		})
	}

	t.Run("x-goog-api-key client key", func(t *testing.T) {
		const reply = `{"id":"v1_ok","status":"completed"}`
		up := newFakeGemini(t, jsonReply(reply))
		base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
		resp := postInteractions(t, context.Background(), base, map[string]string{"X-Goog-Api-Key": clientKey},
			`{"model":"gemini-2.5-flash","input":"hi"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || string(raw) != reply {
			t.Fatalf("status %d: %s; want 200 with the upstream's body", resp.StatusCode, raw)
		}
		got := up.interactions()
		if len(got) != 1 {
			t.Fatalf("upstream got %d Interactions requests (%s), want 1", len(got), up.all())
		}
		if k := got[0].header.Get("X-Goog-Api-Key"); k != interactionsKey {
			t.Errorf("upstream X-Goog-Api-Key = %q, want the account's key, not the client's", k)
		}
	})

	t.Run("wrong client key", func(t *testing.T) {
		up := newFakeGemini(t, jsonReply(`{"id":"v1"}`))
		base := startInteractionsCore(t, interactionsAccount{id: "g1", key: interactionsKey, baseURL: up.URL, enabled: true})
		resp := postInteractions(t, context.Background(), base, map[string]string{"X-Goog-Api-Key": "not-the-client-key"},
			`{"model":"gemini-2.5-flash","input":"hi"}`)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status %d: %s; want 401", resp.StatusCode, raw)
		}
		if got := up.interactions(); len(got) != 0 {
			t.Errorf("upstream got %d Interactions requests, want none", len(got))
		}
	})
}

// The usage an Interactions reply reports is recorded: a whole reply's usage, and the usage on the
// event that completes a stream.
func TestInteractionsUsageRecorded(t *testing.T) {
	up := newFakeGemini(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: content.delta\ndata: {\"event_type\":\"content.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"Hi\"}}\n\n"+
				"event: interaction.complete\ndata: {\"event_type\":\"interaction.complete\",\"interaction\":{\"status\":\"completed\",\"usage\":{\"total_input_tokens\":3,\"total_output_tokens\":2}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"int-1","status":"completed","usage":{"total_input_tokens":12,"total_output_tokens":4,"total_cached_tokens":5}}`)
	})
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": geminiManifest(t),
			"accounts": []any{map[string]any{
				"id":         "interactions",
				"settings":   map[string]any{"base_url": up.URL, "interactions": true},
				"credential": map[string]any{"kind": "api_key", "api_key": interactionsKey},
			}},
		},
	})
	for _, body := range []string{`{"model":"gemini-2.5-flash","input":"x"}`, `{"model":"gemini-2.5-flash","input":"x","stream":true}`} {
		resp := postInteractions(t, context.Background(), base, map[string]string{"Authorization": "Bearer " + clientKey}, body)
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d", body, resp.StatusCode)
		}
	}
	var page usagePage
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, raw := mcall(t, base, managementKey, http.MethodGet, "/usage/events", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /usage/events: status %d: %s", code, raw)
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Events) == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(page.Events) != 2 {
		t.Fatalf("usage events: %+v, want 2", page.Events)
	}
	whole, streamed := page.Events[0].Tokens, page.Events[1].Tokens
	if !i64Is(whole.Input, 12) || !i64Is(whole.Output, 4) || !i64Is(whole.Cached, 5) {
		t.Errorf("whole reply's tokens %+v, want 12 in, 4 out, 5 cached", whole)
	}
	if !page.Events[1].Stream || !i64Is(streamed.Input, 3) || !i64Is(streamed.Output, 2) {
		t.Errorf("stream's event %+v, want a stream with 3 in, 2 out", page.Events[1])
	}
}
