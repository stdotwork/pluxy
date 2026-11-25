package ignite

import (
	"slices"
	"strings"
	"unicode"
)

// small are the words that mark a provider's small, cheap models, best first.
var small = []string{"haiku", "luna", "flash-lite", "lite", "mini", "nano", "flash", "oss", "small"}

// unfit are the words of models a short text request does not suit or that cost more than it
// needs: image and audio models, embeddings, reasoning variants, review models.
var unfit = []string{"image", "audio", "tts", "embed", "thinking", "review", "realtime", "transcribe", "search"}

// families are the words that tie a model to a group of models a usage window counts.
var families = []string{"gemini", "claude", "gpt", "grok", "kimi", "llama"}

// ChooseModel picks the model an ignition request names when the settings name none: among
// models, the account's model IDs in the order it lists them, those a short text request suits,
// and of a window that counts a group of models (counts, as the reading names them) only those of
// the group's families (gemini, claude, gpt and the like). Of those it takes a small model: the
// first word of small that one names decides, and among the models it names the newest (the
// highest version, by the numbers in the ID). With no small model it takes the first that is
// left, and "" when none is.
func ChooseModel(models, counts []string) string {
	var fam []string
	for _, c := range counts {
		for _, f := range families {
			if strings.Contains(strings.ToLower(c), f) {
				fam = append(fam, f)
			}
		}
	}
	var cands []string
	for _, m := range models {
		l := strings.ToLower(m)
		if containsAny(l, unfit) || len(fam) > 0 && !containsAny(l, fam) {
			continue
		}
		cands = append(cands, m)
	}
	for _, word := range small {
		best := ""
		for _, m := range cands {
			if names(m, word) && (best == "" || newer(m, best)) {
				best = m
			}
		}
		if best != "" {
			return best
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// names reports whether model ID m names word: as one of its parts (split at "-", ".", "_" and
// spaces), or, for a word of two parts such as flash-lite, as those parts in a row; so gemini is
// not a mini model.
func names(m, word string) bool {
	sep := func(r rune) bool { return r == '-' || r == '.' || r == '_' || r == ' ' }
	parts, want := strings.FieldsFunc(strings.ToLower(m), sep), strings.FieldsFunc(word, sep)
	for i := 0; i+len(want) <= len(parts); i++ {
		if slices.Equal(parts[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// newer reports whether a names a later version than b: their runs of digits compared in turn as
// numbers, a date-like run (6 digits or more) aside, so claude-haiku-5-5 is newer than
// claude-haiku-4-5-20251001 and gpt-6-luna than gpt-5.6-luna.
func newer(a, b string) bool {
	x, y := version(a), version(b)
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return len(x) > len(y)
}

func version(s string) []int {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }) {
		if len(f) >= 6 {
			continue
		}
		n := 0
		for _, r := range f {
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}
