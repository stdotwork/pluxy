package protocol

import (
	"encoding/base64"
	"math/rand/v2"
	"strings"
	"testing"
)

// bodyRedactor is a Redactor for several made-up secrets: one with characters that JSON and URLs
// escape, two that overlap ("1234efgh"), and one that starts another.
func bodyRedactor() *Redactor {
	return NewRedactor(
		&Credential{Kind: CredentialAPIKey, APIKey: "sk-body-key-0123456789"},
		&Credential{Kind: CredentialAPIKey, APIKey: "sk-sl/ash+42"},
		&Credential{Kind: CredentialAPIKey, APIKey: "abcd1234efgh"},
		&Credential{Kind: CredentialAPIKey, APIKey: "1234efgh5678"},
		&Credential{Kind: CredentialAPIKey, APIKey: "pre-secret-1"},
		&Credential{Kind: CredentialAPIKey, APIKey: "pre-secret-1-longer"},
	)
}

// redactPieces runs pieces through a BodyRedactor and returns what it sent on, joined. It fails
// the test when the BodyRedactor holds back as much as the longest form or more.
func redactPieces(t *testing.T, r *Redactor, pieces []string) string {
	t.Helper()
	b := r.Body()
	var out strings.Builder
	for _, p := range pieces {
		out.WriteString(b.Write(p))
		if len(r.forms) > 0 && len(b.held) >= len(r.forms[0]) {
			t.Fatalf("holding back %d bytes, as long as the longest form (%d): %q", len(b.held), len(r.forms[0]), b.held)
		}
	}
	out.WriteString(b.Flush())
	return out.String()
}

// A secret cut between pieces is removed as it is from the whole body: across two or three
// pieces, at the very end of the body, with an empty piece between, and when the longest of two
// secrets that start the same way wins. The expected values were worked out by hand.
func TestBodyRedactorSplitSecrets(t *testing.T) {
	r := bodyRedactor()
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{"key sk-body-", "key-0123456789 done"}, "key [redacted] done"},
		{[]string{"key sk-bo", "dy-key-01", "23456789 done"}, "key [redacted] done"},
		{[]string{"key sk-bo", "", "dy-key-0123456789"}, "key [redacted]"},
		{[]string{"end sk-body-key-012345678", "9"}, "end [redacted]"},
		// The longer of two secrets that start the same way is removed whole.
		{[]string{"a pre-secret-1", "-longer b"}, "a [redacted] b"},
		// The shorter is removed where the longer does not follow, also at the end of the body.
		{[]string{"a pre-secret-1", "-long"}, "a [redacted]-long"},
		{[]string{"a pre-secret-", "1"}, "a [redacted]"},
		// Of two secrets that overlap, the one that starts first is removed.
		{[]string{"abcd1234", "efgh5678"}, "[redacted]5678"},
		{[]string{"x1234efgh", "5678"}, "x[redacted]"},
		// Forms of a secret: escaped in JSON and in a URL, and base64.
		{[]string{`{"k":"sk-sl\`, `/ash+42"}`}, `{"k":"[redacted]"}`},
		{[]string{"?key=sk-sl%2", "Fash%2B42&x=1"}, "?key=[redacted]&x=1"},
		{[]string{"Basic " + base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))[:10],
			base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))[10:]}, "Basic [redacted]"},
		// The start of a secret that never completes reaches the client as it was sent.
		{[]string{"cut sk-body-key-0", "12"}, "cut sk-body-key-012"},
		{[]string{"cut pre-secret-1-lo"}, "cut [redacted]-lo"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

// Wherever a body is cut, and into however many pieces, what the client gets joined is what
// String returns for the whole body.
func TestBodyRedactorMatchesStringAtEveryCut(t *testing.T) {
	r := bodyRedactor()
	key64 := base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))
	frags := []string{
		"sk-body-key-0123456789", "sk-sl/ash+42", `sk-sl\/ash+42`, "sk-sl%2Fash%2B42", key64,
		"abcd1234efgh", "1234efgh5678", "abcd1234efgh5678", "pre-secret-1", "pre-secret-1-longer",
		// Starts of secrets that never complete, and text around them.
		"sk-body-key-01", "pre-secret-", "pre-secret-1-lo", "abcd1234", key64[:9], "sk-",
		" ", "x", "hello world ", `{"error":"`, `"}`, "data: ", "\n\n", "é", "12345", "a",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	body := func() string {
		var b strings.Builder
		for range 1 + rng.IntN(8) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		return b.String()
	}
	check := func(whole string, pieces []string) {
		t.Helper()
		if got, want := redactPieces(t, r, pieces), r.String(whole); got != want {
			t.Fatalf("body %q cut as %q: got %q, want %q", whole, pieces, got, want)
		}
	}
	for n := range 300 {
		whole := body()
		// Two pieces, cut at every offset, and a byte at a time.
		for i := 0; i <= len(whole); i++ {
			check(whole, []string{whole[:i], whole[i:]})
		}
		bytes := make([]string, len(whole))
		for i := 0; i < len(whole); i++ {
			bytes[i] = whole[i : i+1]
		}
		check(whole, bytes)
		// Three pieces, cut at every pair of offsets, for some bodies.
		if n%10 == 0 {
			for i := 0; i <= len(whole); i++ {
				for j := i; j <= len(whole); j++ {
					check(whole, []string{whole[:i], whole[i:j], whole[j:]})
				}
			}
		}
		// Cut at random, with empty pieces.
		for range 20 {
			var pieces []string
			for rest := whole; rest != ""; {
				k := min(rng.IntN(12), len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
			check(whole, pieces)
		}
	}
}

// A BodyRedactor with nothing to remove, or of a nil Redactor, sends every piece on as it came.
func TestBodyRedactorWithoutSecrets(t *testing.T) {
	for _, r := range []*Redactor{nil, NewRedactor()} {
		b := r.Body()
		if got := b.Write("sk-"); got != "sk-" {
			t.Errorf("Write = %q", got)
		}
		if got := b.Flush(); got != "" {
			t.Errorf("Flush = %q", got)
		}
	}
}
