package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
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
	parse     func(r *http.Request, body []byte) (llm.Request, error)
	stream    func(w io.Writer, req llm.Request) eventWriter
	aggregate func(req llm.Request) aggregator
	// errorBody returns the status and body of an error reply; nil means the OpenAI shape.
	errorBody func(e *llm.Error) (int, []byte)
	// headers are client request headers that belong to the format's body; they travel with
	// the native body.
	headers []string
	// googleKeys accepts the client key where Google's clients send it: in x-goog-api-key or
	// the key query parameter.
	googleKeys bool
}

// bodyOnly adapts a parser for a format whose request is all in its body.
func bodyOnly(parse func(body []byte) (llm.Request, error)) func(*http.Request, []byte) (llm.Request, error) {
	return func(_ *http.Request, body []byte) (llm.Request, error) { return parse(body) }
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
	parse: bodyOnly(openaichat.ParseRequest),
	stream: func(w io.Writer, req llm.Request) eventWriter {
		return openaichat.NewStreamWriter(w, openaichat.WantsStreamUsage(req))
	},
	aggregate: func(llm.Request) aggregator { return openaichat.NewAggregator() },
}

// completionsFormat serves OpenAI's legacy text completions as Chat Completions (see
// openaichat.ParseCompletionsRequest).
var completionsFormat = clientFormat{
	parse: bodyOnly(openaichat.ParseCompletionsRequest),
	stream: func(w io.Writer, req llm.Request) eventWriter {
		return openaichat.NewCompletionsStreamWriter(w, openaichat.WantsStreamUsage(req))
	},
	aggregate: func(llm.Request) aggregator { return openaichat.NewCompletionsAggregator() },
}

var responsesFormat = clientFormat{
	parse:     bodyOnly(openairesponses.ParseRequest),
	stream:    func(w io.Writer, req llm.Request) eventWriter { return openairesponses.NewStreamWriter(w, req) },
	aggregate: func(req llm.Request) aggregator { return openairesponses.NewAggregator(req) },
	headers:   openairesponses.Headers,
}

var messagesFormat = clientFormat{
	parse:     bodyOnly(anthropicmessages.ParseRequest),
	stream:    func(w io.Writer, _ llm.Request) eventWriter { return anthropicmessages.NewStreamWriter(w) },
	aggregate: func(llm.Request) aggregator { return anthropicmessages.NewAggregator() },
	errorBody: anthropicmessages.ErrorResponse,
	headers:   slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders),
}

// geminiFormat serves Gemini's GenerateContent at /v1beta/models/{model}:{method}. The model may
// hold slashes ("publisher/model"), as today's core's catch-all route accepts it. The method
// says whether the reply streams; a stream is always server-sent events, with or without
// alt=sse, as today's core serves it.
var geminiFormat = clientFormat{
	parse: func(r *http.Request, body []byte) (llm.Request, error) {
		model, method, _ := strings.Cut(r.PathValue("action"), ":")
		switch method {
		case "generateContent", "streamGenerateContent":
		default:
			// countTokens is served apart (geminiAction); any other method is not served.
			return llm.Request{}, &llm.Error{Code: llm.CodeNotFound, Scope: llm.ScopeRequest,
				Message: fmt.Sprintf("method %q is not supported on models/%s", method, model)}
		}
		return geminigenerate.ParseRequest(model, method == "streamGenerateContent", body)
	},
	stream:     func(w io.Writer, _ llm.Request) eventWriter { return geminigenerate.NewStreamWriter(w) },
	aggregate:  func(llm.Request) aggregator { return geminigenerate.NewAggregator() },
	errorBody:  geminigenerate.ErrorResponse,
	googleKeys: true,
}

// Generation is what one request runs on from start to end: a config snapshot and the router
// built for it, bound to the extension generation that config named. A reload publishes a new
// one; requests that started on the old one keep it.
type Generation struct {
	Config *Config
	Router *route.Router
	// Attempt runs one attempt as the router does (a WebSocket turn routes with its own).
	Attempt route.AttemptFunc
	// Count runs one count_tokens attempt; nil when counting is not served.
	Count route.AttemptFunc
	// OpenSession opens a session on an extension; nil when sessions are not served.
	OpenSession OpenSessionFunc
	// Usage is the usage record requests are added to; nil while usage statistics are off.
	Usage *usage.Store
	// KeyID returns the ID the management API lists a client key under.
	KeyID func(key string) string
}

