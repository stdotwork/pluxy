package tokenest

import "testing"

// The estimates are pinned next to the count of o200k_base for the same text. Those counts were
// measured once with github.com/tiktoken-go/tokenizer v0.8.1 (today's core's tokenizer); this
// module does not depend on it. The constants were tuned on larger samples: over about 47,000
// o200k_base tokens of English prose (this repository's docs) the estimate is within 1%, over
// 14,500 of Go and TypeScript within 1%, over 2,400 of JSON within 2%, over 340 of Chinese,
// Japanese and Korean within 3%, over 130 of Russian within 4%; per 2 KB chunk it is within about
// 3% on average, and within 7% on a few hundred tokens of chat text.
func TestEstimate(t *testing.T) {
	for _, c := range []struct {
		name, text string
		o200k, est int64
	}{
		{"empty", "", 0, 0},
		{"a word", "hello", 1, 1},
		{"punctuation", "Hello, world!", 4, 4},
		{"a sentence", "The quick brown fox jumps over the lazy dog.", 10, 10},
		{"contractions are one token with the word", "I don't think it's ready; we'll see.", 9, 9},
		{"digits in groups of three", "Order 1234567 shipped on 2026-11-09.", 15, 13},
		{"camel case is cut where the case changes", "getWeatherForCity", 4, 4},
		{"a rare long word is several tokens", "antidisestablishmentarianism", 6, 4},
		{"code", "func main() {\n\tfmt.Println(\"hi\")\n}\n", 10, 10},
		{"JSON", `{"a":1,"b":[true,null],"c":{"d":"e"}}`, 16, 16},
		{"indentation and blank lines", "    indented\n\n\n  more", 6, 6},
		{"Chinese", "今天天气很好，我们一起去公园散步吧。", 13, 12},
		{"Russian", "Привет, как дела?", 6, 5},
		{"a request's text", "Be brief.\nuser\nSay hello\nget_weather\nWeather\n" + `{"type":"object","properties":{"city":{"type":"string"}}}`, 27, 27},
		{"a short request's text", "user\nhi", 3, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Estimate(c.text); got != c.est {
				t.Errorf("Estimate(%q) = %d, want %d (o200k_base: %d)", c.text, got, c.est, c.o200k)
			}
		})
	}
}
