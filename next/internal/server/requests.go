package server

import (
	"cmp"
	"context"
	"net/http"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
)

type Requests struct {
	mu   sync.Mutex
	open map[string]*running
}

func NewRequests() *Requests { return &Requests{open: map[string]*running{}} }

type running struct {
	id       string
	start    time.Time
	endpoint string
	client   netip.Addr
	keyID    string
	cancel   context.CancelFunc

	stopped atomic.Bool

	mu       sync.Mutex
	format   string
	model    string
	stream   bool
	account  string
	provider string
	upstream string
	attempts int
}

func (q *Requests) begin(r *http.Request, g *Generation, f clientFormat) (*http.Request, *trace, *running) {
	if q == nil {
		r, t := requestTrace(r)
		return r, t, nil
	}
	t := traceOf(r)
	ctx := r.Context()
	if t == nil {
		t = newTrace()
		if s := accessStateOf(r); s != nil {
			s.trace = t
		}
		ctx = context.WithValue(ctx, traceKey{}, t)
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &running{id: t.id, start: time.Now(), endpoint: r.URL.Path, client: ClientIP(r), keyID: g.keyIDOf(clientToken(r, f.googleKeys)),
		cancel: cancel, format: f.format}
	t.running = e
	q.add(e)
	return r.WithContext(ctx), t, e
}

func (q *Requests) beginTurn(r *http.Request, g *Generation, id string, start time.Time, model string, cancel context.CancelFunc) *running {
	if q == nil {
		return nil
	}
	e := &running{id: id, start: start, endpoint: r.URL.Path, client: ClientIP(r), keyID: g.keyIDOf(clientToken(r, false)),
		cancel: cancel, format: llm.FormatOpenAIResponses, model: model, stream: true}
	q.add(e)
	return e
}

func (q *Requests) add(e *running) {
	q.mu.Lock()
	q.open[e.id] = e
	q.mu.Unlock()
}

func (q *Requests) end(e *running) {
	if e == nil {
		return
	}
	q.mu.Lock()
	if q.open[e.id] == e {
		delete(q.open, e.id)
	}
	q.mu.Unlock()
	e.cancel()
}

func (e *running) describe(req llm.Request) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.model, e.stream = req.Model, req.Stream
	if req.Native != nil && req.Native.Format != "" {
		e.format = req.Native.Format
	}
	e.mu.Unlock()
}

func (e *running) attempt(a route.Attempt) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.account, e.provider, e.upstream = a.Account, a.Extension, a.Model
	e.attempts++
	e.mu.Unlock()
}

func (e *running) byOperator() bool { return e != nil && e.stopped.Load() }

func (e *running) view() management.RunningRequest {
	v := management.RunningRequest{ID: e.id, StartedAt: e.start.UTC(), Endpoint: e.endpoint, KeyID: e.keyID}
	if e.client.IsValid() {
		v.ClientIP = e.client.String()
	}
	e.mu.Lock()
	v.Format, v.Model, v.Stream = e.format, e.model, e.stream
	v.Account, v.Provider, v.UpstreamModel, v.Attempts = e.account, e.provider, e.upstream, e.attempts
	e.mu.Unlock()
	return v
}

func (q *Requests) List() []management.RunningRequest {
	out := []management.RunningRequest{}
	if q == nil {
		return out
	}
	q.mu.Lock()
	es := make([]*running, 0, len(q.open))
	for _, e := range q.open {
		es = append(es, e)
	}
	q.mu.Unlock()
	slices.SortFunc(es, func(a, b *running) int {
		return cmp.Or(a.start.Compare(b.start), cmp.Compare(a.id, b.id))
	})
	for _, e := range es {
		out = append(out, e.view())
	}
	return out
}

func (q *Requests) Cancel(id string) (management.RunningRequest, bool) {
	if q == nil {
		return management.RunningRequest{}, false
	}
	q.mu.Lock()
	e := q.open[id]
	q.mu.Unlock()
	if e == nil {
		return management.RunningRequest{}, false
	}
	v := e.view()
	e.stopped.Store(true)
	e.cancel()
	return v, true
}

func cancelledError() *llm.Error {
	return &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
}

func (g *Generation) cancelledUnread(w http.ResponseWriter, r *http.Request, f clientFormat, arrived time.Time, metered bool) {
	t := traceOf(r)
	if t == nil || !t.running.byOperator() {
		return
	}
	var m *meter
	if metered {
		m = g.newMeter(r, arrived, llm.Request{}, clientToken(r, f.googleKeys))
	}
	e := cancelledError()
	m.fail(e, f.writeError(w, e))
}

func cancelledBeforeRouting(w http.ResponseWriter, r *http.Request, f clientFormat, m *meter) bool {
	if r.Context().Err() == nil {
		return false
	}
	e := cancelledError()
	m.fail(e, f.writeError(w, e))
	return true
}
