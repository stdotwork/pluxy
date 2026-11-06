package openairesponses

import (
	"encoding/json"
	"net/http"

	"github.com/stdotwork/pluxy/next/llm"
)

// Responses over WebSocket (OpenAI's WebSocket mode): a client sends response.create messages
// on one socket and gets the stream events of each turn back as text messages. A turn may name
// the previous response on the same socket with previous_response_id, whose state lives only on
// that socket. These are the errors both sides of pluxy give for that, in the words today's core
// uses, so core (which keeps the conversation for an account without the upstream WebSocket) and
// a provider extension (which holds the upstream socket) answer alike.

// WebSocket error provider codes.
const (
	CodePreviousResponseNotFound = "previous_response_not_found"
	CodeUpstreamWebsocketLost    = "upstream_websocket_lost"
	CodeMessageTooBig            = "message_too_big"
)

// PreviousResponseNotFound is the error for a turn that continues a response the socket does not
// hold: the client must send the whole conversation again.
func PreviousResponseNotFound() *llm.Error {
	const msg = "Previous response is not available on this websocket; resend the full conversation input without previous_response_id"
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusConflict, Scope: llm.ScopeRequest,
		ProviderCode: CodePreviousResponseNotFound, Message: msg,
		Raw: errorBody(map[string]any{"message": msg, "type": "invalid_request_error", "code": CodePreviousResponseNotFound, "param": "previous_response_id"})}
}

// UpstreamWebsocketLost is the error for a turn whose upstream socket closed before the turn
// ended. The upstream may have run it, so it is not retried; its scope is the request, as it
// says nothing about the account (the next turn opens a new socket), so routing puts no
// cooldown on the account for it.
func UpstreamWebsocketLost() *llm.Error {
	const msg = "Upstream websocket disconnected. The turn was not replayed and its outcome may be unknown. Send a new response.create with full conversation input without previous_response_id."
	return &llm.Error{Code: llm.CodeUnavailable, Status: http.StatusBadGateway, Scope: llm.ScopeRequest,
		ProviderCode: CodeUpstreamWebsocketLost, Message: msg,
		Raw: errorBody(map[string]any{"message": msg, "type": "server_error", "code": CodeUpstreamWebsocketLost})}
}

// MessageTooBig is the error for a turn whose upstream closed its socket because a message was
// too big (close code 1009); reason is the upstream's close reason.
func MessageTooBig(reason string) *llm.Error {
	if reason == "" {
		reason = "message too big"
	}
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusRequestEntityTooLarge, Scope: llm.ScopeRequest,
		ProviderCode: CodeMessageTooBig, Message: reason,
		Raw: errorBody(map[string]any{"message": reason, "type": "invalid_request_error", "code": CodeMessageTooBig})}
}

func errorBody(e map[string]any) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"error": e})
	return b
}

// WarmupMessages returns the two messages a client gets for a warmup (generate: false) that is
// answered without the upstream: response.created and response.completed of an empty response
// with id, as today's core writes them.
func WarmupMessages(id, model string, created int64) [][]byte {
	resp := func(status string) map[string]any {
		r := map[string]any{"id": id, "object": "response", "created_at": created, "status": status, "background": false,
			"error": nil, "output": []any{}}
		if model != "" {
			r["model"] = model
		}
		return r
	}
	done := resp("completed")
	done["usage"] = map[string]any{"input_tokens": 0, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 0,
		"output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 0}
	a, _ := json.Marshal(map[string]any{"type": "response.created", "sequence_number": 0, "response": resp("in_progress")})
	b, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 1, "response": done})
	return [][]byte{a, b}
}

// InterruptedMessage returns the message that ends a turn the client interrupted when the
// upstream cannot be told (an HTTP turn, cancelled instead): response.incomplete with reason
// interrupted for the client's response ID, holding the output items finished so far.
func InterruptedMessage(id string, output []json.RawMessage) []byte {
	if output == nil {
		output = []json.RawMessage{}
	}
	b, _ := json.Marshal(map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete",
		"incomplete_details": map[string]any{"reason": "interrupted"}, "id": id, "output": output}})
	return b
}
