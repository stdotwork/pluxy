package format_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// imageItem returns an image_generation_call item. status "-" leaves the field out.
func imageItem(status, result string) string {
	st := ""
	if status != "-" {
		st = fmt.Sprintf(`"status":%q,`, status)
	}
	res := "null"
	if result != "" {
		res = fmt.Sprintf("%q", result)
	}
	return fmt.Sprintf(`{"id":"ig_1","type":"image_generation_call",%s"output_format":"png","result":%s}`, st, res)
}

// decodeImageReply decodes a reply holding one image item through the real Responses decoder:
// nonstream is one Response body; done is a stream whose output_item.done carries the item and
// whose terminal event repeats it; terminalOnly is a stream where only the terminal event does.
func decodeImageReply(t *testing.T, mode, item string) ([]llm.Event, []llm.Loss) {
	t.Helper()
	resp := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"m","output":[%s]}`, item)
	if mode == "nonstream" {
		evs, losses, err := openairesponses.DecodeResponseLosses([]byte(resp))
		if err != nil {
			t.Fatal(err)
		}
		return evs, losses
	}
	lines := []string{`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","status":"in_progress","model":"m","output":[]}}`}
	if mode == "done" {
		lines = append(lines, fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":%s}`, item))
	}
	lines = append(lines, fmt.Sprintf(`{"type":"response.completed","sequence_number":3,"response":%s}`, resp))
	d := openairesponses.NewStreamDecoder()
	var evs []llm.Event
	for _, l := range lines {
		e, err := d.Decode([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
	}
	end, err := d.Close()
	if err != nil {
		t.Fatal(err)
	}
	return append(evs, end...), d.Losses()
}

func mediaEvents(evs []llm.Event) []*llm.OutputMedia {
	var out []*llm.OutputMedia
	for _, ev := range evs {
		if ev.Type == llm.EventMedia {
			out = append(out, ev.Media)
		}
	}
	return out
}

var imageModes = []string{"nonstream", "done", "terminalOnly"}

// A finished image is carried whatever its status says, so a Gemini client receives it.
func TestResponsesFinishedImageCarriedWhateverItsStatus(t *testing.T) {
	for _, mode := range imageModes {
		for _, status := range []string{"completed", "generating", "in_progress", "-"} {
			t.Run(mode+"/"+status, func(t *testing.T) {
				evs, losses := decodeImageReply(t, mode, imageItem(status, samplePNG))
				if len(losses) != 0 {
					t.Errorf("losses = %+v, want none", losses)
				}
				m := mediaEvents(evs)
				if len(m) != 1 || m[0].MediaType != "image/png" || m[0].Data != samplePNG {
					t.Fatalf("media = %+v, want the one image", m)
				}
				evs = roundTripMediaEvents(t, evs)
				body, sse, aggLosses, streamLosses, _ := renderGemini(t, evs)
				if !strings.Contains(string(body), samplePNG) || !strings.Contains(sse, samplePNG) {
					t.Errorf("Gemini client did not receive the image: body=%s sse=%s", body, sse)
				}
				if len(aggLosses) != 0 || len(streamLosses) != 0 {
					t.Errorf("render losses: %+v / %+v", aggLosses, streamLosses)
				}
			})
		}
	}
}

// An image that cannot be carried is reported, whatever its status says.
func TestResponsesImageNotCarriedIsReported(t *testing.T) {
	for _, mode := range imageModes {
		for _, c := range []struct{ name, item string }{
			{"failed with data", imageItem("failed", samplePNG)},
			{"failed without data", imageItem("failed", "")},
			{"generating without data", imageItem("generating", "")},
			{"no status without data", imageItem("-", "")},
			{"generating invalid base64", imageItem("generating", "!!!not-base64!!!")},
		} {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				evs, losses := decodeImageReply(t, mode, c.item)
				if m := mediaEvents(evs); len(m) != 0 {
					t.Errorf("media carried: %+v", m)
				}
				if len(losses) != 1 || !strings.HasPrefix(losses[0].Path, "output[0]") {
					t.Errorf("losses = %+v, want one at output[0]", losses)
				}
				assertNoMediaBytesInLosses(t, losses, samplePNG, "!!!not-base64!!!")
			})
		}
	}
}

// A partial image arrives on its own event, not in a final item, and stays out.
func TestResponsesPartialImageStaysOut(t *testing.T) {
	d := openairesponses.NewStreamDecoder()
	var evs []llm.Event
	for _, l := range []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","status":"in_progress","model":"m","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"ig_1","type":"image_generation_call","status":"in_progress"}}`,
		fmt.Sprintf(`{"type":"response.image_generation_call.partial_image","sequence_number":3,"output_index":0,"item_id":"ig_1","partial_image_index":0,"partial_image_b64":%q}`, samplePNG),
	} {
		e, err := d.Decode([]byte(l))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
	}
	if m := mediaEvents(evs); len(m) != 0 || len(d.Losses()) != 0 {
		t.Fatalf("partial image: media=%+v losses=%+v, want neither", m, d.Losses())
	}
}
