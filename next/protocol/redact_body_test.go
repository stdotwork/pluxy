package protocol

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
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
// could be part of a number, twice that and maxNumberHold; where what it holds has a backslash or a
// percent sign, which can start an escape, or the body so far has more decodings than String
// tries, more than 12 times the longest form, or, when a form could be part of a number, that,
// maxNumberHold and the longest form together. It fails the test too when Flush leaves anything
// held.
func redactPieces(t *testing.T, r *Redactor, pieces []string) string {
	t.Helper()
	b := r.Body()
	var out, body strings.Builder
	for _, p := range pieces {
		out.WriteString(b.Write(p))
		body.WriteString(p)
		if len(r.forms) > 0 {
			longest := len(r.forms[0])
			limit, more := longest, 0
			if r.numeric {
				limit, more = 2*longest+maxNumberHold, longest+maxNumberHold
			}
			if len(b.held) >= limit && strings.ContainsAny(b.held, `\%`) {
				limit = 12*longest + 1 + more
			}
			if len(b.held) >= limit {
				if _, over := r.decodings(r.r.Replace(body.String()), false, func(string, []span) bool { return false }); over {
					limit = 12*longest + 1 + more
				}
			}
			if len(b.held) >= limit {
				t.Fatalf("holding back %d bytes, %d or more: %q", len(b.held), limit, b.held)
			}
		}
	}
	out.WriteString(b.Flush())
	if b.held != "" {
		t.Fatalf("still holding %q after Flush", b.held)
	}
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
// slash only as \/; the JSON escapes of a JSON escape (a body quoted in JSON twice) or written
// in a URL; and the first character with escapes of both kinds in two layers.
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
	// The first character as a JSON escape whose backslash is a JSON escape and whose u is a
	// percent escape: decoding takes both kinds, in two layers.
	first, n := utf8.DecodeRuneInString(s)
	out = append(out, strings.Replace(uEscape(first, false), bs+"u", bs+"u005c%75", 1)+s[n:])
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
// time and at random. Where String reads the body whole, what the client gets joined is what
// String returns. A body with more decodings than String tries, which String replaces whole, is
// checked for what must not get through instead: a listed form, or the rest of a spelling of a
// secret that a cut splits. Most of the bodies are made to reach that after a Write that sent
// text whole and marked what it held as sent: text with many decodings, escapes, a whole spelling
// of a secret, and more escapes, after a byte that cannot be part of a spelling or right after it,
// so that the body may end inside the run. The escapes can give the run more decodings than String
// tries only together with the part of it that was sent, most often before a spelling with
// escapes of both kinds, as 1000 or more of the bodies have; those are cut in two after the text
// with many decodings, the others also a byte at a time and at random.
func TestBodyRedactorEscapedMatchesStringAtEveryCut(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[0]}, &Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	rng := rand.New(rand.NewPCG(9, 10))
	var secrets, mixed, frags []string
	for _, secret := range splitSecrets {
		for _, sp := range append(spellings(secret), encodings(secret)...) {
			secrets = append(secrets, sp)
			frags = append(frags, sp[:len(sp)/2])
			if strings.Contains(sp, "%75") {
				mixed = append(mixed, sp)
			}
		}
	}
	// Text with more decodings than String tries, and pieces of escapes, which can make a body
	// have more together.
	many := strings.Repeat(bs, 16) + "%25252525"
	atoms := []string{bs, bs + bs, "%25", "%", "%5C", "u", "00", "73", "x"}
	escapes := func(least int) string {
		var e strings.Builder
		for range least + rng.IntN(6) {
			e.WriteString(atoms[rng.IntN(len(atoms))])
		}
		return e.String()
	}
	frags = append(frags, " ", "x", "error: key ", `{"message":"`, `"}`, "data: ", "\n\n", "é", bs, bs+bs, bs+"u00",
		bs+"ud83d", "%", "%5", "%25", bs+"n", bs+`"`, "s", "sk-", "ab/", collapsing, many)
	// Bodies of each kind made so far: random, and the two kinds made to reach many decodings.
	var made [3]int
	matched, safe := 0, 0
	for n := 0; matched < 300 || safe < 100 || made[1] < 200 || made[2] < 1000; n++ {
		if made[n%3] >= []int{300, 200, 1000}[n%3] && (n%3 != 0 || matched >= 300) {
			continue
		}
		made[n%3]++
		var b strings.Builder
		var spelled [][2]int // where whole spellings of secrets are in the body
		secret := func(secrets []string) {
			sp := secrets[rng.IntN(len(secrets))]
			spelled = append(spelled, [2]int{b.Len(), b.Len() + len(sp)})
			b.WriteString(sp)
		}
		switch n % 3 {
		case 1:
			b.WriteString(collapsing + escapes(0))
			secret(secrets)
			b.WriteString([]string{"!", " ", ""}[rng.IntN(3)] + []string{many, escapes(0)}[rng.IntN(2)])
		case 2:
			b.WriteString(collapsing + escapes(3))
			secret(mixed)
			b.WriteString([]string{"!", " ", ""}[rng.IntN(3)] + escapes(0))
		default:
			for range 1 + rng.IntN(5) {
				if rng.IntN(3) == 0 {
					secret(secrets)
				} else {
					b.WriteString(frags[rng.IntN(len(frags))])
				}
			}
		}
		whole := b.String()
		_, over := r.decodings(r.r.Replace(whole), false, func(string, []span) bool { return false })
		if over {
			safe++
		} else {
			matched++
		}
		want := r.String(whole)
		check := func(pieces []string) {
			t.Helper()
			got := redactPieces(t, r, pieces)
			if !over {
				if got != want {
					t.Fatalf("body %q cut as %q: got %q, want %q", whole, pieces, got, want)
				}
				return
			}
			if f := formsIn(r, got); len(f) > 0 {
				t.Fatalf("body %q cut as %q: got %q, which holds %q", whole, pieces, got, f)
			}
			at := 0
			for _, p := range pieces[:len(pieces)-1] {
				at += len(p)
				for _, sp := range spelled {
					// The rest of a spelling cut at at, long enough not to be there by chance and
					// found nowhere else in the body, which String removes from the run of bytes
					// that can be part of a spelling up to the end of it (text before can make it
					// read otherwise: \u00 before ab is one escape).
					if rest := whole[min(at, sp[1]):sp[1]]; sp[0] < at && len(rest) >= 12 && strings.Count(whole, rest) == 1 &&
						strings.Contains(got, rest) && !strings.Contains(r.String(spellingRun(r, whole[:sp[1]], sp[0], sp[1])), rest) {
						t.Fatalf("body %q cut as %q: got %q, which holds %q, the rest of a secret cut there", whole, pieces, got, rest)
					}
				}
			}
		}
		if n%3 == 2 {
			// Cut in two after the text with many decodings, where a secret is cut.
			for i := len(collapsing); i <= len(whole); i++ {
				check([]string{whole[:i], whole[i:]})
			}
			continue
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
}

// spellingRun returns the run of bytes that can be part of a spelling of a form (spells) in x that
// holds x[i:j].
func spellingRun(r *Redactor, x string, i, j int) string {
	b := r.Body()
	for i > 0 && b.spells(x[i-1]) {
		i--
	}
	for j < len(x) && b.spells(x[j]) {
		j++
	}
	return x[i:j]
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

// bs is a backslash, written so that no escape in these tests is itself read as one.
const bs = "\x5c"

// After a Write that sent text whole and marked the end it holds as sent, a later piece that
// makes the text have more decodings than String tries still does not send the rest of a secret
// that began in that end: here "s" spelled as \%750073 (two layers) begins
// "sx-other-secret", and the second piece brings the rest and text with many decodings.
func TestBodyRedactorCollapseAfterSentKeepsRest(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "sx-other-secret"})
	pieces := []string{"err my-api-key" + bs + "u005c%750073x-oth", "er-secret!%2525" + bs + bs + bs + bs}
	got := redactPieces(t, r, pieces)
	if strings.Contains(got, "x-oth") || strings.Contains(got, "er-secret") {
		t.Errorf("%q: got %q, which holds the rest of a secret", pieces, got)
	}
}

// Flush returns all the BodyRedactor holds, also when what it holds was partly sent and has
// more decodings than String tries.
func TestBodyRedactorFlushAfterCollapse(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "3x-other-secret"})
	pieces := []string{"err my-api-key" + bs + "u0073x-oth", "er" + strings.Repeat(bs, 16) + "%25252525"}
	got := redactPieces(t, r, pieces)
	if strings.Contains(got, "x-oth") || strings.Contains(got, "er-secret") {
		t.Errorf("%q: got %q, which holds the rest of a secret", pieces, got)
	}
}

