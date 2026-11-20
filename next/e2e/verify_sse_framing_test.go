package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
)

// These tests read the bytes of core's Chat Completions and Responses streams the way a client
// that deserializes them strictly does: every frame parsed, every field the format requires
// present. A client that turns an event into a typed value fails the whole reply on one missing
// field or one event type it does not know, which is how a proxy's stream breaks a client that
// works against the provider itself.
//
// The fields checked are the ones OpenAI's streaming references mark as required: for a Chat
// chunk, `id`, `object: "chat.completion.chunk"`, `created`, `model` and `choices`; for a
// Responses event, `type` and `sequence_number` on every event, and the item and response shapes
// named in the checks below.

// sseFrame is one event of a stream, as it arrived: its comment lines, event name and data lines.
type sseFrame struct {
	comments []string
	event    string
	data     []string
}

func (f sseFrame) json() (map[string]any, error) {
	if len(f.data) == 0 {
		return nil, fmt.Errorf("event with no data")
	}
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(strings.Join(f.data, "\n")))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// readFrames reads body to its end as server-sent events.
func readFrames(t *testing.T, body io.Reader) []sseFrame {
	t.Helper()
	var (
		frames []sseFrame
		cur    sseFrame
	)
	r := bufio.NewReader(body)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(cur.data) > 0 || cur.event != "" {
					frames = append(frames, cur)
					cur = sseFrame{}
				}
			case strings.HasPrefix(line, ":"):
				cur.comments = append(cur.comments, line)
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					cur.event = value
				case "data":
					cur.data = append(cur.data, value)
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
	}
	if len(cur.data) > 0 || cur.event != "" {
		frames = append(frames, cur)
	}
	return frames
}

// chatStreamReply answers a Chat Completions request as OpenAI's stream does: a role chunk, text,
// a finish chunk, a usage chunk under stream_options.include_usage, and data: [DONE] last.
func chatStreamReply(w http.ResponseWriter, _ *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	chunk := func(s string) {
		fmt.Fprintf(w, "data: %s\n\n", s)
		f.Flush()
	}
	const head = `"id":"chatcmpl-v1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test-upstream"`
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`)
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`)
	chunk(`{` + head + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
	chunk(`{` + head + `,"choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`)
	fmt.Fprint(w, "data: [DONE]\n\n")
	f.Flush()
}

