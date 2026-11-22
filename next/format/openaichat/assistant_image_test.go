package openaichat_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

// OpenAI's Chat Completions create reference allows only text and refusal parts in assistant
// message content (https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create).
// A translated assistant image is dropped with a loss at that part's path, while a Chat client's
// own body and user-role images stay unchanged.
func TestEncodeAssistantImage(t *testing.T) {
	t.Run("messages_text_and_image", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"m","max_tokens":100,"messages":[
				{"role":"user","content":"draw a cat"},
				{"role":"assistant","content":[
					{"type":"text","text":"Here is a cat."},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
				]},
				{"role":"user","content":"thanks"}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "image_url") || strings.Contains(string(body), "aGVsbG8=") {
			t.Errorf("body %s carries the assistant image", body)
		}
		if !strings.Contains(string(body), "Here is a cat.") {
			t.Errorf("body %s lost the assistant text", body)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "messages[1].content[1]" {
			t.Fatalf("losses = %+v, want one dropped loss at messages[1].content[1]", losses)
		}
		if !(&llm.Translation{Losses: losses}).Dropped() {
			t.Error("Translation.Dropped() = false, want true so reject policy refuses it")
		}
	})

	t.Run("messages_image_only_no_tool_calls", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"m","max_tokens":100,"messages":[
				{"role":"user","content":"draw a cat"},
				{"role":"assistant","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
				]},
				{"role":"user","content":"thanks"}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "messages[1].content[0]" {
			t.Fatalf("losses = %+v, want one dropped loss at messages[1].content[0]", losses)
		}
		var out struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if len(out.Messages) != 3 || out.Messages[1].Role != "assistant" || string(out.Messages[1].Content) != `""` {
			t.Errorf("messages[1] = %+v in body %s, want assistant with content \"\" (never [])", out.Messages, body)
		}
	})

	t.Run("messages_image_before_tool_call", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"m","max_tokens":100,"messages":[
				{"role":"user","content":"go"},
				{"role":"assistant","content":[
					{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}},
					{"type":"tool_use","id":"call_1","name":"f","input":{}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "messages[1].content[0]" {
			t.Fatalf("losses = %+v, want one dropped loss at messages[1].content[0]", losses)
		}
		var out struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if len(out.Messages) != 3 {
			t.Fatalf("len(messages) = %d in body %s, want 3", len(out.Messages), body)
		}
		if _, hasContent := out.Messages[1]["content"]; hasContent {
			t.Errorf("assistant tool_calls message carries content %s in body %s, want none", out.Messages[1]["content"], body)
		}
	})

	t.Run("messages_image_after_tool_call", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"m","max_tokens":100,"messages":[
				{"role":"user","content":"go"},
				{"role":"assistant","content":[
					{"type":"tool_use","id":"call_1","name":"f","input":{}},
					{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}
				]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 1 || losses[0].Kind != llm.LossDropped || losses[0].Path != "messages[1].content[1]" {
			t.Fatalf("losses = %+v, want one dropped loss at messages[1].content[1]", losses)
		}
		var out struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		if len(out.Messages) != 3 || out.Messages[1].Role != "assistant" || out.Messages[2].Role != "tool" {
			t.Errorf("messages = %+v in body %s, want [user, assistant, tool] with no empty assistant between tool_calls and tool", out.Messages, body)
		}
	})

	t.Run("same_format_chat_assistant_image_byte_identical", func(t *testing.T) {
		in := `{"model":"m","messages":[{"role":"user","content":"draw"},{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}],"stream":false}`
		req, err := openaichat.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 0 {
			t.Errorf("losses = %+v, want none for same-format Chat body", losses)
		}
		if string(body) != in {
			t.Errorf("body =\n got %s\nwant %s", body, in)
		}
	})

	t.Run("messages_user_image_unchanged", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"m","max_tokens":100,"messages":[
				{"role":"user","content":[
					{"type":"text","text":"look"},
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}
				]}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequest(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 0 {
			t.Errorf("losses = %+v, want none", losses)
		}
		if !strings.Contains(string(body), `"image_url":{"url":"data:image/png;base64,aGVsbG8="}`) {
			t.Errorf("body %s, want user image preserved", body)
		}
	})
}
