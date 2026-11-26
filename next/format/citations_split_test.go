package format_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// citedTexts returns, for each message item of evs, the text its citations cover, as a client
// would cut it from the item's own text with the rune offsets.
func citedTexts(t *testing.T, evs []llm.Event) map[string][]string {
	t.Helper()
	texts := map[int]string{}
	cited := map[string][]string{}
	for _, ev := range evs {
		switch ev.Type {
		case llm.EventTextDelta:
			texts[ev.Index] += ev.Delta
		case llm.EventItemEnd:
			for _, c := range ev.Citations {
				if c.StartIndex == nil || c.EndIndex == nil {
					t.Fatalf("citation %+v of item %d has no range", c, ev.Index)
				}
				r := []rune(texts[ev.Index])
				if *c.StartIndex < 0 || *c.EndIndex > len(r) || *c.StartIndex > *c.EndIndex {
					t.Fatalf("citation %+v is outside item %d (%q)", c, ev.Index, texts[ev.Index])
				}
				cited[texts[ev.Index]] = append(cited[texts[ev.Index]], c.URL+" "+string(r[*c.StartIndex:*c.EndIndex]))
			}
		}
	}
	return cited
}

func geminiJSON(t *testing.T, body string) ([]llm.Event, []llm.Loss) {
	t.Helper()
	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return evs, losses
}

func wantCited(t *testing.T, got map[string][]string, want map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("citations by item text = %q, want %q", got, want)
	}
	for text, w := range want {
		if !slices.Equal(got[text], w) {
			t.Fatalf("citations on %q = %q, want %q (all: %q)", text, got[text], w, got)
		}
	}
}

func hasCitationLoss(losses []llm.Loss, contains string) bool {
	for _, l := range losses {
		if strings.Contains(l.Detail, contains) {
			return true
		}
	}
	return false
}

// Google counts the offsets in bytes within one Part, so the citation for "Alpha" stays on the
// item that holds "Alpha" when a thought signature starts a new item for the text after it.
func TestGeminiCitationAcrossThoughtSignatureStream(t *testing.T) {
	// "Alpha beta. " is 12 bytes, so [12,17) is "Gamma" in the text read as one.
	g := `"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}},{"web":{"uri":"https://b.example/","title":"B"}}],` +
		`"groundingSupports":[{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[0]},` +
		`{"segment":{"startIndex":12,"endIndex":17,"text":"Gamma"},"groundingChunkIndices":[1]}]}`
	evs := geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"Alpha beta. "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Gamma delta.","thoughtSignature":"sig"}]},"finishReason":"STOP",`+g+`}]}`)
	wantCited(t, citedTexts(t, evs), map[string][]string{
		"Alpha beta. ": {"https://a.example/ Alpha"},
		"Gamma delta.": {"https://b.example/ Gamma"},
	})
}

// A signature on an empty last part closes the text item before the grounding is read.
func TestGeminiCitationAcrossEmptySignaturePartStream(t *testing.T) {
	d := geminigenerate.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range []string{
		`{"candidates":[{"content":{"parts":[{"text":"Alpha beta."}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"sig"}]},"finishReason":"STOP",` + groundingAlpha + `}]}`,
	} {
		out, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, out...)
	}
	end, err := d.Close()
	if err != nil {
		t.Fatal(err)
	}
	evs = append(evs, end...)
	wantCited(t, citedTexts(t, evs), map[string][]string{"Alpha beta.": {"https://a.example/ Alpha"}})
	if len(d.Losses()) != 0 {
		t.Errorf("losses = %+v, want none", d.Losses())
	}
}

const groundingAlpha = `"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}}],` +
	`"groundingSupports":[{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[0]}]}`

