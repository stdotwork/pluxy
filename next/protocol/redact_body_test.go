package protocol

import (
	"encoding/base64"
	"fmt"
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
// the test when the BodyRedactor holds back as much as the longest form or more, or, when a form
// could be part of a number, twice that and maxNumberHold.
func redactPieces(t *testing.T, r *Redactor, pieces []string) string {
	t.Helper()
	b := r.Body()
	var out strings.Builder
	for _, p := range pieces {
		out.WriteString(b.Write(p))
		if len(r.forms) > 0 {
			limit := len(r.forms[0])
			if r.numeric {
				limit = 2*limit + maxNumberHold
			}
			if len(b.held) >= limit {
				t.Fatalf("holding back %d bytes, %d or more: %q", len(b.held), limit, b.held)
			}
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

// escapedSecret is a made-up credential that the tests below also write with a JSON escape
// (\u0073 for "s"), which no listed form covers but String decodes.
const escapedSecret = "sk-fake-credential-value-s"

// holdsEscaped reports whether x holds escapedSecret as it is or once its escapes are decoded.
func holdsEscaped(x string) bool {
	d, _, _ := unescapeLayer(x, nil, false, allEscapes)
	return strings.Contains(x, escapedSecret) || strings.Contains(d, escapedSecret)
}

// A secret written with an escape, wholly inside one piece, is removed as String removes it,
// also when it ends with what could be the start of one of its forms (here the plain secret's
// "s"), whether more of the body follows or not.
func TestBodyRedactorEscapedSecretInOnePiece(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	const piece = `error: key \u0073k-fake-credential-value-s`
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{piece, `" rejected`}, `error: key [redacted]" rejected`},
		{[]string{piece}, `error: key [redacted]`},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

// Wherever a body is cut, the client never gets a secret that redacting each piece on its own
// would have removed: here secrets written with an escape outside the listed forms, which a
// piece holds whole or which a cut splits.
func TestBodyRedactorNeverWorseThanEachPiece(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	frags := []string{escapedSecret, "sk-", "s", `" rejected`, "error: key ", " ", "x", `\u00`, "c2st",
		base64.StdEncoding.EncodeToString([]byte(escapedSecret))[:12]}
	for i := range len(escapedSecret) {
		frags = append(frags, escapedSecret[:i]+fmt.Sprintf(`\u%04x`, escapedSecret[i])+escapedSecret[i+1:])
	}
	rng := rand.New(rand.NewPCG(3, 4))
	check := func(whole string, pieces []string) {
		t.Helper()
		var each strings.Builder
		for _, p := range pieces {
			each.WriteString(r.String(p))
		}
		if got := redactPieces(t, r, pieces); holdsEscaped(got) && !holdsEscaped(each.String()) {
			t.Fatalf("body %q cut as %q: got %q, which holds the secret; each piece alone gives %q", whole, pieces, got, each.String())
		}
	}
	for range 400 {
		var b strings.Builder
		for range 1 + rng.IntN(5) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		whole := b.String()
		check(whole, []string{whole})
		for i := 0; i <= len(whole); i++ {
			check(whole, []string{whole[:i], whole[i:]})
		}
		for range 20 {
			var pieces []string
			for rest := whole; rest != ""; {
				k := min(rng.IntN(16), len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
			check(whole, pieces)
		}
	}
}

// collapsing is text with more than maxDecodings decodings, which String replaces whole with
// [redacted]: a model can be led to write such escapes in a reply.
var collapsing = "model said " + strings.Repeat("\\", 16) + "%25252525 ; upstream: invalid key "

// formsIn returns the forms of r's secrets that x holds as they are.
func formsIn(r *Redactor, x string) []string {
	var found []string
	for _, f := range r.forms {
		if strings.Contains(x, f) {
			found = append(found, f)
		}
	}
	return found
}

// A secret cut between pieces is removed also where the text before the cut makes String give up
// and replace it whole: the rest of the secret in the next piece is not sent either.
func TestBodyRedactorCutSecretAfterCollapse(t *testing.T) {
	r := bodyRedactor()
	const key = "sk-body-key-0123456789"
	if r.String(collapsing+"sk-") != Redacted {
		t.Fatalf("the test text does not collapse: %q", r.String(collapsing+"sk-"))
	}
	for _, k := range []int{3, 4, 8} {
		got := redactPieces(t, r, []string{collapsing + key[:k], key[k:] + " rejected"})
		if strings.Contains(got, key[k:]) || len(formsIn(r, got)) > 0 || !strings.HasSuffix(got, " rejected") {
			t.Errorf("cut after %d bytes: got %q, which holds the rest of the secret or loses the text after it", k, got)
		}
	}
}

// Numbers that hold a numeric secret become 0, as String makes them in JSON, wherever a piece
// ends inside one or between it and what comes before it.
func TestBodyRedactorNumericSecret(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{`{"a":12345678`, `9}`}, `{"a":0}`},
		{[]string{`{"a":9123`, `45678}`}, `{"a":0}`},
		{[]string{`{"a":`, `12345678}`}, `{"a":0}`},
		{[]string{`{"a": `, `912345678, "b":1}`}, `{"a": 0, "b":1}`},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

// Wherever a body that holds a numeric secret is cut, what the client gets joined is what String
// returns for the whole body.
func TestBodyRedactorNumericMatchesStringAtEveryCut(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	frags := []string{"12345678", "9", "1234", `{"a":`, `{"a": `, ", ", `"b":`, "[", "]", "}", "-", ".5", "e3", "x", " ", `"n 12345678"`}
	rng := rand.New(rand.NewPCG(5, 6))
	for range 300 {
		var b strings.Builder
		for range 1 + rng.IntN(6) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		whole := b.String()
		for i := 0; i <= len(whole); i++ {
			if got, want := redactPieces(t, r, []string{whole[:i], whole[i:]}), r.String(whole); got != want {
				t.Fatalf("body %q cut at %d: got %q, want %q", whole, i, got, want)
			}
		}
	}
}

// cutTail returns the part after a cut of a form that whole holds across a cut between pieces,
// when got holds it and it is long enough (6 bytes) not to be there by chance; else "".
func cutTail(r *Redactor, whole string, pieces []string, got string) string {
	var cuts []int
	at := 0
	for _, p := range pieces[:len(pieces)-1] {
		at += len(p)
		cuts = append(cuts, at)
	}
	for _, f := range r.forms {
		for off := 0; ; {
			k := strings.Index(whole[off:], f)
			if k < 0 {
				break
			}
			start := off + k
			for _, c := range cuts {
				if c <= start || c >= start+len(f) {
					continue
				}
				if tail := whole[c : start+len(f)]; len(tail) >= 6 && strings.Contains(got, tail) {
					return tail
				}
			}
			off = start + 1
		}
	}
	return ""
}

// Wherever a body is cut after text that makes String give up, no listed form of a secret reaches
// the client, nor the rest of one cut there.
func TestBodyRedactorCollapseAtEveryCut(t *testing.T) {
	r := bodyRedactor()
	frags := []string{"sk-body-key-0123456789", "pre-secret-1-longer", "abcd1234efgh5678", " rejected", "sk-", "x", " "}
	rng := rand.New(rand.NewPCG(7, 8))
	for range 200 {
		var b strings.Builder
		b.WriteString(collapsing)
		for range 1 + rng.IntN(4) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		whole := b.String()
		for i := 0; i <= len(whole); i++ {
			for _, pieces := range [][]string{{whole[:i], whole[i:]}, {whole[:i/2], whole[i/2 : i], whole[i:]}} {
				got := redactPieces(t, r, pieces)
				if f := formsIn(r, got); len(f) > 0 {
					t.Fatalf("body %q cut as %q: got %q, which holds %q", whole, pieces, got, f)
				}
				if tail := cutTail(r, whole, pieces, got); tail != "" {
					t.Fatalf("body %q cut as %q: got %q, which holds %q, the rest of a secret cut there", whole, pieces, got, tail)
				}
			}
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
