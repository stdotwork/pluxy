package sdk

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// A BodyWriter sends each write as a body piece, holding back a UTF-8 character a write cuts in
// two until the next write, so no piece carries half a character.
func TestBodyWriterKeepsCharactersWhole(t *testing.T) {
	var buf bytes.Buffer
	s := &Stream{w: &buf}
	if err := s.StartBody(llm.FormatOpenAIImagesGenerations, "text/event-stream"); err != nil {
		t.Fatal(err)
	}
	text := "data: café 🙂 ok\n\n"
	bw := s.BodyWriter()
	for i := 0; i < len(text); i += 3 {
		if _, err := bw.Write([]byte(text[i:min(i+3, len(text))])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bw.Write([]byte("\xe2\x82")); err != nil { // the start of "€", never finished
		t.Fatal(err)
	}
	if err := bw.Close(); err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for buf.Len() > 0 {
		var r protocol.Reply
		if err := protocol.ReadFrame(&buf, &r); err != nil {
			t.Fatal(err)
		}
		if r.Event.Type != llm.EventNative {
			continue
		}
		var piece string
		if err := json.Unmarshal(r.Event.Raw, &piece); err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(piece, '\uFFFD') && !strings.HasSuffix(got.String()+piece, "\uFFFD") {
			t.Fatalf("piece %q holds a broken character", piece)
		}
		got.WriteString(piece)
	}
	// The unfinished character at the end arrives as U+FFFD, as the protocol says.
	rest, ok := strings.CutPrefix(got.String(), text)
	if !ok || rest == "" || strings.Trim(rest, "\uFFFD") != "" {
		t.Fatalf("body = %q, want %q and U+FFFD", got.String(), text)
	}
}
