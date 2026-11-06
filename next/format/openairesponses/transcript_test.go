package openairesponses

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// trParse decodes a client message.
func trParse(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse message %s: %v", s, err)
	}
	return m
}

// trDecode decodes a turn body, which must be a JSON object.
func trDecode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not a JSON object: %v: %s", err, b)
	}
	return m
}

// trNext makes msg into a turn on tr and returns its decoded body.
func trNext(t *testing.T, tr *Transcript, msg string) map[string]any {
	t.Helper()
	turn, e := tr.Next(trParse(t, msg))
	if e != nil {
		t.Fatalf("Next(%s): %s %d %q", msg, e.Code, e.Status, e.Message)
	}
	return trDecode(t, turn.Body)
}

// trIDs returns the id of each item of a body's input, in order ("" for an item without one).
func trIDs(t *testing.T, m map[string]any) []string {
	t.Helper()
	list, ok := m["input"].([]any)
	if !ok {
		t.Fatalf("input = %v, want a list", m["input"])
	}
	ids := []string{}
	for _, v := range list {
		item, _ := v.(map[string]any)
		id, _ := item["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

func trWantIDs(t *testing.T, m map[string]any, want ...string) {
	t.Helper()
	if got := trIDs(t, m); !reflect.DeepEqual(got, want) {
		t.Fatalf("input ids = %q, want %q: %v", got, want, m["input"])
	}
}

// trStandalone checks what every turn body carries: no type, no previous_response_id, streamed.
func trStandalone(t *testing.T, m map[string]any) {
	t.Helper()
	for _, k := range []string{"type", "previous_response_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("body has %s, want none: %v", k, m)
		}
	}
	if m["stream"] != true {
		t.Errorf("stream = %v, want true", m["stream"])
	}
}

// trWantBadRequest checks a 400 error of the kind every websocket request error is.
func trWantBadRequest(t *testing.T, e *llm.Error, msg string) {
	t.Helper()
	if e == nil {
		t.Fatalf("no error, want 400 %q", msg)
	}
	if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusBadRequest || e.Scope != llm.ScopeRequest || e.Message != msg {
		t.Fatalf("error = {code %q, status %d, scope %q, message %q}, want {code %q, status 400, scope %q, message %q}",
			e.Code, e.Status, e.Scope, e.Message, llm.CodeInvalidRequest, llm.ScopeRequest, msg)
	}
}

const (
	trFirstCreate  = `{"type":"response.create","model":"m1","instructions":"be brief","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`
	trFirstOutput  = `[{"type":"message","role":"assistant","id":"a1","content":"hello"}]`
	trPrevNotFound = "Previous response is not available on this websocket; resend the full conversation input without previous_response_id"
)

// trStarted returns a transcript after one completed turn: input [u1], output [a1].
func trStarted(t *testing.T) *Transcript {
	t.Helper()
	tr := &Transcript{}
	turn, e := tr.Next(trParse(t, trFirstCreate))
	if e != nil {
		t.Fatalf("first create: %s %q", e.Code, e.Message)
	}
	tr.Commit(turn.Body, json.RawMessage(trFirstOutput))
	return tr
}

func TestTranscriptFirstCreate(t *testing.T) {
	t.Run("body is the message without type, streamed", func(t *testing.T) {
		tr := &Transcript{}
		m := trNext(t, tr, `{"type":"response.create","model":"m1","stream":false,"instructions":"be brief","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`)
		trStandalone(t, m)
		if m["model"] != "m1" || m["instructions"] != "be brief" {
			t.Errorf("model/instructions = %v/%v, want m1/be brief", m["model"], m["instructions"])
		}
		trWantIDs(t, m, "u1")
	})

	t.Run("absent input becomes an empty list", func(t *testing.T) {
		tr := &Transcript{}
		m := trNext(t, tr, `{"type":"response.create","model":"m1"}`)
		trStandalone(t, m)
		if list, ok := m["input"].([]any); !ok || len(list) != 0 {
			t.Errorf("input = %v, want an empty list", m["input"])
		}
	})

	t.Run("missing model fails", func(t *testing.T) {
		tr := &Transcript{}
		_, e := tr.Next(trParse(t, `{"type":"response.create","input":[]}`))
		trWantBadRequest(t, e, "missing model in response.create request")
	})

	t.Run("input that is not a list fails", func(t *testing.T) {
		tr := &Transcript{}
		_, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":"hi"}`))
		trWantBadRequest(t, e, "websocket request requires array field: input")
	})
}

func TestTranscriptRejectsBadMessages(t *testing.T) {
	t.Run("append before any create", func(t *testing.T) {
		tr := &Transcript{}
		_, e := tr.Next(trParse(t, `{"type":"response.append","input":[]}`))
		trWantBadRequest(t, e, "websocket request received before response.create")
	})

	t.Run("unsupported type before any create", func(t *testing.T) {
		tr := &Transcript{}
		_, e := tr.Next(trParse(t, `{"type":"response.interrupt"}`))
		trWantBadRequest(t, e, "unsupported websocket request type: response.interrupt")
	})

	t.Run("unsupported type after a turn", func(t *testing.T) {
		tr := trStarted(t)
		_, e := tr.Next(trParse(t, `{"type":"response.interrupt","input":[]}`))
		trWantBadRequest(t, e, "unsupported websocket request type: response.interrupt")
	})

	for _, typ := range []string{"response.create", "response.append"} {
		t.Run("non-array input after a turn, "+typ, func(t *testing.T) {
			tr := trStarted(t)
			_, e := tr.Next(trParse(t, `{"type":"`+typ+`","input":"hi"}`))
			trWantBadRequest(t, e, "websocket request requires array field: input")
		})
	}
}

func TestTranscriptPreviousResponseNotFound(t *testing.T) {
	for _, tc := range []struct{ name, msg string }{
		{"create", `{"type":"response.create","model":"m1","previous_response_id":"resp_1","input":[]}`},
		{"append", `{"type":"response.append","previous_response_id":"resp_1","input":[]}`},
	} {
		t.Run("before any turn, "+tc.name, func(t *testing.T) {
			tr := &Transcript{}
			_, e := tr.Next(trParse(t, tc.msg))
			if e == nil {
				t.Fatal("no error, want previous response not found")
			}
			if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusConflict || e.Scope != llm.ScopeRequest ||
				e.ProviderCode != "previous_response_not_found" || e.Message != trPrevNotFound {
				t.Fatalf("error = %+v, want 409 previous_response_not_found", e)
			}
			want := map[string]any{"message": trPrevNotFound, "type": "invalid_request_error",
				"code": "previous_response_not_found", "param": "previous_response_id"}
			if got := trDecode(t, e.Raw)["error"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("raw error = %v, want %v", got, want)
			}
		})
	}
}

func TestTranscriptContinuesCommittedTurn(t *testing.T) {
	t.Run("input is the last request, last output, then the new input", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trStandalone(t, m)
		trWantIDs(t, m, "u1", "a1", "u2")
		if m["model"] != "m1" || m["instructions"] != "be brief" {
			t.Errorf("model/instructions = %v/%v, want the last request's m1/be brief", m["model"], m["instructions"])
		}
	})

	t.Run("the message's own model and instructions win", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","model":"m2","instructions":"be terse","previous_response_id":"resp_1","input":[]}`)
		if m["model"] != "m2" || m["instructions"] != "be terse" {
			t.Errorf("model/instructions = %v/%v, want m2/be terse", m["model"], m["instructions"])
		}
		trWantIDs(t, m, "u1", "a1")
	})

	t.Run("append merges like create", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.append","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trStandalone(t, m)
		trWantIDs(t, m, "u1", "a1", "u2")
		if m["model"] != "m1" {
			t.Errorf("model = %v, want m1", m["model"])
		}
	})
}