// Source hands out the current generation. The request holds it until release is called.
type Source interface {
	Acquire() (g *Generation, release func())
}

// NewHandler returns core's client API handler. Each request takes the current generation from
// src when it starts and keeps it until it ends.
func NewHandler(src Source) http.Handler {
	mux := http.NewServeMux()
	handle := func(path string, f clientFormat, h func(w http.ResponseWriter, r *http.Request, g *Generation)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			g, release := src.Acquire()
			defer release()
			if !authorized(g.Config, r, f.googleKeys) {
				f.writeError(w, &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"})
				return
			}
			h(w, r, g)
		})
	}
	for path, f := range map[string]clientFormat{"/v1/chat/completions": chatFormat, "/v1/completions": completionsFormat,
		"/v1/responses": responsesFormat, "/v1/messages": messagesFormat} {
		handle(path, f, func(w http.ResponseWriter, r *http.Request, g *Generation) { serve(w, r, g, f) })
	}
	handle("/v1/messages/count_tokens", messagesFormat, func(w http.ResponseWriter, r *http.Request, g *Generation) {
		serveCount(w, r, g, messagesCount)
	})
	handle("/v1beta/models/{action...}", geminiFormat, geminiAction)
	mux.HandleFunc("GET /v1/responses", func(w http.ResponseWriter, r *http.Request) { serveResponsesWebSocket(w, r, src) })
	serveModels(mux, src)
	return withCORS(src, mux)
}

// authorized reports whether the request carries a client key: as a bearer token, or in
// x-api-key as Anthropic clients send it; with googleKeys also in x-goog-api-key or the key
// query parameter, as Google's clients send it. With no client keys nothing is authorized.
func authorized(cfg *Config, r *http.Request, googleKeys bool) bool {
	token := clientToken(r, googleKeys)
	if token == "" {
		return false
	}
	match := 0
	for _, k := range cfg.ClientKeys {
		match |= subtle.ConstantTimeCompare([]byte(token), []byte(k))
	}
	return match == 1
}

// clientToken returns the key the client sent: a bearer token, or else x-api-key; with
// googleKeys (Gemini clients), else x-goog-api-key or the key query parameter.
func clientToken(r *http.Request, googleKeys bool) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		token = r.Header.Get("X-Api-Key")
	}
	if token == "" && googleKeys {
		token = r.Header.Get("X-Goog-Api-Key")
		if token == "" {
			token = r.URL.Query().Get("key")
		}
	}
	return token
}

// writeError writes e as the format's error reply and returns its status.
func (f clientFormat) writeError(w http.ResponseWriter, e *llm.Error) int {
	errorBody := f.errorBody
	if errorBody == nil {
		errorBody = openaichat.ErrorResponse
	}
	status, body := errorBody(e)
	w.Header().Set("Content-Type", "application/json")
	// Tell a rate-limited client when to try again, in whole seconds rounded up (RFC 9110
	// section 10.2.3, RFC 6585 section 4).
	if status == http.StatusTooManyRequests && e.RetryAfterMS > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt((e.RetryAfterMS+999)/1000, 10))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return status
}

