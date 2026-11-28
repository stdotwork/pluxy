package protocol

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

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
				limit = b.escapeHold() + 1 + more
			}
			if len(b.held) >= limit {
				if _, over := r.decodings(r.r.Replace(body.String()), false, func(string, []span) bool { return false }); over {
					limit = b.escapeHold() + 1 + more
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

const parts = 8

func spread[C any](t *testing.T, cases []C, f func(t *testing.T, c C)) {
	t.Helper()
	for k := range parts {
		t.Run(fmt.Sprintf("part%d", k), func(t *testing.T) {
			t.Parallel()
			for i := k; i < len(cases); i += parts {
				f(t, cases[i])
			}
		})
	}
}

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
		{[]string{"a pre-secret-1", "-longer b"}, "a [redacted] b"},
		{[]string{"a pre-secret-1", "-long"}, "a [redacted]-long"},
		{[]string{"a pre-secret-", "1"}, "a [redacted]"},
		{[]string{"abcd1234", "efgh5678"}, "[redacted]5678"},
		{[]string{"x1234efgh", "5678"}, "x[redacted]"},
		{[]string{`{"k":"sk-sl\`, `/ash+42"}`}, `{"k":"[redacted]"}`},
		{[]string{"?key=sk-sl%2", "Fash%2B42&x=1"}, "?key=[redacted]&x=1"},
		{[]string{"Basic " + base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))[:10],
			base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))[10:]}, "Basic [redacted]"},
		{[]string{"cut sk-body-key-0", "12"}, "cut sk-body-key-012"},
		{[]string{"cut pre-secret-1-lo"}, "cut [redacted]-lo"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

func TestBodyRedactorMatchesStringAtEveryCut(t *testing.T) {
	t.Parallel()
	r := bodyRedactor()
	key64 := base64.StdEncoding.EncodeToString([]byte("sk-body-key-0123456789"))
	frags := []string{
		"sk-body-key-0123456789", "sk-sl/ash+42", `sk-sl\/ash+42`, "sk-sl%2Fash%2B42", key64,
		"abcd1234efgh", "1234efgh5678", "abcd1234efgh5678", "pre-secret-1", "pre-secret-1-longer",
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
	check := func(t *testing.T, whole string, pieces []string) {
		t.Helper()
		if got, want := redactPieces(t, r, pieces), r.String(whole); got != want {
			t.Fatalf("body %q cut as %q: got %q, want %q", whole, pieces, got, want)
		}
	}
	type bodyAt struct {
		n      int
		whole  string
		random [][]string
	}
	var bodies []bodyAt
	for n := range 300 {
		c := bodyAt{n: n, whole: body()}
		for range 20 {
			var pieces []string
			for rest := c.whole; rest != ""; {
				k := min(rng.IntN(12), len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
			c.random = append(c.random, pieces)
		}
		bodies = append(bodies, c)
	}
	spread(t, bodies, func(t *testing.T, c bodyAt) {
		whole := c.whole
		for i := 0; i <= len(whole); i++ {
			check(t, whole, []string{whole[:i], whole[i:]})
		}
		bytes := make([]string, len(whole))
		for i := 0; i < len(whole); i++ {
			bytes[i] = whole[i : i+1]
		}
		check(t, whole, bytes)
		if c.n%10 == 0 {
			for i := 0; i <= len(whole); i++ {
				for j := i; j <= len(whole); j++ {
					check(t, whole, []string{whole[:i], whole[i:j], whole[j:]})
				}
			}
		}
		for _, pieces := range c.random {
			check(t, whole, pieces)
		}
	})
}

const escapedSecret = "sk-fake-credential-value-s"

func holdsEscaped(x string) bool {
	d, _, _ := unescapeLayer(x, nil, false, allEscapes)
	return strings.Contains(x, escapedSecret) || strings.Contains(d, escapedSecret)
}

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
		{[]string{first, "er"}, "err [redacted]x-other"},
		{[]string{first}, "err [redacted]x-oth"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

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

func TestBodyRedactorNeverWorseThanEachPiece(t *testing.T) {
	t.Parallel()
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	frags := []string{escapedSecret, "sk-", "s", `" rejected`, "error: key ", " ", "x", `\u00`, "c2st",
		base64.StdEncoding.EncodeToString([]byte(escapedSecret))[:12]}
	for i := range len(escapedSecret) {
		frags = append(frags, escapedSecret[:i]+fmt.Sprintf(`\u%04x`, escapedSecret[i])+escapedSecret[i+1:])
	}
	rng := rand.New(rand.NewPCG(3, 4))
	check := func(t *testing.T, whole string, pieces []string) {
		t.Helper()
		var each strings.Builder
		for _, p := range pieces {
			each.WriteString(r.String(p))
		}
		if got := redactPieces(t, r, pieces); holdsEscaped(got) && !holdsEscaped(each.String()) {
			t.Fatalf("body %q cut as %q: got %q, which holds the secret; each piece alone gives %q", whole, pieces, got, each.String())
		}
	}
	type body struct {
		whole  string
		random [][]string
	}
	var bodies []body
	for range 400 {
		var b strings.Builder
		for range 1 + rng.IntN(5) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		c := body{whole: b.String()}
		for range 20 {
			var pieces []string
			for rest := c.whole; rest != ""; {
				k := min(rng.IntN(16), len(rest))
				pieces = append(pieces, rest[:k])
				rest = rest[k:]
			}
			c.random = append(c.random, pieces)
		}
		bodies = append(bodies, c)
	}
	spread(t, bodies, func(t *testing.T, c body) {
		check(t, c.whole, []string{c.whole})
		for i := 0; i <= len(c.whole); i++ {
			check(t, c.whole, []string{c.whole[:i], c.whole[i:]})
		}
		for _, pieces := range c.random {
			check(t, c.whole, pieces)
		}
	})
}

var collapsing = "model said " + strings.Repeat("\\", 16) + "%25252525 ; upstream: invalid key "

func formsIn(r *Redactor, x string) []string {
	var found []string
	for _, f := range r.forms {
		if strings.Contains(x, f) {
			found = append(found, f)
		}
	}
	return found
}

func TestBodyRedactorCutSecretAfterCollapse(t *testing.T) {
	t.Parallel()
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

func TestBodyRedactorNumericMatchesStringAtEveryCut(t *testing.T) {
	t.Parallel()
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

func TestBodyRedactorCollapseAtEveryCut(t *testing.T) {
	t.Parallel()
	r := bodyRedactor()
	frags := []string{"sk-body-key-0123456789", "pre-secret-1-longer", "abcd1234efgh5678", " rejected", "sk-", "x", " "}
	rng := rand.New(rand.NewPCG(7, 8))
	var bodies []string
	for range 200 {
		var b strings.Builder
		b.WriteString(collapsing)
		for range 1 + rng.IntN(4) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		bodies = append(bodies, b.String())
	}
	spread(t, bodies, func(t *testing.T, whole string) {
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
	})
}

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

var splitSecrets = []string{escapedSecret, "ab/cd+ef/gh-é😀-9"}

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
	first, n := utf8.DecodeRuneInString(s)
	out = append(out, strings.Replace(uEscape(first, false), bs+"u", bs+"u005c%75", 1)+s[n:])
	if k := strings.LastIndexByte(s, '/'); k > strings.IndexByte(s, '/') {
		out = append(out, s[:k]+`\/`+s[k+1:])
	}
	return out
}

func TestBodyRedactorEscapedSecretAtEveryCut(t *testing.T) {
	t.Parallel()
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[0]}, &Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	type body struct{ sp, whole string }
	var bodies []body
	for _, secret := range splitSecrets {
		for _, sp := range spellings(secret) {
			for _, whole := range []string{"error: key " + sp + " rejected", sp, `{"message":"` + sp + `"}`} {
				bodies = append(bodies, body{sp, whole})
			}
		}
	}
	spread(t, bodies, func(t *testing.T, b body) {
		whole := b.whole
		want := r.String(whole)
		if strings.Contains(want, b.sp) {
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
	})
}

func TestBodyRedactorEscapedMatchesStringAtEveryCut(t *testing.T) {
	t.Parallel()
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[0]}, &Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	rng := rand.New(rand.NewPCG(9, 10))
	var secrets, mixed, escapedEnd, doubled, frags []string
	for _, secret := range splitSecrets {
		for _, sp := range append(spellings(secret), encodings(secret)...) {
			secrets = append(secrets, sp)
			frags = append(frags, sp[:len(sp)/2])
			if strings.Contains(sp, "%75") {
				mixed = append(mixed, sp)
			}
			if strings.HasSuffix(sp, uEscape([]rune(secret)[utf8.RuneCountInString(secret)-1], false)) {
				escapedEnd = append(escapedEnd, sp)
			}
		}
		doubled = append(doubled, doubleEscape(secret))
	}
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
	quota := []int{300, 200, 1000, 300, 60, 150}
	var made [6]int
	matched, safe := 0, 0
	var cases []func(t *testing.T)
	for n := 0; made[5] < quota[5]; n++ {
		kind := n % 5
		if matched >= 300 && safe >= 100 && !slices.ContainsFunc([]int{1, 2, 3, 4}, func(k int) bool { return made[k] < quota[k] }) {
			kind = 5
		} else if made[kind] >= quota[kind] && (kind != 0 || matched >= 300) {
			continue
		}
		made[kind]++
		heldAt := 0
		var b strings.Builder
		var spelled [][2]int
		secret := func(secrets []string) {
			sp := secrets[rng.IntN(len(secrets))]
			spelled = append(spelled, [2]int{b.Len(), b.Len() + len(sp)})
			b.WriteString(sp)
		}
		switch kind {
		case 5:
			b.WriteString(collapsing + []string{bs, bs + "u", bs + "u0", bs + "u01", bs + "ud83d" + bs + "udc", bs + bs + "u01",
				bs + "u005cu01", "%", "%1"}[rng.IntN(9)])
			heldAt = b.Len()
			rs := []rune(splitSecrets[1])
			k := rng.IntN(len(rs))
			secret([]string{string(rs[:k]) + uEscape(rs[k], false) + string(rs[k+1:])})
			b.WriteString([]string{" end", "", "!"}[rng.IntN(3)])
		case 1:
			b.WriteString(collapsing + escapes(0))
			secret(secrets)
			b.WriteString([]string{"!", " ", ""}[rng.IntN(3)] + []string{many, escapes(0)}[rng.IntN(2)])
		case 2:
			b.WriteString(collapsing + escapes(3))
			secret(mixed)
			b.WriteString([]string{"!", " ", ""}[rng.IntN(3)] + escapes(0))
		case 3:
			b.WriteString("err ")
			secret(escapedEnd)
			b.WriteString([]string{"x-oth%2x", "x%2", " %2x", bs + "u00", ""}[rng.IntN(5)] + escapes(0))
			secret([]string{mixedSpelling(rng, splitSecrets[rng.IntN(len(splitSecrets))])})
			b.WriteString(" " + escapes(4))
		case 4:
			b.WriteString(frags[rng.IntN(len(frags))])
			secret(doubled)
			b.WriteString([]string{" b", ", b", "s b", "x", ""}[rng.IntN(5)])
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
		var random [][]string
		if kind <= 1 {
			for range 20 {
				var pieces []string
				for rest := whole; rest != ""; {
					k := min(rng.IntN(24), len(rest))
					pieces = append(pieces, rest[:k])
					rest = rest[k:]
				}
				random = append(random, pieces)
			}
		}
		cases = append(cases, func(t *testing.T) {
			want := r.String(whole)
			overAt := map[int]bool{}
			overSoFar := func(pieces []string) bool {
				at := 0
				for _, p := range pieces[:len(pieces)-1] {
					at += len(p)
					o, ok := overAt[at]
					if !ok {
						_, o = r.decodings(r.r.Replace(whole[:at]), false, func(string, []span) bool { return false })
						overAt[at] = o
					}
					if o {
						return true
					}
				}
				return false
			}
			check := func(pieces []string) {
				t.Helper()
				got := redactPieces(t, r, pieces)
				if got == want {
					return
				}
				if !over && !overSoFar(pieces) {
					t.Fatalf("body %q cut as %q: got %q, want %q", whole, pieces, got, want)
				}
				if f := revealed(r, got); f != "" {
					t.Fatalf("body %q cut as %q: got %q, which holds %q in a decoding", whole, pieces, got, f)
				}
				at := 0
				for _, p := range pieces[:len(pieces)-1] {
					at += len(p)
					for _, sp := range spelled {
						if rest := whole[min(at, sp[1]):sp[1]]; sp[0] < at && len(rest) >= 12 && strings.Count(whole, rest) == 1 &&
							strings.Contains(got, rest) && !strings.Contains(r.String(spellingRun(r, whole[:sp[1]], sp[0], sp[1])), rest) {
							t.Fatalf("body %q cut as %q: got %q, which holds %q, the rest of a secret cut there", whole, pieces, got, rest)
						}
					}
				}
			}
			switch kind {
			case 5:
				for i := heldAt; i <= len(whole); i++ {
					check([]string{whole[:heldAt], whole[heldAt:i], whole[i:]})
					check([]string{whole[:i], whole[i:]})
				}
				return
			case 2:
				for i := len(collapsing); i <= len(whole); i++ {
					check([]string{whole[:i], whole[i:]})
				}
				return
			case 3, 4:
				for i := 0; i <= len(whole); i++ {
					check([]string{whole[:i], whole[i:]})
				}
				return
			}
			for i := 0; i <= len(whole); i++ {
				check([]string{whole[:i], whole[i:]})
			}
			bytes := make([]string, len(whole))
			for i := range len(whole) {
				bytes[i] = whole[i : i+1]
			}
			check(bytes)
			for _, pieces := range random {
				check(pieces)
			}
		})
	}
	spread(t, cases, func(t *testing.T, run func(t *testing.T)) { run(t) })
}

func TestBodyRedactorCountsDecodingsAsString(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "3x-other-secret"})
	whole := "err my-api-key" + bs + "u0073x-oth%2x" + bs + bs + "u0033" + bs + "u005cu0078" + bs + "u005cu002D" + bs + "u005cu006f%2574h%65r" +
		bs + "u002D" + bs + bs + "u0073" + bs + "u002565" + bs + "u005c%750063%2572" + bs + "u005cu0065t " + bs + "u00255C" + bs + bs + "%25" +
		bs + "u005c" + bs + bs + "%2525" + bs + bs
	want := "err [redacted]x-oth%2x[redacted] " + bs + "u00255C" + bs + bs + "%25" + bs + "u005c" + bs + bs + "%2525" + bs + bs
	if got := r.String(whole); got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
	for i := 0; i <= len(whole); i++ {
		if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
			t.Errorf("cut at %d: got %q, want %q", i, got, want)
		}
	}
}

func mixedSpelling(rng *rand.Rand, secret string) string {
	var b strings.Builder
	for _, c := range secret {
		u, p := uEscape(c, false), pctEscape(string(c), false)
		b.WriteString([]string{string(c), u, bs + u, strings.ReplaceAll(u, bs+"u", bs+"u005cu"), p,
			strings.ReplaceAll(p, "%", "%25"), strings.ReplaceAll(p, "%", bs+"u0025")}[rng.IntN(7)])
	}
	return b.String()
}

func TestBodyRedactorNeverCutsInsideAnEscapeOfADecoding(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: splitSecrets[1]})
	whole := "err x%2" + bs + "u0061" + bs + "u005cu0062/c%64" + bs + "u00252b" + bs + bs + "u0065%2566%252fg" + bs + bs + "u0068" + bs +
		"u00252d" + bs + "u0025c3" + bs + "u0025a9%f0%9f%98%80%252d" + bs + "u0039 end"
	want := r.String(whole)
	if want != whole {
		t.Fatalf("String = %q, want the body as it is", want)
	}
	for i := 0; i <= len(whole); i++ {
		if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
			t.Errorf("cut at %d: got %q, want %q", i, got, want)
		}
	}
}

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

