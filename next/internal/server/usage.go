package server

import (
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
)

// meter records one client request in the usage record when it ends. A nil meter records
// nothing, so callers need not check whether usage statistics are on.
type meter struct {
	store *usage.Store
	ev    usage.Event
	start time.Time
	sent  int // the status already sent, once a streamed reply began; else 0
	done  bool
}

// newMeter returns the meter of a request that arrived at start, or nil when the generation
// keeps no usage record. key is the client key the request came with.
func (g *Generation) newMeter(start time.Time, endpoint string, req llm.Request, key string) *meter {
	if g.Usage == nil {
		return nil
	}
	m := &meter{store: g.Usage, start: start}
	m.ev = usage.Event{Timestamp: start.UTC(), Endpoint: endpoint, Stream: req.Stream, Model: req.Model}
	if len(g.Config.ClientKeys) > 0 && key != "" && g.KeyID != nil {
		m.ev.KeyID = g.KeyID(key)
	}
	return m
}

// attempt notes the account an attempt went to; the last one is the request's.
func (m *meter) attempt(a route.Attempt, trace string) {
	if m == nil {
		return
	}
	m.ev.ID, m.ev.Provider, m.ev.Account = trace, a.Extension, a.Account
	m.ev.UpstreamModel = ""
	if a.Model != m.ev.Model {
		m.ev.UpstreamModel = a.Model
	}
}

// finish records the request as ended by ev, its finish event: a complete reply.
func (m *meter) finish(ev llm.Event) {
	if m == nil || m.done {
		return
	}
	e := m.event(http.StatusOK)
	e.Tokens = usage.TokensOf(ev.Usage)
	m.store.Add(e)
}

// fail records the request as failed with e; status is the HTTP status of the reply, which is
// 200 for a streamed reply that had begun.
func (m *meter) fail(e *llm.Error, status int) {
	if m == nil || m.done {
		return
	}
	if e.Code == llm.CodeCancelled {
		// The client went away: nothing reached it but what was sent already.
		status = m.sent
	}
	ev := m.event(status)
	ev.Failed, ev.ErrorCode, ev.Tokens = true, e.Code, usage.TokensOf(e.Usage)
	m.store.Add(ev)
}

// drop records nothing for the request.
func (m *meter) drop() {
	if m != nil {
		m.done = true
	}
}

// event returns the request's event as it ends now with status, and marks the meter done.
func (m *meter) event(status int) usage.Event {
	m.done = true
	e := m.ev
	e.LatencyMS = time.Since(m.start).Milliseconds()
	e.Status = status
	return e
}

// metered is a request's stream that records the request as cancelled when it is closed before
// the caller recorded how it ended: the client went away.
type metered struct {
	Stream
	m *meter
}

func (s metered) Close() {
	s.Stream.Close()
	s.m.fail(&llm.Error{Code: llm.CodeCancelled}, s.m.sent)
}

// meterStream returns stream recording into m; stream itself when m is nil.
func meterStream(stream Stream, m *meter) Stream {
	if m == nil {
		return stream
	}
	return metered{stream, m}
}
