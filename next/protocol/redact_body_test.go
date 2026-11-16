package protocol

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"slices"
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
// could be part of a number, twice that and maxNumberHold; where the body so far holds a backslash
// or a percent sign, which can start an escape, more than 12 times the longest form, or, when a
// form could be part of a number, that, maxNumberHold and the longest form together.
func redactPieces(t *testing.T, r *Redactor, pieces []string) string {
	t.Helper()
	b := r.Body()
	var out, body strings.Builder
	for _, p := range pieces {
		out.WriteString(b.Write(p))
		body.WriteString(p)
		if len(r.forms) > 0 {
			longest := len(r.forms[0])
			limit := longest
			if strings.ContainsAny(body.String(), `\%`) {
				limit = 12*longest + 1
			}
			if r.numeric {
				limit += maxNumberHold + longest
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

// Where Write sends text whole because a secret spelled with an escape ends where a form of
// another secret starts (the "3" of \u0073 starts "3x-other-secret"), it keeps holding the end of
// that form the text whole left as it was: no part of the second secret reaches the client before
// a later piece shows whether it is one, and a body that breaks off there sends none of it.
func TestBodyRedactorFallbackKeepsHoldingTheUnchangedEnd(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "3x-other-secret"})
	first := "err my-api-key" + fmt.Sprintf(`\u%04x`, 's') + "x-oth"
	if got := r.Body().Write(first); got != "err [redacted]" {
		t.Errorf("Write(%q) = %q, want %q", first, got, "err [redacted]")
	}
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{first, "er-secret rejected"}, "err [redacted][redacted] rejected"},
		{[]string{first, "er-sec", "", "ret rejected"}, "err [redacted][redacted] rejected"},
		// The second secret never completes: what was held goes out once the body ends.
		{[]string{first, "er"}, "err [redacted]x-other"},
		{[]string{first}, "err [redacted]x-oth"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

// After text sent whole, a number holding the secret that ends in 0 still becomes 0, as String on
// the whole body makes it: the 0 String writes is not taken for the number's own last digit.
func TestBodyRedactorFallbackNumberEndingInZero(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345670"})
	pieces := []string{"x:12345670:12345670", "}"}
	want := r.String(strings.Join(pieces, ""))
	if want != "x:[redacted]:0}" {
		t.Fatalf("String = %q, want %q", want, "x:[redacted]:0}")
	}
	if got := redactPieces(t, r, pieces); got != want {
		t.Errorf("%q: got %q, want %q", pieces, got, want)
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
// and replace it whole: the rest of the secret in the next piece is not sent either. That holds
// for every form of every secret, cut at every byte, also when the next piece is empty or still
// leaves the secret undecided (a base64 form can go on with its padding) and the body ends there.
func TestBodyRedactorCutSecretAfterCollapse(t *testing.T) {
	r := bodyRedactor()
	if r.String(collapsing+"sk-") != Redacted {
		t.Fatalf("the test text does not collapse: %q", r.String(collapsing+"sk-"))
	}
	for _, f := range slices.Compact(slices.Sorted(slices.Values(r.forms))) {
		for k := 1; k < len(f); k++ {
			for _, c := range []struct {
				pieces []string
				want   string
			}{
				// The text before the cut becomes [redacted], and so does the rest of the secret.
				{[]string{collapsing + f[:k], f[k:] + " rejected"}, Redacted + Redacted + " rejected"},
				{[]string{collapsing + f[:k], f[k:], " rejected"}, Redacted + Redacted + " rejected"},
				{[]string{collapsing + f[:k], "", f[k:] + " rejected"}, Redacted + Redacted + " rejected"},
				{[]string{collapsing + f[:k], f[k:]}, Redacted + Redacted},
			} {
				if got := redactPieces(t, r, c.pieces); got != c.want {
					t.Errorf("%q: got %q, want %q", c.pieces[1:], got, c.want)
				}
			}
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

// Wherever a body that holds a numeric secret is cut, into two pieces or three, what the client
// gets joined is what String returns for the whole body. After text that makes String give up,
// no form of the secret reaches the client, nor the rest of one cut there.
func TestBodyRedactorNumericMatchesStringAtEveryCut(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	frags := []string{"12345678", "9", "1234", `{"a":`, `{"a": `, ", ", `"b":`, "[", "]", "}", "-", ".5", "e3", "x", " ", `"n 12345678"`,
		"hello there", "v1.2.3", "x-"}
	rng := rand.New(rand.NewPCG(5, 6))
	for n := range 300 {
		var b strings.Builder
		for range 1 + rng.IntN(6) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		whole := b.String()
		want := r.String(whole)
		for i := 0; i <= len(whole); i++ {
			if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
				t.Fatalf("body %q cut at %d: got %q, want %q", whole, i, got, want)
			}
			// Three pieces, cut at every pair of offsets, for some bodies. A middle piece with a quote
			// can be a JSON string on its own (`":, -12345678"`), which String reads as JSON, so a
			// number in it may become 0 where the whole body gives [redacted]: there the secret must
			// still not reach the client.
			for j := i; n%10 == 0 && j <= len(whole); j++ {
				pieces := []string{whole[:i], whole[i:j], whole[j:]}
				got := redactPieces(t, r, pieces)
				if !strings.Contains(pieces[1], `"`) && got != want {
					t.Fatalf("body %q cut at %d and %d: got %q, want %q", whole, i, j, got, want)
				}
				if f := formsIn(r, got); len(f) > 0 {
					t.Fatalf("body %q cut at %d and %d: got %q, which holds %q", whole, i, j, got, f)
				}
			}
		}
		if n%3 != 0 {
			continue
		}
		whole = collapsing + whole
		for i := len(collapsing) - 1; i <= len(whole); i++ {
			for _, pieces := range [][]string{{whole[:i], whole[i:]}, {whole[:i], whole[i : i+(len(whole)-i)/2], whole[i+(len(whole)-i)/2:]}} {
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

// With a numeric secret, Write holds back only what String could still read as a JSON number
// holding it: a number after a colon, bracket or comma, or such a character at the end. Other
// text made of the characters of numbers is sent on at once.
func TestBodyRedactorNumericHoldsOnlyANumber(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "12345678"})
	for _, c := range []struct{ piece, want string }{
		{"hello there", "hello there"},
		{"see", "see"},
		{"x-", "x-"},
		{"v1.2.3", "v1.2.3"},
		{"e3", "e3"},
		{`{"a": `, `{"a"`},
		{`{"a":-`, `{"a"`},
		{`[1,2`, `[1`},
		{`{"a": 1.5e`, `{"a"`},
	} {
		if got := r.Body().Write(c.piece); got != c.want {
			t.Errorf("Write(%q) = %q, want %q", c.piece, got, c.want)
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

// splitSecrets are made-up credentials for the tests of secrets spelled with escapes: one of
// letters and dashes, and one with slashes, a plus, a two-byte and a four-byte character.
var splitSecrets = []string{escapedSecret, "ab/cd+ef/gh-é😀-9"}

// uEscape returns the JSON escape of c (a surrogate pair beyond U+FFFF), with upper- or
// lower-case hex digits.
func uEscape(c rune, upper bool) string {
	f := `\u%04x`
	if upper {
		f = `\u%04X`
	}
	if c > 0xFFFF {
		c -= 0x10000
		return fmt.Sprintf(f, 0xD800+(c>>10)) + fmt.Sprintf(f, 0xDC00+(c&0x3FF))
	}
	return fmt.Sprintf(f, c)
}

// pctEscape returns the percent escape of each byte of s.
func pctEscape(s string, upper bool) string {
	f := "%%%02x"
	if upper {
		f = "%%%02X"
	}
	var b strings.Builder
	for i := range len(s) {
		fmt.Fprintf(&b, f, s[i])
	}
	return b.String()
}

// spellings returns ways of writing s that String decodes to s and that no listed form covers:
// every character, or one character, as a JSON escape; percent escapes in lower case; the second
// slash only as \/; and the JSON escapes of a JSON escape (a body quoted in JSON twice) or written
// in a URL.
func spellings(s string) []string {
	var out []string
	all := func(f func(c rune) string) string {
		var b strings.Builder
		for _, c := range s {
			b.WriteString(f(c))
		}
		return b.String()
	}
	out = append(out,
		all(func(c rune) string { return uEscape(c, false) }),
		all(func(c rune) string { return uEscape(c, true) }),
		pctEscape(s, false),
		all(func(c rune) string { return strings.ReplaceAll(uEscape(c, false), `\`, `\\`) }),
		all(func(c rune) string { return strings.ReplaceAll(uEscape(c, false), `\`, "%5C") }),
	)
	i := 0
	for _, c := range s {
		out = append(out, s[:i]+uEscape(c, false)+s[i+len(string(c)):])
		i += len(string(c))
	}
	if k := strings.LastIndexByte(s, '/'); k > strings.IndexByte(s, '/') {
		out = append(out, s[:k]+`\/`+s[k+1:])
	}
	return out
}

// A secret spelled with escapes outside the listed forms and cut between two or three pieces at
// any place is removed as String removes it from the whole body.
func TestBodyRedactorEscapedSecretAtEveryCut(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[0]}, &Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	for _, secret := range splitSecrets {
		for _, sp := range spellings(secret) {
			for _, whole := range []string{"error: key " + sp + " rejected", sp, `{"message":"` + sp + `"}`} {
				want := r.String(whole)
				if strings.Contains(want, sp) {
					t.Fatalf("String(%q) = %q, which still holds the secret", whole, want)
				}
				for i := 0; i <= len(whole); i++ {
					if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
						t.Fatalf("body %q cut at %d: got %q, want %q", whole, i, got, want)
					}
					for j := i; j <= len(whole); j += 3 {
						if got := redactPieces(t, r, []string{whole[:i], whole[i:j], whole[j:]}); got != want {
							t.Fatalf("body %q cut at %d and %d: got %q, want %q", whole, i, j, got, want)
						}
					}
				}
			}
		}
	}
}

// Random bodies of secrets in every spelling of spellings and every listed form, starts of them
// that never complete, escapes and other text, cut at every offset into two pieces, a byte at a
// time and at random: what the client gets joined is what String returns for the whole body. A
// body with more decodings than String tries, which String replaces whole, is left out; the test
// fails when that leaves out more than a few.
func TestBodyRedactorEscapedMatchesStringAtEveryCut(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[0]}, &Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	var frags []string
	for _, secret := range splitSecrets {
		for _, sp := range append(spellings(secret), encodings(secret)...) {
			frags = append(frags, sp, sp[:len(sp)/2])
		}
	}
	frags = append(frags, " ", "x", "error: key ", `{"message":"`, `"}`, "data: ", "\n\n", "é", `\`, `\\`, `\u00`,
		`\ud83d`, "%", "%5", "%25", `\n`, `\"`, "s", "sk-", "ab/")
	rng := rand.New(rand.NewPCG(9, 10))
	ran, left := 0, 0
	for ran < 300 {
		var b strings.Builder
		for range 1 + rng.IntN(5) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		whole := b.String()
		if _, over := r.decodings(r.r.Replace(whole), false, func(string, []span) bool { return false }); over {
			left++
			continue
		}
		ran++
		want := r.String(whole)
		check := func(pieces []string) {
			t.Helper()
			if got := redactPieces(t, r, pieces); got != want {
				t.Fatalf("body %q cut as %q: got %q, want %q", whole, pieces, got, want)
			}
		}
		for i := 0; i <= len(whole); i++ {
			check([]string{whole[:i], whole[i:]})
		}
		bytes := make([]string, len(whole))
		for i := range len(whole) {
			bytes[i] = whole[i : i+1]
		}
		check(bytes)
		for range 20 {
			var pieces []string
			for rest := whole; rest != ""; {
				k := min(rng.IntN(24), len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
			check(pieces)
		}
	}
	if left > ran/10 {
		t.Fatalf("%d bodies left out for every %d run", left, ran)
	}
}

// doubleEscape returns s with every character written as a JSON escape, and every character of
// those as a JSON escape again: 36 bytes for each byte of a secret of letters and dashes, more
// than Write holds back for.
func doubleEscape(s string) string {
	var b strings.Builder
	for _, c := range s {
		for _, d := range uEscape(c, false) {
			b.WriteString(uEscape(d, false))
		}
	}
	return b.String()
}

// A spelling too long to hold back is replaced with [redacted] from its start up to the first byte
// that cannot be part of a spelling, wherever it is cut; the text after that byte comes through.
// So is the start of one that a piece ends with and that never completes: it may have been one.
// The expected values were worked out by hand.
func TestBodyRedactorSpellingTooLongToHold(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	long := doubleEscape(escapedSecret)
	if r.String("a "+long+" b") != "a [redacted] b" {
		t.Fatalf("String does not remove the spelling: %q", r.String("a "+long+" b"))
	}
	for _, whole := range []string{"a " + long + " b", "a " + long + ", b"} {
		want := strings.Replace(whole, long, Redacted, 1)
		for i := 0; i <= len(whole); i++ {
			for _, pieces := range [][]string{{whole[:i], whole[i:]}, {whole[:i/2], whole[i/2 : i], whole[i:]}} {
				if got := redactPieces(t, r, pieces); got != want {
					t.Fatalf("%q cut as %q: got %q, want %q", whole, pieces, got, want)
				}
			}
		}
	}
	partial := doubleEscape(escapedSecret[:20])
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{"a " + partial, " b"}, "a [redacted] b"},
		{[]string{"a " + partial, "", "x", " b"}, "a [redacted] b"},
		{[]string{"a " + partial}, "a [redacted]"},
		// Held whole in one piece, it is not cut, and String leaves it as it is.
		{[]string{"a " + partial + " b"}, "a " + partial + " b"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}
