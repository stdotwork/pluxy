package server

import (
	"net/http"
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
)

type meter struct {
	store *usage.Store
	ev    usage.Event
	start time.Time
	sent  int

	attempts int
	open     bool
	cur      llm.Tried
	tried    []llm.Tried
	done     bool
	running  *running
}

func (g *Generation) newMeter(r *http.Request, start time.Time, req llm.Request, key string) *meter {
	if g.Usage == nil {
		return nil
	}
	m := &meter{store: g.Usage, start: start}
	if t := traceOf(r); t != nil {
		m.running = t.running
	}
	m.ev = usage.Event{Timestamp: start.UTC(), Endpoint: r.URL.Path, Stream: req.Stream, Model: req.Model}
	if m.running != nil {
		m.ev.KeyID = m.running.keyID
	} else {
		m.ev.KeyID = g.keyIDOf(key)
	}
	if m.ev.KeyID != "" {
		m.ev.Label = g.Config.ClientKeyLabels[m.ev.KeyID]
	}
	if a := ClientIP(r); a.IsValid() {
		m.ev.ClientIP = a.String()
	}
	return m
}

func (g *Generation) keyIDOf(key string) string {
	if len(g.Config.ClientKeys) > 0 && key != "" && g.KeyID != nil {
		return g.KeyID(key)
	}
	return ""
}

func (m *meter) response(ev llm.Event) {
	if m == nil || m.done || ev.Type != llm.EventResponseStart || ev.Response == nil || ev.Response.Model == "" {
		return
	}
	m.ev.ServedModel = ev.Response.Model
}

func (m *meter) attempt(a route.Attempt, trace string) {
	if m == nil {
		return
	}
	m.ev.ID, m.ev.Provider, m.ev.Account, m.ev.ChainStep, m.ev.Why = trace, a.Extension, a.Account, a.Step, a.Why
	m.ev.UpstreamModel = ""
	if a.Model != m.ev.Model {
		m.ev.UpstreamModel = a.Model
	}
	m.attempts++
	m.open, m.cur = true, llm.Tried{Account: a.Public, Provider: a.Extension, Model: a.Model}
}

const maxTried = 16

func (m *meter) attemptFailed(t llm.Tried) {
	if m == nil {
		return
	}
	m.open = false
	m.addTried(t)
}

func (m *meter) addTried(t llm.Tried) {
	if len(m.tried) < maxTried {
		m.tried = append(m.tried, t)
		return
	}
	m.tried[maxTried-1] = t
}

func (m *meter) finish(ev llm.Event) {
	if m == nil || m.done {
		return
	}
	e := m.event(http.StatusOK, "ok")
	e.Tokens = usage.TokensOf(ev.Usage)
	m.store.Add(e)
}

func (m *meter) fail(e *llm.Error, status int) {
	if m == nil || m.done {
		return
	}
	if e.Code == llm.CodeCancelled && !m.running.byOperator() {
		status = m.sent
	}
	ev := m.event(status, route.ResultCode(e))
	ev.Failed, ev.ErrorCode, ev.Tokens = true, e.Code, usage.TokensOf(e.Usage)
	if e.Routed != nil {
		ev.LeftOut = leftOut(e.Routed.LeftOut)
	}
	m.store.Add(ev)
}

const maxLeftOut = 8

func leftOut(l []llm.LeftOut) []llm.LeftOut {
	if len(l) == 0 {
		return nil
	}
	out := slices.Clone(l[:min(len(l), maxLeftOut)])
	for i := range out {
		out[i].ID = ""
	}
	return out
}

func (m *meter) drop() {
	if m != nil {
		m.done = true
	}
}

func (m *meter) event(status int, result string) usage.Event {
	m.done = true
	e := m.ev
	e.Attempts = m.attempts
	if m.open {
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

func meterStream(stream Stream, m *meter) Stream {
	if m == nil {
		return stream
	}
	return metered{stream, m}
}
