// Package secretshapes is the table of secret shapes that the tests of protocol (the extension
// side's AccountSecrets) and of configdoc (core's rules for settings) both run, so the two paths
// are held to one set of expected values, written by hand. Only tests import it.
package secretshapes

import (
	"encoding/base64"
	"slices"
	"strings"
)

// Made-up values. Nothing here is or was a real credential.
const (
	Token = "Qx7Rv2Lm9Tz4"                      // 12 bytes
	Other = "Wp8Ys3Nk6Df1"                      // 12 bytes
	Long  = "Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr9St1Uv2" // 33 bytes: one long token
)

// Want says, written by hand, what one kind of field must do with one shape of value.
type Want struct {
	At4, At8 []string // runs of real secret, counted from 4 bytes and from 8
	Stay     []string // public words that stay in an error
	// written says a URL's query value also counts as written, with its escapes, at 4 bytes, so
	// whether a 4 to 7 byte cut of it stays depends on where the escapes fall.
	Written bool
}

func at4(runs ...string) Want { return Want{At4: runs} }
func at8(runs ...string) Want { return Want{At8: runs} }
func keep(words ...string) Want {
	return Want{Stay: words}
}

func (w Want) keep(words ...string) Want {
	w.Stay = append(slices.Clone(w.Stay), words...)
	return w
}

func (w Want) asWritten() Want {
	w.Written = true
	return w
}

func (w Want) and(runs ...string) Want {
	w.At4 = append(slices.Clone(w.At4), runs...)
	return w
}

// The kinds of field a shape is crossed with. Fields of one kind follow one rule.
const (
	InCredential = "credential" // api_key, access_token, refresh_token, a login's secret input
	InHeader     = "header"     // a secret header other than Cookie
	InCookie     = "cookie"     // the Cookie header
	InSettings   = "settings"   // a string below a secret settings key
	InUserinfo   = "userinfo"   // a URL's user name or password: proxy, settings URL, login URL
	InQuery      = "query"      // a URL's secret query value: settings URL, login URL
)

// Row is one input shape and what each kind of field must do with it.
type Row struct {
	Name, Value string
	Want        map[string]Want
	Cannot      map[string]string // kind of field the shape cannot reach, and why
	Forms       []string          // parts NewRedactor must remove whole from a credential's field
	NoRedactor  string            // why the Redactor's row is left out
}

// shape starts a row whose every kind of field wants w.
func shape(name, value string, w Want) Row {
	r := Row{Name: name, Value: value, Want: map[string]Want{}, Cannot: map[string]string{}}
	for _, k := range []string{InCredential, InHeader, InCookie, InSettings, InUserinfo, InQuery} {
		r.Want[k] = w
	}
	return r
}

// in sets what the named kinds of field want.
func (r Row) in(w Want, kinds ...string) Row {
	for _, k := range kinds {
		r.Want[k] = w
	}
	return r
}

// redacts lists the parts NewRedactor must remove from a credential's field, besides its whole.
func (r Row) redacts(forms ...string) Row {
	r.Forms = forms
	return r
}

// not says why a kind of field cannot hold this shape.
func (r Row) not(why string, kinds ...string) Row {
	for _, k := range kinds {
		r.Cannot[k] = why
		delete(r.Want, k)
	}
	return r
}

// notRedactor says why the Redactor's test leaves this shape out.
func (r Row) notRedactor(why string) Row {
	r.NoRedactor = why
	return r
}

// The kinds of field that hold a value as it is, and those that read a whole value out of a URL.
var (
	shapeSide = []string{InHeader, InCookie, InSettings, InUserinfo, InQuery}
	shapeURL  = []string{InUserinfo, InQuery}
	// shapeParsed are the kinds that read a value's structure: all but a URL's values, which are
	// secret whole.
	shapeParsed = []string{InCredential, InHeader, InCookie, InSettings}
	// shapeNotCookie are those that count a single name=value whole.
	shapeNotCookie = []string{InCredential, InHeader, InSettings, InUserinfo, InQuery}
)

