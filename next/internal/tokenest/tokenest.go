// Package tokenest estimates how many tokens a text takes, with the standard library only.
//
// shortcut: this is a heuristic, not a tokenizer. It splits the text into the pieces OpenAI's
// o200k_base pre-tokenizer would (words, digit groups, punctuation runs, line breaks) and counts
// each piece by its length, so it is close to o200k_base on ordinary text but not exact: it does
// not know which words are one token, and the other providers' tokenizers differ from o200k_base
// anyway. Upgrade it (a vocabulary-based tokenizer in this package) when clients need counts
// that match a provider's exactly, which a heuristic cannot give.
package tokenest

import (
	"math"
	"unicode"
)

// How many characters one token takes, by kind of piece. The values come from comparing the
// estimate with o200k_base on prose, code and JSON (see tokenest_test.go).
const (
	// A run of letters in a script that has words: wordChars letters per token, at least one
	// token. A common word is one token whatever its length and a rare one several, so this is
	// the average over both.
	wordChars = 7.0
	// A run of letters in another script (Cyrillic, Greek, Arabic, Hebrew ...), whose words the
	// vocabulary splits into more tokens: nonLatinChars letters per token.
	nonLatinChars = 6.0
	// A run of CJK characters: charsPerCJK characters per token.
	charsPerCJK = 1.5
	// A run of digits: up to digitGroup digits per token, as o200k_base's pre-tokenizer groups them.
	digitGroup = 3.0
	// A run of punctuation, symbols and underscores: punctChars of them per token. Common
	// sequences such as `":"`, `{"` and `":{"` are one token each.
	punctChars = 4.0
)

// Estimate returns the estimated number of tokens in s.
func Estimate(s string) int64 {
	var total float64
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case isCJK(r):
			j := i
			for j < len(rs) && isCJK(rs[j]) {
				j++
			}
			total += math.Ceil(float64(j-i) / charsPerCJK)
			i = j
		case unicode.IsLetter(r):
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsMark(rs[j])) && !isCJK(rs[j]) {
				j++
			}
			total += wordTokens(rs[i:j])
			i = j
			// A common contraction ('s, 't, 're, 've, 'm, 'll, 'd) is one token with its word.
			i += contraction(rs[i:])
		case unicode.IsDigit(r):
			j := i
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			total += math.Ceil(float64(j-i) / digitGroup)
			i = j
		case unicode.IsSpace(r):
			j := i
			lastBreak := -1
			for j < len(rs) && unicode.IsSpace(rs[j]) {
				if rs[j] == '\n' || rs[j] == '\r' {
					lastBreak = j
				}
				j++
			}
			// A run of line breaks, with the spaces before them, is one token.
			spaces := j - i
			if lastBreak >= 0 {
				total++
				spaces = j - lastBreak - 1
			}
			// Of the whitespace after that, the last character joins the piece that follows
			// when it is a space (before anything) or any whitespace (before a letter); the others
			// are one token. A last character that cannot join, or the end of the text, is a token
			// of its own.
			if spaces > 1 {
				total++
			}
			if spaces >= 1 && (j == len(rs) || (rs[j-1] != ' ' && !unicode.IsLetter(rs[j]))) {
				total++
			}
			i = j
		default:
			j := i
			for j < len(rs) && !unicode.IsSpace(rs[j]) && !unicode.IsLetter(rs[j]) && !unicode.IsDigit(rs[j]) {
				j++
			}
			// One punctuation mark right before a letter belongs to the word (_weather, .go).
			joined := j-i == 1 && j < len(rs) && unicode.IsLetter(rs[j]) && (i == 0 || !unicode.IsSpace(rs[i-1]))
			if !joined {
				total += math.Ceil(float64(j-i) / punctChars)
			}
			// Line breaks right after punctuation belong to its piece.
			for j < len(rs) && (rs[j] == '\n' || rs[j] == '\r') {
				j++
			}
			i = j
		}
	}
	return int64(total)
}

// wordTokens counts a run of letters. A word written in camel case (getWeather) is cut where a
// lower-case letter meets an upper-case one, as the pre-tokenizer does.
func wordTokens(w []rune) float64 {
	var total float64
	start := 0
	for i := 1; i <= len(w); i++ {
		if i == len(w) || (unicode.IsLower(w[i-1]) && unicode.IsUpper(w[i])) {
			per := wordChars
			if !unicode.Is(unicode.Latin, w[start]) {
				per = nonLatinChars
			}
			total += math.Max(1, math.Ceil(float64(i-start)/per))
			start = i
		}
	}
	return total
}

// contraction returns the length of an English contraction ending at the start of rs ('s, 't,
// 're, 've, 'm, 'll or 'd, not followed by a letter), or 0.
func contraction(rs []rune) int {
	if len(rs) < 2 || rs[0] != '\'' {
		return 0
	}
	for _, c := range []string{"s", "t", "re", "ve", "m", "ll", "d"} {
		n := len(c)
		if len(rs) < 1+n {
			continue
		}
		if string(rs[1:1+n]) != c && string(rs[1:1+n]) != string(unicode.ToUpper([]rune(c)[0]))+c[1:] {
			continue
		}
		if len(rs) > 1+n && unicode.IsLetter(rs[1+n]) {
			continue
		}
		return 1 + n
	}
	return 0
}

// isCJK reports whether r is a Han, Hiragana, Katakana or Hangul character, which the
// pre-tokenizer does not split into words and which take about a token each.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