// A spelling too long to hold back that holds no secret takes with it, when it is replaced, no
// more of the data after it than could still be part of a spelling: here an unfinished
// surrogate pair escaped twice, then 1 MiB of hex digits, which no spelling of a form runs
// through.
func TestBodyRedactorReplacedSpellingIsBounded(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test"})
	head := "prefix " + doubleEscape(bs+"ud83d"+bs+"udc")
	hex := strings.Repeat("0123456789abcdef", 65536)
	got := redactPieces(t, r, []string{head, hex + " suffix"})
	if !strings.HasPrefix(got, "prefix ") || !strings.HasSuffix(got, " suffix") {
		t.Fatalf("got %d bytes, starting %.40q and ending %.40q", len(got), got, got[max(0, len(got)-40):])
	}
	if lost := len(head) + len(hex) + len(" suffix") - len(got); lost > len(head)+r.Body().maxStretch() {
		t.Errorf("lost %d bytes of the body, more than the spelling (%d) and %d after it", lost, len(head)-len("prefix "), r.Body().maxStretch())
	}
}

// A credential that holds U+FFFD is removed from JSON that writes it with invalid UTF-8, as
// String removes it (a JSON decoder reads the invalid byte as U+FFFD), wherever the body is cut.
func TestBodyRedactorInvalidUTF8(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "pa" + string(utf8.RuneError) + "ss"})
	whole := `{"m":"key pa` + "\xff" + `ss"}`
	want := `{"m":"key [redacted]"}`
	if got := r.String(whole); got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	for i := 0; i <= len(whole); i++ {
		if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
			t.Errorf("cut at %d: got %q, want %q", i, got, want)
		}
	}
}

