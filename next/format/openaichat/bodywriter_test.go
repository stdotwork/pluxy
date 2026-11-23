package openaichat

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// TestAppendStringMatchesMarshal checks the escaping against json.Marshal, the writer's reference:
// every byte value alone and between letters, the runes that need an escape, and random bytes.
func TestAppendStringMatchesMarshal(t *testing.T) {
	var cases []string
	for b := 0; b < 256; b++ {
		cases = append(cases, string([]byte{byte(b)}), "a"+string([]byte{byte(b)})+"b")
	}
	cases = append(cases, "", "plain", "a\u2028b\u2029c", "<&>", "日本語 ☃ 𝄞", "\ufffd", "a\xe2\x80", "\xf0\x9f\x98", "\xc0\xaf", "\xed\xa0\x80")
	r := rand.New(rand.NewSource(1))
	for range 2000 {
		b := make([]byte, r.Intn(64))
		for i := range b {
			b[i] = byte(r.Intn(256))
		}
		cases = append(cases, string(b))
	}
	for _, s := range cases {
		want, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := appendString([]byte("x"), s); string(got) != "x"+string(want) {
			t.Fatalf("appendString(%q) = %s, json.Marshal %s", s, got[1:], want)
		}
	}
}
