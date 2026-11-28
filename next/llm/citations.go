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

// MaxCitationBytes bounds what one reply's citations hold, and, separately, what its sources hold.
// What does not fit is left out and reported as a loss.
const MaxCitationBytes = 4 << 20

const citationOverhead = 64

// CiteBudget counts the citations and the sources one reply has kept, each against
// MaxCitationBytes. Use one per reply; the zero value is ready.
type CiteBudget struct {
	cites, sources     int
	citesFull, srcFull bool
	cut                bool
}

func (b *CiteBudget) take(used *int, full *bool, n int) bool {
	if *full || *used+n > MaxCitationBytes {
		*full, b.cut = true, true
		return false
	}
	*used += n
	return true
}

// AddCitation reports whether c fits, and counts it if so. Once one does not fit, none after it does.
func (b *CiteBudget) AddCitation(c Citation) bool {
	return b.take(&b.cites, &b.citesFull, len(c.URL)+len(c.Title)+citationOverhead)
}

// AddSource reports whether s fits, and counts it if so. Once one does not fit, none after it does.
func (b *CiteBudget) AddSource(s Source) bool {
	return b.take(&b.sources, &b.srcFull, len(s.URL)+len(s.Title)+citationOverhead)
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

// LossPathCitations and LossPathFinish are the paths of the losses for what a reply's size limits
// left out of the model's events. A client that is given the upstream's body has all of it, so
// ReplyLosses leaves them out for such a client.
const (
	LossPathCitations = "citations"
	LossPathFinish    = "finish"
)

// WithLoss adds the loss for citations and sources left out, if there were any, to losses.
func (b *CiteBudget) WithLoss(losses []Loss) []Loss {
	if !b.cut {
		return losses
	}
	return AddLoss(slices.Clip(losses), Loss{Kind: LossDropped, Path: LossPathCitations,
		Detail: fmt.Sprintf("the reply's citations, or its sources, passed %d MiB; the rest are left out", MaxCitationBytes>>20)})
}

// ReplyLosses returns the losses of a reply that its client suffers. A client that is given the
// upstream's body (bodyReaches; see Request.BodyReachesClient) gets everything the size limits left
// out of the model, and everything a candidate after the first held that the model has no place
// for, so it loses none of that.
func ReplyLosses(losses []Loss, bodyReaches bool) []Loss {
	if !bodyReaches {
		return losses
	}
	return slices.DeleteFunc(slices.Clone(losses), func(l Loss) bool {
		return l.Path == LossPathCitations || l.Path == LossPathFinish || laterCandidatePath(l.Path)
	})
}

func laterCandidatePath(path string) bool {
	for _, p := range []string{"candidates[", "choices["} {
		rest, ok := strings.CutPrefix(path, p)
		if !ok {
			continue
		}
		n, _, ok := strings.Cut(rest, "]")
		return ok && n != "" && n != "0" && strings.Trim(n, "0123456789") == ""
	}
	return false
}

// BodyReachesClient reports whether the client of r is given the body of the upstream's reply, in
// the upstream's format, rather than a reply built again from the model's events: the request came
// in that format, and the renderer of that format passes the body on for this pair of shapes of
// reply (upstreamStreamed says whether the upstream's reply is a stream). A Messages renderer
// always does. A Responses one does but for a stream built from a whole reply, a Gemini one but for
// a whole reply built from a stream, and a Chat one keeps the annotations the upstream sent.
func (r *Request) BodyReachesClient(upstream string, upstreamStreamed bool) bool {
	if r == nil || r.Native == nil || r.Native.Format != upstream {
		return false
	}
	switch upstream {
	case FormatAnthropicMessages, FormatOpenAIChat:
		return true
	case FormatOpenAIResponses:
		return !r.Stream || upstreamStreamed
	case FormatGeminiGenerate:
		return r.Stream || !upstreamStreamed
	}
	return false
}
