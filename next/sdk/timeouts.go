package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// Default upstream timeout limits.
const (
	DefaultHeaderTimeout = 60 * time.Second
	DefaultIdleTimeout   = 5 * time.Minute
	DefaultTotalTimeout  = 10 * time.Minute
)

const (
	timeoutHeader = "header"
	timeoutIdle   = "idle"
	timeoutTotal  = "total"
)

var (
	timeoutLogMu sync.Mutex
	timeoutLog   io.Writer = os.Stderr
)

// LogTimeout writes a log line naming which timeout limit fired (header, idle or total), the
// account, and the limit's value.
func LogTimeout(limit, accountID string, value time.Duration) {
	timeoutLogMu.Lock()
	w := timeoutLog
	timeoutLogMu.Unlock()
	if w == nil {
		return
	}
	if accountID == "" {
		fmt.Fprintf(w, "upstream timeout: %s limit (%s) exceeded\n", limit, value)
		return
	}
	fmt.Fprintf(w, "upstream timeout: %s limit (%s) exceeded on account %q\n", limit, value, accountID)
}

type timeoutError struct {
	kind      string
	limit     time.Duration
	account   string
	retryable bool
}

func (e *timeoutError) Error() string {
	switch e.kind {
	case timeoutHeader:
		return fmt.Sprintf("upstream response header timeout after %s", e.limit)
	case timeoutIdle:
		return fmt.Sprintf("upstream stream idle timeout after %s", e.limit)
	default:
		return fmt.Sprintf("upstream request timeout after %s", e.limit)
	}
}

// Timeout implements net.Error.
func (e *timeoutError) Timeout() bool { return true }

// Temporary implements net.Error.
func (e *timeoutError) Temporary() bool { return true }

// Retryable reports whether err from an upstream HTTP request happened before the upstream could
// have processed or answered the request (a failed dial or a timeout before any response byte),
// so trying another account is safe.
func Retryable(err error) bool {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return true
	}
	var te *timeoutError
	return errors.As(err, &te) && te.retryable
}

// IdleTimeout returns the idle timeout for ctx from its UpstreamInfo, or fallback when none is
// configured. A returned duration of 0 means the idle limit is turned off.
func hasTimeouts(info Info) bool {
	if info.Timeouts != nil {
		return true
	}
	switch info.Operation {
	case protocol.OpGenerate, protocol.OpCountTokens, protocol.OpEndpoint, protocol.OpSession, protocol.TypeSessionTurn:
		return true
	}
	return false
}

func IdleTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	info, ok := UpstreamInfo(ctx)
	if !ok || !hasTimeouts(info) {
		return fallback
	}
	_, idle, _ := resolvedTimeouts(info)
	return idle
}

func resolvedTimeouts(info Info) (header, idle, total time.Duration) {
	header = DefaultHeaderTimeout
	idle = DefaultIdleTimeout
	total = DefaultTotalTimeout
	t := info.Timeouts
	if t == nil {
		return header, idle, total
	}
	if t.HeaderMS != nil {
		header = max(0, time.Duration(*t.HeaderMS)*time.Millisecond)
	}
	if t.IdleMS != nil {
		idle = max(0, time.Duration(*t.IdleMS)*time.Millisecond)
	}
	if t.TotalMS != nil {
		total = max(0, time.Duration(*t.TotalMS)*time.Millisecond)
	}
	return header, idle, total
}

func isStreamedRequest(req *http.Request, info Info) bool {
	if info.Stream {
		return true
	}
	if strings.Contains(strings.ToLower(req.Header.Get("Accept")), "text/event-stream") {
		return true
	}
	if strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/connect+proto") {
		return true
	}
	if req.URL != nil && strings.Contains(req.URL.Path, "streamGenerateContent") {
		return true
	}
	return false
}

type timeoutTransport struct {
	next http.RoundTripper
	info Info
}

func (t timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	headerLimit, idleLimit, totalLimit := resolvedTimeouts(t.info)
	wsUpgrade := t.info.Operation == protocol.OpSession || strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
	if wsUpgrade {
		return t.roundTripHeaderOnly(req, headerLimit)
	}
	if isStreamedRequest(req, t.info) {
		return t.roundTripStreamed(req, headerLimit, idleLimit)
	}
	return t.roundTripTotal(req, totalLimit)
}

