package server

import (
	"compress/gzip"
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
	"github.com/stdotwork/pluxy/next/internal/host"
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
const MaxResponseBody = llm.MaxNonStreamedReplyBytes

// Stream is the reply to a generate call: events until one terminal event.
type Stream interface {
	Next() llm.Event
	Close()
}

// GenerateFunc starts a generate call on the provider extension.
type GenerateFunc func(ctx context.Context, account protocol.Account, req llm.Request) Stream

// clientFormat is a client API core serves: how to read its requests and write its replies.
type clientFormat struct {
	// format is the client's canonical wire format (such as llm.FormatOpenAIChat), used to
	// decide which upstream rate-limit response headers belong to this client's format.
	format    string
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
	// noKeepAlive keeps keep-alive comments off the format's streams: Google's clients read
	// every SSE line as JSON, and Google's API never sends a comment.
	noKeepAlive bool
}

// markParsed records that req's items, tools and origin are what its native body parses to
// (llm.Native.Parsed), as they are right after a format's parser made req: core may then leave
// them out of a generate call to an extension that parses the body itself. Every parser a
// clientFormat or the Responses WebSocket reads a generate request with keeps the body it parsed
// as the native body; the legacy completions parser keeps the Chat body it parsed.
func markParsed(req *llm.Request) {
	if req.Native != nil {
		req.Native.Parsed = req.Native.Body
	}
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
	format: llm.FormatOpenAIChat,
	parse:  bodyOnly(openaichat.ParseRequest),
	stream: func(w io.Writer, req llm.Request) eventWriter {
		return openaichat.NewStreamWriter(w, openaichat.WantsStreamUsage(req))
	},
	aggregate: func(llm.Request) aggregator { return openaichat.NewAggregator() },
}

// completionsFormat serves OpenAI's legacy text completions as Chat Completions (see
// openaichat.ParseCompletionsRequest).
var completionsFormat = clientFormat{
	format: llm.FormatOpenAIChat,
	parse:  bodyOnly(openaichat.ParseCompletionsRequest),
	stream: func(w io.Writer, req llm.Request) eventWriter {
		return openaichat.NewCompletionsStreamWriter(w, openaichat.WantsStreamUsage(req))
	},
	aggregate: func(llm.Request) aggregator { return openaichat.NewCompletionsAggregator() },
}

var responsesFormat = clientFormat{
	format:    llm.FormatOpenAIResponses,
	parse:     bodyOnly(openairesponses.ParseRequest),
	stream:    func(w io.Writer, req llm.Request) eventWriter { return openairesponses.NewStreamWriter(w, req) },
	aggregate: func(req llm.Request) aggregator { return openairesponses.NewAggregator(req) },
	headers:   openairesponses.Headers,
}

var messagesFormat = clientFormat{
	format:    llm.FormatAnthropicMessages,
	parse:     bodyOnly(anthropicmessages.ParseRequest),
	stream:    func(w io.Writer, _ llm.Request) eventWriter { return anthropicmessages.NewStreamWriter(w) },
	aggregate: func(llm.Request) aggregator { return anthropicmessages.NewAggregator() },
	errorBody: anthropicmessages.ErrorResponse,
	headers:   slices.Concat(anthropicmessages.Headers, anthropicmessages.ClientHeaders, anthropicmessages.ClaudeCodeHeaders),
}

// geminiFormat serves Gemini's GenerateContent at /v1beta/models/{model}:{method}. The model may
// hold slashes ("publisher/model"), as today's core's catch-all route accepts it. The method
// says whether the reply streams; a stream is always server-sent events, with or without
// alt=sse, as today's core serves it.
var geminiFormat = clientFormat{
	format: llm.FormatGeminiGenerate,
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
	stream:      func(w io.Writer, _ llm.Request) eventWriter { return geminigenerate.NewStreamWriter(w) },
	aggregate:   func(llm.Request) aggregator { return geminigenerate.NewAggregator() },
	errorBody:   geminigenerate.ErrorResponse,
	googleKeys:  true,
	noKeepAlive: true,
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
	// Endpoint runs one endpoint attempt (a provider endpoint beyond generation, such as
	// Responses compaction); nil when endpoints are not served.
	Endpoint route.AttemptFunc
	// OpenSession opens a session on an extension; nil when sessions are not served.
	OpenSession OpenSessionFunc
	// RequestLog writes request logs; nil: none.
	RequestLog *RequestLog
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
		mux.HandleFunc("POST "+path, accessLog(func(w http.ResponseWriter, r *http.Request) {
			g, release := src.Acquire()
			defer release()
			if e := refused(g.Config, r, f.googleKeys); e != nil {
				f.writeError(w, e)
				return
			}
			// Every reply to a request that may use the API has the trace ID, also one sent before
			// the request reaches routing (a body that does not parse, no account for the model).
			r, t := requestTrace(r)
			w.Header().Set(TraceHeader, t.id)
			logged(w, r, g, func(w http.ResponseWriter, r *http.Request) { h(w, r, g) })
		}))
	}
	// The Codex CLI sends its requests to /backend-api/codex/responses when its chatgpt_base_url
	// points at core; as in today's core, that path is an alias of /v1/responses. Droid's OpenAI
	// provider posts to the bare /responses, which is the same route.
	for path, f := range map[string]clientFormat{"/v1/chat/completions": chatFormat, "/v1/completions": completionsFormat,
		"/v1/responses": responsesFormat, "/responses": responsesFormat, "/backend-api/codex/responses": responsesFormat, "/v1/messages": messagesFormat} {
		handle(path, f, func(w http.ResponseWriter, r *http.Request, g *Generation) { serve(w, r, g, f) })
	}
	handle("/v1/messages/count_tokens", messagesFormat, func(w http.ResponseWriter, r *http.Request, g *Generation) {
		serveCount(w, r, g, messagesCount)
	})
	handle("/v1beta/models/{action...}", geminiFormat, geminiAction)
	for path, ep := range endpointRoutes {
		handle(path, clientFormat{googleKeys: ep.googleKeys, errorBody: ep.errorBody}, func(w http.ResponseWriter, r *http.Request, g *Generation) { serveEndpoint(w, r, g, ep) })
	}
	// The WebSocket, on /v1/responses and on its aliases (see above).
	for _, path := range []string{"/v1/responses", "/responses", "/backend-api/codex/responses"} {
		mux.HandleFunc("GET "+path, accessLog(func(w http.ResponseWriter, r *http.Request) { serveResponsesWebSocket(w, r, src) }))
	}
	serveModels(mux, src)
	// Claude Code probes its configured Anthropic endpoint before sending Messages. Answer the
	// routes locally: the hello probe needs no account, and telemetry is accepted and dropped.
	mux.HandleFunc("GET /api/hello", serveClaudeCodeProbe)
	mux.HandleFunc("POST /api/event_logging/batch", serveClaudeCodeProbe)
	// The health probe a container or load balancer sends. It takes no key and reads no config,
	// so it is not refused, counted or logged as a client request.
	mux.HandleFunc("GET /healthz", serveHealthz)
	return withCORS(src, mux)
}

