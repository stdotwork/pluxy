package server

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

// endpoint is a client route served by endpoint calls (protocol minor version 7): a provider
// endpoint beyond generation, whose reply the account's extension writes.
type endpoint struct {
	// format names the endpoint in the call (llm.Native.Format).
	format string
	// headers are the client's request headers that travel with the body.
	headers []string
	// modelOptional makes a request that names no model go to any account (route.Request.AnyModel).
	modelOptional bool
	// form, when set, also takes a multipart/form-data body (or one without a Content-Type),
	// read into its JSON form (formJSON); any other body but JSON is refused.
	form bool
	// defaultModel is the model of a request that names none.
	defaultModel string
	// check refuses a request the endpoint does not take, before it is routed. fromForm is true
	// for a body that came as multipart/form-data.
	check func(body map[string]json.RawMessage, fromForm bool) *llm.Error
	// notJSON, when set, is the message that refuses a body that is not a JSON object.
	notJSON string
	// modelPrefix is a prefix the body's model may carry that is not part of the model's name;
	// routing reads the model without it.
	modelPrefix string
	// googleKeys accepts the client key where Google's clients send it (clientFormat.googleKeys).
	googleKeys bool
	// errorBody returns the status and body of an error reply; nil means the OpenAI shape.
	errorBody func(e *llm.Error) (int, []byte)
}

// compactEndpoint is OpenAI's Responses compaction, which the Codex CLI calls to shorten a long
// conversation. Its reply is one JSON object, so a request that asks for a stream is refused, as
// today's core refuses it.
var compactEndpoint = endpoint{
	format:  llm.FormatOpenAIResponsesCompact,
	headers: openairesponses.Headers,
	check: func(body map[string]json.RawMessage, _ bool) *llm.Error {
		if string(body["stream"]) == "true" {
			return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: "Streaming not supported for compact responses"}
		}
		return nil
	},
}

// alphaSearchEndpoint is Codex's search endpoint. Its body is Codex's own shape, which core passes
// on as it came; the model is optional. The headers are those today's core passes on.
var alphaSearchEndpoint = endpoint{
	format:        llm.FormatCodexAlphaSearch,
	headers:       []string{"version", "user-agent", "session_id", "x-client-request-id"},
	modelOptional: true,
}

// interactionsEndpoint is Gemini's Interactions API. A request names a model or an agent (a
// Google-run agent, which has no model); one with an agent goes to any account, and only a native
// Interactions account serves it. The checks and their messages are today's core's
// (core/sdk/api/handlers/gemini/interactions_handlers.go), and so is the models/ prefix it
// removes from the model. Whether the reply streams is the extension's to say.
var interactionsEndpoint = endpoint{
	format:        llm.FormatGeminiInteractions,
	headers:       []string{"api-revision"},
	modelOptional: true,
	notJSON:       "invalid JSON body",
	modelPrefix:   "models/",
	googleKeys:    true,
	errorBody:     interactionsError,
	check: func(body map[string]json.RawMessage, _ bool) *llm.Error {
		bad := func(msg string) *llm.Error {
			return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: msg}
		}
		named := func(k string) bool {
			var v string
			_ = json.Unmarshal(body[k], &v)
			return strings.TrimSpace(v) != ""
		}
		if named("model") == named("agent") {
			return bad("request requires exactly one of model or agent")
		}
		if s, ok := body["stream"]; ok && string(s) != "true" && string(s) != "false" {
			return bad("stream must be a boolean")
		}
		return nil
	},
}

// interactionsError writes an Interactions error as today's core does: an upstream's Google error
// in Google's shape (geminigenerate.ErrorResponse keeps its code, message, status and details),
// and an error core or the extension raised itself in the OpenAI shape.
func interactionsError(e *llm.Error) (int, []byte) {
	if len(e.Raw) > 0 && e.ProviderCode != "" {
		return geminigenerate.ErrorResponse(e)
	}
	return openaichat.ErrorResponse(e)
}

// endpointRoutes are the client routes endpoint calls serve, by path. /backend-api/codex is where
// the Codex CLI sends a ChatGPT sign-in's requests (its chatgpt_base_url), as today's core serves
// it.
var endpointRoutes = map[string]endpoint{
	"/v1/responses/compact":                compactEndpoint,
	"/backend-api/codex/responses/compact": compactEndpoint,
	"/v1/alpha/search":                     alphaSearchEndpoint,
	"/backend-api/codex/alpha/search":      alphaSearchEndpoint,
	"/v1/images/generations":               imagesGenerationsEndpoint,
	"/v1/images/edits":                     imagesEditsEndpoint,
	"/v1beta/interactions":                 interactionsEndpoint,
}