// messagesStreamReply answers a Messages request as Anthropic streams one.
func messagesStreamReply(w http.ResponseWriter, _ *http.Request, _ claudetest.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	f := w.(http.Flusher)
	event := func(typ, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		f.Flush()
	}
	event("message_start", `{"type":"message_start","message":{"id":"msg_v1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":1}}}`)
	event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
	event("content_block_stop", `{"type":"content_block_stop","index":0}`)
	event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`)
	event("message_stop", `{"type":"message_stop"}`)
}

// streamUpstreams are the two upstream formats the tests read replies from: a Chat Completions
// server and an Anthropic Messages server.
var streamUpstreams = []struct {
	name string
	// start runs core in front of the upstream and returns its base URL and the model a client
	// asks for.
	start func(t *testing.T) (base, model string)
}{
	{"chat", func(t *testing.T) (string, string) {
		u := newUpstream(t)
		u.respond = chatStreamReply
		return startCore(t, u), "gpt-test"
	}},
	{"messages", func(t *testing.T) (string, string) {
		srv := claudetest.New(t)
		srv.Messages = messagesStreamReply
		base := runCore(t, map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
			"extension": map[string]any{
				"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
				"accounts": []any{map[string]any{
					"id": "key", "settings": srv.Settings(""),
					"credential": map[string]any{"kind": "api_key", "api_key": srv.IssueKey()},
					"models":     []string{"claude-opus-4-6"},
				}},
			},
		})
		return base, "claude-opus-4-6"
	}},
}

// streamBody runs one streamed request and returns the frames core wrote, failing the test on a
// status other than 200.
func streamBody(t *testing.T, base, path, body string) []sseFrame {
	t.Helper()
	resp := postPath(t, context.Background(), base, path, clientKey, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return readFrames(t, resp.Body)
}

// The Chat Completions stream a strict client deserializes: every data frame is one
// chat.completion.chunk with the required fields, and data: [DONE] comes last, once, with nothing
// after it — the same contract the upstreams' own replies follow, from a Chat and a Messages
// upstream.
func TestVerifyChatStreamChunksForStrictParser(t *testing.T) {
	for _, up := range streamUpstreams {
		t.Run(up.name, func(t *testing.T) {
			base, model := up.start(t)
			frames := streamBody(t, base,
				"/v1/chat/completions",
				`{"model":"`+model+`","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`)

			if len(frames) == 0 {
				t.Fatal("the stream had no events")
			}
			for i, f := range frames {
				if len(f.comments) > 0 {
					t.Errorf("frame %d carries comment lines %q; a strict parser reads data frames only", i, f.comments)
				}
				if len(f.event) > 0 {
					t.Errorf("frame %d has an event: line %q; a Chat Completions stream has none", i, f.event)
				}
			}
			done := 0
			for i, f := range frames {
				if len(f.data) == 1 && f.data[0] == "[DONE]" {
					done++
					if i != len(frames)-1 {
						t.Errorf("data: [DONE] is frame %d of %d; it must be the last", i, len(frames))
					}
					continue
				}
				if done > 0 {
					t.Errorf("frame %d of %d follows data: [DONE]", i, len(frames))
				}
				c, err := f.json()
				if err != nil {
					t.Errorf("frame %d is not a JSON chunk (%v): %q", i, err, strings.Join(f.data, "\n"))
					continue
				}
				if id, ok := c["id"].(string); !ok || id == "" {
					t.Errorf("frame %d has id %v; every chunk carries a non-empty id", i, c["id"])
				}
				if o, ok := c["object"].(string); !ok || o != "chat.completion.chunk" {
					t.Errorf("frame %d has object %v, want chat.completion.chunk", i, c["object"])
				}
				if n, ok := c["created"].(json.Number); !ok || !isInt(n) || n.String() == "0" || strings.HasPrefix(n.String(), "-") {
					t.Errorf("frame %d has created %v; every chunk carries a Unix timestamp", i, c["created"])
				}
				if m, ok := c["model"].(string); !ok || m == "" {
					t.Errorf("frame %d has model %v; every chunk names the model", i, c["model"])
				}
				choices, ok := c["choices"].([]any)
				if !ok {
					t.Errorf("frame %d has choices %v, want an array (empty on the usage chunk)", i, c["choices"])
					continue
				}
				if len(choices) == 0 {
					u, ok := c["usage"].(map[string]any)
					if !ok {
						t.Errorf("frame %d has no choices and no usage object: %v", i, c)
						continue
					}
					for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
						if n, ok := u[k].(json.Number); !ok || !isInt(n) {
							t.Errorf("frame %d usage %s = %v, want an integer", i, k, u[k])
						}
					}
				}
			}
			if done != 1 {
				t.Errorf("the stream has %d data: [DONE] frames, want exactly one at the end", done)
			}
		})
	}
}

// responsesEventTypes are the event types a Responses stream from core may carry; a client that
// reads them into an enum fails on any other one.
var responsesEventTypes = map[string]bool{
	"response.created":                       true,
	"response.in_progress":                   true,
	"response.output_item.added":             true,
	"response.output_item.done":              true,
	"response.content_part.added":            true,
	"response.content_part.done":             true,
	"response.output_text.delta":             true,
	"response.output_text.done":              true,
	"response.refusal.delta":                 true,
	"response.refusal.done":                  true,
	"response.function_call_arguments.delta": true,
	"response.function_call_arguments.done":  true,
	"response.reasoning_summary_part.added":  true,
	"response.reasoning_summary_part.done":   true,
	"response.reasoning_summary_text.delta":  true,
	"response.reasoning_summary_text.done":   true,
	"response.reasoning_text.delta":          true,
	"response.reasoning_text.done":           true,
	"response.web_search_call.in_progress":   true,
	"response.web_search_call.searching":     true,
	"response.web_search_call.completed":     true,
	"response.completed":                     true,
	"response.incomplete":                    true,
	"response.failed":                        true,
	"error":                                  true,
}

