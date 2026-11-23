package llm

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
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

// MaxCitationBytes bounds what one reply's citations and sources hold. Citations ride on their
// message item's end and sources on the finish event, so without a limit a reply whose blocks
// each fit a protocol frame could still fail on the last one. What does not fit is left out and
// reported as a loss.
const MaxCitationBytes = 4 << 20

// citationOverhead is about the JSON one citation or source adds around its URL and title.
const citationOverhead = 64

// CiteBudget counts the citations and sources one reply has kept against MaxCitationBytes. Use one
// per reply; the zero value is ready.
type CiteBudget struct {
	used int
	full bool // one did not fit
	cut  bool // one was left out, whatever the reason
}

func (b *CiteBudget) take(n int) bool {
	if b.full || b.used+n > MaxCitationBytes {
		b.full, b.cut = true, true
		return false
	}
	b.used += n
	return true
}

// AddCitation reports whether c fits, and counts it if so. Once one does not fit, none after it does.
func (b *CiteBudget) AddCitation(c Citation) bool {
	return b.take(len(c.URL) + len(c.Title) + citationOverhead)
}

// AddSource reports whether s fits, and counts it if so. Once one does not fit, none after it does.
func (b *CiteBudget) AddSource(s Source) bool {
	return b.take(len(s.URL) + len(s.Title) + citationOverhead)
}

// KeepCitations returns the leading citations of cs that fit.
func (b *CiteBudget) KeepCitations(cs []Citation) []Citation {
	for i, c := range cs {
		if !b.AddCitation(c) {
			return cs[:i]
		}
	}
	return cs
}

// LeftOut records that a citation or source was left out for a reason of the caller's.
func (b *CiteBudget) LeftOut() { b.cut = true }

// Cut reports whether any citation or source was left out.
func (b *CiteBudget) Cut() bool { return b.cut }

// WithLoss adds the loss for citations and sources left out, if there were any, to losses.
func (b *CiteBudget) WithLoss(losses []Loss) []Loss {
	if !b.cut {
		return losses
	}
	return AddLoss(slices.Clip(losses), Loss{Kind: LossDropped, Path: "citations",
		Detail: fmt.Sprintf("the reply's citations and sources passed %d MiB; the rest are left out", MaxCitationBytes>>20)})
}
