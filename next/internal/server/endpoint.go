package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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
	// check refuses a request the endpoint does not take, before it is routed.
	check func(body map[string]json.RawMessage) *llm.Error
}

// compactEndpoint is OpenAI's Responses compaction, which the Codex CLI calls to shorten a long
// conversation. Its reply is one JSON object, so a request that asks for a stream is refused, as
// today's core refuses it.
var compactEndpoint = endpoint{
	format:  llm.FormatOpenAIResponsesCompact,
	headers: openairesponses.Headers,
	check: func(body map[string]json.RawMessage) *llm.Error {
		if string(body["stream"]) == "true" {
			return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: "Streaming not supported for compact responses"}
		}
		return nil
	},
}

// endpointRoutes are the client routes endpoint calls serve, by path. /backend-api/codex is where
// the Codex CLI sends a ChatGPT sign-in's requests (its chatgpt_base_url), as today's core serves
// it.
var endpointRoutes = map[string]endpoint{
	"/v1/responses/compact":                compactEndpoint,
	"/backend-api/codex/responses/compact": compactEndpoint,
}

// serveEndpoint serves a client request to a provider endpoint: the body, a JSON object, goes to
// an account that serves its model as an endpoint call, and the account's extension writes the
// reply. Routing moves on to another account until a reply starts, as for a generate request.
func serveEndpoint(w http.ResponseWriter, r *http.Request, g *Generation, ep endpoint) {
	arrived := time.Now()
	var f clientFormat // OpenAI-shaped errors
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
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil || fields == nil {
		f.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest, Message: "the request body is not a JSON object"})
		return
	}
	if ep.check != nil {
		if e := ep.check(fields); e != nil {
			f.writeError(w, e)
			return
		}
	}
	var model string
	_ = json.Unmarshal(fields["model"], &model)
	req := llm.Request{Model: model, Native: &llm.Native{Format: ep.format, Body: body}}
	takeThinkingSuffix(&req)
	clientFormat{headers: ep.headers}.nativeHeaders(r, &req)

	r, t := requestTrace(r)
	m := g.newMeter(r, arrived, req, clientToken(r, false))
	rr := routeRequest(r, req, body)
	rr.Attempt = g.Endpoint
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