func TestTranscriptReplacement(t *testing.T) {
	t.Run("a function call in a create without parent replaces the transcript", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","input":[{"type":"function_call","id":"fc-9","call_id":"c9","name":"f","arguments":"{}"},{"type":"function_call_output","id":"o9","call_id":"c9","output":"ok"}]}`)
		trStandalone(t, m)
		trWantIDs(t, m, "fc-9", "o9")
		if m["model"] != "m1" {
			t.Errorf("model = %v, want m1 copied from the last request", m["model"])
		}
	})

	t.Run("an assistant message in an append replaces the transcript", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.append","input":[{"type":"message","role":"assistant","id":"a9","content":"patched"}]}`)
		trWantIDs(t, m, "a9")
	})

	t.Run("a tool output alone merges", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","input":[{"type":"function_call_output","id":"o2","call_id":"c1","output":"ok"}]}`)
		trWantIDs(t, m, "u1", "a1", "o2")
	})

	t.Run("a function call with a parent merges", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call","id":"fc-9","call_id":"c9","name":"f","arguments":"{}"}]}`)
		trWantIDs(t, m, "u1", "a1", "fc-9")
	})
}

// Codex's own compaction: a summary user message that starts with compactionSummaryPrefix.
func TestTranscriptCodexCompactionSummary(t *testing.T) {
	summary := fmt.Sprintf("%q", compactionSummaryPrefix+"\nSummary body.")

	t.Run("summary in a create without parent replaces the transcript", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","input":[{"type":"message","role":"developer","id":"d1","content":"ctx"},{"type":"message","role":"user","id":"s1","content":`+summary+`},{"type":"message","role":"user","id":"u9","content":"continue"}]}`)
		trStandalone(t, m)
		trWantIDs(t, m, "d1", "s1", "u9")
	})

	t.Run("summary with a parent merges", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"developer","id":"d1","content":"ctx"},{"type":"message","role":"user","id":"s1","content":`+summary+`}]}`)
		trWantIDs(t, m, "u1", "a1", "d1", "s1")
	})

	t.Run("ordinary user text is not a summary", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","input":[{"type":"message","role":"user","id":"u9","content":"Please summarize this."}]}`)
		trWantIDs(t, m, "u1", "a1", "u9")
	})
}