// serveClaudeCodeProbe answers a Claude Code probe with the status it expects. A GET pattern
// also matches HEAD; net/http sends HEAD's headers and no body.
func serveClaudeCodeProbe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// serveHealthz answers 200 with the text "ok". A GET pattern also matches HEAD; net/http sends
// HEAD's headers and no body.
func serveHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

// refused returns the error that answers a request that may not use the client API, or nil when
// it may: a request that carries no configured client key is refused as unauthorized; one that
// does is refused as forbidden while a client key is one of the example config's public ones.
// Today's core does the same (its example API-key safe mode), and the config's notices say so
// at every load. Only a request with a configured key learns of the state.
func refused(cfg *Config, r *http.Request, googleKeys bool) *llm.Error {
	if !authorized(cfg, r, googleKeys) {
		return errClientKey
	}
	if ex := cfg.ExampleClientKeys(); len(ex) > 0 {
		return &llm.Error{Code: llm.CodePermission, Status: http.StatusForbidden, ProviderCode: ExampleKeyCode,
			Message: "the client API is disabled because client_keys holds the example config's key " + strings.Join(ex, ", ") +
				"; replace it with your own key, then retry"}
	}
	return nil
}

// errClientKey is the refusal of a missing or wrong client key. writeError and the WebSocket's
// sendError tell it by identity, since an upstream's 401 can have the same code and status. It is
// shared between requests, so nothing may change it.
var errClientKey = &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"}

// ExampleKeyCode is the provider code of the error that refuses requests while a client key is
// one of the example config's.
const ExampleKeyCode = "unsafe_example_api_key"

