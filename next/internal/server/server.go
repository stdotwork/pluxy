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
	"sync"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/cred"
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

var responsesFormat = clientFormat{
	parse:     bodyOnly(openairesponses.ParseRequest),
	stream:    func(w io.Writer, req llm.Request) eventWriter { return openairesponses.NewStreamWriter(w, req) },
	aggregate: func(req llm.Request) aggregator { return openairesponses.NewAggregator(req) },
}

var messagesFormat = clientFormat{
	parse:     bodyOnly(anthropicmessages.ParseRequest),
	stream:    func(w io.Writer, _ llm.Request) eventWriter { return anthropicmessages.NewStreamWriter(w) },
	aggregate: func(llm.Request) aggregator { return anthropicmessages.NewAggregator() },
	errorBody: anthropicmessages.ErrorResponse,
	headers:   anthropicmessages.Headers,
}

// geminiFormat serves Gemini's GenerateContent at /v1beta/models/{model}:{method}. The method
// says whether the reply streams; a stream is always server-sent events, with or without
// alt=sse, as today's core serves it.
var geminiFormat = clientFormat{
	parse: func(r *http.Request, body []byte) (llm.Request, error) {
		model, method, _ := strings.Cut(r.PathValue("action"), ":")
		switch method {
		case "generateContent", "streamGenerateContent":
		default:
			// shortcut: countTokens is not served; no client in the corpus relies on it.
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

// New returns core's HTTP handler. Requests are routed over cfg's accounts and, when creds is
// not nil, the accounts added by login; each attempt is a call to generate. With a management
// key in cfg, it also serves the management API.
func New(cfg *Config, generate GenerateFunc, creds *cred.Manager) (http.Handler, error) {
	router, err := route.New(cfg.Routing, cfg.Extension.Accounts, func(ctx context.Context, a protocol.Account, req llm.Request) route.Stream {
		return generate(ctx, a, req)
	}, nil)
	if err != nil {
		return nil, err
	}
	if creds != nil {
		router.UseCredentials(creds)
		// One update at a time, each reading the accounts when it runs: an update that read an
		// older set can never be published after a newer one.
		var mu sync.Mutex
		update := func() {
			mu.Lock()
			defer mu.Unlock()
			accounts := slices.Clone(cfg.Extension.Accounts)
			for _, a := range creds.Accounts() {
				if slices.ContainsFunc(cfg.Extension.Accounts, func(c route.Account) bool { return c.ID == a.ID }) {
					log.Printf("account %q is both in the config and added by login; using the config's", a.ID)
					continue
				}
				accounts = append(accounts, a)
			}
			if err := router.SetAccounts(accounts); err != nil {
				log.Printf("updating accounts: %v", err)
			}
		}
		creds.OnChange(update)
		update()
	}
	mux := http.NewServeMux()
	addManagement(mux, cfg, creds)
	formats := map[string]clientFormat{"/v1/chat/completions": chatFormat, "/v1/responses": responsesFormat, "/v1/messages": messagesFormat,
		"/v1beta/models/{action}": geminiFormat}
	for path, f := range formats {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			if !authorized(cfg, r, f.googleKeys) {
				f.writeError(w, &llm.Error{Code: llm.CodeAuth, Message: "missing or invalid API key", Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key"})
				return
			}
			serve(w, r, cfg, router, f)
		})
	}
	return mux, nil
}

// authorized reports whether the request carries a client key: as a bearer token, or in
// x-api-key as Anthropic clients send it; with googleKeys also in x-goog-api-key or the key
// query parameter, as Google's clients send it.
func authorized(cfg *Config, r *http.Request, googleKeys bool) bool {
	if len(cfg.ClientKeys) == 0 {
		return true
	}
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

func (f clientFormat) writeError(w http.ResponseWriter, e *llm.Error) {
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
}

func serve(w http.ResponseWriter, r *http.Request, cfg *Config, router *route.Router, f clientFormat) {
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
	req.DropPolicy = cfg.DropPolicy
	if req.Native != nil {
		for _, h := range f.headers {
			if v := r.Header.Values(h); len(v) > 0 {
				if req.Native.Headers == nil {
					req.Native.Headers = map[string]string{}
				}
				req.Native.Headers[h] = strings.Join(v, ",")
			}
		}
	}
	stream := router.Generate(r.Context(), routeRequest(r, req, body))
	defer stream.Close()

	// Nothing is sent to the client until the first event, so an error before it becomes a
	// proper HTTP error response.
	ev := stream.Next()
	if ev.Type == llm.EventError {
		logFailure(r, ev.Error)
		f.writeError(w, ev.Error)
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
				f.writeError(w, e)
				return
			}
			ev = stream.Next()
		}
		if ev.Type == llm.EventError {
			logFailure(r, ev.Error)
			f.writeError(w, ev.Error)
			return
		}
		out, err := agg.Response()
		if err != nil {
			f.writeError(w, &llm.Error{Code: llm.CodeInternal, Message: err.Error()})
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