func escapeEach(s string) string {
	var b strings.Builder
	for _, c := range s {
		b.WriteString(uEscape(c, false))
	}
	return b.String()
}

func doubleEscape(s string) string { return escapeEach(escapeEach(s)) }

func TestBodyRedactorSpellingTooLongToHold(t *testing.T) {
	t.Parallel()
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
	partial := doubleEscape(escapedSecret[:25])
	for _, c := range []struct {
		pieces []string
		want   string
	}{
		{[]string{"a " + partial, " b"}, "a [redacted] b"},
		{[]string{"a " + partial, "", "x", " b"}, "a [redacted]x b"},
		{[]string{"a " + partial, "s", " b"}, "a [redacted] b"},
		{[]string{"a " + partial}, "a [redacted]"},
		{[]string{"a " + partial + " b"}, "a " + partial + " b"},
	} {
		if got := redactPieces(t, r, c.pieces); got != c.want {
			t.Errorf("%q: got %q, want %q", c.pieces, got, c.want)
		}
	}
}

const bs = "\x5c"

func TestBodyRedactorCollapseAfterSentKeepsRest(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "sx-other-secret"})
	whole := "err 73my-api-key" + bs + "u005c%750073x-other-secret!" + strings.Repeat(bs, 9) + "73%25%2573"
	if _, over := r.decodings(r.r.Replace(whole[16:]), false, func(string, []span) bool { return false }); !over {
		t.Fatalf("the text after the first secret does not have more decodings than String tries")
	}
	pieces := []string{whole[:29], whole[29:]}
	got := redactPieces(t, r, pieces)
	if strings.Contains(got, "x-oth") || strings.Contains(got, "er-secret") {
		t.Errorf("%q: got %q, which holds the rest of a secret", pieces, got)
	}
}