func b64(s string) string    { return base64.StdEncoding.EncodeToString([]byte(s)) }
func b64raw(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) }
func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// Rows returns every input shape the code's branches read. See the comment on each group.
func Rows() []Row {
	tok, oth, long := Token, Other, Long
	random := string([]byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff,
		0xf0, 0xe1, 0xd2, 0xc3, 0xb4, 0xa5, 0x96, 0x87, 0x78, 0x69, 0x5a, 0x4b, 0x3c, 0x2d, 0x1e, 0x0f, 0xfb})
	const (
		vendor   = "sk-proj-" + Token + Other                     // 32 bytes: a long token behind a public prefix
		text44   = "c2VydmljZS1hY2NvdW50LTE6cDdRdzJFcjVhYWFhYWE=" // base64 of "service-account-1:p7Qw2Er5aaaaaaa"
		longPair = "QUJDREVGR0hJSktMTU5PUFI=U1RVVldYWVphYmNk"     // 40 bytes, one run with "=" inside
		pemLine  = "FAKEKEYBODY" + Token + Other + Token
	)
	pem := "-----BEGIN PRIVATE KEY-----\n" + pemLine + "\n-----END PRIVATE KEY-----\n"
	basicPass := b64("fake-user:" + tok)
	basicShort := b64("fake-user:Qx7Rv2")
	basicEmpty := b64(tok + ":")
	basicNone := b64(tok)
	basicColon := b64("fake-user:Qx7Rv2:Lm9Tz4Wp")
	basicLatin := b64("fake-user:Qx7R\xe9v2Lm9Tz4")
	basicSafe := b64url("fake-user:Qx7>Rv2?Lm9Tz4")

	return []Row{
		// Plain tokens: short, under the side minimum, long, and with a vendor's public prefix.
		shape("plain token", tok, at4(tok)).redacts(),
		shape("token under 8 bytes", "Qx7Rv2", keep("Qx7Rv2")).in(at4("Qx7Rv2"), InCredential),
		shape("long token", long, at8(long)),
		shape("vendor key, long", vendor, at8(tok+oth).keep("project", "sk-proj-")),
		shape("vendor key, under 16 bytes after its prefix", "sk-proj-Qx7R", at4("sk-proj-Qx7R")),

		// Base64: padded, unpadded, URL-safe, of text and of random bytes.
		shape("one = of padding", "RmFrZVRva2VuMTIzNDU=", at4("RmFrZVRva2VuMTIzNDU=")),
		shape("== of padding", "RmFrZVRva2VuMTIzNA==", at4("RmFrZVRva2VuMTIzNA==")),
		shape("unpadded base64 of text", "RmFrZVRva2VuMTIzNA", at4("RmFrZVRva2VuMTIzNA")),
		shape("URL-safe base64 of text", b64url("fake>>token??"), at4(b64url("fake>>token??"))),
		shape("long base64 of text", text44, at4(text44)),
		shape("long URL-safe base64 of text", b64url("fake-service-user:fake>>pass??word"), at4(b64url("fake-service-user:fake>>pass??word"))),
		shape("base64 of random bytes", b64(random), at8(b64(random))).in(at8(b64(random)).asWritten(), InQuery),
		shape("unpadded base64 of random bytes", b64raw(random), at8(b64raw(random))).in(at8(b64raw(random)).asWritten(), InQuery),
		shape("URL-safe base64 of random bytes", b64url(random), at8(b64url(random))),

		// name=value, and what looks like one.
		shape("name=value", "gwname="+tok, at4("gwname="+tok)).
			in(at4(tok).keep("gwname"), InCookie).redacts(tok),
		shape("a=b=c", "gwname=innerpart="+tok, at4("gwname=innerpart="+tok)).
			in(at4("innerpart="+tok).keep("gwname"), InCookie).redacts("innerpart="+tok, tok),
		shape("=abc", "="+tok, at4(tok)),
		shape("abc=def==", "gwname=Qx7Rv2Lm9Tz==", at4("gwname=Qx7Rv2Lm9Tz==")).
			in(at4("Qx7Rv2Lm9Tz==").keep("gwname"), InCookie).redacts("Qx7Rv2Lm9Tz=="),
		shape("empty value", tok+"=", at4(tok+"=")),
		// A run of only "=" may count or not: nothing is asserted but that it is no part.
		shape("only =", "========", Want{}),
		shape("name=value of one long run", longPair, at8(longPair).and("U1RVVldYWVphYmNk")).
			in(at4("U1RVVldYWVphYmNk").keep("QUJDREVG"), InCookie).
			in(at4(longPair), InQuery).redacts("U1RVVldYWVphYmNk"),
		shape("name=value with a final ;", "gwname="+tok+";", at4("gwname="+tok)).
			in(at4(tok).keep("gwname"), InCookie),
		shape("quoted name=value", `gwname="`+tok+`"`, at4("gwname=", tok)).
			in(at4(tok).keep("gwname"), InCookie).redacts(tok),

		// Cookie lists. A real one counts by its values, in every field that reads structure.
		shape("cookie list", "sid="+tok+"; theme="+oth, at4(tok, oth)).
			in(at4(tok, oth).keep("theme"), shapeParsed...).redacts(tok, oth),
		shape("cookie list with a short value", "sid="+tok+"; theme=dark", at4(tok)).
			in(at4(tok, "dark").keep("theme"), InCredential).
			in(at4(tok).keep("theme", "dark"), InHeader, InCookie, InSettings),
		shape("quoted cookie list", `sid="`+tok+`"; csrf="`+oth+`"`, at4(tok, oth)).
			in(at4(tok, oth).keep("csrf"), shapeParsed...).redacts(tok, oth),
		// What is no pair among pairs: the list is no real one, so the value counts whole too.
		shape("empty pair among real ones", "sid="+tok+"; other=", at4(tok, "other=")),
		shape("padded token among real pairs", "sid="+tok+"; "+oth+"==", at4(tok, oth+"==")),
		shape("token among real pairs", tok+"; sid="+oth, at4(tok, oth)),
		shape("=abc among real pairs", "sid="+tok+"; ="+oth, at4(tok, oth)),
		shape("quoted value holding ;", `a="`+tok+`;`+oth+`"; sid=Gh5Jc0Bv9Xz2`, at4(tok, oth, "Gh5Jc0Bv9Xz2")),
		shape("non-ASCII value among real pairs", "sid="+tok+"; x=Päss"+oth, at4(tok, oth)),
		// A value is a secret of its own, whatever it holds.
		shape("cookie value holding =", "sid=Fk3="+tok+"; x="+oth, at4("Fk3="+tok, oth)).
			in(at4("Fk3="+tok, oth).keep("sid"), shapeParsed...).redacts("Fk3="+tok, oth),
		shape("cookie value holding :", "sid=Fk3Rv2:"+tok+"; x="+oth, at4("Fk3Rv2:"+tok, oth)).
			in(at4("Fk3Rv2:"+tok, oth).keep("sid"), shapeParsed...).redacts("Fk3Rv2:"+tok, oth),

		// Bearer, in any case, with a short, missing or long token.
		shape("Bearer", "Bearer "+tok, at4(tok)).
			in(at4(tok).keep("Bearer"), shapeParsed...).redacts(tok),
		shape("bEaReR and a tab", "bEaReR\t"+tok, at4(tok)).
			in(at4(tok).keep("bEaReR"), shapeParsed...).redacts(tok),
		shape("Bearer, short token", "Bearer Qx7R", at4("Qx7R", "Bearer")).
			in(at4("Qx7R").keep("Bearer"), InCredential),
		shape("Bearer alone", "Bearer", keep("Bearer")).in(at4("Bearer"), InCredential),
		shape("Bearer, long token", "Bearer "+long, at4(long)).
			in(at8(long).keep("Bearer"), shapeParsed...).redacts(long),

		// Basic, with a password that is there, short, empty, missing, holds ":", is not UTF-8,
		// or is encoded URL-safe.
		shape("Basic", "Basic "+basicPass, at4(basicPass, tok).keep("fake-user")).
			in(at4(basicPass, tok).keep("fake-user", "Basic"), shapeParsed...).redacts(basicPass, tok),
		shape("bAsIc", "bAsIc "+basicPass, at4(basicPass, tok).keep("fake-user")).
			in(at4(basicPass, tok).keep("fake-user", "bAsIc"), shapeParsed...).redacts(basicPass, tok),
		shape("Basic, short password", "Basic "+basicShort, at4(basicShort).keep("fake-user", "Qx7Rv2")).
			in(at4(basicShort, "Qx7Rv2").keep("fake-user"), InCredential),
		shape("Basic, empty password", "Basic "+basicEmpty, at4(basicEmpty, tok)).redacts(basicEmpty, tok),
		shape("Basic, no colon", "Basic "+basicNone, at4(basicNone, tok)).redacts(basicNone, tok),
		shape("Basic, password holding :", "Basic "+basicColon, at4(basicColon, "Qx7Rv2:Lm9Tz4Wp").keep("fake-user")).
			redacts(basicColon, "Qx7Rv2:Lm9Tz4Wp"),
		shape("Basic, Latin-1 password", "Basic "+basicLatin, at4(basicLatin, "Qx7R", "v2Lm9Tz4").keep("fake-user")).
			redacts(basicLatin, "Qx7R\xe9v2Lm9Tz4"),
		shape("Basic, URL-safe", "Basic "+basicSafe, at4(basicSafe, "Qx7>Rv2?Lm9Tz4").keep("fake-user")).
			redacts(basicSafe, "Qx7>Rv2?Lm9Tz4"),

		// Another scheme is no structure: the value counts whole.
		shape("Token scheme", "Token "+tok, at4(tok, "Token")),

		// A public prefix and a header line.
		shape("prefix$token", "devin-session-token$"+tok, at4(tok)).
			in(at4(tok).keep("devin", "session"), shapeParsed...).redacts(tok),
		shape("prefix:token", "dca:"+tok, at4(tok)).
			in(at4(tok).keep("dca"), shapeParsed...).redacts(tok),
		shape("header line", "X-Goog-Api-Key: "+tok, at4(tok)).
			in(at4(tok).keep("Goog"), shapeParsed...).redacts(tok),
		shape("prefix, short token", "dca:Qx7R", at4("Qx7R", "dca:")).
			in(at4("Qx7R").keep("dca"), InCredential),
		// A first half that has a prefix's shape is public by the rule; a URL's value is not read.
		shape("key:secret", "AKIDFAKEEXAMPLE:"+tok, at4("AKIDFAKEEXAMPLE", tok)).
			in(at4(tok).keep("AKIDFAKEEXAMPLE"), shapeParsed...).redacts(tok),
		shape("pair behind a prefix", "dca:gwname="+tok, at4("gwname="+tok)).
			in(at4("gwname="+tok).keep("dca"), InCredential, InHeader, InSettings).
			in(at4(tok).keep("gwname", "dca"), InCookie).redacts("gwname="+tok, tok),
		shape("pair behind Bearer", "Bearer gwname="+tok, at4("gwname="+tok)).
			in(at4("gwname="+tok).keep("Bearer"), InCredential, InHeader, InSettings).
			in(at4(tok).keep("gwname", "Bearer"), InCookie).redacts("gwname="+tok, tok),

		// Nesting, up to and past the depth SecretParts splits.
		shape("four schemes", "Bearer Bearer Bearer Bearer "+tok, at4(tok)).
			in(at4(tok).keep("Bearer"), shapeParsed...).redacts(tok),
		shape("five schemes", "Bearer Bearer Bearer Bearer Bearer "+tok, at4(tok)).redacts(tok),
		shape("nine schemes", strings.Repeat("Bearer ", 9)+tok, at4(tok)).redacts(tok),
		shape("six prefixes", "aa:bb:cc:dd:ee:ff:"+tok, at4(tok)).redacts(tok),
		shape("six pairs", "aa=bb=cc=dd=ee=ff="+tok, at4(tok)).redacts(tok),

		// JSON, whitespace, line breaks and PEM keys.
		shape("JSON value", `{"api_key":"`+tok+`","user":"svc-fake"}`, at4(tok, "svc-fake")),
		shape("whitespace around", "  "+tok+"\n", at4(tok)),
		shape("long token and a newline", long+"\n", at8(long)).in(at4(long), InQuery),
		shape("two lines", tok+"\n"+oth, at4(tok, oth)),
		shape("PEM private key", pem, at8(pemLine)).in(at4(pemLine), InQuery),

		// A URL as the value. Settings read it by its secrets; a field that holds it counts it.
		shape("URL with secrets", "https://gw-user:"+tok+"@gw.example/v1?token="+oth, at4(tok, oth)).
			in(at4(tok, oth).keep("https", "example"), InSettings),

		// Text outside the token characters.
		shape("non-ASCII", "пароль-"+tok, at4("пароль-"+tok)),
		shape("punctuation", "P@ss!w0rd#Fake1", at4("P@ss!w0rd#Fake1")),
		shape("not UTF-8", "Qx7R\xe9v2Lm9Tz4", at4("Qx7R", "v2Lm9Tz4")).
			not("settings are JSON, which cannot carry a string that is not UTF-8", InSettings).
			notRedactor("NewRedactor reads a credential through JSON, which turns the byte into U+FFFD"),
	}
}

