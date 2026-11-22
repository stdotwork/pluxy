package geminigenerate

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// responseOrder returns the call IDs of the functionResponse parts of contents[i], in order.
func responseOrder(t *testing.T, body []byte, i int) []string {
	t.Helper()
	var b struct {
		Contents []struct {
			Parts []struct {
				FunctionResponse *struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &b); err != nil || len(b.Contents) <= i {
		t.Fatalf("no contents[%d] in %s (%v)", i, body, err)
	}
	var ids []string
	for _, p := range b.Contents[i].Parts {
		if p.FunctionResponse != nil {
			ids = append(ids, p.FunctionResponse.ID)
		}
	}
	return ids
}

// Gemini matches function responses to the calls they answer by position (Google's function
// calling guide: "include the responses in the same order as they were requested"), so tool
// results the client sent in another order go out in the order of the calls, including two calls
// to the same function.
func TestEncodeOrdersFunctionResponsesByCall(t *testing.T) {
	user := llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Weather and time?"}}}
	for _, c := range []struct {
		name  string
		items []llm.Item
		want  string
	}{
		{"different functions", []llm.Item{user,
			{Type: llm.ItemTypeToolCall, CallID: "call_a", Name: "get_weather", Arguments: `{"city":"Oslo"}`},
			{Type: llm.ItemTypeToolCall, CallID: "call_b", Name: "get_time", Arguments: `{"city":"Oslo"}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_b", Output: "12:00"},
			{Type: llm.ItemTypeToolResult, CallID: "call_a", Output: "4C"},
		}, "call_a call_b"},
		{"one function twice", []llm.Item{user,
			{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "get_weather", Arguments: `{"city":"Oslo"}`},
			{Type: llm.ItemTypeToolCall, CallID: "call_2", Name: "get_weather", Arguments: `{"city":"Bergen"}`},
			{Type: llm.ItemTypeToolCall, CallID: "call_3", Name: "get_weather", Arguments: `{"city":"Tromso"}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_3", Output: "-2C"},
			{Type: llm.ItemTypeToolResult, CallID: "call_1", Output: "4C"},
			{Type: llm.ItemTypeToolResult, CallID: "call_2", Output: "6C"},
		}, "call_1 call_2 call_3"},
		{"results with text after them", []llm.Item{user,
			{Type: llm.ItemTypeToolCall, CallID: "call_a", Name: "get_weather", Arguments: `{}`},
			{Type: llm.ItemTypeToolCall, CallID: "call_b", Name: "get_time", Arguments: `{}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_b", Output: "12:00"},
			{Type: llm.ItemTypeToolResult, CallID: "call_a", Output: "4C"},
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Thanks"}}},
		}, "call_a call_b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, _, err := EncodeRequest(llm.Request{Model: "gemini-x", Items: c.items})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(responseOrder(t, body, 2), " "); got != c.want {
				t.Errorf("function responses in order %q, want %q: %s", got, c.want, body)
			}
		})
	}
}

// callIDs returns the call IDs of the tool calls in events.
func callIDs(events []llm.Event) []string {
	var ids []string
	for _, ev := range events {
		if ev.Type == llm.EventItemStart && ev.Item != nil && ev.Item.Kind == llm.ItemToolCall {
			ids = append(ids, ev.Item.CallID)
		}
	}
	return ids
}

// Gemini's functionCall.id is optional. The IDs made for calls that come without one are unique
// across the replies of a conversation, not only within one reply: a client that replays the
// history must not find two calls with one ID.
func TestMadeCallIDsUniqueAcrossReplies(t *testing.T) {
	reply := func(responseID string) string {
		id := ""
		if responseID != "" {
			id = `,"responseId":"` + responseID + `"`
		}
		return `{"candidates":[{"content":{"role":"model","parts":[` +
			`{"functionCall":{"name":"get_weather","args":{"city":"Oslo"}}},` +
			`{"functionCall":{"name":"get_weather","args":{"city":"Bergen"}}}]},"finishReason":"STOP","index":0}]` + id + `}`
	}
	for _, c := range []struct{ name, first, second string }{
		{"with response IDs", "resp-1", "resp-2"},
		{"without response IDs", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, r := range []string{c.first, c.second} {
				d := NewStreamDecoder()
				events, err := d.Decode([]byte(reply(r)))
				if err != nil {
					t.Fatal(err)
				}
				end, err := d.Close()
				if err != nil {
					t.Fatal(err)
				}
				streamed := callIDs(append(events, end...))
				other := r // another reply, not streamed
				if r != "" {
					other = r + "-whole"
				}
				whole, err := DecodeResponse([]byte(reply(other)))
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range append(streamed, callIDs(whole)...) {
					if id == "" || seen[id] {
						t.Errorf("call ID %q made twice (or empty) across replies", id)
					}
					seen[id] = true
				}
			}
			if len(seen) != 8 {
				t.Errorf("%d distinct call IDs, want 8", len(seen))
			}
		})
	}
	// A call that comes with an ID keeps it.
	d := NewStreamDecoder()
	events, _ := d.Decode([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"fc-1","name":"f","args":{}}}]},"finishReason":"STOP","index":0}],"responseId":"r"}`))
	if ids := callIDs(events); len(ids) != 1 || ids[0] != "fc-1" {
		t.Errorf("call IDs %q, want Gemini's own fc-1", ids)
	}
}

// A reply that ends with finish reason MALFORMED_FUNCTION_CALL (the model wrote a function call
// Gemini could not parse) is an error that names it, streamed or not, and not a plain stop: the
// client would otherwise take an empty or cut reply as the model's answer.
func TestMalformedFunctionCallIsAnError(t *testing.T) {
	const reply = `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL",` +
		`"finishMessage":"Malformed function call: print(default_api.get_weather(city=))","index":0}],` +
		`"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":0,"totalTokenCount":10},"responseId":"r"}`
	check := func(name string, err error) {
		t.Helper()
		var e *llm.Error
		if !errors.As(err, &e) {
			t.Fatalf("%s: err = %v, want an *llm.Error", name, err)
		}
		if e.ProviderCode != "MALFORMED_FUNCTION_CALL" || !strings.Contains(e.Message, "MALFORMED_FUNCTION_CALL") ||
			!strings.Contains(e.Message, "default_api.get_weather(city=)") || e.Usage == nil {
			t.Errorf("%s: error %+v, want one naming MALFORMED_FUNCTION_CALL, with Gemini's finish message and the usage", name, e)
		}
	}
	d := NewStreamDecoder()
	if _, err := d.Decode([]byte(reply)); err != nil {
		t.Fatal(err)
	}
	end, err := d.Close()
	for _, ev := range end {
		if ev.Type == llm.EventFinish {
			t.Errorf("stream: finished with %+v", ev.Finish)
		}
	}
	check("stream", err)
	_, err = DecodeResponse([]byte(reply))
	check("response", err)
}

// A function call with null or missing args is still a call, with {} as its arguments, and the
// reply finishes with tool calls: it does not come out empty.
func TestCallWithoutArgsIsACall(t *testing.T) {
	events, err := DecodeResponse([]byte(`{"candidates":[{"content":{"role":"model","parts":[` +
		`{"functionCall":{"name":"list_files","args":null}},{"functionCall":{"name":"now"}}]},"finishReason":"STOP","index":0}],"responseId":"r"}`))
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	var finish *llm.Finish
	for _, ev := range events {
		if ev.Type == llm.EventToolInputDelta {
			args = append(args, ev.Delta)
		}
		if ev.Type == llm.EventFinish {
			finish = ev.Finish
		}
	}
	if strings.Join(args, " ") != "{} {}" || len(callIDs(events)) != 2 {
		t.Errorf("calls %q with arguments %q, want two calls with {}", callIDs(events), args)
	}
	if finish == nil || finish.Reason != llm.FinishToolCalls {
		t.Errorf("finish %+v, want tool calls", finish)
	}
}

// A functionResponse with neither a name nor an id answers the earliest call still waiting, of
// any name, so on an upstream that pairs results by id it reaches its call. Responses that carry
// a name or an id pair as before.
func TestParseNamelessResponsePairsWithEarliestCall(t *testing.T) {
	call := func(name, id string) string {
		idField := ""
		if id != "" {
			idField = `"id":"` + id + `",`
		}
		return `{"functionCall":{` + idField + `"name":"` + name + `","args":{}}}`
	}
	response := func(name, id string) string {
		fields := `"response":{"result":"ok"}`
		if name != "" {
			fields = `"name":"` + name + `",` + fields
		}
		if id != "" {
			fields = `"id":"` + id + `",` + fields
		}
		return `{"functionResponse":{` + fields + `}}`
	}
	for _, c := range []struct {
		name      string
		calls     []string
		responses []string
		want      []string
	}{
		{"across name queues", []string{call("a", "ca"), call("b", "cb"), call("a", "ca2")},
			[]string{response("", ""), response("", ""), response("", "")}, []string{"ca", "cb", "ca2"}},
		{"after a named response took a later call", []string{call("a", "ca"), call("b", "cb")},
			[]string{response("b", ""), response("", "")}, []string{"cb", "ca"}},
		{"after an id response took the earliest call", []string{call("a", "ca"), call("b", "cb")},
			[]string{response("", "ca"), response("", "")}, []string{"ca", "cb"}},
		{"calls without ids", []string{call("a", ""), call("b", "")},
			[]string{response("", ""), response("", "")}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := `{"contents":[{"role":"user","parts":[{"text":"go"}]},` +
				`{"role":"model","parts":[` + strings.Join(c.calls, ",") + `]},` +
				`{"role":"user","parts":[` + strings.Join(c.responses, ",") + `]}]}`
			req, err := ParseRequest("gemini-2.5-pro", false, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			var calls, results []string
			for _, it := range req.Items {
				switch it.Type {
				case llm.ItemTypeToolCall:
					calls = append(calls, it.CallID)
				case llm.ItemTypeToolResult:
					results = append(results, it.CallID)
				}
			}
			want := c.want
			if want == nil { // ids Gemini did not send are made up: the results must match them
				want = calls
			}
			if strings.Join(results, ",") != strings.Join(want, ",") {
				t.Errorf("result call IDs %q, want %q (calls %q)", results, want, calls)
			}
		})
	}
}