// The Responses stream a strict client deserializes: every event names its type in an event: line
// and in the JSON, carries a sequence_number, uses a type the API defines, keeps the item and
// response fields the API requires, and ends with one terminal event and no data: [DONE] — from a
// Chat and a Messages upstream, which core translates.
func TestVerifyResponsesStreamEventsForStrictParser(t *testing.T) {
	for _, up := range streamUpstreams {
		t.Run(up.name, func(t *testing.T) {
			base, model := up.start(t)
			frames := streamBody(t, base, "/v1/responses", `{"model":"`+model+`","stream":true,"input":"hi"}`)

			if len(frames) == 0 {
				t.Fatal("the stream had no events")
			}
			last := int64(0)
			terminals := 0
			for i, f := range frames {
				if len(f.comments) > 0 {
					t.Errorf("event %d carries comment lines %q; a strict parser reads data events only", i, f.comments)
				}
				if f.event == "" {
					t.Errorf("event %d has no event: line: %q", i, strings.Join(f.data, "\n"))
				}
				if len(f.data) == 1 && f.data[0] == "[DONE]" {
					t.Errorf("event %d is data: [DONE]; a Responses stream ends with response.completed, not [DONE]", i)
					continue
				}
				ev, err := f.json()
				if err != nil {
					t.Errorf("event %d is not JSON (%v): %q", i, err, strings.Join(f.data, "\n"))
					continue
				}
				typ, _ := ev["type"].(string)
				if typ != f.event {
					t.Errorf("event %d has event: %q and type %q; they must name the same event", i, f.event, typ)
				}
				if !responsesEventTypes[typ] {
					t.Errorf("event %d has type %q, which the API does not define", i, typ)
				}
				n, ok := ev["sequence_number"].(json.Number)
				if !ok || !isInt(n) {
					t.Errorf("event %d has sequence_number %v, want an integer on every event", i, ev["sequence_number"])
				} else if seq, err := n.Int64(); err != nil {
					t.Errorf("event %d has sequence_number %v, want an integer on every event", i, n)
				} else {
					if seq <= last {
						t.Errorf("event %d has sequence_number %d after %d, want it increasing", i, seq, last)
					}
					last = seq
				}
				switch typ {
				case "response.completed", "response.incomplete", "response.failed":
					terminals++
					if i != len(frames)-1 {
						t.Errorf("event %d of %d is %s; the terminal event must be last", i, len(frames), typ)
					}
					if typ == "response.completed" {
						checkResponseObject(t, i, ev["response"])
					}
				case "response.output_item.done":
					checkDoneItem(t, i, ev["item"])
				}
			}
			if terminals != 1 {
				t.Errorf("the stream has %d terminal events, want exactly one", terminals)
			}
		})
	}
}

