package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

type logprobOut struct {
	Content []struct {
		Token string `json:"token"`
	} `json:"content"`
}

const logprobsBody = `{"content":[{"token":"Hi","logprob":-0.1,"bytes":[72,105],"top_logprobs":[]},` +
	`{"token":"!","logprob":-0.2,"bytes":[33],"top_logprobs":[]}],"refusal":null}`

func choiceLogprobs(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Choices []struct {
			Logprobs *logprobOut `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	var tokens []string
	if len(out.Choices) > 0 && out.Choices[0].Logprobs != nil {
		for _, e := range out.Choices[0].Logprobs.Content {
			tokens = append(tokens, e.Token)
		}
	}
	return tokens
}

func streamLogprobs(t *testing.T, stream string) ([]string, int) {
	t.Helper()
	var tokens []string
	chunks := 0
	for _, block := range strings.Split(strings.TrimSuffix(stream, "\n\n"), "\n\n") {
		data, ok := strings.CutPrefix(block, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Logprobs *logprobOut `json:"logprobs"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			t.Fatalf("chunk %s: %v", data, err)
		}
		for _, c := range chunk.Choices {
			if c.Logprobs == nil {
				continue
			}
			chunks++
			for _, e := range c.Logprobs.Content {
				tokens = append(tokens, e.Token)
			}
		}
	}
	return tokens, chunks
}

func logprobsChunk(delta, logprobs string) string {
	return `{"id":"x","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":` + delta +
		`,"logprobs":` + logprobs + `,"finish_reason":null}]}`
}

func TestLogprobsReachChatClient(t *testing.T) {
	t.Run("nonstream", func(t *testing.T) {
		body := `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,` +
			`"message":{"role":"assistant","content":"Hi!"},"logprobs":` + logprobsBody + `,"finish_reason":"stop"}]}`
		evs, err := DecodeResponse([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		a := NewAggregator()
		for _, ev := range evs {
			a.Add(ev)
		}
		out, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		if got := choiceLogprobs(t, out); strings.Join(got, ",") != "Hi,!" {
			t.Errorf("logprobs = %q, want [Hi !]; body %s", got, out)
		}
	})
	t.Run("stream", func(t *testing.T) {
		evs := streamReply(t,
			logprobsChunk(`{"content":"Hi"}`, `{"content":[{"token":"Hi","logprob":-0.1,"bytes":[72,105],"top_logprobs":[]}]}`),
			logprobsChunk(`{"content":"!"}`, `{"content":[{"token":"!","logprob":-0.2,"bytes":[33],"top_logprobs":[]}]}`),
			finishChunk)
		var out strings.Builder
		w := NewStreamWriter(&out, false)
		for _, ev := range evs {
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		tokens, chunks := streamLogprobs(t, out.String())
		if chunks != 2 || strings.Join(tokens, ",") != "Hi,!" {
			t.Errorf("stream has %d chunks with logprobs and tokens %q, want 2 and [Hi !]:\n%s", chunks, tokens, out.String())
		}

		a := NewAggregator()
		for _, ev := range evs {
			a.Add(ev)
		}
		body, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		if got := choiceLogprobs(t, body); strings.Join(got, ",") != "Hi,!" {
			t.Errorf("aggregated logprobs = %q, want [Hi !]; body %s", got, body)
		}
	})
	t.Run("non-Chat upstream raw is not logprobs", func(t *testing.T) {
		a := NewAggregator()
		a.Add(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m", Format: llm.FormatAnthropicMessages}})
		a.Add(llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}})
		a.Add(llm.Event{Type: llm.EventTextDelta, Index: 0, Delta: "Hi", Raw: json.RawMessage(logprobsBody)})
		a.Add(llm.Event{Type: llm.EventItemEnd, Index: 0})
		a.Add(llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}})
		body, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "logprobs") {
			t.Errorf("body %s: a %s reply's raw was written as logprobs", body, llm.FormatAnthropicMessages)
		}
	})
}
