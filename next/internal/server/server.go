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
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// MaxRequestBody limits a client request body. The request travels to the extension in one
// frame, as parsed items plus the original body. JSON escaping can make the parsed items longer
// than the body (U+2028 takes six bytes escaped, three raw), so a body under this limit can
// still exceed protocol.MaxFrame; the host then fails the request with status 413.
const MaxRequestBody = 24 << 20

// MaxResponseBody limits what core collects for a non-streamed reply; streamed replies are not
// collected, so they have no limit. It matches what the provider extension reads of a plain
// JSON upstream body (protocol.MaxFrame).
const MaxResponseBody = 64 << 20

// Stream is the reply to a generate call: events until one terminal event.
type Stream interface {
	Next() llm.Event
	Close()
}

// GenerateFunc starts a generate call on the provider extension.
type GenerateFunc func(ctx context.Context, account protocol.Account, req llm.Request) Stream

// clientFormat is a client API core serves: how to read its requests and write its replies.
type clientFormat struct {
	parse     func(body []byte) (llm.Request, error)
	stream    func(w io.Writer, req llm.Request) eventWriter
	aggregate func(req llm.Request) aggregator
}

type eventWriter interface{ Write(ev llm.Event) error }

type aggregator interface {
	Add(ev llm.Event)
	// Size is about how many bytes of content the aggregator holds; core stops collecting a
	// reply above MaxResponseBody.
	Size() int
	Response() ([]byte, error)
}

// lossReporter is a writer or aggregator that reports what the client format could not carry.
type lossReporter interface{ Losses() []llm.Loss }

var chatFormat = clientFormat{
	parse: openaichat.ParseRequest,
	stream: func(w io.Writer, req llm.Request) eventWriter {
		return openaichat.NewStreamWriter(w, openaichat.WantsStreamUsage(req))
	},
	aggregate: func(llm.Request) aggregator { return openaichat.NewAggregator() },
}

var responsesFormat = clientFormat{
	parse:     openairesponses.ParseRequest,
	stream:    func(w io.Writer, req llm.Request) eventWriter { return openairesponses.NewStreamWriter(w, req) },
	aggregate: func(req llm.Request) aggregator { return openairesponses.NewAggregator(req) },
}

// New returns core's HTTP handler. Requests are routed over cfg's accounts, and each attempt
// is a call to generate.
func New(cfg *Config, generate GenerateFunc) (http.Handler, error) {
	router, err := route.New(cfg.Routing, cfg.Extension.Accounts, func(ctx context.Context, a protocol.Account, req llm.Request) route.Stream {
		return generate(ctx, a, req)
	}, nil)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	for path, f := range map[string]clientFormat{"/v1/chat/completions": chatFormat, "/v1/responses": responsesFormat} {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			if !authorized(cfg, r) {
				writeError(w, &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"})
				return
			}
			serve(w, r, cfg, router, f)
		})
	}
	return mux, nil
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

func serve(w http.ResponseWriter, r *http.Request, cfg *Config, router *route.Router, f clientFormat) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is too large", Status: http.StatusRequestEntityTooLarge})
			return
		}
		return // the client went away
	}
	req, err := f.parse(body)
	if err != nil {
		writeError(w, asLLMError(err))
		return
	}
	req.DropPolicy = cfg.DropPolicy
	stream := router.Generate(r.Context(), routeRequest(r, req, body))
	defer stream.Close()

	// Nothing is sent to the client until the first event, so an error before it becomes a
	// proper HTTP error response.
	ev := stream.Next()
	if ev.Type == llm.EventError {
		logFailure(r, ev.Error)
		writeError(w, ev.Error)
		return
	}
	var request *llm.Translation
	if ev.Type == llm.EventResponseStart {
		request = ev.Response.Translation
	}
	if !req.Stream {
		agg := f.aggregate(req)
		defer func() { logTranslation(r, request, agg) }()
		for {
			agg.Add(ev)
			if ev.Terminal() {
				break
			}
			if agg.Size() > MaxResponseBody {
				// Returning closes the stream, which cancels the call and the upstream request.
				e := &llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream reply is larger than %d MiB, the limit for a non-streamed response; send the request with \"stream\": true", MaxResponseBody>>20)}
				logFailure(r, e)
				writeError(w, e)
				return
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
	sw := f.stream(w, req)
	defer func() { logTranslation(r, request, sw) }()
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

// logTranslation logs what translation lost on the way to the upstream (request) and back to
// the client (reply), when anything was lost.
func logTranslation(r *http.Request, request *llm.Translation, reply any) {
	var parts []string
	if request != nil && !request.Passthrough {
		for _, l := range request.Losses {
			parts = append(parts, fmt.Sprintf("request %s %s (%s)", l.Kind, l.Path, l.Detail))
		}
	}
	if lr, ok := reply.(lossReporter); ok {
		for _, l := range lr.Losses() {
			parts = append(parts, fmt.Sprintf("reply %s %s (%s)", l.Kind, l.Path, l.Detail))
		}
	}
	if len(parts) > 0 {
		log.Printf("%s %s: translation: %s", r.Method, r.URL.Path, strings.Join(parts, "; "))
	}
}

func logFailure(r *http.Request, e *llm.Error) {
	if e.Code == llm.CodeCancelled {
		return
	}
	log.Printf("%s %s: %s", r.Method, r.URL.Path, e.Error())
}
