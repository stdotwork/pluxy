package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// TraceHeader carries a request's trace ID in the reply, under today's core's name. The same ID
// is in every log line core writes about the request, with the account it went to, so a reply
// can be matched with what core logged about it.
const TraceHeader = "X-CPA-TRACE-ID"

// trace is one client request's trace ID and the account its last attempt went to.
type trace struct {
	id      string
	account string
}

type traceKey struct{}

// newTrace returns a trace with a new ID: the time in UTC (YYYYMMDDhhmmss, as today's core
// writes it) and 16 random hex digits.
func newTrace() *trace {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &trace{id: time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b[:])}
}

// withTrace returns r carrying t, for the log lines written about it.
func withTrace(r *http.Request, t *trace) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), traceKey{}, t))
}

// traceOf returns the trace r carries, or nil.
func traceOf(r *http.Request) *trace {
	t, _ := r.Context().Value(traceKey{}).(*trace)
	return t
}

// setHeader sets the trace header once the request went to an account, as today's core does: a
// request refused before that (no client key, a body that does not parse, no account for the
// model) gets none.
func (t *trace) setHeader(w http.ResponseWriter) {
	if t != nil && t.account != "" {
		w.Header().Set(TraceHeader, t.id)
	}
}

// logPrefix returns what a log line about r starts with: its method and path, where it came
// from (ClientIP), and its trace ID and account when it has them.
func logPrefix(r *http.Request) string {
	p := r.Method + " " + logField(r.URL.Path)
	if a := ClientIP(r); a.IsValid() {
		p += " from " + a.String()
	}
	if t := traceOf(r); t != nil {
		p += " [trace " + t.id
		if t.account != "" {
			p += " account " + logField(t.account)
		}
		p += "]"
	}
	return p
}