func TestTranscriptCompactionItems(t *testing.T) {
	t.Run("client input with a compaction item is the whole conversation: no merge", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"compaction","id":"cmp-1","encrypted_content":"enc"},{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trWantIDs(t, m, "cmp-1", "u2")
		if m["model"] == nil {
			t.Error("the model was not copied from the last request")
		}
	})

	t.Run("compaction in the last output replaces the last trigger", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":[{"type":"message","role":"user","id":"u1","content":"hi"},{"type":"compaction_trigger","id":"t1"}]}`))
		if e != nil {
			t.Fatalf("first create: %s %q", e.Code, e.Message)
		}
		tr.Commit(turn.Body, json.RawMessage(`[{"type":"compaction","id":"cmp-1","encrypted_content":"enc"},{"type":"message","role":"assistant","id":"a1","content":"hi"}]`))
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trWantIDs(t, m, "u1", "cmp-1", "a1", "u2")
	})
}

func TestTranscriptDedupe(t *testing.T) {
	t.Run("a repeated tool call with the same call_id keeps the first", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":[{"type":"function_call","id":"fc-1","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","id":"o1","call_id":"c1","output":"ok"}]}`))
		if e != nil {
			t.Fatalf("first create: %s %q", e.Code, e.Message)
		}
		tr.Commit(turn.Body, json.RawMessage(`[{"type":"function_call","id":"fc-1","call_id":"c1","name":"f","arguments":"{}"}]`))
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"m2","content":"next"}]}`)
		trWantIDs(t, m, "fc-1", "o1", "m2")
	})

	t.Run("items sharing an id keep the last", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":[{"type":"message","role":"user","id":"m1","content":"old"}]}`))
		if e != nil {
			t.Fatalf("first create: %s %q", e.Code, e.Message)
		}
		tr.Commit(turn.Body, json.RawMessage(`[]`))
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"m1","content":"new"}]}`)
		trWantIDs(t, m, "m1")
		list := m["input"].([]any)
		if got := list[0].(map[string]any)["content"]; got != "new" {
			t.Errorf("kept item content = %v, want new", got)
		}
	})

	t.Run("an earlier item that a tool output answers beats a later unanswered one", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":[{"type":"function_call","id":"X","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","id":"o1","call_id":"c1","output":"ok"}]}`))
		if e != nil {
			t.Fatalf("first create: %s %q", e.Code, e.Message)
		}
		tr.Commit(turn.Body, json.RawMessage(`[]`))
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"X","content":"x"}]}`)
		trWantIDs(t, m, "X", "o1")
		if got := m["input"].([]any)[0].(map[string]any)["type"]; got != "function_call" {
			t.Errorf("kept X type = %v, want function_call", got)
		}
	})

	t.Run("a later answered item beats an earlier unanswered one", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","model":"m1","input":[{"type":"message","role":"user","id":"X","content":"x"}]}`))
		if e != nil {
			t.Fatalf("first create: %s %q", e.Code, e.Message)
		}
		tr.Commit(turn.Body, json.RawMessage(`[{"type":"function_call","id":"X","call_id":"c1","name":"f","arguments":"{}"}]`))
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","id":"o1","call_id":"c1","output":"ok"}]}`)
		trWantIDs(t, m, "X", "o1")
		if got := m["input"].([]any)[0].(map[string]any)["type"]; got != "function_call" {
			t.Errorf("kept X type = %v, want function_call", got)
		}
	})
}

