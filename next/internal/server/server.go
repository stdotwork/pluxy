package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// MaxRequestBody limits a client request body. The request travels to the extension in one
// frame, as parsed items plus the original body, so this stays well under protocol.MaxFrame.
const MaxRequestBody = 24 << 20

// Stream is the reply to a generate call: events until one terminal event.
type Stream interface {
	Next() llm.Event
	Close()
}

// GenerateFunc starts a generate call on the provider extension.
type GenerateFunc func(ctx context.Context, account protocol.Account, req llm.Request) Stream

// New returns core's HTTP handler.
func New(cfg *Config, generate GenerateFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !authorized(cfg, r) {
			writeError(w, &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"})
			return
		}
		chat(w, r, cfg, generate)
	})
	return mux
}

func authorized(cfg *Config, r *http.Request) bool {
	if len(cfg.ClientKeys) == 0 {
		return true
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return false
	}
	match := 0
	for _, k := range cfg.ClientKeys {
		match |= subtle.ConstantTimeCompare([]byte(token), []byte(k))
	}
	return match == 1
}

func writeError(w http.ResponseWriter, e *llm.Error) {
	status, body := openaichat.ErrorResponse(e)
	w.Header().Set("Content-Type", "application/json")
	// Tell a rate-limited client when to try again, in whole seconds rounded up (RFC 9110
	// section 10.2.3, RFC 6585 section 4).
	if status == http.StatusTooManyRequests && e.RetryAfterMS > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt((e.RetryAfterMS+999)/1000, 10))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func chat(w http.ResponseWriter, r *http.Request, cfg *Config, generate GenerateFunc) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is too large", Status: http.StatusRequestEntityTooLarge})
			return
		}
		return // the client went away
	}
	req, err := openaichat.ParseRequest(body)
	if err != nil {
		writeError(w, asLLMError(err))
		return
	}
	acct, ok := cfg.accountFor(req.Model)
	if !ok {
		writeError(w, &llm.Error{Code: llm.CodeNotFound, Message: fmt.Sprintf("no account serves model %q", req.Model), ProviderCode: "model_not_found"})
		return
	}
	stream := generate(r.Context(), protocol.Account{ID: acct.ID, Settings: acct.Settings, Credential: acct.Credential}, req)
	defer stream.Close()

	// Nothing is sent to the client until the first event, so an error before it becomes a
	// proper HTTP error response.
	ev := stream.Next()
	if ev.Type == llm.EventError {
		logFailure(r, ev.Error)
		writeError(w, ev.Error)
		return
	}
	if !req.Stream {
		agg := openaichat.NewAggregator()
		for {
			agg.Add(ev)
			if ev.Terminal() {
				break
			}
			ev = stream.Next()
		}
		if ev.Type == llm.EventError {
			logFailure(r, ev.Error)
			writeError(w, ev.Error)
			return
		}
		out, err := agg.Response()
		if err != nil {
			writeError(w, &llm.Error{Code: llm.CodeInternal, Message: err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	sw := openaichat.NewStreamWriter(w, openaichat.WantsStreamUsage(req))
	for {
		// Writes block while the client is slow, and the next event is not read until the
		// write is done: that is the backpressure path to the extension.
		if err := sw.Write(ev); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		if ev.Terminal() {
			if ev.Type == llm.EventError {
				logFailure(r, ev.Error)
			}
			return
		}
		ev = stream.Next()
	}
}

func asLLMError(err error) *llm.Error {
	var e *llm.Error
	if errors.As(err, &e) {
		return e
	}
	return &llm.Error{Code: llm.CodeInternal, Message: err.Error()}
}

func logFailure(r *http.Request, e *llm.Error) {
	if e.Code == llm.CodeCancelled {
		return
	}
	log.Printf("%s %s: %s", r.Method, r.URL.Path, e.Error())
}