func serve(w http.ResponseWriter, r *http.Request, g *Generation, f clientFormat) {
	arrived := time.Now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			f.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is too large", Status: http.StatusRequestEntityTooLarge})
			return
		}
		return // the client went away
	}
	req, err := f.parse(r, body)
	if err != nil {
		f.writeError(w, asLLMError(err))
		return
	}
	req.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&req)
	f.nativeHeaders(r, &req)
	t := newTrace()
	r = withTrace(r, t)
	m := g.newMeter(arrived, r.URL.Path, req, clientToken(r, f.googleKeys))
	rr := routeRequest(r, req, body)
	rr.Attempting = func(a route.Attempt) {
		t.account = a.Account
		m.attempt(a, t.id)
	}
	stream := meterStream(g.Router.Generate(r.Context(), rr), m)
	defer stream.Close()
	fail := func(e *llm.Error) {
		logFailure(r, e)
		m.fail(e, f.writeError(w, e))
	}

	// Nothing is sent to the client until the first event, so an error before it becomes a
	// proper HTTP error response. By then the request has gone to its account, if any.
	ev := stream.Next()
	t.setHeader(w)
	if ev.Type == llm.EventError {
		fail(ev.Error)
		return
	}
	var request *llm.Translation
	if ev.Type == llm.EventResponseStart {
		request = ev.Response.Translation
	}
	if !req.Stream {
		agg := f.aggregate(req)
		defer func() { logTranslation(r, request, agg) }()
		tooLarge := func() {
			fail(&llm.Error{Code: llm.CodeUpstream, Message: fmt.Sprintf("the upstream reply is larger than %d MiB, the limit for a non-streamed response; send the request with \"stream\": true", MaxResponseBody>>20)})
		}
		for {
			agg.Add(ev)
			// The terminal event counts too: it can carry content, such as a Responses
			// upstream's final Response object.
			if agg.Size() > MaxResponseBody {
				// Returning closes the stream, which cancels the call and the upstream request.
				tooLarge()
				return
			}
			if ev.Terminal() {
				break
			}
			ev = stream.Next()
		}
		if ev.Type == llm.EventError {
			fail(ev.Error)
			return
		}
		out, err := agg.Response()
		if err != nil {
			e := &llm.Error{Code: llm.CodeInternal, Message: err.Error()}
			m.fail(e, f.writeError(w, e))
			return
		}
		// Rendering can add to what the reply holds, such as losses that name each tool call.
		// shortcut: until then it can hold up to about twice the limit; record those losses as
		// events arrive if that memory matters.
		if agg.Size() > MaxResponseBody {
			tooLarge()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m != nil {
			m.sent = http.StatusOK
		}
		if _, err := w.Write(out); err != nil {
			m.fail(&llm.Error{Code: llm.CodeCancelled}, http.StatusOK) // the client went away
			return
		}
		m.finish(ev)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if m != nil {
		m.sent = http.StatusOK
	}
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
				m.fail(ev.Error, http.StatusOK)
			} else {
				m.finish(ev)
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

// logLossLimit is how many losses one translation log line names; the rest are counted.
const logLossLimit = 20

// logFieldLimit is how many bytes of a loss's path or detail a translation log line keeps.
const logFieldLimit = 200

// logField cuts s to logFieldLimit bytes, on a UTF-8 boundary, and adds an ellipsis when it cuts.
func logField(s string) string {
	if len(s) <= logFieldLimit {
		return s
	}
	cut := logFieldLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// logTranslation logs what translation lost on the way to the upstream (request) and back to
// the client (reply), when anything was lost. The line names at most logLossLimit losses, with
// their paths and details cut by logField, and says how many more there were.
func logTranslation(r *http.Request, request *llm.Translation, reply any) {
	var parts []string
	n := 0
	add := func(side string, l llm.Loss) {
		n++
		if len(parts) < logLossLimit {
			parts = append(parts, fmt.Sprintf("%s %s %s (%s)", side, l.Kind, logField(l.Path), logField(l.Detail)))
		}
	}
	if request != nil && !request.Passthrough {
		for _, l := range request.Losses {
			add("request", l)
		}
	}
	if lr, ok := reply.(lossReporter); ok {
		for _, l := range lr.Losses() {
			add("reply", l)
		}
	}
	if n > 0 {
		line := strings.Join(parts, "; ")
		if n > len(parts) {
			line += fmt.Sprintf("; and %d more", n-len(parts))
		}
		log.Printf("%s: translation: %s", logPrefix(r), line)
	}
}

func logFailure(r *http.Request, e *llm.Error) {
	if e.Code == llm.CodeCancelled {
		return
	}
	log.Printf("%s: %s", logPrefix(r), e.Error())
}