func TestBodyRedactorFlushAfterCollapse(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "my-api-keys"}, &Credential{Kind: CredentialAPIKey, APIKey: "3x-other-secret"})
	pieces := []string{"err my-api-key" + bs + "u0073x-oth", "er" + strings.Repeat(bs, 16) + "%25252525"}
	got := redactPieces(t, r, pieces)
	if strings.Contains(got, "x-oth") || strings.Contains(got, "er-secret") {
		t.Errorf("%q: got %q, which holds the rest of a secret", pieces, got)
	}
}

func TestBodyRedactorReplacedSpellingIsBounded(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	head := doubleEscape(escapedSecret[:25])
	if len(head) <= r.Body().escapeHold() {
		t.Fatalf("the spelling takes %d bytes, which Write can hold back", len(head))
	}
	for _, data := range []string{
		strings.Repeat("0123456789abcdef", 1<<14),
		strings.Repeat("%41%42%2F%3D", 1<<13),
		strings.Repeat(`line-one`+bs+`nline`+bs+`ttwo`+bs+`"q`+bs+`"`, 1<<11),
		strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo0MjQy"+bs+"/", 1<<12),
	} {
		for _, pieces := range [][]string{{"prefix " + head, data + " suffix"}, {"prefix " + head, data[:1000], data[1000:] + " suffix"}} {
			got := redactPieces(t, r, pieces)
			rest, ok := strings.CutPrefix(got, "prefix "+Redacted)
			if lost := len(data) + len(" suffix") - len(rest); !ok || lost > 0 || !strings.HasSuffix(data+" suffix", rest) {
				t.Errorf("%.40q... cut as %d pieces: got %.60q..., which loses %d bytes after the spelling", data, len(pieces), got, lost)
			}
		}
	}
}

