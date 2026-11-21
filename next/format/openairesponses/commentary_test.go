package openairesponses

import (
	"encoding/json"
	"strings"
	"testing"
)

// A Codex reply can hold a commentary message before the final answer. Both are output items of
// their own: the commentary must not become part of the answer's text.
func TestCommentaryItemStaysOutOfFinalAnswer(t *testing.T) {
	commentary := `{"id":"msg_c","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Looking."}]}`
	final := `{"id":"msg_f","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]}`
	out := writeStream(t, []string{
		`{"type":"response.created","response":{"id":"r1","model":"m","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_c","type":"message","role":"assistant","phase":"commentary","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_c","content_index":0,"delta":"Looking."}`,
		`{"type":"response.output_item.done","output_index":0,"item":` + commentary + `}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_f","type":"message","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_f","content_index":0,"delta":"Done."}`,
		`{"type":"response.output_item.done","output_index":1,"item":` + final + `}`,
		`{"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","output":[` + commentary + `,` + final + `]}}`,
	})

	var done string
	for _, block := range strings.Split(out, "\n\n") {
		if strings.HasPrefix(block, "event: response.completed\n") {
			done = strings.TrimPrefix(strings.SplitN(block, "\n", 2)[1], "data: ")
		}
	}
	if done == "" {
		t.Fatalf("stream has no response.completed:\n%s", out)
	}
	var ev struct {
		Response struct {
			Output []struct {
				ID      string `json:"id"`
				Phase   string `json:"phase"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(done), &ev); err != nil {
		t.Fatalf("response.completed %s: %v", done, err)
	}
	got := ev.Response.Output
	if len(got) != 2 || got[0].ID != "msg_c" || got[1].ID != "msg_f" {
		t.Fatalf("output = %#v, want the commentary and the final answer as separate items", got)
	}
	if got[0].Phase != "commentary" || len(got[0].Content) != 1 || got[0].Content[0].Text != "Looking." {
		t.Errorf("commentary item = %#v, want its own text", got[0])
	}
	if got[1].Phase != "final_answer" || len(got[1].Content) != 1 || got[1].Content[0].Text != "Done." {
		t.Errorf("final item = %#v, want only the final answer's text", got[1])
	}
}

// A non-streamed Response keeps its message items apart too.
func TestCommentaryItemStaysApartWhenAggregated(t *testing.T) {
	body := `{"id":"r1","object":"response","model":"m","status":"completed","output":[` +
		`{"id":"msg_c","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Looking."}]},` +
		`{"id":"msg_f","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]}]}`
	evs, err := DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	req, err := ParseRequest([]byte(`{"model":"m","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	a := NewAggregator(req)
	for _, ev := range evs {
		a.Add(ev)
	}
	got, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), `"phase":"commentary"`) != 1 || strings.Count(string(got), `"phase":"final_answer"`) != 1 {
		t.Errorf("response = %s, want the two items unchanged", got)
	}
	if strings.Contains(string(got), "Looking.Done.") || strings.Contains(string(got), "Done.Looking.") {
		t.Errorf("response = %s, want the texts kept apart", got)
	}
}
