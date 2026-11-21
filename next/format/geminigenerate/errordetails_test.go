package geminigenerate

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A Gemini client keeps the details of a Google error, and gets no details from another format's
// error, whose details mean something else.
func TestErrorDetailsOnlyFromGoogle(t *testing.T) {
	const details = `[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"X"}]`
	for _, c := range []struct{ name, raw, want string }{
		{"Google error", `{"error":{"code":400,"message":"m","status":"INVALID_ARGUMENT","details":` + details + `}}`, details},
		{"Messages error", `{"type":"error","error":{"type":"api_error","message":"m","details":{"error_code":"x"}}}`, ""},
		{"OpenAI-style error with a list", `{"error":{"code":"c","message":"m","details":[{"a":1}]}}`, ""},
	} {
		_, body := ErrorResponse(&llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "m", Raw: json.RawMessage(c.raw)})
		var got struct {
			Error struct {
				Details json.RawMessage `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if string(got.Error.Details) != c.want {
			t.Errorf("%s: body = %s, want details %q", c.name, body, c.want)
		}
	}
}