func TestTranscriptTokenCountsInReplay(t *testing.T) {
	tr := &Transcript{}
	turn, e := tr.Next(trParse(t, trFirstCreate))
	if e != nil {
		t.Fatalf("first create: %s %q", e.Code, e.Message)
	}
	tr.Commit(turn.Body, json.RawMessage(`[
		{"type":"message","role":"assistant","id":"a1","internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_token_count":64},"content":"hi"},
		{"type":"message","role":"assistant","id":"a2","internal_chat_message_metadata_passthrough":{"content_token_count":3},"content":"only count"},
		{"type":"message","role":"assistant","id":"a3","internal_chat_message_metadata_passthrough":{"turn_id":"t3"},"content":"no count"},
		{"type":"function_call","id":"fc-1","call_id":"c1","name":"f","arguments":"{}","internal_chat_message_metadata_passthrough":{"turn_id":"t1","content_token_count":5}}
	]`))
	m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
	trWantIDs(t, m, "u1", "a1", "a2", "a3", "fc-1", "u2")
	list := m["input"].([]any)
	passthrough := func(i int) (map[string]any, bool) {
		item := list[i].(map[string]any)
		p, ok := item["internal_chat_message_metadata_passthrough"].(map[string]any)
		return p, ok
	}
	if p, ok := passthrough(1); !ok || !reflect.DeepEqual(p, map[string]any{"turn_id": "t1"}) {
		t.Errorf("replayed a1 passthrough = %v (present %t), want only turn_id", p, ok)
	}
	if _, ok := passthrough(2); ok {
		t.Errorf("replayed a2 keeps an emptied passthrough object")
	}
	if p, ok := passthrough(3); !ok || !reflect.DeepEqual(p, map[string]any{"turn_id": "t3"}) {
		t.Errorf("replayed a3 passthrough = %v, want unchanged", p)
	}
	if p, ok := passthrough(4); !ok || !reflect.DeepEqual(p, map[string]any{"turn_id": "t1"}) {
		t.Errorf("replayed fc-1 passthrough = %v, want only turn_id", p)
	}

	t.Run("client-sent input keeps its token count", func(t *testing.T) {
		tr := trStarted(t)
		m := trNext(t, tr, `{"type":"response.append","input":[{"type":"function_call_output","id":"o9","call_id":"c9","output":"ok","internal_chat_message_metadata_passthrough":{"content_token_count":99}}]}`)
		last := m["input"].([]any)[2].(map[string]any)
		want := map[string]any{"content_token_count": float64(99)}
		if got := last["internal_chat_message_metadata_passthrough"]; !reflect.DeepEqual(got, want) {
			t.Errorf("client-sent passthrough = %v, want %v", got, want)
		}
	})
}

