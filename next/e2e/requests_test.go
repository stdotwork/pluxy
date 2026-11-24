package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// runningRequest is a request as GET /requests lists it.
type runningRequest struct {
	ID            string    `json:"id"`
	StartedAt     time.Time `json:"started_at"`
	Endpoint      string    `json:"endpoint"`
	Format        string    `json:"format"`
	Stream        bool      `json:"stream"`
	Model         string    `json:"model"`
	KeyID         string    `json:"key_id"`
	ClientIP      string    `json:"client_ip"`
	Account       string    `json:"account"`
	Provider      string    `json:"provider"`
	UpstreamModel string    `json:"upstream_model"`
	Attempts      int       `json:"attempts"`
}

// watchedGate is a gate that also counts the "gated" calls that left it before it opened: an
// extension whose call was cancelled gives up its request to the gate.
type watchedGate struct {
	*gate
	abandoned atomic.Int64
}

func newWatchedGate(t *testing.T) *watchedGate {
	w := &watchedGate{gate: &gate{release: make(chan struct{})}}
	w.Server = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-w.release:
		case <-r.Context().Done():
			w.abandoned.Add(1)
		}
	}))
	t.Cleanup(func() { w.open(); w.Close() })
	return w
}

// requestsCore is a core with testext, whose "gated" model holds a stream open.
type requestsCore struct {
	mcore
	gate *watchedGate
	log  *coreLog
}

func startRequestsCore(t *testing.T) *requestsCore {
	t.Helper()
	g := newWatchedGate(t)
	base, path, log := runCoreArgs(t, lifecycleConfig(testPackage(t, "0.1.0"), g.gate, nil), noWatch...)
	return &requestsCore{mcore: mcore{base: base, path: path}, gate: g, log: log}
}

func (c *requestsCore) list(t *testing.T) []runningRequest {
	t.Helper()
	var reply struct {
		Requests []runningRequest `json:"requests"`
	}
	b := c.expect(t, http.StatusOK, http.MethodGet, "/requests", nil)
	if err := json.Unmarshal(b, &reply); err != nil || reply.Requests == nil {
		t.Fatalf("GET /requests: %s (error %v)", b, err)
	}
	return reply.Requests
}

