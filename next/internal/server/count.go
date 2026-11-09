package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// countFormat is a client API's token count: Anthropic's /v1/messages/count_tokens or Gemini's
// countTokens. The request goes to an account's extension as a count_tokens call, routed as a
// generate request is.
type countFormat struct {
	clientFormat
	// format is the client's format: an upstream count reply in it is passed on as it came.
	format string
	// reply is the client's count reply for a count from an upstream in another format.
	reply func(tokens int64) []byte
}

var messagesCount = countFormat{
	clientFormat: clientFormat{
		parse:     bodyOnly(anthropicmessages.ParseRequest),
		errorBody: anthropicmessages.ErrorResponse,
		headers:   messagesFormat.headers,
	},
	format: llm.FormatAnthropicMessages,
	reply: func(n int64) []byte {
		b, _ := json.Marshal(map[string]int64{"input_tokens": n})
		return b
	},
}

// geminiCount reads a countTokens body: contents, or a whole GenerateContent request under
// generateContentRequest. The body as the client sent it is the native body.
var geminiCount = countFormat{
	clientFormat: clientFormat{
		parse: func(r *http.Request, body []byte) (llm.Request, error) {
			model, _, _ := strings.Cut(r.PathValue("action"), ":")
			var wrapped struct {
				GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
			}
			inner := body
			if json.Unmarshal(body, &wrapped) == nil && len(wrapped.GenerateContentRequest) > 0 {
				inner = wrapped.GenerateContentRequest
			}
			req, err := geminigenerate.ParseRequest(model, false, inner)
			if err != nil {
				return req, err
			}
			req.Native.Body = append(json.RawMessage(nil), body...)
			return req, nil
		},
		errorBody:  geminigenerate.ErrorResponse,
		googleKeys: true,
	},
	format: llm.FormatGeminiGenerate,
	reply: func(n int64) []byte {
		b, _ := json.Marshal(map[string]any{"totalTokens": n, "promptTokensDetails": []any{map[string]any{"modality": "TEXT", "tokenCount": n}}})
		return b
	},
}

// serveCount answers a token count: the upstream's own count reply when it is in the client's
// format, otherwise the count in the client's shape. When no account can count, core's estimate
// in the client's shape (token_count_estimate).
func serveCount(w http.ResponseWriter, r *http.Request, g *Generation, f countFormat) {
	if g.Count == nil {
		f.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "token counting is not served"})
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
	req, err := f.parse(r, body)
	if err != nil {
		f.writeError(w, asLLMError(err))
		return
	}
	req.Stream = false
	req.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&req)
	f.nativeHeaders(r, &req)

	r, t := requestTrace(r)
	rr := routeRequest(r, req, body)
	// Whether an attempt reached an account that cannot count: today's core counts such an
	// account itself, so the request then gets an estimate whatever the other accounts answered.
	var cannot atomic.Bool
	rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		return &countWatch{Stream: g.Count(ctx, ext, a, req), cannot: &cannot}
	}
	rr.Attempting = func(a route.Attempt) { t.account = a.Account }
	stream := g.Router.Generate(r.Context(), rr)
	defer stream.Close()
	ev := stream.Next()
	t.setHeader(w)
	var format string
	for ; !ev.Terminal(); ev = stream.Next() {
		if ev.Type == llm.EventResponseStart && ev.Response != nil {
			format = ev.Response.Format
		}
	}
	if ev.Type == llm.EventError {
		// An account that cannot count was reached (its upstream has no count endpoint): core
		// estimates, as today's core counts such an account itself, unless that is turned off.
		// Without one, an upstream that counts and refuses, or a request an extension refuses,
		// stays an error.
		if (cannot.Load() || isCannotCount(ev.Error)) && g.Config.TokenCountEstimateOn() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(f.reply(estimateInputTokens(req)))
			return
		}
		logFailure(r, ev.Error)
		f.writeError(w, ev.Error)
		return
	}
	u := ev.Usage
	if u == nil || u.InputTokens == nil {
		e := &llm.Error{Code: llm.CodeExtensionProtocol, Message: "the extension's count reply has no input_tokens"}
		logFailure(r, e)
		f.writeError(w, e)
		return
	}
	out := f.reply(*u.InputTokens)
	if format == f.format && json.Valid(u.Raw) && len(u.Raw) > 0 && u.Raw[0] == '{' {
		out = u.Raw
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// nativeHeaders copies the client's headers that belong to the format's body into the native
// body's headers.
func (f clientFormat) nativeHeaders(r *http.Request, req *llm.Request) {
	if req.Native == nil {
		return
	}
	for _, h := range f.headers {
		if v := r.Header.Values(h); len(v) > 0 {
			if req.Native.Headers == nil {
				req.Native.Headers = map[string]string{}
			}
			req.Native.Headers[h] = strings.Join(v, ",")
		}
	}
}

// countMethod is the Gemini method that counts tokens.
const countMethod = "countTokens"

// geminiAction serves /v1beta/models/{model}:{method}: countTokens counts, the other methods
// generate.
func geminiAction(w http.ResponseWriter, r *http.Request, g *Generation) {
	if _, method, _ := strings.Cut(r.PathValue("action"), ":"); method == countMethod {
		serveCount(w, r, g, geminiCount)
		return
	}
	serve(w, r, g, geminiFormat)
}

// isCannotCount reports whether e is an account's answer that its upstream cannot count tokens.
func isCannotCount(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeUnsupported && e.Scope != llm.ScopeRequest
}

// countWatch is a count attempt's stream that notes when the account answers it cannot count.
type countWatch struct {
	route.Stream
	cannot *atomic.Bool
}

func (s *countWatch) Next() llm.Event {
	ev := s.Stream.Next()
	if ev.Type == llm.EventError && isCannotCount(ev.Error) {
		s.cannot.Store(true)
	}
	return ev
}
