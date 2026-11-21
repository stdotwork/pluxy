// Package textdoc carries a plain-text document from a format that has document blocks of that
// type into the text of a format that has none. The text stays exactly as the client sent it; a
// line before and after it names the document, so the model can tell it from the message's own
// text.
package textdoc

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/media"
)

// FromAnthropic returns the text and title of a Messages plain-text document block
// ({"type":"document","source":{"type":"text","media_type":"text/plain","data":"..."},
// "title":"..."}), or ok false for any other block. A source of another media type is not a
// plain-text document, whatever its type says.
func FromAnthropic(raw []byte) (text, title string, ok bool) {
	var b struct {
		Type   string `json:"type"`
		Title  string `json:"title"`
		Source *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Type != "document" || b.Source == nil || b.Source.Type != "text" {
		return "", "", false
	}
	if media.Type(b.Source.MediaType) != "text/plain" {
		return "", "", false
	}
	return b.Source.Data, b.Title, true
}

// Wrap returns text delimited as a document, naming title when there is one. A title is put on
// one line, its line breaks (and the other separators a line break can be written with) becoming
// spaces, so it cannot end the delimiter early.
func Wrap(title, text string) string {
	name := strings.NewReplacer("\r", " ", "\n", " ", "\v", " ", "\f", " ", "\u2028", " ", "\u2029", " ", "\u0085", " ").Replace(title)
	if name != "" {
		return "[Document: " + name + "]\n" + text + "\n[End of document: " + name + "]"
	}
	return "[Document]\n" + text + "\n[End of document]"
}