func TestTranscriptWarmup(t *testing.T) {
	t.Run("IsWarmup", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			msg  string
			want bool
		}{
			{"create with generate false", `{"type":"response.create","generate":false}`, true},
			{"create with generate true", `{"type":"response.create","generate":true}`, false},
			{"create without generate", `{"type":"response.create"}`, false},
			{"append with generate false", `{"type":"response.append","generate":false}`, false},
			{"quoted false is not generate false", `{"type":"response.create","generate":"false"}`, false},
		} {
			if got := IsWarmup(trParse(t, tc.msg)); got != tc.want {
				t.Errorf("%s: IsWarmup = %t, want %t", tc.name, got, tc.want)
			}
		}
	})

	t.Run("a warmup on a fresh transcript is marked and streamed", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		if !turn.Warmup {
			t.Fatal("Warmup = false, want true")
		}
		m := trDecode(t, turn.Body)
		trStandalone(t, m)
		trWantIDs(t, m, "u1")
	})

	t.Run("a create with the warmup's ID merges the warmup's input", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		tr.Warmup(turn.Body, "resp_prewarm_x")
		m := trNext(t, tr, `{"type":"response.create","previous_response_id":"resp_prewarm_x","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trStandalone(t, m)
		trWantIDs(t, m, "u1", "u2")
		if m["model"] != "m1" {
			t.Errorf("model = %v, want m1 from the warmup", m["model"])
		}
	})

	t.Run("an append with the warmup's ID merges too", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		tr.Warmup(turn.Body, "resp_prewarm_x")
		m := trNext(t, tr, `{"type":"response.append","previous_response_id":"resp_prewarm_x","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`)
		trWantIDs(t, m, "u1", "u2")
	})

	t.Run("a different previous_response_id after a warmup is not found", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		tr.Warmup(turn.Body, "resp_prewarm_x")
		_, e = tr.Next(trParse(t, `{"type":"response.create","previous_response_id":"resp_other","input":[]}`))
		if e == nil || e.ProviderCode != "previous_response_not_found" || e.Status != http.StatusConflict {
			t.Fatalf("error = %+v, want previous_response_not_found 409", e)
		}
	})

	t.Run("Warmup removes generate from the stored request", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		tr.Warmup(turn.Body, "resp_prewarm_x")
		if _, ok := tr.last["generate"]; ok {
			t.Errorf("stored request keeps generate: %s", tr.last["generate"])
		}
		if _, ok := tr.last["model"]; !ok {
			t.Errorf("stored request lost model")
		}
	})

	t.Run("a create without parent after a warmup starts again", func(t *testing.T) {
		tr := &Transcript{}
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"model":"m1","input":[{"type":"message","role":"user","id":"u1","content":"hi"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		tr.Warmup(turn.Body, "resp_prewarm_x")
		turn, e = tr.Next(trParse(t, `{"type":"response.create","input":[{"type":"message","role":"user","id":"u3","content":"fresh"}]}`))
		if e != nil {
			t.Fatalf("create: %s %q", e.Code, e.Message)
		}
		if turn.Warmup {
			t.Error("Warmup = true, want false")
		}
		m := trDecode(t, turn.Body)
		trStandalone(t, m)
		trWantIDs(t, m, "u3")
	})

	t.Run("a warmup with the committed turn as parent merges and is marked", func(t *testing.T) {
		tr := trStarted(t)
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		if !turn.Warmup {
			t.Fatal("Warmup = false, want true")
		}
		trWantIDs(t, trDecode(t, turn.Body), "u1", "a1", "u2")
	})

	t.Run("a warmup without parent on a committed transcript starts again", func(t *testing.T) {
		tr := trStarted(t)
		turn, e := tr.Next(trParse(t, `{"type":"response.create","generate":false,"input":[{"type":"message","role":"user","id":"u9","content":"new"}]}`))
		if e != nil {
			t.Fatalf("warmup: %s %q", e.Code, e.Message)
		}
		if !turn.Warmup {
			t.Fatal("Warmup = false, want true")
		}
		trWantIDs(t, trDecode(t, turn.Body), "u9")
	})
}

func TestTranscriptNextDoesNotChangeState(t *testing.T) {
	t.Run("the same message gives the same body twice", func(t *testing.T) {
		tr := trStarted(t)
		msg := `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"message","role":"user","id":"u2","content":"more"}]}`
		first := trNext(t, tr, msg)
		second := trNext(t, tr, msg)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("second Next differs:\n first %v\nsecond %v", first, second)
		}
		trWantIDs(t, second, "u1", "a1", "u2")
	})

	t.Run("a create on a fresh transcript does not start it", func(t *testing.T) {
		tr := &Transcript{}
		trNext(t, tr, trFirstCreate)
		if tr.Started() {
			t.Fatal("Started = true after Next only, want false")
		}
	})
}

