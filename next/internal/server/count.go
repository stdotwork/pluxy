package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type countFormat struct {
	clientFormat
	format string
	reply  func(tokens int64) []byte
}

var messagesCount = countFormat{
	clientFormat: clientFormat{
		format:    llm.FormatAnthropicMessages,
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

func serveCount(w http.ResponseWriter, r *http.Request, g *Generation, f countFormat) {
	if g.Count == nil {
		f.writeError(w, &llm.Error{Code: llm.CodeNotFound, Status: http.StatusNotFound, Message: "token counting is not served"})
		return
	}
	body, e, ok := readRequestBody(w, r)
	if !ok {
		g.cancelledUnread(w, r, f.clientFormat, time.Time{}, false)
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
	req.Stream = false
	req.DropPolicy = g.Config.DropPolicy
	takeThinkingSuffix(&req)
	f.nativeHeaders(r, &req)

	r, t := requestTrace(r)
	if cancelledBeforeRouting(w, r, f.clientFormat, nil) {
		return
	}
	rr := routeRequest(r, req, body)
	var cannot atomic.Bool
	rr.Attempt = func(ctx context.Context, ext string, a protocol.Account, req llm.Request) route.Stream {
		return &countWatch{Stream: g.Count(ctx, ext, a, req), cannot: &cannot}
	}
	rr.Attempting = t.attempt
	stream := g.Router.Generate(host.WithPayloadClient(r.Context(), req.Model, r.Header), rr)
	defer stream.Close()
	ev := stream.Next()
	t.setHeaders(w, g.Config.Routing.ExplainOn())
	var format string
	var replyHeaders map[string]string
	for ; !ev.Terminal(); ev = stream.Next() {
		if ev.Type == llm.EventResponseStart && ev.Response != nil {
			format = ev.Response.Format
			replyHeaders = ev.Response.Headers
		}
	}
	if ev.Type == llm.EventError {
		if (cannot.Load() || isCannotCount(ev.Error)) && g.Config.TokenCountEstimateOn() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(f.reply(estimateInputTokens(req)))
			return
		}
		logFailure(r, ev.Error)
		f.writeError(w, t.explained(ev.Error))
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
	f.writeRateLimitHeaders(w, replyHeaders)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

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

const (
	countMethod              = "countTokens"
	embedContentMethod       = "embedContent"
	batchEmbedContentsMethod = "batchEmbedContents"
)

func geminiAction(w http.ResponseWriter, r *http.Request, g *Generation) {
	model, method, _ := strings.Cut(r.PathValue("action"), ":")
	switch method {
	case countMethod:
		serveCount(w, r, g, geminiCount)
		return
	case embedContentMethod, batchEmbedContentsMethod:
		if strings.TrimSpace(model) == "" {
			geminiFormat.writeError(w, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: "model is required in the request path"})
			return
		}
		ep := geminiEmbedContentEndpoint
		if method == batchEmbedContentsMethod {
			ep = geminiBatchEmbedContentsEndpoint
		}
		serveEndpointWithModel(w, r, g, ep, model)
		return
	}
	serve(w, r, g, geminiFormat)
}

func isCannotCount(e *llm.Error) bool {
	return e != nil && e.Code == llm.CodeUnsupported && e.Scope != llm.ScopeRequest && len(e.Dropped) == 0
}

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
