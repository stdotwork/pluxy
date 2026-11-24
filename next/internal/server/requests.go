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

// Requests lists the client requests core is serving now, by trace ID, so the management API can
// show them and cancel one. A request is in it from the moment it passed the client key check
// until its handler returns, however that happens: a reply, an error, the client going away, a
// cancel or a panic. A Responses WebSocket is in it while one of its turns runs, under the
// socket's trace ID: a turn is what has a model, an account, a usage event and a context of its
// own, and a socket between turns is serving nothing.
//
// A request pays one insert and one delete, each under mu for the map operation only. A nil
// *Requests lists nothing.
type Requests struct {
	mu   sync.Mutex
	open map[string]*running
}

// NewRequests returns an empty registry. Core keeps one for its whole run, across reloads.
func NewRequests() *Requests { return &Requests{open: map[string]*running{}} }

// running is one request in the registry.
type running struct {
	// Set before the request is listed and never changed.
	id       string
	start    time.Time
	endpoint string
	client   netip.Addr
	// key is the client key the request came with and keyID the generation's Generation.KeyID.
	// The list shows keyID(key), worked out when it is read, and never key.
	key    string
	keyID  func(string) string
	cancel context.CancelFunc

	// stopped is set when the management API cancelled the request.
	stopped atomic.Bool

	// mu guards what the request learns as it goes. The request takes it once when its body is
	// parsed and once for each attempt; never for a streamed event.
	mu       sync.Mutex
	format   string
	model    string
	stream   bool
	account  string
	provider string
	upstream string
	attempts int
}

// begin gives r its trace and a context the registry can cancel, and lists the request until end
// is called with the entry it returns. With a nil registry it only gives r its trace, and the
// entry is nil.
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
	e := &running{id: t.id, start: time.Now(), endpoint: r.URL.Path, client: ClientIP(r), key: clientToken(r, f.googleKeys),
		keyID: g.KeyID, cancel: cancel, format: f.format}
	t.running = e
	q.add(e)
	return r.WithContext(ctx), t, e
}

// beginTurn lists a Responses WebSocket turn that arrived at start on the socket whose upgrade
// request is r and whose trace ID is id, until end is called with the entry it returns. cancel
// ends the turn. A nil registry returns nil.
func (q *Requests) beginTurn(r *http.Request, g *Generation, id string, start time.Time, model string, cancel context.CancelFunc) *running {
	if q == nil {
		return nil
	}
	e := &running{id: id, start: start, endpoint: r.URL.Path, client: ClientIP(r), key: clientToken(r, false), keyID: g.KeyID,
		cancel: cancel, format: llm.FormatOpenAIResponses, model: model, stream: true}
	q.add(e)
	return e
}

func (q *Requests) add(e *running) {
	q.mu.Lock()
	q.open[e.id] = e
	q.mu.Unlock()
}

// end takes e off the list and releases its context. A nil e is a request that was not listed.
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

// describe notes what the parsed request asks for.
func (e *running) describe(req llm.Request) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.model, e.stream = req.Model, req.Stream
	if e.format == "" && req.Native != nil {
		e.format = req.Native.Format // an endpoint beyond generation names its format here
	}
	e.mu.Unlock()
}

// attempt notes the account an attempt went to.
func (e *running) attempt(a route.Attempt) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.account, e.provider, e.upstream = a.Account, a.Extension, a.Model
	e.attempts++
	e.mu.Unlock()
}

// byOperator reports whether the management API cancelled the request.
func (e *running) byOperator() bool { return e != nil && e.stopped.Load() }

// view returns e as the management API shows it.
func (e *running) view() management.RunningRequest {
	v := management.RunningRequest{ID: e.id, StartedAt: e.start.UTC(), Endpoint: e.endpoint}
	if e.client.IsValid() {
		v.ClientIP = e.client.String()
	}
	if e.key != "" && e.keyID != nil {
		v.KeyID = e.keyID(e.key)
	}
	e.mu.Lock()
	v.Format, v.Model, v.Stream = e.format, e.model, e.stream
	v.Account, v.Provider, v.UpstreamModel, v.Attempts = e.account, e.provider, e.upstream, e.attempts
	e.mu.Unlock()
	return v
}

// List returns the requests being served, oldest first.
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

// Cancel cancels the context of the request with trace ID id and returns the request as it was
// then. The request ends as one whose client went away does: the call to the extension is
// closed, the client gets the cancelled error, and the request leaves the list when its handler
// returns. It reports false when no such request is being served. Cancelling a request again
// before it has left the list changes nothing and reports true.
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
