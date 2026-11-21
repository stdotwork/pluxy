package openaichat

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// The non-streamed reply reports a dropped call like the stream does, and does not finish as
// tool_calls when none survived.
func TestDecodeResponseLossesAndFinish(t *testing.T) {
	body := `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[
		{"id":"call_a","type":"function","function":{"arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	events, losses, err := DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "choices[0].message.tool_calls[0]" {
		t.Errorf("losses = %+v, want one dropped call", losses)
	}
	last := events[len(events)-1]
	if last.Finish == nil || last.Finish.Reason != llm.FinishStop {
		t.Errorf("finish = %+v, want a stop", last.Finish)
	}

	body = `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[
		{"id":"call_a","type":"function","function":{"arguments":"{}"}},
		{"id":"call_b","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	events, losses, err = DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 1 {
		t.Errorf("losses = %+v, want one dropped call", losses)
	}
	if last := events[len(events)-1]; last.Finish == nil || last.Finish.Reason != llm.FinishToolCalls {
		t.Errorf("finish = %+v, want tool_calls", last.Finish)
	}
	if evs, err := DecodeResponse([]byte(body)); err != nil || len(evs) == 0 {
		t.Errorf("DecodeResponse = %v, %v", evs, err)
	}
}
