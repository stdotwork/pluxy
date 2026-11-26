package openaichat

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/format/sse"
	"github.com/stdotwork/pluxy/next/llm"
)

// OpenAI's legacy text completions (POST /v1/completions) are served as Chat Completions, as
// today's core serves them: the request becomes a Chat request with the prompt as one user
// message, and the Chat reply is written back in the completions shape. The upstream sees a
// Chat request.

// completionsPrompt is the user message of a request with an empty prompt, as today's core sends
// it.
const completionsPrompt = "Complete this:"

// ParseCompletionsRequest reads a legacy completions request as the Chat request it is served
// as. The prompt (a string, or a list of one string) becomes the one user message; max_tokens,
// temperature, top_p, frequency_penalty, presence_penalty, stop, stream, logprobs (as a switch),
// top_logprobs and echo are carried over, as today's core carries them; other fields (n, suffix,
// best_of, user, ...) are left out.
func ParseCompletionsRequest(body []byte) (llm.Request, error) {
	in := rawjson.Object(body)
	if in == nil {
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return llm.Request{}, invalid("request body is not valid JSON: %v", err)
		}
		return llm.Request{}, invalid("request body must be a JSON object")
	}
	var prompt string
	if raw, ok := in["prompt"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if json.Unmarshal(raw, &prompt) != nil {
			var list []string
			if json.Unmarshal(raw, &list) != nil || len(list) > 1 {
				// Several prompts ask for one completion each, which one Chat request cannot
				// carry; token arrays have no Chat form either.
				return llm.Request{}, invalid("prompt must be a string or a list of one string")
			}
			if len(list) == 1 {
				prompt = list[0]
			}
		}
	}
	if prompt == "" {
		prompt = completionsPrompt
	}
	out := map[string]any{"model": "", "messages": []map[string]string{{"role": llm.RoleUser, "content": prompt}}}
	var model string
	if json.Unmarshal(in["model"], &model) == nil {
		out["model"] = model
	}
	// Numbers keep their kind (max_tokens and top_logprobs whole, the rest as given); logprobs
	// and echo become switches; stop goes as it came.
	for _, k := range []string{"max_tokens", "top_logprobs"} {
		var f float64
		if json.Unmarshal(in[k], &f) == nil {
			out[k] = int64(f)
		}
	}
	for _, k := range []string{"temperature", "top_p", "frequency_penalty", "presence_penalty"} {
		var f float64
		if json.Unmarshal(in[k], &f) == nil {
			out[k] = f
		}
	}
	if raw, ok := in["stop"]; ok {
		out["stop"] = raw
	}
	for _, k := range []string{"stream", "logprobs", "echo"} {
		if raw, ok := in[k]; ok {
			out[k] = truthy(raw)
		}
	}
	chat, err := json.Marshal(out)
	if err != nil {
		return llm.Request{}, err
	}
	return ParseRequest(chat)
}

// truthy reads a JSON value as a switch: true, a non-zero number or a string that says true.
func truthy(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v := v.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return strings.EqualFold(v, "true") || v == "1"
	}
	return false
}

// textCompletion is a reply in the completions shape.
type textCompletion struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []map[string]any `json:"choices"`
	Usage   json.RawMessage  `json:"usage,omitempty"`
}