// checkResponseObject checks the fields of the Response a response.completed event carries.
func checkResponseObject(t *testing.T, i int, v any) {
	t.Helper()
	r, ok := v.(map[string]any)
	if !ok {
		t.Errorf("event %d: response %v is not an object", i, v)
		return
	}
	for _, k := range []string{"id", "object", "model", "status"} {
		s, ok := r[k].(string)
		if !ok || s == "" {
			t.Errorf("event %d: response.%s = %v, want a non-empty string", i, k, r[k])
		}
	}
	if o, _ := r["object"].(string); o != "response" {
		t.Errorf("event %d: response.object = %q, want response", i, o)
	}
	if n, ok := r["created_at"].(json.Number); !ok || !isInt(n) {
		t.Errorf("event %d: response.created_at = %v, want a Unix timestamp", i, r["created_at"])
	}
	if _, ok := r["output"].([]any); !ok {
		t.Errorf("event %d: response.output = %v, want an array", i, r["output"])
	}
	u, ok := r["usage"].(map[string]any)
	if !ok {
		t.Errorf("event %d: response.usage = %v, want the token usage", i, r["usage"])
		return
	}
	for _, k := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if n, ok := u[k].(json.Number); !ok || !isInt(n) {
			t.Errorf("event %d: response.usage.%s = %v, want an integer", i, k, u[k])
		}
	}
	in, ok := u["input_tokens_details"].(map[string]any)
	if !ok {
		t.Errorf("event %d: response.usage.input_tokens_details = %v, want an object", i, u["input_tokens_details"])
	} else if n, ok := in["cached_tokens"].(json.Number); !ok || !isInt(n) {
		t.Errorf("event %d: response.usage.input_tokens_details.cached_tokens = %v, want an integer", i, in["cached_tokens"])
	}
	out, ok := u["output_tokens_details"].(map[string]any)
	if !ok {
		t.Errorf("event %d: response.usage.output_tokens_details = %v, want an object", i, u["output_tokens_details"])
	} else if n, ok := out["reasoning_tokens"].(json.Number); !ok || !isInt(n) {
		t.Errorf("event %d: response.usage.output_tokens_details.reasoning_tokens = %v, want an integer", i, out["reasoning_tokens"])
	}
}

// checkDoneItem checks the item a response.output_item.done event carries: the fields its type
// requires, and an output_text part with the annotations and logprobs arrays the API gives it.
func checkDoneItem(t *testing.T, i int, v any) {
	t.Helper()
	it, ok := v.(map[string]any)
	if !ok {
		t.Errorf("event %d: item %v is not an object", i, v)
		return
	}
	if id, ok := it["id"].(string); !ok || id == "" {
		t.Errorf("event %d: item.id = %v, want a non-empty string", i, it["id"])
	}
	typ, _ := it["type"].(string)
	switch typ {
	case "message":
		if role, _ := it["role"].(string); role != "assistant" {
			t.Errorf("event %d: message item role = %q, want assistant", i, role)
		}
		if s, _ := it["status"].(string); s == "" {
			t.Errorf("event %d: message item has no status", i)
		}
		parts, ok := it["content"].([]any)
		if !ok {
			t.Errorf("event %d: message item content = %v, want an array", i, it["content"])
			return
		}
		for j, p := range parts {
			part, ok := p.(map[string]any)
			if !ok {
				t.Errorf("event %d: content part %d is not an object", i, j)
				continue
			}
			if pt, _ := part["type"].(string); pt != "output_text" {
				t.Errorf("event %d: content part %d type = %q, want output_text", i, j, pt)
			}
			if _, ok := part["text"].(string); !ok {
				t.Errorf("event %d: content part %d has no text", i, j)
			}
			if _, ok := part["annotations"].([]any); !ok {
				t.Errorf("event %d: content part %d annotations = %v, want an array", i, j, part["annotations"])
			}
			if _, ok := part["logprobs"].([]any); !ok {
				t.Errorf("event %d: content part %d logprobs = %v, want an array", i, j, part["logprobs"])
			}
		}
	case "reasoning":
		if _, ok := it["summary"].([]any); !ok {
			t.Errorf("event %d: reasoning item summary = %v, want an array", i, it["summary"])
		}
	case "function_call":
		for _, k := range []string{"call_id", "name", "arguments"} {
			if s, ok := it[k].(string); !ok || s == "" && k != "arguments" {
				t.Errorf("event %d: function_call item %s = %v, want a string", i, k, it[k])
			}
		}
	default:
		t.Errorf("event %d: item type %q, which the API does not define", i, typ)
	}
}

// isInt reports whether n is an integer rather than a fractional number.
func isInt(n json.Number) bool {
	return !strings.ContainsAny(n.String(), ".eE")
}