// BenchmarkBodyRedactor streams 4 MiB bodies through a BodyRedactor in pieces of 256 and 4096
// bytes, and in one piece: JSON with and without escapes, and the worst cases for it, text made
// only of escapes or of bytes that escapes are made of, and one large piece whose end makes Write
// send it whole.
func BenchmarkBodyRedactor(b *testing.B) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-test/key+42"},
		&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "3x-other-secret"})
	const size = 4 << 20
	fill := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	for _, c := range []struct {
		name   string
		body   string
		pieces []int
	}{
		{"plain_json", string(largeBody(`{"type":"text","text":"Hello there, a long reply.","n":12345}`)), []int{256, 4096}},
		{"json_backslashes", string(largeBody(`{"type":"text","text":"printf(\"%d\\n\", x); // C:\\temp"}`)), []int{256, 4096}},
		{"backslashes", fill(bs + bs), []int{256, 4096}},
		{"percent25", fill("%25"), []int{256, 4096}},
		{"percents", fill("%"), []int{256, 4096}},
		{"hex", fill("0123456789abcdef"), []int{256, 4096}},
		{"fallback_end", fill("x ") + "err my-api-key" + bs + "u0073x-oth", []int{size + 64}},
	} {
		for _, n := range c.pieces {
			b.Run(fmt.Sprintf("%s/%d", c.name, n), func(b *testing.B) {
				b.SetBytes(int64(len(c.body)))
				b.ReportAllocs()
				for range b.N {
					br := r.Body()
					for i := 0; i < len(c.body); i += n {
						_ = br.Write(c.body[i:min(i+n, len(c.body))])
					}
					_ = br.Flush()
				}
			})
		}
	}
}
