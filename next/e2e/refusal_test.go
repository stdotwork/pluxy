package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
)

// Refusal stop reasons translate across the HTTP seam in both directions: a streamed Claude
// reply with stop_reason "refusal" reaches a Chat Completions client with finish_reason
// "content_filter" and a Responses client as response.incomplete with incomplete_details.reason
// "content_filter", and a non-streamed Chat reply with finish_reason "content_filter" reaches
// a Messages client with stop_reason "refusal".
func TestRefusalFinishReason(t *testing.T) {
	claudeRefusalStream := func(w http.ResponseWriter, _ *http.Request, _ claudetest.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []struct{ name, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"msg_corpus","type":"message","role":"assistant","content":[],"model":"claude-test-upstream","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":14,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":1}}}`},
			{"ping", `{"type":"ping"}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":4}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			io.WriteString(w, "event: "+ev.name+"\ndata: "+ev.data+"\n\n")
		}
	}

	t.Run("claude stream refusal to chat", func(t *testing.T) {
		srv := claudetest.New(t)
		srv.Messages = claudeRefusalStream
		key := srv.IssueKey()
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id": "key", "settings": srv.Settings(""),
					"credential": map[string]any{"kind": "api_key", "api_key": key},
					"models":     []string{"claude-test"},
				}},
			},
		})

		resp := post(t, context.Background(), base, clientKey,
			`{"model":"claude-test","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Say hello"}],"stream":true}`)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var dataLines []string
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				dataLines = append(dataLines, d)
			}
		}
		if len(dataLines) != 5 {
			t.Fatalf("got %d SSE data events, want 5:\n%s", len(dataLines), raw)
		}
		if dataLines[4] != "[DONE]" {
			t.Errorf("last event = %q, want [DONE]", dataLines[4])
		}

		type chunk struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Model   string `json:"model"`
			Choices []struct {
				Index        int            `json:"index"`
				Delta        map[string]any `json:"delta"`
				FinishReason *string        `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		var chunks []chunk
		for i := range 4 {
			var c chunk
			if err := json.Unmarshal([]byte(dataLines[i]), &c); err != nil {
				t.Fatalf("chunk %d (%s): %v", i, dataLines[i], err)
			}
			if c.ID != "msg_corpus" || c.Object != "chat.completion.chunk" || c.Model != "claude-test-upstream" {
				t.Errorf("chunk %d header = %+v, want id=msg_corpus object=chat.completion.chunk model=claude-test-upstream", i, c)
			}
			if len(c.Choices) != 1 || c.Choices[0].Index != 0 {
				t.Fatalf("chunk %d choices = %+v, want one choice at index 0", i, c.Choices)
			}
			if len(c.Usage) != 0 {
				t.Errorf("chunk %d unexpectedly includes usage: %s", i, c.Usage)
			}
			chunks = append(chunks, c)
		}
		if chunks[0].Choices[0].Delta["role"] != "assistant" || chunks[0].Choices[0].Delta["content"] != "" || chunks[0].Choices[0].FinishReason != nil {
			t.Errorf("chunk 0 = %+v, want assistant role with empty content and nil finish_reason", chunks[0].Choices[0])
		}
		if chunks[1].Choices[0].Delta["content"] != "Hello" || chunks[1].Choices[0].FinishReason != nil {
			t.Errorf("chunk 1 = %+v, want Hello delta", chunks[1].Choices[0])
		}
		if chunks[2].Choices[0].Delta["content"] != " there" || chunks[2].Choices[0].FinishReason != nil {
			t.Errorf("chunk 2 = %+v, want ' there' delta", chunks[2].Choices[0])
		}
		if len(chunks[3].Choices[0].Delta) != 0 || chunks[3].Choices[0].FinishReason == nil || *chunks[3].Choices[0].FinishReason != "content_filter" {
			gotReason := "<nil>"
			if chunks[3].Choices[0].FinishReason != nil {
				gotReason = *chunks[3].Choices[0].FinishReason
			}
			t.Errorf("chunk 3 delta=%v finish_reason=%q, want empty delta and finish_reason content_filter", chunks[3].Choices[0].Delta, gotReason)
		}

		reqs := srv.Requests()
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(reqs))
		}
		if got := reqs[0].Header.Get("X-Api-Key"); got != key {
			t.Errorf("upstream X-Api-Key = %q, want %q", got, key)
		}
		if got := reqs[0].Header.Get("Authorization"); got != "" {
			t.Errorf("upstream Authorization = %q, want empty", got)
		}
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
			t.Fatalf("upstream body: %v", err)
		}
		if _, hasMeta := sent["metadata"]; hasMeta {
			t.Errorf("upstream body unexpectedly has metadata: %s", sent["metadata"])
		}
	})

	t.Run("chat content_filter to messages", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"chatcmpl-corpus","object":"chat.completion","created":1700000001,"model":"gpt-test-upstream",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"Once upon a time there"},"finish_reason":"content_filter"}],`+
				`"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`)
		}
		base := startCore(t, u)
		resp := postPath(t, context.Background(), base, "/v1/messages", clientKey,
			`{"model":"gpt-test","max_tokens":5,"messages":[{"role":"user","content":"Tell me a long story."}]}`)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		var got struct {
			ID           string  `json:"id"`
			Type         string  `json:"type"`
			Role         string  `json:"role"`
			Model        string  `json:"model"`
			StopReason   string  `json:"stop_reason"`
			StopSequence *string `json:"stop_sequence"`
			Content      []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
		if got.ID != "chatcmpl-corpus" || got.Type != "message" || got.Role != "assistant" || got.Model != "gpt-test-upstream" {
			t.Errorf("reply header = %+v, want id=chatcmpl-corpus type=message role=assistant model=gpt-test-upstream", got)
		}
		if got.StopReason != "refusal" || got.StopSequence != nil {
			t.Errorf("stop_reason = %q (stop_sequence = %v), want refusal (nil)", got.StopReason, got.StopSequence)
		}
		if len(got.Content) != 1 || got.Content[0].Type != "text" || got.Content[0].Text != "Once upon a time there" {
			t.Errorf("content = %+v, want single text block 'Once upon a time there'", got.Content)
		}
		if got.Usage.InputTokens != 12 || got.Usage.OutputTokens != 5 {
			t.Errorf("usage = %+v, want input_tokens=12 output_tokens=5", got.Usage)
		}
	})

	t.Run("claude stream refusal to responses", func(t *testing.T) {
		srv := claudetest.New(t)
		srv.Messages = claudeRefusalStream
		key := srv.IssueKey()
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id": "key", "settings": srv.Settings(""),
					"credential": map[string]any{"kind": "api_key", "api_key": key},
					"models":     []string{"claude-test"},
				}},
			},
		})

		resp := postPath(t, context.Background(), base, "/v1/responses", clientKey,
			`{"model":"claude-test","instructions":"Be brief.","input":"Say hello","stream":true}`)
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}

		type sseEvent struct {
			name string
			data string
		}
		var events []sseEvent
		var cur sseEvent
		sc := bufio.NewScanner(strings.NewReader(string(raw)))
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = strings.TrimPrefix(line, "data: ")
			case line == "" && (cur.name != "" || cur.data != ""):
				events = append(events, cur)
				cur = sseEvent{}
			}
		}
		if cur.name != "" || cur.data != "" {
			events = append(events, cur)
		}

		wantOrder := []string{
			"response.created",
			"response.in_progress",
			"response.output_item.added",
			"response.content_part.added",
			"response.output_text.delta",
			"response.output_text.delta",
			"response.output_text.done",
			"response.content_part.done",
			"response.output_item.done",
			"response.incomplete",
		}
		if len(events) != len(wantOrder) {
			t.Fatalf("got %d SSE events, want %d:\n%s", len(events), len(wantOrder), raw)
		}
		for i, wantName := range wantOrder {
			if events[i].name != wantName {
				t.Errorf("event[%d].event = %q, want %q", i, events[i].name, wantName)
			}
			var hdr struct {
				Type           string `json:"type"`
				SequenceNumber int    `json:"sequence_number"`
			}
			if err := json.Unmarshal([]byte(events[i].data), &hdr); err != nil {
				t.Fatalf("event[%d] data (%s): %v", i, events[i].data, err)
			}
			if hdr.Type != wantName || hdr.SequenceNumber != i+1 {
				t.Errorf("event[%d] = %+v, want type=%q sequence_number=%d", i, hdr, wantName, i+1)
			}
		}

		var d1, d2 struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal([]byte(events[4].data), &d1)
		_ = json.Unmarshal([]byte(events[5].data), &d2)
		if d1.Delta != "Hello" || d2.Delta != " there" {
			t.Errorf("deltas = %q, %q; want %q, %q", d1.Delta, d2.Delta, "Hello", " there")
		}
		var textDone struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(events[6].data), &textDone)
		if textDone.Text != "Hello there" {
			t.Errorf("output_text.done text = %q, want %q", textDone.Text, "Hello there")
		}

		var last struct {
			Type           string `json:"type"`
			SequenceNumber int    `json:"sequence_number"`
			Response       struct {
				ID                string `json:"id"`
				Object            string `json:"object"`
				Model             string `json:"model"`
				Instructions      string `json:"instructions"`
				Status            string `json:"status"`
				Background        bool   `json:"background"`
				Error             any    `json:"error"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Output []struct {
					ID      string `json:"id"`
					Type    string `json:"type"`
					Role    string `json:"role"`
					Status  string `json:"status"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					TotalTokens  int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(events[9].data), &last); err != nil {
			t.Fatalf("final event (%s): %v", events[9].data, err)
		}
		r := last.Response
		if r.ID != "msg_corpus" || r.Object != "response" || r.Model != "claude-test" || r.Instructions != "Be brief." || r.Background || r.Error != nil {
			t.Errorf("final response header = %+v", r)
		}
		if r.Status != "incomplete" || r.IncompleteDetails == nil || r.IncompleteDetails.Reason != "content_filter" {
			t.Errorf("final response status=%q incomplete_details=%+v, want incomplete / content_filter", r.Status, r.IncompleteDetails)
		}
		if len(r.Output) != 1 || r.Output[0].ID != "msg_msg_corpus_0" || r.Output[0].Type != "message" || r.Output[0].Role != "assistant" ||
			r.Output[0].Status != "incomplete" || len(r.Output[0].Content) != 1 || r.Output[0].Content[0].Type != "output_text" || r.Output[0].Content[0].Text != "Hello there" {
			t.Errorf("final response output = %+v, want single incomplete message with text 'Hello there'", r.Output)
		}
		if r.Usage.InputTokens != 14 || r.Usage.OutputTokens != 4 || r.Usage.TotalTokens != 18 {
			t.Errorf("final response usage = %+v, want 14/4/18", r.Usage)
		}

		reqs := srv.Requests()
		if len(reqs) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(reqs))
		}
		if got := reqs[0].Header.Get("X-Api-Key"); got != key {
			t.Errorf("upstream X-Api-Key = %q, want %q", got, key)
		}
		if got := reqs[0].Header.Get("Authorization"); got != "" {
			t.Errorf("upstream Authorization = %q, want empty", got)
		}
		var sent map[string]json.RawMessage
		if err := json.Unmarshal(reqs[0].Body, &sent); err != nil {
			t.Fatalf("upstream body: %v", err)
		}
		if _, hasMeta := sent["metadata"]; hasMeta {
			t.Errorf("upstream body unexpectedly has metadata: %s", sent["metadata"])
		}
	})
}