func TestBodyRedactorDropsUpToFlush(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "pa" + string(utf8.RuneError) + "ss-key-0123"})
	head := escapeEach(doubleEscape(bs + "ud83d" + bs + "udc"))
	got := redactPieces(t, r, []string{"a " + head, "\xef\xbf"})
	if got != "a "+Redacted {
		t.Errorf("got %q, want %q", got, "a "+Redacted)
	}
}

func TestBodyRedactorEscapeRunsComeThrough(t *testing.T) {
	t.Parallel()
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-fake-credential-v1"})
	for _, unit := range []string{bs, bs + "u005c", "%5C", bs + "%5C", "%255C", "%2525" + bs + bs + bs + bs,
		"%25" + bs + bs + "%5C", bs + "%", "%25", "%41%42", bs + "n" + bs + "t", "QUJD" + bs + "/"} {
		whole := strings.Repeat(unit, 20000/len(unit))
		if r.String(whole) != whole {
			t.Fatalf("String changes %.20q...", whole)
		}
		for _, n := range []int{7, 333, 1000, 4095, len(whole)} {
			var pieces []string
			for i := 0; i < len(whole); i += n {
				pieces = append(pieces, whole[i:min(i+n, len(whole))])
			}
			if got := redactPieces(t, r, pieces); got != whole {
				t.Errorf("%.20q... in pieces of %d: got %d bytes, want %d", whole, n, len(got), len(whole))
			}
		}
	}
}

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
	r = NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "pa" + string(utf8.RuneError) + "ss-key"})
	whole = `{"m":"key pa` + string(utf8.RuneError) + `ss-key"}`
	want = `{"m":"key [redacted]"}`
	for i := 0; i <= len(whole); i++ {
		if got := redactPieces(t, r, []string{whole[:i], whole[i:]}); got != want {
			t.Errorf("cut at %d: got %q, want %q", i, got, want)
		}
	}
}

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
		{"backslashes", fill(bs + bs), []int{256, 4096, size}},
		{"percent25", fill("%25"), []int{256, 4096, size}},
		{"percents", fill("%"), []int{256, 4096}},
		{"percent2525_backslashes", fill("%2525" + bs + bs + bs + bs), []int{256, 4096, size}},
		{"percent25_backslashes_percent5C", fill("%25" + bs + bs + "%5C"), []int{256, 4096, size}},
		{"backslash_percent", fill(bs + "%"), []int{256, 4096, size}},
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