// serveEndpoint serves a client request to a provider endpoint: the body, a JSON object, goes to
// an account that serves its model as an endpoint call, and the account's extension writes the
// reply. Routing moves on to another account until a reply starts, as for a generate request.
func serveEndpoint(w http.ResponseWriter, r *http.Request, g *Generation, ep endpoint) {
	arrived := time.Now()
	f := clientFormat{errorBody: ep.errorBody} // OpenAI-shaped errors unless the endpoint has its own
	if g.Endpoint == nil {
		f.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "this endpoint is not served"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			f.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Message: "request body is too large", Status: http.StatusRequestEntityTooLarge})
		}
		return
	}
	keepBody(r, body)
	fromForm := false
	if ep.form {
		switch mt := mediaType(r.Header.Get("Content-Type")); {
		case mt == "application/json":
		case mt == "multipart/form-data" || mt == "":
			form, e := formJSON(r.Header.Get("Content-Type"), body)
			if e != nil {
				f.writeError(w, e)
				return
			}
			body, fromForm = form, true
		default:
			f.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: fmt.Sprintf("Invalid request: unsupported Content-Type %q", mt)})
			return
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		msg := cmp.Or(ep.notJSON, "the request body is not a JSON object")
		f.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: msg})
		return
	}
	if ep.check != nil {
		if e := ep.check(fields, fromForm); e != nil {
			f.writeError(w, e)
			return
		}
	}
	var model string
	_ = json.Unmarshal(fields["model"], &model)
	if rest, ok := strings.CutPrefix(strings.TrimSpace(model), ep.modelPrefix); ok && ep.modelPrefix != "" && rest != "" {
		model = rest
	}
	if strings.TrimSpace(model) == "" {
		model = ep.defaultModel
	}
	req := llm.Request{Model: model, Stream: string(fields["stream"]) == "true", Native: &llm.Native{Format: ep.format, Body: body}}
	takeThinkingSuffix(&req)
	clientFormat{headers: ep.headers}.nativeHeaders(r, &req)

	r, t := requestTrace(r)
	m := g.newMeter(r, arrived, req, clientToken(r, ep.googleKeys))
	rr := routeRequest(r, req, body)
	rr.Attempt = g.Endpoint
	rr.AnyModel = ep.modelOptional
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

	ev := stream.Next()
	t.setHeader(w)
	if ev.Type == llm.EventError {
		fail(ev.Error)
		return
	}
	var reply llm.EndpointReply
	if ev.Type != llm.EventResponseStart || ev.Response == nil || json.Unmarshal(ev.Response.Raw, &reply) != nil || reply.ContentType == "" {
		fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: "the extension's endpoint reply does not start with its content type"})
		return
	}
	if ev.Response.Format != ep.format {
		// A reply for another endpoint must not reach this one's client.
		fail(&llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("the extension answered a %s request with a %s reply", ep.format, ev.Response.Format)})
		return
	}
	sse := strings.HasPrefix(reply.ContentType, "text/event-stream")
	w.Header().Set("Content-Type", reply.ContentType)
	if sse {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(http.StatusOK)
	if m != nil {
		m.sent = http.StatusOK
	}
	rc := http.NewResponseController(w)
	for {
		ev = stream.Next()
		switch ev.Type {
		case llm.EventNative:
			var chunk string
			if json.Unmarshal(ev.Raw, &chunk) != nil {
				e := &llm.Error{Code: llm.CodeExtensionProtocol, Message: "an endpoint reply's body piece is not a JSON string"}
				logFailure(r, e)
				m.fail(e, http.StatusOK)
				return
			}
			if _, err := io.WriteString(w, chunk); err != nil {
				m.fail(&llm.Error{Code: llm.CodeCancelled}, http.StatusOK) // the client went away
				return
			}
			if sse {
				if err := rc.Flush(); err != nil {
					m.fail(&llm.Error{Code: llm.CodeCancelled}, http.StatusOK)
					return
				}
			}
		case llm.EventFinish:
			m.finish(ev)
			return
		case llm.EventError:
			// The reply has started: a stream gets the error as its last event; any other body
			// ends where it broke off.
			logFailure(r, ev.Error)
			if sse {
				_, body := openaichat.ErrorResponse(ev.Error)
				_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", body)
				_ = rc.Flush()
			}
			m.fail(ev.Error, http.StatusOK)
			return
		default:
			// Items have no place in an endpoint's reply; the extension broke the protocol.
			e := &llm.Error{Code: llm.CodeExtensionProtocol, Message: fmt.Sprintf("an endpoint reply carries a %s event", ev.Type)}
			logFailure(r, e)
			m.fail(e, http.StatusOK)
			return
		}
	}
}
