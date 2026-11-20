package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func msPtr(v int64) *int64 { return &v }

type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureTimeoutLogs(t *testing.T) *syncLogBuffer {
	t.Helper()
	b := &syncLogBuffer{}
	timeoutLogMu.Lock()
	old := timeoutLog
	timeoutLog = b
	timeoutLogMu.Unlock()
	t.Cleanup(func() {
		timeoutLogMu.Lock()
		timeoutLog = old
		timeoutLogMu.Unlock()
	})
	return b
}

func callCtx(accountID string, stream bool, timeouts *protocol.Timeouts) context.Context {
	ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{
		Op:      protocol.OpGenerate,
		Account: &protocol.Account{ID: accountID, Timeouts: timeouts},
		Request: &llm.Request{Model: "m", Stream: stream},
	})
	return ctx
}

func TestTimeoutHeaderLimit(t *testing.T) {
	logs := captureTimeoutLogs(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	c := &http.Client{Transport: Upstream(&http.Transport{})}
	req, err := http.NewRequestWithContext(callCtx("acct-hdr", true, &protocol.Timeouts{HeaderMS: msPtr(80), IdleMS: msPtr(500)}), http.MethodPost, srv.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected header timeout error")
	}
	if !Retryable(err) {
		t.Errorf("Retryable(%v) = false, want true for pre-header timeout", err)
	}
	if got := logs.String(); !strings.Contains(got, "header") || !strings.Contains(got, "acct-hdr") || !strings.Contains(got, "80ms") {
		t.Errorf("log = %q, want header, acct-hdr, and 80ms", got)
	}

	// 0 turns the header limit off.
	srvFast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok")
	}))
	defer srvFast.Close()
	req2, _ := http.NewRequestWithContext(callCtx("acct-hdr-off", true, &protocol.Timeouts{HeaderMS: msPtr(0), IdleMS: msPtr(500)}), http.MethodPost, srvFast.URL, strings.NewReader("{}"))
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatalf("expected header_ms=0 to wait past 100ms: %v", err)
	}
	resp2.Body.Close()
}

func TestTimeoutIdleLimit(t *testing.T) {
	logs := captureTimeoutLogs(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	c := &http.Client{Transport: Upstream(&http.Transport{})}
	req, _ := http.NewRequestWithContext(callCtx("acct-idle", true, &protocol.Timeouts{HeaderMS: msPtr(1000), IdleMS: msPtr(80)}), http.MethodPost, srv.URL, strings.NewReader("{}"))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if err == nil {
		t.Fatal("expected idle timeout reading stalled stream")
	}
	if Retryable(err) {
		t.Errorf("Retryable(%v) = true, want false after headers arrived", err)
	}
	if got := logs.String(); !strings.Contains(got, "idle") || !strings.Contains(got, "acct-idle") || !strings.Contains(got, "80ms") {
		t.Errorf("log = %q, want idle, acct-idle, and 80ms", got)
	}

	// Stream with gaps shorter than idle_ms completes even when total duration exceeds both
	// idle_ms and total_ms.
	srvHealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 5 {
			if i > 0 {
				time.Sleep(45 * time.Millisecond)
			}
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
		}
	}))
	defer srvHealthy.Close()
	req2, _ := http.NewRequestWithContext(callCtx("acct-healthy", true, &protocol.Timeouts{HeaderMS: msPtr(200), IdleMS: msPtr(120), TotalMS: msPtr(50)}), http.MethodPost, srvHealthy.URL, strings.NewReader("{}"))
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatalf("healthy stream Do failed: %v", err)
	}
	defer resp2.Body.Close()
	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("healthy stream read failed: %v", err)
	}
	if !strings.Contains(string(body), "data: 4") {
		t.Errorf("body = %q, want data: 4", body)
	}
}

func TestTimeoutTotalLimit(t *testing.T) {
	logs := captureTimeoutLogs(t)
	release := make(chan struct{})

	// 1. Pre-header total timeout on non-streamed request is retryable.
	srvNoHeader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srvNoHeader.Close()

	c := &http.Client{Transport: Upstream(&http.Transport{})}
	req1, _ := http.NewRequestWithContext(callCtx("acct-tot-pre", false, &protocol.Timeouts{HeaderMS: msPtr(20), TotalMS: msPtr(80)}), http.MethodPost, srvNoHeader.URL, strings.NewReader("{}"))
	resp1, err := c.Do(req1)
	if err == nil {
		resp1.Body.Close()
		t.Fatal("expected pre-header total timeout")
	}
	if !Retryable(err) {
		t.Errorf("Retryable(%v) = false, want true before headers", err)
	}
	if got := logs.String(); !strings.Contains(got, "total") || !strings.Contains(got, "acct-tot-pre") || !strings.Contains(got, "80ms") {
		t.Errorf("log = %q, want total, acct-tot-pre, and 80ms", got)
	}

	// 2. Post-header total timeout on non-streamed request is NOT retryable.
	srvSlowBody := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srvSlowBody.Close()
	defer close(release)
	req2, _ := http.NewRequestWithContext(callCtx("acct-tot-post", false, &protocol.Timeouts{HeaderMS: msPtr(20), TotalMS: msPtr(80)}), http.MethodPost, srvSlowBody.URL, strings.NewReader("{}"))
	resp2, err := c.Do(req2)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	_, err = io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if err == nil {
		t.Fatal("expected post-header total timeout reading body")
	}
	if Retryable(err) {
		t.Errorf("Retryable(%v) = true, want false after headers", err)
	}
	if got := logs.String(); !strings.Contains(got, "acct-tot-post") {
		t.Errorf("log = %q, want acct-tot-post", got)
	}

	// 3. Non-streamed request slower than header_ms succeeds within total_ms; total_ms=0 turns it off.
	srvSlowOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		io.WriteString(w, "ok")
	}))
	defer srvSlowOK.Close()
	req3, _ := http.NewRequestWithContext(callCtx("acct-tot-ok", false, &protocol.Timeouts{HeaderMS: msPtr(20), TotalMS: msPtr(250)}), http.MethodPost, srvSlowOK.URL, strings.NewReader("{}"))
	resp3, err := c.Do(req3)
	if err != nil {
		t.Fatalf("slow non-streamed Do failed: %v", err)
	}
	b3, err := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if err != nil || string(b3) != "ok" {
		t.Fatalf("got (%q, %v), want (\"ok\", nil)", b3, err)
	}
	req4, _ := http.NewRequestWithContext(callCtx("acct-tot-off", false, &protocol.Timeouts{HeaderMS: msPtr(20), TotalMS: msPtr(0)}), http.MethodPost, srvSlowOK.URL, strings.NewReader("{}"))
	resp4, err := c.Do(req4)
	if err != nil {
		t.Fatalf("total_ms=0 Do failed: %v", err)
	}
	resp4.Body.Close()
}