func revealed(r *Redactor, x string) string {
	seen := map[string]bool{x: true}
	frontier := []string{x}
	for depth := 0; ; depth++ {
		for _, d := range frontier {
			if f := formsIn(r, d); len(f) > 0 {
				return f[0]
			}
		}
		if depth == maxDepth {
			return ""
		}
		var next []string
		for _, d := range frontier {
			for _, kind := range []int{allEscapes, jsonEscapes, percentEscapes} {
				if e, _, changed := unescapeLayer(d, nil, false, kind); changed && !seen[e] {
					seen[e] = true
					next = append(next, e)
				}
			}
		}
		frontier = next
	}
}

func TestBodyRedactorDropStopKeepsSecretWhole(t *testing.T) {
	t.Parallel()
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: escapedSecret})
	if b := r.Body(); len(r.forms[0]) != 36 || b.maxStretch() != 84 {
		t.Fatalf("longest form %d bytes, maxStretch %d; the runs below are sized for 36 and 84", len(r.forms[0]), b.maxStretch())
	}
	deep := "a " + escapeEach(doubleEscape(bs+"ud83d"+bs+"udc"))
	partial := "a " + doubleEscape(escapedSecret[:25])
	many := strings.Repeat(bs, 16) + "%25252525"
	pad := strings.Repeat("x", maxChunk-len(deep))
	hex := strings.Repeat("0123456789abcdef", 6)
	check := func(t *testing.T, pieces []string, got string) {
		t.Helper()
		rest := strings.ReplaceAll(got, Redacted, "|")
		for i := 0; i+4 <= len(escapedSecret); i++ {
			if strings.Contains(rest, escapedSecret[i:i+4]) || !strings.HasSuffix(got, Redacted+" end") {
				t.Fatalf("%.30q... cut as %d pieces, ending %q: got %q, which holds part of the secret", pieces[0], len(pieces), pieces[len(pieces)-1], got)
			}
		}
		if f := revealed(r, got); f != "" {
			t.Fatalf("%.30q... cut as %d pieces, ending %q: got %q, which holds %q in a decoding", pieces[0], len(pieces), pieces[len(pieces)-1], got, f)
		}
	}
	runs := make([]int, 91)
	for n := range runs {
		runs[n] = n
	}
	spread(t, runs, func(t *testing.T, n int) {
		tail := hex[:n] + escapedSecret + " end"
		for _, c := range [][2]string{{deep, tail}, {partial, many + tail}} {
			for k := 0; k <= len(c[1]); k++ {
				pieces := []string{c[0], c[1][:k], c[1][k:]}
				check(t, pieces, redactPieces(t, r, pieces))
			}
		}
		pieces := []string{pad + deep + tail}
		check(t, pieces, redactPieces(t, r, pieces))
	})
}

