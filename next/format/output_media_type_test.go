package format_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A media type is parsed whole before it is compared, so nothing after the subtype can hide an
// SVG from the refusal: a comma and markup, parameters, case, whitespace, or a type not listed.
var hostileMediaTypes = []string{
	"image/svg+xml,<svg/onload=alert(1)>",
	"image/svg+xml,",
	"image/svg+xml <svg/onload=alert(1)>",
	"IMAGE/SVG+XML",
	"  image/svg+xml  ",
	"image/svg+xml;charset=utf-8",
	"image/svg+xml; charset",
	"image/svg",
	"image/png,<svg/onload=alert(1)>",
	"image/",
	"image/x-unlisted",
	"text/html",
	"image/png\x00",
}

// jsonString quotes s as JSON, so control characters survive into the body.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func assertNoCarriedMedia(t *testing.T, evs []llm.Event, losses []llm.Loss) {
	t.Helper()
	for _, ev := range evs {
		if ev.Type == llm.EventMedia || (ev.Candidate != nil && ev.Candidate.Media != nil) {
			t.Errorf("media carried: %+v", ev)
		}
	}
	if len(losses) == 0 {
		t.Errorf("no loss reported for the refused media")
	}
	assertNoMediaBytesInLosses(t, losses, samplePNG)
}

func TestGeminiHostileMediaTypeRefused(t *testing.T) {
	for _, mt := range hostileMediaTypes {
		body := fmt.Sprintf(`{"responseId":"g","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"kept"},{"inlineData":{"mimeType":%s,"data":%q}}]},"finishReason":"STOP"}]}`, jsonString(mt), samplePNG)
		t.Run(fmt.Sprintf("nonstream/%q", mt), func(t *testing.T) {
			evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			assertNoCarriedMedia(t, evs, losses)
		})
		t.Run(fmt.Sprintf("stream/%q", mt), func(t *testing.T) {
			d := geminigenerate.NewStreamDecoder()
			evs, err := d.Decode([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			end, err := d.Close()
			if err != nil {
				t.Fatal(err)
			}
			assertNoCarriedMedia(t, append(evs, end...), d.Losses())
		})
	}
}

func TestResponsesHostileOutputFormatRefused(t *testing.T) {
	for _, f := range []string{"svg+xml,<svg/onload=alert(1)>", "svg+xml", "SVG", " svg ", "png,<svg/onload=alert(1)>", "x-unlisted"} {
		item := fmt.Sprintf(`{"id":"ig_1","type":"image_generation_call","status":"completed","output_format":%s,"result":%q}`, jsonString(f), samplePNG)
		t.Run(fmt.Sprintf("nonstream/%q", f), func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"m","output":[%s]}`, item)
			evs, losses, err := openairesponses.DecodeResponseLosses([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			assertNoCarriedMedia(t, evs, losses)
		})
		t.Run(fmt.Sprintf("stream/%q", f), func(t *testing.T) {
			d := openairesponses.NewStreamDecoder()
			var evs []llm.Event
			for _, line := range []string{
				`{"type":"response.created","sequence_number":1,"response":{"id":"r","object":"response","status":"in_progress","model":"m","output":[]}}`,
				fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":%s}`, item),
				fmt.Sprintf(`{"type":"response.completed","sequence_number":3,"response":{"id":"r","object":"response","status":"completed","model":"m","output":[%s]}}`, item),
			} {
				e, err := d.Decode([]byte(line))
				if err != nil {
					t.Fatal(err)
				}
				evs = append(evs, e...)
			}
			end, err := d.Close()
			if err != nil {
				t.Fatal(err)
			}
			assertNoCarriedMedia(t, append(evs, end...), d.Losses())
		})
	}
}

// The Gemini renderer checks the type again, so an extension that sends a hostile type on a
// media event reaches the client with no inlineData part, streamed or not.
func TestGeminiRenderRefusesHostileMediaType(t *testing.T) {
	for _, mt := range hostileMediaTypes {
		t.Run(fmt.Sprintf("%q", mt), func(t *testing.T) {
			evs := []llm.Event{
				{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m"}},
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: mt, Data: samplePNG}},
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: "stop"}},
			}
			body, sse, aggLosses, streamLosses, _ := renderGemini(t, evs)
			if strings.Contains(string(body), "inlineData") || strings.Contains(sse, "inlineData") {
				t.Errorf("hostile media type reached the client: body=%s sse=%s", body, sse)
			}
			if len(aggLosses) == 0 || len(streamLosses) == 0 {
				t.Errorf("no loss reported: %+v / %+v", aggLosses, streamLosses)
			}
		})
	}
}