func TestTranscriptCommitAndReset(t *testing.T) {
	t.Run("Started is false before a turn and true after Commit", func(t *testing.T) {
		tr := &Transcript{}
		if tr.Started() {
			t.Fatal("Started = true on a new transcript")
		}
		if !trStarted(t).Started() {
			t.Fatal("Started = false after Commit")
		}
	})

	t.Run("Reset forgets the conversation", func(t *testing.T) {
		tr := trStarted(t)
		tr.Reset()
		if tr.Started() {
			t.Fatal("Started = true after Reset")
		}
		_, e := tr.Next(trParse(t, `{"type":"response.create","previous_response_id":"resp_1","input":[]}`))
		if e == nil || e.Status != http.StatusConflict || e.ProviderCode != "previous_response_not_found" {
			t.Fatalf("error = %+v, want previous_response_not_found 409", e)
		}
		m := trNext(t, tr, `{"type":"response.create","model":"m2","input":[{"type":"message","role":"user","id":"u5","content":"new"}]}`)
		trWantIDs(t, m, "u5")
	})
}

func TestWebsocketErrors(t *testing.T) {
	rawError := func(t *testing.T, e *llm.Error) map[string]any {
		t.Helper()
		m := trDecode(t, e.Raw)
		inner, ok := m["error"].(map[string]any)
		if !ok || len(m) != 1 {
			t.Fatalf("raw = %s, want {\"error\":{...}}", e.Raw)
		}
		return inner
	}

	t.Run("PreviousResponseNotFound", func(t *testing.T) {
		e := PreviousResponseNotFound()
		if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusConflict || e.Scope != llm.ScopeRequest ||
			e.ProviderCode != "previous_response_not_found" || e.Message != trPrevNotFound {
			t.Fatalf("error = %+v", e)
		}
		want := map[string]any{"message": trPrevNotFound, "type": "invalid_request_error",
			"code": "previous_response_not_found", "param": "previous_response_id"}
		if got := rawError(t, e); !reflect.DeepEqual(got, want) {
			t.Fatalf("raw error = %v, want %v", got, want)
		}
	})

	t.Run("UpstreamWebsocketLost", func(t *testing.T) {
		e := UpstreamWebsocketLost()
		if e.Code != llm.CodeUnavailable || e.Status != http.StatusBadGateway || e.Scope != llm.ScopeRequest ||
			e.ProviderCode != "upstream_websocket_lost" || e.Retryable {
			t.Fatalf("error = %+v, want unavailable 502 request scope, not retryable", e)
		}
		want := map[string]any{"message": e.Message, "type": "server_error", "code": "upstream_websocket_lost"}
		if got := rawError(t, e); !reflect.DeepEqual(got, want) {
			t.Fatalf("raw error = %v, want %v", got, want)
		}
	})

	t.Run("MessageTooBig without a reason", func(t *testing.T) {
		e := MessageTooBig("")
		if e.Code != llm.CodeInvalidRequest || e.Status != http.StatusRequestEntityTooLarge || e.Scope != llm.ScopeRequest ||
			e.ProviderCode != "message_too_big" || e.Message != "message too big" {
			t.Fatalf("error = %+v", e)
		}
		want := map[string]any{"message": "message too big", "type": "invalid_request_error", "code": "message_too_big"}
		if got := rawError(t, e); !reflect.DeepEqual(got, want) {
			t.Fatalf("raw error = %v, want %v", got, want)
		}
	})

	t.Run("MessageTooBig with the upstream reason", func(t *testing.T) {
		e := MessageTooBig("frame too large")
		if e.Message != "frame too large" {
			t.Fatalf("Message = %q, want the upstream reason", e.Message)
		}
		want := map[string]any{"message": "frame too large", "type": "invalid_request_error", "code": "message_too_big"}
		if got := rawError(t, e); !reflect.DeepEqual(got, want) {
			t.Fatalf("raw error = %v, want %v", got, want)
		}
	})
}

