package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// accessState is what a request's access line reads when the request ends: the trace core
// created for it, if any, with the account its last attempt went to.
type accessState struct{ trace *trace }

type accessStateKey struct{}

// withAccessState returns r carrying s as the state of its access line.
func withAccessState(r *http.Request, s *accessState) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), accessStateKey{}, s))
}

// accessStateOf returns the access state r carries, or nil.
func accessStateOf(r *http.Request) *accessState {
	s, _ := r.Context().Value(accessStateKey{}).(*accessState)
	return s
}

// statusWriter is the client's ResponseWriter, keeping the reply's status for the access line.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap keeps http.ResponseController's Flush and Hijack working through the wrapper: a reply
// still streams, and the Responses WebSocket still upgrades.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// accessLog wraps h to write the request's access line when it ends: after a streamed reply ends,
// or when a WebSocket it upgraded closes. Only the routes NewHandler serves are wrapped, so
// /healthz, the management API, the panel and a path no route serves leave no line.
func accessLog(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		state := &accessState{}
		sw := &statusWriter{ResponseWriter: w}
		h(sw, withAccessState(r, state))
		writeAccessLine(sw.status, start, r, state.trace)
	}
}

// writeAccessLine logs one client request as
//
//	<status> | <latency> | <client ip> | <METHOD> <path> | <trace id> | <account>
//
// with the path alone (never the query string, where Gemini clients send their key), and "-"
// for a trace ID or account the request has none of. A reply of 400 or more is a warning line,
// which the log's level mechanism marks by the "warning: " prefix; every other line is info.
func writeAccessLine(status int, start time.Time, r *http.Request, t *trace) {
	if status == 0 {
		// The handler returned before writing anything: net/http sends 200.
		status = http.StatusOK
	}
	latency := time.Since(start)
	if latency > time.Minute {
		latency = latency.Truncate(time.Second)
	} else {
		latency = latency.Truncate(time.Millisecond)
	}
	client := "-"
	if a := ClientIP(r); a.IsValid() {
		client = a.String()
	}
	traceID, account := "-", "-"
	if t != nil {
		traceID = t.id
		if t.account != "" {
			account = logField(t.account)
		}
	}
	line := fmt.Sprintf("%d | %s | %s | %s %s | %s | %s", status, latency, client, r.Method, logField(r.URL.Path), traceID, account)
	if status >= http.StatusBadRequest {
		log.Printf("warning: %s", line)
		return
	}
	log.Print(line)
}
