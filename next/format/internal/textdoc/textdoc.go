package textdoc

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/media"
)

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

func Wrap(title, text string) string {
	name := strings.NewReplacer("\r", " ", "\n", " ", "\v", " ", "\f", " ", "\u2028", " ", "\u2029", " ", "\u0085", " ").Replace(title)
	if name != "" {
		return "[Document: " + name + "]\n" + text + "\n[End of document: " + name + "]"
	}
	return "[Document]\n" + text + "\n[End of document]"
}
