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

	attempts int         // the attempts made so far
	open     bool        // an attempt has begun and not failed
	cur      llm.Tried   // the open attempt, its result not yet known
	tried    []llm.Tried // the attempts that ended, up to maxTried
	done     bool
	// running is the request's entry in the registry of requests being served, which knows
	// whether the management API cancelled it; nil for a request with none.
	running *running
}

// newMeter returns the meter of a request r that arrived at start, or nil when the generation
// keeps no usage record. key is the client key the request came with.
func (g *Generation) newMeter(r *http.Request, start time.Time, req llm.Request, key string) *meter {
	if g.Usage == nil {
		return nil
	}
	m := &meter{store: g.Usage, start: start}
	if t := traceOf(r); t != nil {
		m.running = t.running
	}
	m.ev = usage.Event{Timestamp: start.UTC(), Endpoint: r.URL.Path, Stream: req.Stream, Model: req.Model}
	if a := ClientIP(r); a.IsValid() {
		m.ev.ClientIP = a.String()
	}
	if len(g.Config.ClientKeys) > 0 && key != "" && g.KeyID != nil {
		m.ev.KeyID = g.KeyID(key)
	}
	return m
}

// response notes the model the upstream reply says served the request.
func (m *meter) response(ev llm.Event) {
	if m == nil || m.done || ev.Type != llm.EventResponseStart || ev.Response == nil || ev.Response.Model == "" {
		return
	}
	m.ev.ServedModel = ev.Response.Model
}

// attempt notes the account an attempt went to; the last one is the request's.
func (m *meter) attempt(a route.Attempt, trace string) {
	if m == nil {
		return
	}
	m.ev.ID, m.ev.Provider, m.ev.Account, m.ev.ChainStep = trace, a.Extension, a.Account, a.Step
	m.ev.UpstreamModel = ""
	if a.Model != m.ev.Model {
		m.ev.UpstreamModel = a.Model
	}
	m.attempts++
	m.open, m.cur = true, llm.Tried{Account: a.Public, Provider: a.Extension, Model: a.Model}
}

// maxTried is how many attempts an event keeps: the first maxTried-1 and the last.
const maxTried = 16

// attemptFailed notes that the open attempt ended without a reply that began, as t says.
func (m *meter) attemptFailed(t llm.Tried) {
	if m == nil {
		return
	}
	m.open = false
	m.addTried(t)
}

// addTried keeps t as the newest attempt, in the last slot once the list is full.
func (m *meter) addTried(t llm.Tried) {
	if len(m.tried) < maxTried {
		m.tried = append(m.tried, t)
		return
	}
	m.tried[maxTried-1] = t
}

// finish records the request as ended by ev, its finish event: a complete reply.
func (m *meter) finish(ev llm.Event) {
	if m == nil || m.done {
		return
	}
	e := m.event(http.StatusOK, "ok")
	e.Tokens = usage.TokensOf(ev.Usage)
	m.store.Add(e)
}

// fail records the request as failed with e; status is the HTTP status of the reply, which is
// 200 for a streamed reply that had begun.
func (m *meter) fail(e *llm.Error, status int) {
	if m == nil || m.done {
		return
	}
	if e.Code == llm.CodeCancelled && !m.running.byOperator() {
		// The client went away: nothing reached it but what was sent already. A request the
		// management API cancelled has a client that is still there and got the reply.
		status = m.sent
	}
	ev := m.event(status, route.ResultCode(e))
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
func (m *meter) event(status int, result string) usage.Event {
	m.done = true
	e := m.ev
	e.Attempts = m.attempts
	if m.open {
		// The attempt that was running ends the request: with ok when it answered, else with
		// the error's result code. One attempt that succeeded is told by the event's account already.
		m.cur.Result = result
		if result != "ok" || m.attempts > 1 {
			m.addTried(m.cur)
		}
	}
	e.Tried = m.tried
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

func (s metered) Next() llm.Event {
	ev := s.Stream.Next()
	s.m.response(ev)
	return ev
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