func TestTimeoutDefaultsAndHelpers(t *testing.T) {
	ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{
		Op:      protocol.OpGenerate,
		Account: &protocol.Account{ID: "a"},
		Request: &llm.Request{Model: "m", Stream: true},
	})
	info, _ := UpstreamInfo(ctx)
	h, i, tot := resolvedTimeouts(info)
	if h != DefaultHeaderTimeout || i != DefaultIdleTimeout || tot != DefaultTotalTimeout {
		t.Errorf("defaults = (%v, %v, %v), want (%v, %v, %v)", h, i, tot, DefaultHeaderTimeout, DefaultIdleTimeout, DefaultTotalTimeout)
	}
	if got := IdleTimeout(context.Background(), 42*time.Second); got != 42*time.Second {
		t.Errorf("IdleTimeout without info = %v, want 42s", got)
	}
	if got := IdleTimeout(ctx, 42*time.Second); got != DefaultIdleTimeout {
		t.Errorf("IdleTimeout with info = %v, want %v", got, DefaultIdleTimeout)
	}
	ctxZero, _ := withUpstreamInfo(context.Background(), protocol.Call{
		Type:    protocol.TypeSessionTurn,
		Account: &protocol.Account{ID: "a", Timeouts: &protocol.Timeouts{IdleMS: msPtr(0)}},
	})
	if got := IdleTimeout(ctxZero, 42*time.Second); got != 0 {
		t.Errorf("IdleTimeout with idle_ms=0 = %v, want 0", got)
	}
}

func TestTimeoutParentCancelNotLogged(t *testing.T) {
	logs := captureTimeoutLogs(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)

	baseCtx, cancel := context.WithCancel(callCtx("acct-cancel", true, &protocol.Timeouts{HeaderMS: msPtr(200)}))
	time.AfterFunc(40*time.Millisecond, cancel)
	c := &http.Client{Transport: Upstream(&http.Transport{})}
	req, _ := http.NewRequestWithContext(baseCtx, http.MethodPost, srv.URL, strings.NewReader("{}"))
	resp, err := c.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if Retryable(err) {
		t.Errorf("Retryable(%v) = true, want false on parent cancellation", err)
	}
	if got := logs.String(); got != "" {
		t.Errorf("expected no timeout log on parent cancel, got %q", got)
	}
}

type fakeRWCRoundTripper struct{}

type fakeRWC struct {
	io.Reader
}

func (fakeRWC) Write(p []byte) (int, error) { return len(p), nil }
func (fakeRWC) Close() error                { return nil }

func (fakeRWCRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Body:       fakeRWC{strings.NewReader("")},
	}, nil
}

func TestTimeoutWebSocketUpgradePreservesRWC(t *testing.T) {
	ctx, _ := withUpstreamInfo(context.Background(), protocol.Call{
		Op:      protocol.OpSession,
		Account: &protocol.Account{ID: "acct-ws", Timeouts: &protocol.Timeouts{HeaderMS: msPtr(500)}},
	})
	rt := timeoutTransport{next: fakeRWCRoundTripper{}, info: func() Info { i, _ := UpstreamInfo(ctx); return i }()}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/v1/responses", nil)
	req.Header.Set("Upgrade", "websocket")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, ok := resp.Body.(io.ReadWriteCloser); !ok {
		t.Fatalf("101 resp.Body does not implement io.ReadWriteCloser (%T)", resp.Body)
	}
}

// Core finds an upstream timeout in an extension's failure by the phrase in the message (the
// protocol has no field for it), so every kind of timeoutError must carry it.
func TestTimeoutMessagesCarryThePhrase(t *testing.T) {
	for _, kind := range []string{timeoutHeader, timeoutIdle, timeoutTotal} {
		err := fmt.Errorf("upstream unreachable: %w", &timeoutError{kind: kind, limit: time.Minute})
		e := &llm.Error{Code: llm.CodeUnavailable, Message: err.Error()}
		if !e.IsTimeout() {
			t.Errorf("%s: %q is not recognised as a timeout", kind, e.Message)
		}
	}
}
