package openaichat

import (
	"encoding/json"
	"strings"
	"testing"
)

type logprobOut struct {
	Content []struct {
		Token string `json:"token"`
	} `json:"content"`
}

// logprobsBody is a Chat reply message's logprobs: one entry per content token.
const logprobsBody = `{"content":[{"token":"Hi","logprob":-0.1,"bytes":[72,105],"top_logprobs":[]},` +
	`{"token":"!","logprob":-0.2,"bytes":[33],"top_logprobs":[]}],"refusal":null}`

func responseLogprobs(t *testing.T, body []byte) []string {
	t.Helper()
	var out struct {
		Choices []struct {
			Message struct {
				Logprobs *logprobOut `json:"logprobs"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	var tokens []string
	if len(out.Choices) > 0 && out.Choices[0].Message.Logprobs != nil {
		for _, e := range out.Choices[0].Message.Logprobs.Content {
			tokens = append(tokens, e.Token)
		}
	}
	return tokens
}

// A Chat client that asked for logprobs gets them back from a Chat upstream, streamed or not.
func TestLogprobsReachChatClient(t *testing.T) {
	t.Run("nonstream", func(t *testing.T) {
		evs, err := DecodeResponse([]byte(completionBody(`{"role":"assistant","content":"Hi!","logprobs":` + logprobsBody + `}`)))
		if err != nil {
			t.Fatal(err)
		}
		a := NewAggregator()
		for _, ev := range evs {
			a.Add(ev)
		}
		body, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		if got := responseLogprobs(t, body); strings.Join(got, ",") != "Hi,!" {
			t.Errorf("logprobs = %q, want [Hi !]; body %s", got, body)
		}
	})
	t.Run("stream", func(t *testing.T) {
		evs := streamReply(t,
			deltaChunk(`{"content":"Hi","logprobs":{"content":[{"token":"Hi","logprob":-0.1,"bytes":[72,105],"top_logprobs":[]}]}}`),
			deltaChunk(`{"content":"!","logprobs":{"content":[{"token":"!","logprob":-0.2,"bytes":[33],"top_logprobs":[]}]}}`),
			finishChunk)
		var out strings.Builder
		w := NewStreamWriter(&out, false)
		for _, ev := range evs {
			if err := w.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		if n := strings.Count(out.String(), `"logprobs":{"content":[{"token":`); n != 2 {
			t.Errorf("stream has %d chunks with logprobs, want 2:\n%s", n, out.String())
		}
		if !strings.Contains(out.String(), `"token":"Hi"`) || !strings.Contains(out.String(), `"token":"!"`) {
			t.Errorf("stream lost a token's logprobs:\n%s", out.String())
		}

		a := NewAggregator()
		for _, ev := range evs {
			a.Add(ev)
		}
		body, err := a.Response()
		if err != nil {
			t.Fatal(err)
		}
		if got := responseLogprobs(t, body); strings.Join(got, ",") != "Hi,!" {
			t.Errorf("aggregated logprobs = %q, want [Hi !]; body %s", got, body)
		}
	})
}
