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

const completionsPrompt = "Complete this:"

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

type textCompletion struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []map[string]any `json:"choices"`
	Usage   json.RawMessage  `json:"usage,omitempty"`
}

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

type contentOnly struct {
	Content *string `json:"content"`
}

type CompletionsAggregator struct{ *Aggregator }

func NewCompletionsAggregator() *CompletionsAggregator {
	return &CompletionsAggregator{NewAggregator()}
}

func (a *CompletionsAggregator) Response() ([]byte, error) {
	chat, err := a.Aggregator.Response()
	if err != nil {
		return nil, err
	}
	return toCompletion(chat, false)
}

type CompletionsStreamWriter struct {
	*StreamWriter
	conv *completionsConverter
}

func NewCompletionsStreamWriter(w io.Writer, includeUsage bool) *CompletionsStreamWriter {
	conv := &completionsConverter{w: w}
	return &CompletionsStreamWriter{StreamWriter: NewStreamWriter(conv, includeUsage), conv: conv}
}

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

func (c *completionsConverter) frame(frame []byte) error {
	data, ok := bytes.CutPrefix(frame, []byte("data: "))
	if !ok || bytes.Equal(data, []byte("[DONE]")) {
		_, err := c.w.Write(append(frame, '\n', '\n'))
		return err
	}
	var probe struct {
		Choices []struct {
			Delta        *contentOnly `json:"delta"`
			FinishReason *string      `json:"finish_reason"`
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
