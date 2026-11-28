package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

type accessState struct{ trace *trace }

type accessStateKey struct{}

func withAccessState(r *http.Request, s *accessState) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), accessStateKey{}, s))
}

func accessStateOf(r *http.Request) *accessState {
	s, _ := r.Context().Value(accessStateKey{}).(*accessState)
	return s
}

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

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func accessLog(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		state := &accessState{}
		sw := &statusWriter{ResponseWriter: w}
		h(sw, withAccessState(r, state))
		writeAccessLine(sw.status, start, r, state.trace)
	}
}

// never the query string, where Gemini clients send their key.
func writeAccessLine(status int, start time.Time, r *http.Request, t *trace) {
	if status == 0 {
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
