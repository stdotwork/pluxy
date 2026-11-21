package openaichat

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A Chat client keeps the param of an OpenAI-format error, wherever that format puts it, and gets
// none from an error in another format.
func TestErrorKeepsParam(t *testing.T) {
	for _, c := range []struct{ name, raw, want string }{
		{"HTTP error", `{"error":{"message":"m","type":"invalid_request_error","param":"messages","code":"c"}}`, "messages"},
		{"stream error object", `{"message":"m","param":"tools","code":"c"}`, "tools"},
		{"Responses error event", `{"type":"error","code":"c","message":"m","param":"input"}`, "input"},
		{"response.failed", `{"type":"response.failed","response":{"error":{"code":"c","message":"m","param":"input"}}}`, "input"},
		{"null param", `{"error":{"message":"m","param":null}}`, ""},
		{"param that is not text", `{"error":{"message":"m","param":{"a":1}}}`, ""},
		{"Messages error", `{"type":"error","error":{"type":"api_error","message":"m"}}`, ""},
		{"Google error", `{"error":{"code":400,"message":"m","status":"INVALID_ARGUMENT"}}`, ""},
	} {
		_, body := ErrorResponse(&llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "m", Raw: json.RawMessage(c.raw)})
		var got struct {
			Error map[string]any `json:"error"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		param, has := got.Error["param"]
		if (c.want == "" && has) || (c.want != "" && param != c.want) {
			t.Errorf("%s: body = %s, want param %q", c.name, body, c.want)
		}
	}
}
