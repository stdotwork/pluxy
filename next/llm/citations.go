package llm

import (
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Citation is a web source cited by a piece of reply text (protocol minor version 31).
// StartIndex and EndIndex, when non-nil, give the half-open [StartIndex, EndIndex) range of the
// cited text in Unicode code points (Go runes) from the start of the message item's text.
// Nil indices mean the citation applies without a narrower range, or the upstream's range could
// not be mapped.
type Citation struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	StartIndex *int   `json:"start_index,omitempty"`
	EndIndex   *int   `json:"end_index,omitempty"`
}

// Source is a web source returned by a provider's hosted web search (protocol minor version 31).
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// Citations is a list of web citations whose empty JSON array parses to nil.
type Citations []Citation

// UnmarshalJSON decodes Citations and normalizes an empty JSON array to nil.
func (c *Citations) UnmarshalJSON(data []byte) error {
	var list []Citation
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		*c = nil
		return nil
	}
	*c = list
	return nil
}

// Sources is a list of web search sources whose empty JSON array parses to nil.
type Sources []Source

// UnmarshalJSON decodes Sources and normalizes an empty JSON array to nil.
func (s *Sources) UnmarshalJSON(data []byte) error {
	var list []Source
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		*s = nil
		return nil
	}
	*s = list
	return nil
}

// ValidCitationURL reports whether raw is an absolute http or https URL with a non-empty host.
// Non-http schemes (javascript:, data:, file:, ftp:) and relative paths are rejected so clients
// never receive unsafe links.
func ValidCitationURL(raw string) bool {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	}
	return false
}

// UTF8ByteToRuneOffset converts a UTF-8 byte offset in s to a Unicode code point (rune) offset.
// It returns false when byteOff is out of bounds or falls inside a multi-byte UTF-8 sequence.
func UTF8ByteToRuneOffset(s string, byteOff int) (int, bool) {
	if byteOff < 0 || byteOff > len(s) {
		return 0, false
	}
	if byteOff == len(s) {
		return utf8.RuneCountInString(s), true
	}
	if !utf8.RuneStart(s[byteOff]) {
		return 0, false
	}
	return utf8.RuneCountInString(s[:byteOff]), true
}

// RuneToUTF8ByteOffset converts a Unicode code point (rune) offset in s to a UTF-8 byte offset.
// It returns false when runeOff is negative or exceeds the rune count of s.
func RuneToUTF8ByteOffset(s string, runeOff int) (int, bool) {
	if runeOff < 0 {
		return 0, false
	}
	runes := 0
	for i := range s {
		if runes == runeOff {
			return i, true
		}
		runes++
	}
	if runes == runeOff {
		return len(s), true
	}
	return 0, false
}
