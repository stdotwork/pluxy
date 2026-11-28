package openaichat

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestStreamDecoderFinishAfterDroppedCalls(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"none survived", []string{
			chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"arguments":"{}"}}]}`),
		}, llm.FinishStop},
		{"one survived", []string{
			chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"arguments":"{}"}}]}`),
			chunk(`{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"f","arguments":"{}"}}]}`),
		}, llm.FinishToolCalls},
	} {
		chunks := append(c.chunks,
			`{"id":"x","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`[DONE]`)
		_, events := decodeAll(t, chunks...)
		last := events[len(events)-1]
		if last.Finish == nil || last.Finish.Reason != c.want {
			t.Errorf("%s: finish = %+v, want %s", c.name, last.Finish, c.want)
		}
		if last.Finish != nil && last.Finish.RawReason != "tool_calls" {
			t.Errorf("%s: raw reason = %q, want the upstream's", c.name, last.Finish.RawReason)
		}
	}
}
