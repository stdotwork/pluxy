package server

import (
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/internal/tokenest"
	"github.com/stdotwork/pluxy/next/llm"
)

// The estimate counts the text today's core's local counter counts, in the same order: the
// system text; each message's role and text; reasoning; a tool call's ID, name and arguments; a
// tool result's call ID and output; each tool's name, description and parameters; the tool choice.
// The expected numbers are what o200k_base gives today's core for the first three requests
// (core/internal/runtime/executor tests and the corpus), within the estimate's tolerance.
func TestEstimateInputTokens(t *testing.T) {
	messages := func(body string) llm.Request {
		t.Helper()
		req, err := anthropicmessages.ParseRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	gemini := func(body string) llm.Request {
		t.Helper()
		req, err := geminigenerate.ParseRequest("m", false, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	for _, c := range []struct {
		name string
		req  llm.Request
		// text is what the estimate is made from; want is today's core's count (o200k_base) and
		// est the estimate of this core.
		text      string
		want, est int64
	}{
		{"system, message and tool", messages(`{"model":"m","system":"Be brief.","messages":[{"role":"user","content":"Say hello"}],"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]}`),
			"Be brief.\nuser\nSay hello\nget_weather\nWeather\n" + `{"type":"object","properties":{"city":{"type":"string"}}}`, 27, 27},
		{"system and a text block", messages(`{"model":"m","system":"client system instructions","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`),
			"client system instructions\nuser\nhello", 7, 8},
		{"a Gemini client's contents", gemini(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), "user\nhi", 3, 3},
		{"calls, results and thinking", messages(`{"model":"m","system":[{"type":"text","text":" sys "},{"type":"text","text":"  "}],"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"x"},{"type":"tool_use","id":"toolu_1","name":"look","input":{"a": 1}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}],
			"tool_choice":{"type":"tool","name":"look"}}`),
			"sys\nuser\nq\nassistant\nhmm\ntoolu_1\nlook\n" + `{"a":1}` + "\nuser\ntoolu_1\nfound\ntool\nlook", 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := estimateText(c.req); got != c.text {
				t.Errorf("text = %q, want %q", got, c.text)
			}
			got := estimateInputTokens(c.req)
			if c.est != 0 && got != c.est {
				t.Errorf("estimate = %d, want %d (today's core: %d)", got, c.est, c.want)
			}
		})
	}
}

// Images and files add nothing, and a request with no text is 0.
func TestEstimateInputTokensIgnoresNonText(t *testing.T) {
	req, err := anthropicmessages.ParseRequest([]byte(`{"model":"m","max_tokens":9,"temperature":1,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	// Only the role is text.
	if got, want := estimateInputTokens(req), tokenest.Estimate("user"); got != want {
		t.Errorf("estimate = %d, want %d", got, want)
	}
	if got := estimateInputTokens(llm.Request{}); got != 0 {
		t.Errorf("estimate of nothing = %d", got)
	}
}