func (t timeoutTransport) roundTripHeaderOnly(req *http.Request, headerLimit time.Duration) (*http.Response, error) {
	if headerLimit <= 0 {
		return t.next.RoundTrip(req)
	}
	parentCtx := req.Context()
	rtCtx, rtCancel := context.WithCancelCause(parentCtx)
	var mu sync.Mutex
	headersDone := false
	var fired *timeoutError
	timer := time.AfterFunc(headerLimit, func() {
		mu.Lock()
		if headersDone || fired != nil || parentCtx.Err() != nil {
			mu.Unlock()
			return
		}
		te := &timeoutError{kind: timeoutHeader, limit: headerLimit, account: t.info.AccountID, retryable: true}
		fired = te
		mu.Unlock()
		LogTimeout(timeoutHeader, t.info.AccountID, headerLimit)
		rtCancel(te)
	})
	resp, err := t.next.RoundTrip(req.WithContext(rtCtx))
	mu.Lock()
	headersDone = true
	te := fired
	mu.Unlock()
	timer.Stop()
	if err != nil {
		rtCancel(nil)
		if te != nil && parentCtx.Err() == nil {
			return nil, te
		}
		return nil, err
	}
	if te != nil && parentCtx.Err() == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		rtCancel(nil)
		return nil, te
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		rtCancel(nil)
		return resp, nil
	}
	if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
		resp.Body = cancelOnCloseRWBody{ReadWriteCloser: rwc, cancel: func() { rtCancel(nil) }}
		return resp, nil
	}
	resp.Body = cancelOnCloseBody{ReadCloser: resp.Body, cancel: func() { rtCancel(nil) }}
	return resp, nil
}

func (t timeoutTransport) roundTripStreamed(req *http.Request, headerLimit, idleLimit time.Duration) (*http.Response, error) {
	if headerLimit <= 0 && idleLimit <= 0 {
		return t.next.RoundTrip(req)
	}
	parentCtx := req.Context()
	rtCtx, rtCancel := context.WithCancelCause(parentCtx)
	var mu sync.Mutex
	headersDone := false
	var fired *timeoutError
	var timer *time.Timer
	if headerLimit > 0 {
		timer = time.AfterFunc(headerLimit, func() {
			mu.Lock()
			if headersDone || fired != nil || parentCtx.Err() != nil {
				mu.Unlock()
				return
			}
			te := &timeoutError{kind: timeoutHeader, limit: headerLimit, account: t.info.AccountID, retryable: true}
			fired = te
			mu.Unlock()
			LogTimeout(timeoutHeader, t.info.AccountID, headerLimit)
			rtCancel(te)
		})
	}
	resp, err := t.next.RoundTrip(req.WithContext(rtCtx))
	mu.Lock()
	headersDone = true
	te := fired
	mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if err != nil {
		rtCancel(nil)
		if te != nil && parentCtx.Err() == nil {
			return nil, te
		}
		return nil, err
	}
	if te != nil && parentCtx.Err() == nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		rtCancel(nil)
		return nil, te
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		rtCancel(nil)
		return resp, nil
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
			resp.Body = cancelOnCloseRWBody{ReadWriteCloser: rwc, cancel: func() { rtCancel(nil) }}
		} else {
			resp.Body = cancelOnCloseBody{ReadCloser: resp.Body, cancel: func() { rtCancel(nil) }}
		}
		return resp, nil
	}
	if idleLimit > 0 {
		resp.Body = newIdleTimeoutBody(resp.Body, parentCtx, rtCtx, rtCancel, idleLimit, t.info.AccountID)
		return resp, nil
	}
	resp.Body = cancelOnCloseBody{ReadCloser: resp.Body, cancel: func() { rtCancel(nil) }}
	return resp, nil
}

func (t timeoutTransport) roundTripTotal(req *http.Request, totalLimit time.Duration) (*http.Response, error) {
	if totalLimit <= 0 {
		return t.next.RoundTrip(req)
	}
	parentCtx := req.Context()
	rtCtx, rtCancel := context.WithCancelCause(parentCtx)
	state := &totalTimeoutState{
		parentCtx: parentCtx,
		rtCtx:     rtCtx,
		cancel:    rtCancel,
		limit:     totalLimit,
		accountID: t.info.AccountID,
	}
	state.timer = time.AfterFunc(totalLimit, state.fire)
	resp, err := t.next.RoundTrip(req.WithContext(rtCtx))
	state.mu.Lock()
	te := state.fired
	if te != nil && parentCtx.Err() == nil {
		state.mu.Unlock()
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		state.timer.Stop()
		rtCancel(nil)
		return nil, te
	}
	state.headersDone = true
	state.mu.Unlock()
	if err != nil {
		state.stop()
		return nil, err
	}
	if resp.Body == nil || resp.Body == http.NoBody {
		state.stop()
		return resp, nil
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		state.timer.Stop()
		if rwc, ok := resp.Body.(io.ReadWriteCloser); ok {
			resp.Body = cancelOnCloseRWBody{ReadWriteCloser: rwc, cancel: func() { rtCancel(nil) }}
		} else {
			resp.Body = cancelOnCloseBody{ReadCloser: resp.Body, cancel: func() { rtCancel(nil) }}
		}
		return resp, nil
	}
	resp.Body = &totalTimeoutBody{rc: resp.Body, state: state}
	return resp, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel func()
}