// authorized reports whether the request carries a client key: as a bearer token, or in
// x-api-key as Anthropic clients send it; with googleKeys also in x-goog-api-key or the key
// query parameter, as Google's clients send it. With no client keys nothing is authorized.
func authorized(cfg *Config, r *http.Request, googleKeys bool) bool {
	token := clientToken(r, googleKeys)
	if token == "" {
		return false
	}
	// The token is hashed once and compared with each key's digest, none ending the loop early,
	// so the time the check takes shows neither which key matched nor how long a key is.
	sent := cfg.digest([]byte(token))
	match := 0
	for _, d := range cfg.clientKeyDigests() {
		match |= subtle.ConstantTimeCompare(sent[:], d[:])
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

// writeRateLimitHeaders writes the subset of headers that are documented rate-limit headers for
// f.format. Headers outside f.format's allowlist are ignored.
func (f clientFormat) writeRateLimitHeaders(w http.ResponseWriter, headers map[string]string) {
	if f.format == "" || len(headers) == 0 {
		return
	}
	allowed := llm.FilterRateLimitHeaders(f.format, headers)
	if len(allowed) == 0 {
		return
	}
	keys := make([]string, 0, len(allowed))
	for k := range allowed {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := w.Header()
	for _, k := range keys {
		h.Set(k, allowed[k])
	}
}

// writeError writes e as the format's error reply and returns its status.
func (f clientFormat) writeError(w http.ResponseWriter, e *llm.Error) int {
	errorBody := f.errorBody
	if errorBody == nil {
		errorBody = openaichat.ErrorResponse
	}
	status, body := errorBody(e)
	if e != nil && e.UsageLimit == nil && e != errClientKey && len(e.Headers) > 0 {
		f.writeRateLimitHeaders(w, e.Headers)
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case e.UsageLimit != nil:
		// The usage-limit reply tells the client to stop until the reset, not when to retry.
		for _, h := range e.UsageLimit.Headers() {
			w.Header().Set(h[0], h[1])
		}
	case e == errClientKey:
		// Trying again with the same key cannot succeed.
		w.Header().Set("X-Should-Retry", "false")
	case (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || status == 529) && e.RetryAfterMS > 0:
		// Tell a rate-limited or overloaded client when to try again, in whole seconds rounded up
		// (RFC 9110 section 10.2.3, RFC 6585 section 4).
		w.Header().Set("Retry-After", strconv.FormatInt(int64(llm.WholeSeconds(time.Duration(e.RetryAfterMS)*time.Millisecond)/time.Second), 10))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return status
}

func serve(w http.ResponseWriter, r *http.Request, g *Generation, f clientFormat) {
	arrived := time.Now()
	body, e, ok := readRequestBody(w, r)
	if !ok {
		return
	}
	if e != nil {
		f.writeError(w, e)
		return
	}
	keepBody(r, body)
	req, err := f.parse(r, body)
	if err != nil {
		f.writeError(w, asLLMError(err))
		return
	}
	markParsed(&req)
	req.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&req)
	f.nativeHeaders(r, &req)
	r, t := requestTrace(r)
	m := g.newMeter(r, arrived, req, clientToken(r, f.googleKeys))
	rr := routeRequest(r, req, body)
	rr.Attempting = func(a route.Attempt) {
		t.attempt(a)
		m.attempt(a, t.id)
	}
	rr.AttemptFailed = m.attemptFailed
	stream := meterStream(g.Router.Generate(host.WithPayloadClient(r.Context(), req.Model, r.Header), rr), m)
	defer stream.Close()
	fail := func(e *llm.Error) {
		logFailure(r, e)
		m.fail(e, f.writeError(w, t.explained(e)))
	}

	// Nothing is sent to the client until the first event, so an error before it becomes a
	// proper HTTP error response. By then the request has gone to its account, if any.
	ev := stream.Next()
	t.setHeaders(w, g.Config.Routing.ExplainOn())
	if ev.Type == llm.EventError {
		fail(ev.Error)
		return
	}
	var request *llm.Translation
	var replyHeaders map[string]string
	if ev.Type == llm.EventResponseStart && ev.Response != nil {
		request = ev.Response.Translation
		replyHeaders = ev.Response.Headers
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
		f.writeRateLimitHeaders(w, replyHeaders)
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

	f.writeRateLimitHeaders(w, replyHeaders)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if m != nil {
		m.sent = http.StatusOK
	}
	rc := http.NewResponseController(w)
	var out io.Writer = w
	flush := rc.Flush
	var ka *keepAlive
	if !f.noKeepAlive {
		ka = startKeepAlive(w, rc)
		defer ka.stop()
		out, flush = ka, ka.Flush
	}
	sw := f.stream(out, req)
	defer func() { logTranslation(r, request, sw) }()
	for {
		// Writes block while the client is slow, and the next event is not read until the
		// write is done: that is the backpressure path to the extension.
		if ev.Terminal() && ka != nil {
			ka.finish()
		}
		if err := sw.Write(ev); err != nil {
			return
		}
		if err := flush(); err != nil {
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

// readRequestBody reads a client request body within MaxRequestBody after undoing gzip content
// encoding. A gzip body that expands past the limit is refused before it can reach the parser.
func readRequestBody(w http.ResponseWriter, r *http.Request) ([]byte, *llm.Error, bool) {
	encoding, supported := requestContentEncoding(r.Header)
	if !supported {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusUnsupportedMediaType, Scope: llm.ScopeRequest,
			Message: fmt.Sprintf("unsupported Content-Encoding %q", encoding)}, true
	}

	var reader io.Reader = r.Body
	if strings.EqualFold(encoding, "gzip") {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: "request body is not valid gzip"}, true
		}
		defer zr.Close()
		reader = zr
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, io.NopCloser(reader), MaxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is too large", Status: http.StatusRequestEntityTooLarge}, true
		}
		if strings.EqualFold(encoding, "gzip") && r.Context().Err() == nil {
			return nil, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: "request body is not valid gzip"}, true
		}
		return nil, nil, false // the client went away
	}
	r.Header.Del("Content-Encoding")
	r.ContentLength = int64(len(body))
	return body, nil, true
}

// requestContentEncoding returns the one content coding core decodes or the name to refuse.
// Repeated identity codings add nothing, and one gzip coding is supported.
func requestContentEncoding(h http.Header) (string, bool) {
	var codings []string
	for _, value := range h.Values("Content-Encoding") {
		for _, coding := range strings.Split(value, ",") {
			coding = strings.TrimSpace(coding)
			if coding != "" && !strings.EqualFold(coding, "identity") {
				codings = append(codings, coding)
			}
		}
	}
	if len(codings) == 0 {
		return "", true
	}
	if len(codings) == 1 && strings.EqualFold(codings[0], "gzip") {
		return "gzip", true
	}
	return strings.Join(codings, ", "), false
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
// the client (reply), when anything was lost. A passthrough request can lose something too: its
// endpoint's repairs (Codex's history repairs, an effort clamp, a thinking fit) change the body
// the client sent. The line names at most logLossLimit losses, with their paths and details cut
// by logField, and says how many more there were.
func logTranslation(r *http.Request, request *llm.Translation, reply any) {
	var parts []string
	n := 0
	add := func(side string, l llm.Loss) {
		n++
		if len(parts) < logLossLimit {
			parts = append(parts, fmt.Sprintf("%s %s %s (%s)", side, l.Kind, logField(l.Path), logField(l.Detail)))
		}
	}
	if request != nil {
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
	if t := traceOf(r); t != nil {
		t.failed = true
	}
	log.Printf("%s: %s%s", logPrefix(r), e.Error(), routedSummary(e.Routed))
}

// logRoutedLimit is how many accounts a failure line names under tried, and under left_out; the
// rest are counted.
const logRoutedLimit = 8

// routedSummary returns what a failure line adds to the error's own message when routing ended
// the request: the model asked for, each account tried with how it ended, and each account left
// out with why, as " model=m tried=[acc_1(p): rate_limit; ...] left_out=[acc_2: cooling until
// 2026-11-09T12:03:10Z; ...]". It is empty for an error routing did not end. It holds what the
// client's `pluxy` member holds (public account IDs, providers, models, result codes, health
// states and times) and never an upstream's words, so it is logged whether or not
// routing.explain shows it to the client.
func routedSummary(x *llm.Explain) string {
	if x == nil {
		return ""
	}
	var b strings.Builder
	if x.Model != "" {
		b.WriteString(" model=" + logToken(x.Model))
	}
	var tried []string
	for _, t := range x.Tried {
		tried = append(tried, logToken(t.Account)+routedTarget(t.Provider, t.Model, x.Model)+": "+logToken(t.Result))
	}
	b.WriteString(" tried=" + routedList(tried))
	var left []string
	for _, l := range x.LeftOut {
		s := logToken(l.Account) + routedTarget(l.Provider, l.Model, x.Model) + ": " + logToken(l.State)
		if l.Until != nil {
			s += " until " + l.Until.UTC().Format(time.RFC3339)
		}
		left = append(left, s)
	}
	b.WriteString(" left_out=" + routedList(left))
	return b.String()
}

// routedTarget is "(provider)", or "(provider model)" when the model sent upstream is not the one
// asked for; it is empty when the entry names no provider.
func routedTarget(provider, model, asked string) string {
	if provider == "" {
		return ""
	}
	if model != "" && model != asked {
		return "(" + logToken(provider) + " " + logToken(model) + ")"
	}
	return "(" + logToken(provider) + ")"
}

// routedList joins entries as "[a; b]", naming the first logRoutedLimit and counting the rest.
func routedList(entries []string) string {
	more := ""
	if len(entries) > logRoutedLimit {
		more = fmt.Sprintf("; +%d more", len(entries)-logRoutedLimit)
		entries = entries[:logRoutedLimit]
	}
	return "[" + strings.Join(entries, "; ") + more + "]"
}

// logToken is logField(s), quoted when it holds a space, a quote, a bracket, a semicolon or a
// control character, so one field cannot pass for two or end the line.
func logToken(s string) string {
	s = logField(s)
	if strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`"[];()`, r)
	}) {
		return strconv.Quote(s)
	}
	return s
}
