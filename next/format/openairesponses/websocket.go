package openairesponses

import (
	"encoding/json"
	"net/http"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	CodePreviousResponseNotFound = "previous_response_not_found"
	CodeUpstreamWebsocketLost    = "upstream_websocket_lost"
	CodeMessageTooBig            = "message_too_big"
)

func PreviousResponseNotFound() *llm.Error {
	const msg = "Previous response is not available on this websocket; resend the full conversation input without previous_response_id"
	return &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusConflict, Scope: llm.ScopeRequest,
		ProviderCode: CodePreviousResponseNotFound, Message: msg,
		Raw: errorBody(map[string]any{"message": msg, "type": "invalid_request_error", "code": CodePreviousResponseNotFound, "param": "previous_response_id"})}
}

func UpstreamWebsocketLost() *llm.Error {
	const msg = "Upstream websocket disconnected. The turn was not replayed and its outcome may be unknown. Send a new response.create with full conversation input without previous_response_id."
	return &llm.Error{Code: llm.CodeUnavailable, Status: http.StatusBadGateway, Scope: llm.ScopeRequest,
		ProviderCode: CodeUpstreamWebsocketLost, Message: msg,
		Raw: errorBody(map[string]any{"message": msg, "type": "server_error", "code": CodeUpstreamWebsocketLost})}
}

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

func InterruptedMessage(id string, output []json.RawMessage) []byte {
	if output == nil {
		output = []json.RawMessage{}
	}
	b, _ := json.Marshal(map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete",
		"incomplete_details": map[string]any{"reason": "interrupted"}, "id": id, "output": output}})
	return b
}
