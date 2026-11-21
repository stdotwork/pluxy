package anthropicmessages

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A Messages client keeps the request ID and error details of a Messages upstream's error, and
// gets neither from an error in another format.
func TestErrorKeepsMessagesFields(t *testing.T) {
	const details = `{"error_code":"enforced_spend_limit_reached"}`
	raw := `{"type":"error","error":{"type":"rate_limit_error","message":"limit","details":` + details + `},"request_id":"req_1"}`
	for _, c := range []struct {
		name, raw, requestID, details string
	}{
		{"messages error", raw, "req_1", details},
		{"no optional fields", `{"type":"error","error":{"type":"api_error","message":"m"}}`, "", ""},
		{"null fields", `{"type":"error","error":{"type":"api_error","message":"m","details":null},"request_id":null}`, "", ""},
		{"OpenAI error", `{"error":{"message":"m","type":"t","param":"p","code":"c"},"request_id":"req_2"}`, "", ""},
		{"Google error", `{"error":{"code":400,"message":"m","status":"INVALID_ARGUMENT","details":[{"@type":"x"}]}}`, "", ""},
		{"Responses error event", `{"type":"error","code":"c","message":"m","param":"p","request_id":"req_3"}`, "", ""},
		{"Codex WebSocket error", `{"type":"error","status":400,"error":{"type":"t","message":"m","details":{"a":1}},"request_id":"req_4"}`, "", ""},
	} {
		_, body := ErrorResponse(&llm.Error{Code: llm.CodeUpstream, Status: 429, Message: "limit", Raw: json.RawMessage(c.raw)})
		var got struct {
			RequestID string `json:"request_id"`
			Error     struct {
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.RequestID != c.requestID || string(got.Error.Details) != c.details {
			t.Errorf("%s: body = %s, want request_id %q and details %q", c.name, body, c.requestID, c.details)
		}
	}
}