func TestGeminiCitationAcrossThoughtSignatureJSON(t *testing.T) {
	// Part 1 holds the signature, so its text is the second item; the offsets count within each part.
	evs, losses := geminiJSON(t, `{"candidates":[{"content":{"parts":[{"text":"Alpha beta."},{"text":"Gamma delta.","thoughtSignature":"sig"}]},"finishReason":"STOP",`+
		`"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}},{"web":{"uri":"https://b.example/","title":"B"}}],`+
		`"groundingSupports":[{"segment":{"partIndex":0,"startIndex":0,"endIndex":5},"groundingChunkIndices":[0]},`+
		`{"segment":{"partIndex":1,"startIndex":0,"endIndex":5},"groundingChunkIndices":[1]}]}}]}`)
	wantCited(t, citedTexts(t, evs), map[string][]string{
		"Alpha beta.":  {"https://a.example/ Alpha"},
		"Gamma delta.": {"https://b.example/ Gamma"},
	})
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

const twoSources = `"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}},{"web":{"uri":"https://b.example/","title":"B"}}]`

func TestGeminiCitationAcrossFunctionCallJSON(t *testing.T) {
	evs, losses := geminiJSON(t, `{"candidates":[{"content":{"parts":[{"text":"Alpha beta."},{"functionCall":{"name":"f","args":{}}},{"text":"Gamma delta."}]},"finishReason":"STOP",`+
		`"groundingMetadata":{`+twoSources+`,`+
		`"groundingSupports":[{"segment":{"partIndex":0,"startIndex":0,"endIndex":5},"groundingChunkIndices":[0]},`+
		`{"segment":{"partIndex":2,"startIndex":0,"endIndex":5},"groundingChunkIndices":[1]}]}}]}`)
	wantCited(t, citedTexts(t, evs), map[string][]string{
		"Alpha beta.":  {"https://a.example/ Alpha"},
		"Gamma delta.": {"https://b.example/ Gamma"},
	})
	if len(losses) != 0 {
		t.Errorf("losses = %+v, want none", losses)
	}
}

func TestGeminiCitationAcrossFunctionCallStream(t *testing.T) {
	// The text arrives in separate chunks; the offsets read against the whole text. The call and
	// the grounding come in one chunk, so the item the call closes still takes its citation.
	g := `"groundingMetadata":{` + twoSources + `,` +
		`"groundingSupports":[{"segment":{"startIndex":0,"endIndex":5},"groundingChunkIndices":[0]},` +
		`{"segment":{"startIndex":11,"endIndex":16},"groundingChunkIndices":[1]}]}`
	evs := geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"Alpha beta."}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{}}},{"text":"Gamma delta."}]},"finishReason":"STOP",`+g+`}]}`)
	wantCited(t, citedTexts(t, evs), map[string][]string{
		"Alpha beta.":  {"https://a.example/ Alpha"},
		"Gamma delta.": {"https://b.example/ Gamma"},
	})
}

// Offsets are bytes, and the item they land in counts runes from its own start.
func TestGeminiCitationAcrossSplitNonASCII(t *testing.T) {
	// "héllo " is 7 bytes; 日本語 is bytes [7,16) of the text read as one.
	g := `"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}}],` +
		`"groundingSupports":[{"segment":{"startIndex":7,"endIndex":16},"groundingChunkIndices":[0]}]}`
	evs := geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"héllo "}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"日本語 end","thoughtSignature":"sig"}]},"finishReason":"STOP",`+g+`}]}`)
	wantCited(t, citedTexts(t, evs), map[string][]string{"日本語 end": {"https://a.example/ 日本語"}})
}

// A citation that cannot be placed on one item is a reported loss, not a citation elsewhere.
func TestGeminiCitationThatCannotBePlacedIsALoss(t *testing.T) {
	t.Run("range across two items", func(t *testing.T) {
		d := geminigenerate.NewStreamDecoder()
		var evs []llm.Event
		for _, c := range []string{
			`{"candidates":[{"content":{"parts":[{"text":"Alpha beta. "}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Gamma delta.","thoughtSignature":"sig"}]},"finishReason":"STOP",` +
				`"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}}],` +
				`"groundingSupports":[{"segment":{"startIndex":6,"endIndex":16},"groundingChunkIndices":[0]}]}}]}`,
		} {
			out, err := d.Decode([]byte(c))
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, out...)
		}
		if got := citedTexts(t, evs); len(got) != 0 {
			t.Errorf("citations = %q, want none", got)
		}
		if !hasCitationLoss(d.Losses(), "citation not placed") {
			t.Errorf("losses = %+v, want a citation loss", d.Losses())
		}
	})
	t.Run("item ended in an earlier chunk", func(t *testing.T) {
		d := geminigenerate.NewStreamDecoder()
		var evs []llm.Event
		for _, c := range []string{
			`{"candidates":[{"content":{"parts":[{"text":"Alpha beta."},{"functionCall":{"name":"f","args":{}}}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Gamma delta."}]},"finishReason":"STOP",` + groundingAlpha + `}]}`,
		} {
			out, err := d.Decode([]byte(c))
			if err != nil {
				t.Fatal(err)
			}
			evs = append(evs, out...)
		}
		if got := citedTexts(t, evs); len(got) != 0 {
			t.Errorf("citations = %q, want none", got)
		}
		if !hasCitationLoss(d.Losses(), "citation not placed") {
			t.Errorf("losses = %+v, want a citation loss", d.Losses())
		}
	})
}

// A Chat client gets the same ranges from the decoder's events.
func TestGeminiCitationAcrossSplitReachesChatClient(t *testing.T) {
	evs, _ := geminiJSON(t, `{"candidates":[{"content":{"parts":[{"text":"Alpha beta."},{"functionCall":{"name":"f","args":{}}},{"text":"Gamma delta."}]},"finishReason":"STOP",`+
		`"groundingMetadata":{`+twoSources+`,`+
		`"groundingSupports":[{"segment":{"partIndex":0,"startIndex":0,"endIndex":5},"groundingChunkIndices":[0]},`+
		`{"segment":{"partIndex":2,"startIndex":0,"endIndex":5},"groundingChunkIndices":[1]}]}}]}`)
	msg := renderChatAgg(t, evs)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	r := []rune(content)
	got := map[string]string{}
	anns, _ := msg["annotations"].([]any)
	for _, a := range anns {
		uc := a.(map[string]any)["url_citation"].(map[string]any)
		s, e := int(uc["start_index"].(float64)), int(uc["end_index"].(float64))
		if s < 0 || e > len(r) || s > e {
			t.Fatalf("annotation %v is outside %q", uc, content)
		}
		got[uc["url"].(string)] = string(r[s:e])
	}
	want := map[string]string{"https://a.example/": "Alpha", "https://b.example/": "Gamma"}
	if len(got) != 2 || got["https://a.example/"] != "Alpha" || got["https://b.example/"] != "Gamma" {
		t.Errorf("chat annotations over %q cite %v, want %v", content, got, want)
	}
}