// waitListed returns the list once it holds n requests.
func (c *requestsCore) waitListed(t *testing.T, n int) []runningRequest {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		l := c.list(t)
		if len(l) == n {
			return l
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /requests lists %d requests, want %d: %+v", len(l), n, l)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// heldStream is a streamed request for testext's "gated" model, read up to its first text: the
// extension then waits at the gate.
type heldStream struct {
	trace string
	body  *bufio.Reader
}

func (c *requestsCore) hold(t *testing.T) heldStream {
	t.Helper()
	resp := post(t, context.Background(), c.base, clientKey, `{"model":"gated","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	s := heldStream{trace: resp.Header.Get("X-CPA-TRACE-ID"), body: bufio.NewReader(resp.Body)}
	for {
		var ch chunk
		if json.Unmarshal([]byte(readData(t, s.body)), &ch) == nil && len(ch.Choices) > 0 && ch.Choices[0].Delta.Content != "" {
			return s
		}
	}
}

func (c *requestsCore) lastUsageEvent(t *testing.T, trace string) usageEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p, raw := c.usageEvents(t, "")
		for _, e := range p.Events {
			if e.ID == trace {
				return e
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the usage record has no event for %s: %s", trace, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The management API lists the requests core is serving and cancels one: a stream the test
// extension holds open is listed with what the other calls show of it, a cancel ends it for the
// client and for the extension, and requests that end leave the list.
func TestRunningRequests(t *testing.T) {
	c := startRequestsCore(t)

	t.Run("the calls need the management key", func(t *testing.T) {
		for _, call := range [][2]string{{http.MethodGet, "/requests"}, {http.MethodPost, "/requests/20260101000000-0000000000000000/cancel"}} {
			for _, key := range []string{"", "not-the-key", clientKey} {
				code, b := mcall(t, c.base, key, call[0], call[1], nil)
				if code != http.StatusUnauthorized || !strings.Contains(string(b), `"unauthorized"`) {
					t.Errorf("%s %s with key %q: status %d, body %s; want 401 unauthorized", call[0], call[1], key, code, b)
				}
			}
		}
	})

	t.Run("nothing is listed while nothing runs", func(t *testing.T) {
		if b := c.expect(t, http.StatusOK, http.MethodGet, "/requests", nil); strings.TrimSpace(string(b)) != `{"requests":[]}` {
			t.Errorf("GET /requests = %s, want an empty list", b)
		}
	})

	t.Run("cancelling a trace nobody is serving is not found", func(t *testing.T) {
		b := c.expect(t, http.StatusNotFound, http.MethodPost, "/requests/20260101000000-0000000000000000/cancel", nil)
		var e struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if err := json.Unmarshal(b, &e); err != nil || e.Error.Code != "not_found" || e.Error.Message == "" {
			t.Errorf("body %s, want the management error shape with code not_found", b)
		}
	})

	t.Run("a held stream is listed, then cancelled", func(t *testing.T) {
		s := c.hold(t)
		l := c.waitListed(t, 1)
		got := l[0]
		if got.StartedAt.IsZero() || time.Since(got.StartedAt) > time.Minute || time.Since(got.StartedAt) < 0 {
			t.Errorf("started_at = %v, want a moment ago", got.StartedAt)
		}
		got.StartedAt = time.Time{}

		// The client key and the account are named as GET /client-keys and GET /accounts name them.
		var keys struct {
			Keys []struct{ ID, Masked string } `json:"keys"`
		}
		if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/client-keys", nil), &keys); err != nil || len(keys.Keys) != 1 {
			t.Fatalf("GET /client-keys: %+v (error %v)", keys, err)
		}
		var accounts struct {
			Accounts []struct {
				ID             string `json:"id"`
				ActiveRequests int    `json:"active_requests"`
			} `json:"accounts"`
		}
		if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/accounts", nil), &accounts); err != nil || len(accounts.Accounts) != 1 {
			t.Fatalf("GET /accounts: %+v (error %v)", accounts, err)
		}
		want := runningRequest{ID: s.trace, Endpoint: "/v1/chat/completions", Format: "openai.chat", Stream: true, Model: "gated",
			KeyID: keys.Keys[0].ID, ClientIP: "127.0.0.1", Account: accounts.Accounts[0].ID, Provider: "test/lifecycle", UpstreamModel: "gated", Attempts: 1}
		if got != want || s.trace == "" || want.KeyID == "" || want.Account != "acct" {
			t.Errorf("listed as\n %+v\nwant\n %+v", got, want)
		}
		if raw := c.expect(t, http.StatusOK, http.MethodGet, "/requests", nil); strings.Contains(string(raw), clientKey) {
			t.Errorf("GET /requests holds the client key: %s", raw)
		}
		if accounts.Accounts[0].ActiveRequests != 1 || status(t, c.base).current().ActiveRequests != 1 {
			t.Errorf("before the cancel the account and the extension do not each count one request")
		}

		var cancelled struct {
			Request runningRequest `json:"request"`
		}
		b := c.expect(t, http.StatusOK, http.MethodPost, "/requests/"+s.trace+"/cancel", nil)
		if err := json.Unmarshal(b, &cancelled); err != nil || cancelled.Request.ID != s.trace || cancelled.Request.Model != "gated" {
			t.Errorf("the cancel answered %s, want the request it cancelled", b)
		}

		// The client's stream ends with the cancelled error, without the rest of the reply.
		end := rest(s.body)
		if !strings.Contains(end, "the request was cancelled") || strings.Contains(end, " done") || strings.Contains(end, "[DONE]") {
			t.Errorf("after the cancel the client's stream held %q, want the cancelled error and nothing of the reply", end)
		}
		// The extension's call stopped: it gave up its request to the gate, which never opened.
		deadline := time.Now().Add(10 * time.Second)
		for c.gate.abandoned.Load() != 1 || status(t, c.base).current().ActiveRequests != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("the extension's call did not stop: %d calls left the gate, the extension counts %d requests",
					c.gate.abandoned.Load(), status(t, c.base).current().ActiveRequests)
			}
			time.Sleep(10 * time.Millisecond)
		}
		c.waitListed(t, 0)
		if e := c.lastUsageEvent(t, s.trace); e.ErrorCode != "cancelled" || !e.Failed || e.Status != http.StatusOK || e.Model != "gated" || e.Account != "acct" {
			t.Errorf("usage has %+v, want the request failed as cancelled with status 200", e)
		}
		c.log.waitFor(t, "request "+s.trace+" cancelled by an operator")
		// It has ended: a second cancel finds nothing to cancel.
		c.expect(t, http.StatusNotFound, http.MethodPost, "/requests/"+s.trace+"/cancel", nil)
	})

	t.Run("pluxy requests shows a held stream and pluxy request cancel ends it", func(t *testing.T) {
		s := c.hold(t)
		c.waitListed(t, 1)
		target := []string{"--url", c.base, "--key", managementKey}
		r := runCLI(t, nil, append([]string{"requests"}, target...)...)
		lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
		if r.code != 0 || len(lines) != 2 || strings.Join(strings.Fields(lines[0]), " ") != "AGE MODEL ACCOUNT KEY ENDPOINT TRACE" {
			t.Fatalf("pluxy requests: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
		}
		if f := strings.Fields(lines[1]); len(f) != 6 || f[1] != "gated" || f[2] != "acct" || f[4] != "/v1/chat/completions" || f[5] != s.trace {
			t.Errorf("pluxy requests row = %q, want the held request %s", lines[1], s.trace)
		}
		var listed struct {
			Requests []runningRequest `json:"requests"`
		}
		r = runCLI(t, nil, append([]string{"requests", "--json"}, target...)...)
		if err := json.Unmarshal([]byte(r.stdout), &listed); r.code != 0 || err != nil || len(listed.Requests) != 1 || listed.Requests[0].ID != s.trace {
			t.Errorf("pluxy requests --json: exit %d, stdout %q (error %v)", r.code, r.stdout, err)
		}
		r = runCLI(t, nil, append([]string{"request", "cancel", s.trace}, target...)...)
		if r.code != 0 || strings.TrimSpace(r.stdout) != "request "+s.trace+" cancelled" {
			t.Errorf("pluxy request cancel: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
		}
		if end := rest(s.body); !strings.Contains(end, "the request was cancelled") {
			t.Errorf("after pluxy request cancel the client's stream held %q", end)
		}
		c.waitListed(t, 0)
		// Not found is exit code 5.
		r = runCLI(t, nil, append([]string{"request", "cancel", s.trace}, target...)...)
		if r.code != 5 || r.stdout != "" || !strings.Contains(r.stderr, "not_found") {
			t.Errorf("pluxy request cancel of an ended request: exit %d, stdout %q, stderr %q; want exit 5", r.code, r.stdout, r.stderr)
		}
	})

	t.Run("two streams are both listed, oldest first, and leave when they end", func(t *testing.T) {
		first := c.hold(t)
		c.waitListed(t, 1)
		second := c.hold(t)
		l := c.waitListed(t, 2)
		if l[0].ID != first.trace || l[1].ID != second.trace || l[0].StartedAt.After(l[1].StartedAt) {
			t.Errorf("listed %s then %s, want %s then %s", l[0].ID, l[1].ID, first.trace, second.trace)
		}
		c.gate.open()
		for _, s := range []heldStream{first, second} {
			if end := rest(s.body); !strings.Contains(end, " done") || !strings.Contains(end, "[DONE]") {
				t.Errorf("the released stream %s ended with %q, want the whole reply", s.trace, end)
			}
			if e := c.lastUsageEvent(t, s.trace); e.Failed || e.ErrorCode != "" {
				t.Errorf("usage has %+v for a request that ended normally", e)
			}
		}
		c.waitListed(t, 0)
	})
}

// Many requests start, end and are cancelled at once while the list is read: each client gets an
// answer, and afterwards core lists none and counts none on the account or the extension.
func TestRunningRequestsUnderLoad(t *testing.T) {
	c := startRequestsCore(t)
	const n = 48
	stop := make(chan struct{})
	var cancels atomic.Int64
	var canceller sync.WaitGroup
	canceller.Add(1)
	go func() { // cancels whatever is listed with an account, until told to stop
		defer canceller.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			code, b := mcall(t, c.base, managementKey, http.MethodGet, "/requests", nil)
			var reply struct {
				Requests []runningRequest `json:"requests"`
			}
			if code != http.StatusOK || json.Unmarshal(b, &reply) != nil {
				t.Errorf("GET /requests: %d %s", code, b)
				return
			}
			for _, r := range reply.Requests {
				if r.Model != "silent" && r.Model != "gated" {
					continue // the quick ones end by themselves
				}
				switch code, b := mcall(t, c.base, managementKey, http.MethodPost, "/requests/"+r.ID+"/cancel", nil); code {
				case http.StatusOK:
					cancels.Add(1)
				case http.StatusNotFound: // it ended first
				default:
					t.Errorf("cancel of %s: %d %s", r.ID, code, b)
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var clients sync.WaitGroup
	var answered, cancelled atomic.Int64
	for i := range n {
		clients.Add(1)
		go func() {
			defer clients.Done()
			// A third answer at once, a third wait before their reply starts, a third after it started.
			model := []string{"quick", "silent", "gated"}[i%3]
			stream := "true"
			if model == "quick" && i%2 == 0 {
				stream = "false"
			}
			resp, err := streamRequest(context.Background(), c.base, "/v1/chat/completions", `{"model":"`+model+`","stream":`+stream+`,"messages":[{"role":"user","content":"hi"}]}`)
			if err != nil {
				t.Errorf("a %s request: %v", model, err)
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			switch {
			case strings.Contains(string(b), "the request was cancelled"):
				cancelled.Add(1)
			case model == "quick" && resp.StatusCode == http.StatusOK:
				answered.Add(1)
			default:
				t.Errorf("a %s request ended with status %d: %s", model, resp.StatusCode, b)
			}
		}()
	}
	clients.Wait()
	close(stop)
	canceller.Wait()
	if answered.Load() != n/3 || cancelled.Load() != n-n/3 {
		t.Errorf("%d requests were answered and %d cancelled, want %d and %d", answered.Load(), cancelled.Load(), n/3, n-n/3)
	}
	if cancels.Load() < int64(n-n/3) {
		t.Errorf("%d cancels succeeded, fewer than the %d requests that only a cancel ends", cancels.Load(), n-n/3)
	}
	c.waitListed(t, 0)
	deadline := time.Now().Add(10 * time.Second)
	for status(t, c.base).current().ActiveRequests != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the extension still counts %d requests", status(t, c.base).current().ActiveRequests)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