func TestWebsocketWarmupMessages(t *testing.T) {
	msgs := WarmupMessages("resp_w", "m1", 1700000000)
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	created := trDecode(t, msgs[0])
	if created["type"] != "response.created" || created["sequence_number"] != float64(0) {
		t.Fatalf("first message = %v, want response.created sequence 0", created)
	}
	r := created["response"].(map[string]any)
	wantCreated := map[string]any{"id": "resp_w", "object": "response", "created_at": float64(1700000000),
		"status": "in_progress", "background": false, "error": nil, "output": []any{}, "model": "m1"}
	if !reflect.DeepEqual(r, wantCreated) {
		t.Fatalf("created response = %v, want %v", r, wantCreated)
	}

	done := trDecode(t, msgs[1])
	if done["type"] != "response.completed" || done["sequence_number"] != float64(1) {
		t.Fatalf("second message = %v, want response.completed sequence 1", done)
	}
	d := done["response"].(map[string]any)
	if d["id"] != "resp_w" || d["status"] != "completed" || d["model"] != "m1" || d["created_at"] != float64(1700000000) {
		t.Fatalf("completed response = %v", d)
	}
	wantUsage := map[string]any{"input_tokens": float64(0), "input_tokens_details": map[string]any{"cached_tokens": float64(0)},
		"output_tokens": float64(0), "output_tokens_details": map[string]any{"reasoning_tokens": float64(0)}, "total_tokens": float64(0)}
	if !reflect.DeepEqual(d["usage"], wantUsage) {
		t.Fatalf("usage = %v, want zeros %v", d["usage"], wantUsage)
	}

	t.Run("no model leaves the field out", func(t *testing.T) {
		for _, b := range WarmupMessages("resp_w", "", 0) {
			if _, ok := trDecode(t, b)["response"].(map[string]any)["model"]; ok {
				t.Fatalf("message has a model field: %s", b)
			}
		}
	})
}

func TestWebsocketInterruptedMessage(t *testing.T) {
	t.Run("no output is an empty list", func(t *testing.T) {
		m := trDecode(t, InterruptedMessage("resp_i", nil))
		if m["type"] != "response.incomplete" {
			t.Fatalf("type = %v, want response.incomplete", m["type"])
		}
		r := m["response"].(map[string]any)
		if r["id"] != "resp_i" || r["status"] != "incomplete" ||
			!reflect.DeepEqual(r["incomplete_details"], map[string]any{"reason": "interrupted"}) {
			t.Fatalf("response = %v", r)
		}
		if out, ok := r["output"].([]any); !ok || len(out) != 0 {
			t.Fatalf("output = %v, want []", r["output"])
		}
	})

	t.Run("finished output items are kept", func(t *testing.T) {
		m := trDecode(t, InterruptedMessage("resp_i", []json.RawMessage{json.RawMessage(`{"type":"message","id":"m1"}`)}))
		out := m["response"].(map[string]any)["output"].([]any)
		if len(out) != 1 || out[0].(map[string]any)["id"] != "m1" {
			t.Fatalf("output = %v, want the item m1", out)
		}
	})
}
