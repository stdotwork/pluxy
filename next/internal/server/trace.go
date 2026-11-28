package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

const TraceHeader = "X-CPA-TRACE-ID"

const (
	ProviderHeader = "X-Pluxy-Provider"
	ModelHeader    = "X-Pluxy-Model"
	AttemptsHeader = "X-Pluxy-Attempts"
)

type trace struct {
	id                      string
	account                 string
	public, provider, model string
	attempts                int
	failed                  bool
	body                    []byte
	upstream                *upstreamLog
	running                 *running
}

type traceKey struct{}

func newTrace() *trace {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &trace{id: time.Now().UTC().Format("20060102150405") + "-" + hex.EncodeToString(b[:])}
}

func withTrace(r *http.Request, t *trace) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), traceKey{}, t))
}

func traceOf(r *http.Request) *trace {
	t, _ := r.Context().Value(traceKey{}).(*trace)
	return t
}

func (t *trace) attempt(a route.Attempt) {
	t.account, t.public, t.provider, t.model = a.Account, a.Public, a.Extension, a.Model
	t.attempts++
	t.running.attempt(a)
}

func (t *trace) setHeaders(w http.ResponseWriter, explain bool) {
	if t == nil {
		return
	}
	h := w.Header()
	h.Set(TraceHeader, t.id)
	if explain && t.account != "" {
		h.Set(PinHeader, t.public)
		h.Set(ProviderHeader, t.provider)
		if t.model != "" {
			h.Set(ModelHeader, t.model)
		}
		h.Set(AttemptsHeader, strconv.Itoa(t.attempts))
	}
}

func (t *trace) explained(e *llm.Error) *llm.Error {
	if t != nil && e != nil && e.Explain != nil {
		e.Explain.Trace = t.id
	}
	return e
}

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