func (b cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

type cancelOnCloseRWBody struct {
	io.ReadWriteCloser
	cancel func()
}

func (b cancelOnCloseRWBody) Close() error {
	err := b.ReadWriteCloser.Close()
	b.cancel()
	return err
}

type idleTimeoutBody struct {
	rc        io.ReadCloser
	parentCtx context.Context
	rtCtx     context.Context
	cancel    context.CancelCauseFunc
	limit     time.Duration
	accountID string

	mu     sync.Mutex
	timer  *time.Timer
	gen    uint64
	closed bool
	fired  *timeoutError
}

func newIdleTimeoutBody(rc io.ReadCloser, parentCtx, rtCtx context.Context, cancel context.CancelCauseFunc, limit time.Duration, accountID string) *idleTimeoutBody {
	b := &idleTimeoutBody{
		rc:        rc,
		parentCtx: parentCtx,
		rtCtx:     rtCtx,
		cancel:    cancel,
		limit:     limit,
		accountID: accountID,
	}
	b.mu.Lock()
	b.armLocked()
	b.mu.Unlock()
	return b
}

func (b *idleTimeoutBody) armLocked() {
	b.gen++
	g := b.gen
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(b.limit, func() {
		b.fire(g)
	})
}

func (b *idleTimeoutBody) fire(g uint64) {
	b.mu.Lock()
	if b.closed || b.gen != g || b.fired != nil || b.parentCtx.Err() != nil {
		b.mu.Unlock()
		return
	}
	te := &timeoutError{kind: timeoutIdle, limit: b.limit, account: b.accountID}
	b.fired = te
	b.mu.Unlock()
	LogTimeout(timeoutIdle, b.accountID, b.limit)
	b.cancel(te)
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	fired := b.fired
	switch {
	case err == io.EOF:
		if b.timer != nil {
			b.timer.Stop()
		}
		b.gen++
	case n > 0 && err == nil && fired == nil && !b.closed:
		b.armLocked()
	}
	b.mu.Unlock()
	if err != nil && err != io.EOF {
		if fired != nil && b.parentCtx.Err() == nil {
			return n, fired
		}
		if cause := context.Cause(b.rtCtx); cause != nil && b.parentCtx.Err() == nil {
			var te *timeoutError
			if errors.As(cause, &te) {
				return n, te
			}
		}
	}
	return n, err
}

func (b *idleTimeoutBody) Close() error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.gen++
		if b.timer != nil {
			b.timer.Stop()
		}
	}
	b.mu.Unlock()
	err := b.rc.Close()
	b.cancel(nil)
	return err
}

type totalTimeoutState struct {
	parentCtx context.Context
	rtCtx     context.Context
	cancel    context.CancelCauseFunc
	limit     time.Duration
	accountID string

	mu          sync.Mutex
	timer       *time.Timer
	headersDone bool
	closed      bool
	fired       *timeoutError
}

func (s *totalTimeoutState) fire() {
	s.mu.Lock()
	if s.closed || s.fired != nil || s.parentCtx.Err() != nil {
		s.mu.Unlock()
		return
	}
	te := &timeoutError{kind: timeoutTotal, limit: s.limit, account: s.accountID, retryable: !s.headersDone}
	s.fired = te
	s.mu.Unlock()
	LogTimeout(timeoutTotal, s.accountID, s.limit)
	s.cancel(te)
}

func (s *totalTimeoutState) stop() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		if s.timer != nil {
			s.timer.Stop()
		}
	}
	s.mu.Unlock()
	s.cancel(nil)
}

type totalTimeoutBody struct {
	rc    io.ReadCloser
	state *totalTimeoutState
}

func (b *totalTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err == io.EOF {
		b.state.mu.Lock()
		if b.state.timer != nil {
			b.state.timer.Stop()
		}
		b.state.mu.Unlock()
		return n, err
	}
	if err != nil {
		b.state.mu.Lock()
		fired := b.state.fired
		b.state.mu.Unlock()
		if fired != nil && b.state.parentCtx.Err() == nil {
			return n, fired
		}
		if cause := context.Cause(b.state.rtCtx); cause != nil && b.state.parentCtx.Err() == nil {
			var te *timeoutError
			if errors.As(cause, &te) {
				return n, te
			}
		}
	}
	return n, err
}

func (b *totalTimeoutBody) Close() error {
	b.state.mu.Lock()
	if !b.state.closed {
		b.state.closed = true
		if b.state.timer != nil {
			b.state.timer.Stop()
		}
	}
	b.state.mu.Unlock()
	err := b.rc.Close()
	b.state.cancel(nil)
	return err
}