// Cuts returns the cuts of run that must go at limit n: its start, its middle, its end.
func Cuts(run string, n int) []string {
	if len(run) <= n {
		return []string{run}
	}
	return []string{run[:n], run[(len(run)-n)/2 : (len(run)-n)/2+n], run[len(run)-n:]}
}

// URLRow is one URL and which of its words count.
type URLRow struct {
	Name, URL string
	Gone      []string // runs whose first 4 bytes must leave an error
	Stay      []string // words that stay in an error
	Whole     []string // values core's Redactor must remove whole
}

// URLRows returns the shapes of a URL's secret parts: its user name, its password and the
// parameters named like a secret, in the query or the fragment, split on "&" or ";".
func URLRows() []URLRow {
	const tok, oth = Token, Other
	return []URLRow{
		{"user and password", "https://" + tok + ":" + oth + "@gw.example/v1", []string{tok, oth}, []string{"https", "example"}, []string{tok, oth}},
		{"percent-encoded password", "https://x:Qx7R%40v2Lm9@gw.example/v1", []string{"Qx7R", "v2Lm9"}, []string{"example"}, []string{"Qx7R@v2Lm9"}},
		{"password holding :", "https://x:Fk3Rv2:" + tok + "@gw.example/", []string{"Fk3Rv2", tok}, []string{"example"}, []string{"Fk3Rv2:" + tok}},
		{"password holding =", "https://x:Fk3Rv2=" + tok + "@gw.example/", []string{"Fk3Rv2", tok}, []string{"example"}, []string{"Fk3Rv2=" + tok}},
		{"secret query value", "https://gw.example/v1?token=" + tok + "&model=gpt-fake", []string{tok}, []string{"model", "gpt-fake", "example"}, []string{tok}},
		{"query value holding =", "https://gw.example/v1?token=Fk3Rv2=" + tok, []string{"Fk3Rv2", tok}, []string{"example"}, []string{"Fk3Rv2=" + tok}},
		{"query split on ;", "https://gw.example/v1?model=m;token=" + tok, []string{tok}, []string{"model", "example"}, []string{tok}},
		{"query split on ; before &", "https://gw.example/v1?model=m;token=" + tok + "&region=eu", []string{tok}, []string{"model", "region", "example"}, []string{tok}},
		{"fragment of an OAuth redirect", "https://gw.example/cb#access_token=" + tok + "&state=fakestate", []string{tok}, []string{"fakestate", "example"}, []string{tok}},
		{"fragment split on ;", "https://gw.example/cb#state=fakestate;access_token=" + tok, []string{tok}, []string{"fakestate", "example"}, []string{tok}},
		{"fragment of a page's route", "https://gw.example/#/cb?code=" + tok + "&state=fakestate", []string{tok}, []string{"fakestate", "example"}, []string{tok}},
		{"secret value in the query and the fragment", "https://gw.example/cb?code=" + tok + "#id_token=" + oth, []string{tok, oth}, []string{"example"}, []string{tok, oth}},
		{"other parameters only", "https://gw.example/v1?model=gpt-fake#section", nil, []string{"model", "gpt-fake", "section", "example"}, nil},
		{"fragment with no parameter", "https://gw.example/docs#section", nil, []string{"docs", "section", "example"}, nil},
	}
}

// NumberRow is account settings holding a number.
type NumberRow struct {
	Name, Settings string
	Gone           []string // text that must leave an error
	Stay           []string // text that stays in one
}

// NumberRows returns the shapes of a number in settings: below a secret key it counts with its
// exact digits, from 8 bytes.
func NumberRows() []NumberRow {
	return []NumberRow{
		{"number below a secret key", `{"api_key":1234567890}`, []string{"1234", "7890"}, []string{"123"}},
		{"digits a float64 cannot hold", `{"api_key":12345678901234567891}`, []string{"56789012", "7891"}, nil},
		{"number with a fraction", `{"api_key":1234567.125}`, []string{"1234", "7.125"}, nil},
		{"number deep below a secret key", `{"client_secret":{"rotate":[9876543210]}}`, []string{"9876", "3210"}, nil},
		{"number under 8 bytes", `{"api_key":1234567}`, nil, []string{"1234567"}},
		{"number below a plain key", `{"retries":1234567890}`, nil, []string{"1234567890"}},
	}
}