func TestBodyRedactorEscapeCutAfterCollapse(t *testing.T) {
	for _, c := range []struct {
		secret string
		pieces []string
	}{
		{"1234efgh5678", []string{collapsing + bs + "u", "1234" + bs + "u0065", "fgh5678 end"}},
		{"fake-key-0123456789", []string{collapsing + bs, "fake-key-01" + bs + "u0032", "3456789 end"}},
		{"nk-test-secret-1234", []string{collapsing + bs, "nk-test-s" + bs + "u0065", "cret-1234 end"}},
		{"12345670", []string{collapsing + bs + "ud83d" + bs + "udc", "12345" + bs + "u0036", "70 end"}},
	} {
		r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: c.secret})
		if want := r.String(strings.Join(c.pieces, "")); want != Redacted {
			t.Fatalf("String = %q, want %q", want, Redacted)
		}
		if got := redactPieces(t, r, c.pieces); revealed(r, got) != "" {
			t.Errorf("%q: got %q, which holds %q in a decoding", c.pieces[1:], got, revealed(r, got))
		}
	}
}

func TestBodyRedactorEscapeCutAfterCollapseEverywhere(t *testing.T) {
	secrets := []string{"bk-test-value-0077", "fake-key-0123456789", "nk-test-secret-1234", "rk-sample-token-xy",
		"tk-made-up-9876543", "uk-fixture-key-424", "1234efgh5678", "12345670"}
	starts := []string{bs, bs + "u", bs + "u0", bs + "u00", bs + "u01", bs + "ud83d", bs + "ud83d" + bs, bs + "ud83d" + bs + "u",
		bs + "ud83d" + bs + "ud", bs + "ud83d" + bs + "udc", bs + bs, bs + bs + "u", bs + bs + "u0", bs + bs + "u00",
		bs + bs + "ud83d" + bs + bs + "udc", bs + "u005c", bs + "u005cu", bs + "u005cu0", bs + "u005c" + bs + "u005cu", "%", "%1",
		"%2", "%25", "%5", "%5C", "%5Cu", "%5Cu0", "%255C", bs + "u0025", bs + "u00255"}
	if len(starts) != 30 {
		t.Fatalf("%d starts", len(starts))
	}
	t.Parallel()
	type spelling struct {
		r       *Redactor
		spelled string
	}
	var cases []spelling
	for _, secret := range secrets {
		r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: secret})
		for j := -1; j < len(secret); j++ {
			if testing.Short() && j > 1 && j != len(secret)/2 && j != len(secret)-1 {
				continue
			}
			spelled := secret
			if j >= 0 {
				spelled = secret[:j] + uEscape(rune(secret[j]), false) + secret[j+1:]
			}
			cases = append(cases, spelling{r, spelled})
		}
	}
	var runs atomic.Int64
	spread(t, cases, func(t *testing.T, c spelling) {
		for _, start := range starts {
			for k := 0; k <= len(c.spelled); k++ {
				pieces := []string{collapsing + start, c.spelled[:k], c.spelled[k:] + " end"}
				got := redactPieces(t, c.r, pieces)
				runs.Add(1)
				if f := revealed(c.r, got); f != "" {
					t.Fatalf("%q: got %q, which holds %q in a decoding", pieces[1:], got, f)
				}
			}
		}
	})
	t.Cleanup(func() { t.Logf("%d runs", runs.Load()) })
}