// toCompletion turns a Chat reply or stream chunk into the completions shape: its id, created,
// model and usage, and each choice's index, text (the message's or delta's content),
// finish_reason and logprobs. In a chunk an absent or empty content is text "" and a null
// finish_reason is left out.
func toCompletion(chat []byte, chunk bool) ([]byte, error) {
	var in struct {
		ID      string          `json:"id"`
		Created int64           `json:"created"`
		Model   string          `json:"model"`
		Usage   json.RawMessage `json:"usage"`
		Choices []struct {
			Index        int64           `json:"index"`
			Message      *contentOnly    `json:"message"`
			Delta        *contentOnly    `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
			Logprobs     json.RawMessage `json:"logprobs"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(chat, &in); err != nil {
		return nil, err
	}
	out := textCompletion{ID: in.ID, Object: "text_completion", Created: in.Created, Model: in.Model, Choices: []map[string]any{}, Usage: in.Usage}
	for _, c := range in.Choices {
		choice := map[string]any{"index": c.Index}
		switch {
		case chunk:
			text := ""
			if c.Delta != nil && c.Delta.Content != nil {
				text = *c.Delta.Content
			}
			choice["text"] = text
		case c.Message != nil && c.Message.Content != nil:
			choice["text"] = *c.Message.Content
		case c.Message == nil && c.Delta != nil && c.Delta.Content != nil:
			choice["text"] = *c.Delta.Content
		}
		if c.FinishReason != nil {
			choice["finish_reason"] = *c.FinishReason
		}
		if len(c.Logprobs) > 0 {
			choice["logprobs"] = c.Logprobs
		}
		out.Choices = append(out.Choices, choice)
	}
	return json.Marshal(out)
}

// contentOnly reads a message's or delta's content as text; null and absent are nil.
type contentOnly struct {
	Content *string `json:"content"`
}

// CompletionsAggregator collects events into a non-streamed completions reply.
type CompletionsAggregator struct{ *Aggregator }

// NewCompletionsAggregator returns an aggregator for one completions reply.
func NewCompletionsAggregator() *CompletionsAggregator {
	return &CompletionsAggregator{NewAggregator()}
}

// Response returns the reply in the completions shape.
func (a *CompletionsAggregator) Response() ([]byte, error) {
	chat, err := a.Aggregator.Response()
	if err != nil {
		return nil, err
	}
	return toCompletion(chat, false)
}

// CompletionsStreamWriter writes events to a client as a completions stream: the Chat stream's
// chunks in the completions shape. A chunk with no text, no finish reason and no usage (such as
// the first, which names the role) is left out; "[DONE]" and an error object go as they are.
type CompletionsStreamWriter struct {
	*StreamWriter
	conv *completionsConverter
}

// NewCompletionsStreamWriter returns a writer for one completions stream. includeUsage adds
// the final usage chunk.
func NewCompletionsStreamWriter(w io.Writer, includeUsage bool) *CompletionsStreamWriter {
	conv := &completionsConverter{w: w}
	return &CompletionsStreamWriter{StreamWriter: NewStreamWriter(conv, includeUsage), conv: conv}
}

// completionsConverter takes the Chat stream's bytes and writes each event converted.
type completionsConverter struct {
	w   io.Writer
	buf []byte
}

func (c *completionsConverter) Write(p []byte) (int, error) {
	c.buf = append(c.buf, p...)
	for {
		end := bytes.Index(c.buf, []byte("\n\n"))
		if end < 0 {
			return len(p), nil
		}
		frame := c.buf[:end]
		c.buf = c.buf[end+2:]
		if err := c.frame(frame); err != nil {
			return 0, err
		}
	}
}

// frame converts one event of the Chat stream, which StreamWriter writes as one data line.
func (c *completionsConverter) frame(frame []byte) error {
	data, ok := bytes.CutPrefix(frame, []byte("data: "))
	if !ok || bytes.Equal(data, []byte("[DONE]")) {
		_, err := c.w.Write(append(frame, '\n', '\n'))
		return err
	}
	var probe struct {
		Choices []struct {
			Delta *contentOnly `json:"delta"`
			// FinishReason is a string or null.
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.Error != nil {
		_, err := c.w.Write(append(frame, '\n', '\n'))
		return err
	}
	keep := len(probe.Usage) > 0 && !bytes.Equal(probe.Usage, []byte("null"))
	for _, ch := range probe.Choices {
		if (ch.Delta != nil && ch.Delta.Content != nil && *ch.Delta.Content != "") || (ch.FinishReason != nil && *ch.FinishReason != "") {
			keep = true
		}
	}
	if !keep {
		return nil
	}
	out, err := toCompletion(data, true)
	if err != nil {
		return err
	}
	return sse.WriteData(c.w, out)
}