func TestBodyRedactorOnePieceMemoryIsBounded(t *testing.T) {
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: "sk-fake-credential-v1"})
	unit := "%2525" + bs + bs + bs + bs
	body := strings.Repeat(unit, (4<<20)/len(unit))
	defer debug.SetGCPercent(debug.SetGCPercent(5))
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(sample)
	base := sample[0].Value.Uint64()
	var peak atomic.Uint64
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		for {
			select {
			case <-done:
				return
			default:
			}
			if metrics.Read(s); s[0].Value.Uint64() > peak.Load() {
				peak.Store(s[0].Value.Uint64())
			}
			time.Sleep(20 * time.Microsecond)
		}
	}()
	b := r.Body()
	out := b.Write(body)
	out += b.Flush()
	close(done)
	<-stopped
	if out != body {
		t.Fatalf("got %d bytes, want the body as it is (%d bytes)", len(out), len(body))
	}
	if extra := int64(peak.Load()) - int64(base) - int64(len(body)); extra > 16<<20 {
		t.Errorf("Write took %d MiB besides the body and what it returns", extra>>20)
	} else {
		t.Logf("Write took %d KiB besides the body and what it returns", extra>>10)
	}
}

func TestBodyRedactorLargePieceCutsAtACharacter(t *testing.T) {
	r := bodyRedactor()
	for _, prefix := range []string{collapsing, "plain "} {
		for off := 1; off <= 3; off++ {
			for _, ch := range []string{"é", "€", "😀"} {
				piece := prefix + strings.Repeat("x", maxChunk-len(prefix)-off) + ch + " tail"
				b := r.Body()
				got := b.Write(piece) + b.Flush()
				if !utf8.ValidString(got) {
					t.Errorf("%.6q, %q %d bytes before the cut: the body ends %q, not UTF-8", prefix, ch, off, got[max(0, len(got)-24):])
				}
			}
		}
	}
}
