package protocol

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// Made-up values. Nothing here is or was a real credential.
const (
	shapeToken = "Qx7Rv2Lm9Tz4"                      // 12 bytes
	shapeOther = "Wp8Ys3Nk6Df1"                      // 12 bytes
	shapeLong  = "Ab3Cd5Ef7Gh9Ij1Kl3Mn5Op7Qr9St1Uv2" // 33 bytes: one long token
)

// shapeWant says, written by hand, what one kind of field must do with one shape of value.
type shapeWant struct {
	at4, at8 []string // runs of real secret, counted from 4 bytes and from 8
	stay     []string // public words that stay in an error
	// written says a URL's query value also counts as written, with its escapes, at 4 bytes, so
	// whether a 4 to 7 byte cut of it stays depends on where the escapes fall.
	written bool
}

func at4(runs ...string) shapeWant { return shapeWant{at4: runs} }
func at8(runs ...string) shapeWant { return shapeWant{at8: runs} }
func keep(words ...string) shapeWant {
	return shapeWant{stay: words}
}

func (w shapeWant) keep(words ...string) shapeWant {
	w.stay = append(slices.Clone(w.stay), words...)
	return w
}

func (w shapeWant) asWritten() shapeWant {
	w.written = true
	return w
}

func (w shapeWant) and(runs ...string) shapeWant {
	w.at4 = append(slices.Clone(w.at4), runs...)
	return w
}

// The kinds of field a shape is crossed with. Fields of one kind follow one rule.
const (
	inCredential = "credential" // api_key, access_token, refresh_token, a login's secret input
	inHeader     = "header"     // a secret header other than Cookie
	inCookie     = "cookie"     // the Cookie header
	inSettings   = "settings"   // a string below a secret settings key
	inUserinfo   = "userinfo"   // a URL's user name or password: proxy, settings URL, login URL
	inQuery      = "query"      // a URL's secret query value: settings URL, login URL
)

// shapeRow is one input shape and what each kind of field must do with it.
type shapeRow struct {
	name, value string
	want        map[string]shapeWant
	cannot      map[string]string // kind of field the shape cannot reach, and why
	forms       []string          // parts NewRedactor must remove whole from a credential's field
	noRedactor  string            // why the Redactor's row is left out
}

// shape starts a row whose every kind of field wants w.
func shape(name, value string, w shapeWant) shapeRow {
	r := shapeRow{name: name, value: value, want: map[string]shapeWant{}, cannot: map[string]string{}}
	for _, k := range []string{inCredential, inHeader, inCookie, inSettings, inUserinfo, inQuery} {
		r.want[k] = w
	}
	return r
}

// in sets what the named kinds of field want.
func (r shapeRow) in(w shapeWant, kinds ...string) shapeRow {
	for _, k := range kinds {
		r.want[k] = w
	}
	return r
}

// redacts lists the parts NewRedactor must remove from a credential's field, besides its whole.
func (r shapeRow) redacts(forms ...string) shapeRow {
	r.forms = forms
	return r
}

// not says why a kind of field cannot hold this shape.
func (r shapeRow) not(why string, kinds ...string) shapeRow {
	for _, k := range kinds {
		r.cannot[k] = why
		delete(r.want, k)
	}
	return r
}

// notRedactor says why the Redactor's test leaves this shape out.
func (r shapeRow) notRedactor(why string) shapeRow {
	r.noRedactor = why
	return r
}

// The kinds of field that hold a value as it is, and those that read a whole value out of a URL.
var (
	shapeSide = []string{inHeader, inCookie, inSettings, inUserinfo, inQuery}
	shapeURL  = []string{inUserinfo, inQuery}
	// shapeParsed are the kinds that read a value's structure: all but a URL's values, which are
	// secret whole.
	shapeParsed = []string{inCredential, inHeader, inCookie, inSettings}
	// shapeNotCookie are those that count a single name=value whole.
	shapeNotCookie = []string{inCredential, inHeader, inSettings, inUserinfo, inQuery}
)

func b64(s string) string    { return base64.StdEncoding.EncodeToString([]byte(s)) }
func b64raw(s string) string { return base64.RawStdEncoding.EncodeToString([]byte(s)) }
func b64url(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// shapeRows is every input shape the code's branches read. See the comment on each group.
func shapeRows() []shapeRow {
	tok, oth, long := shapeToken, shapeOther, shapeLong
	random := string([]byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0xf8, 0xf9, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff,
		0xf0, 0xe1, 0xd2, 0xc3, 0xb4, 0xa5, 0x96, 0x87, 0x78, 0x69, 0x5a, 0x4b, 0x3c, 0x2d, 0x1e, 0x0f, 0xfb})
	const (
		vendor   = "sk-proj-" + shapeToken + shapeOther           // 32 bytes: a long token behind a public prefix
		text44   = "c2VydmljZS1hY2NvdW50LTE6cDdRdzJFcjVhYWFhYWE=" // base64 of "service-account-1:p7Qw2Er5aaaaaaa"
		longPair = "QUJDREVGR0hJSktMTU5PUFI=U1RVVldYWVphYmNk"     // 40 bytes, one run with "=" inside
		pemLine  = "FAKEKEYBODY" + shapeToken + shapeOther + shapeToken
	)
	pem := "-----BEGIN PRIVATE KEY-----\n" + pemLine + "\n-----END PRIVATE KEY-----\n"
	basicPass := b64("fake-user:" + tok)
	basicShort := b64("fake-user:Qx7Rv2")
	basicEmpty := b64(tok + ":")
	basicNone := b64(tok)
	basicColon := b64("fake-user:Qx7Rv2:Lm9Tz4Wp")
	basicLatin := b64("fake-user:Qx7R\xe9v2Lm9Tz4")
	basicSafe := b64url("fake-user:Qx7>Rv2?Lm9Tz4")

	return []shapeRow{
		// Plain tokens: short, under the side minimum, long, and with a vendor's public prefix.
		shape("plain token", tok, at4(tok)).redacts(),
		shape("token under 8 bytes", "Qx7Rv2", keep("Qx7Rv2")).in(at4("Qx7Rv2"), inCredential),
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
		shape("base64 of random bytes", b64(random), at8(b64(random))).in(at8(b64(random)).asWritten(), inQuery),
		shape("unpadded base64 of random bytes", b64raw(random), at8(b64raw(random))).in(at8(b64raw(random)).asWritten(), inQuery),
		shape("URL-safe base64 of random bytes", b64url(random), at8(b64url(random))),

		// name=value, and what looks like one.
		shape("name=value", "gwname="+tok, at4("gwname="+tok)).
			in(at4(tok).keep("gwname"), inCookie).redacts(tok),
		shape("a=b=c", "gwname=innerpart="+tok, at4("gwname=innerpart="+tok)).
			in(at4("innerpart="+tok).keep("gwname"), inCookie).redacts("innerpart="+tok, tok),
		shape("=abc", "="+tok, at4(tok)),
		shape("abc=def==", "gwname=Qx7Rv2Lm9Tz==", at4("gwname=Qx7Rv2Lm9Tz==")).
			in(at4("Qx7Rv2Lm9Tz==").keep("gwname"), inCookie).redacts("Qx7Rv2Lm9Tz=="),
		shape("empty value", tok+"=", at4(tok+"=")),
		// A run of only "=" may count or not: nothing is asserted but that it is no part.
		shape("only =", "========", shapeWant{}),
		shape("name=value of one long run", longPair, at8(longPair).and("U1RVVldYWVphYmNk")).
			in(at4("U1RVVldYWVphYmNk").keep("QUJDREVG"), inCookie).
			in(at4(longPair), inQuery).redacts("U1RVVldYWVphYmNk"),
		shape("name=value with a final ;", "gwname="+tok+";", at4("gwname="+tok)).
			in(at4(tok).keep("gwname"), inCookie),
		shape("quoted name=value", `gwname="`+tok+`"`, at4("gwname=", tok)).
			in(at4(tok).keep("gwname"), inCookie).redacts(tok),

		// Cookie lists. A real one counts by its values, in every field that reads structure.
		shape("cookie list", "sid="+tok+"; theme="+oth, at4(tok, oth)).
			in(at4(tok, oth).keep("theme"), shapeParsed...).redacts(tok, oth),
		shape("cookie list with a short value", "sid="+tok+"; theme=dark", at4(tok)).
			in(at4(tok, "dark").keep("theme"), inCredential).
			in(at4(tok).keep("theme", "dark"), inHeader, inCookie, inSettings),
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
			in(at4("Qx7R").keep("Bearer"), inCredential),
		shape("Bearer alone", "Bearer", keep("Bearer")).in(at4("Bearer"), inCredential),
		shape("Bearer, long token", "Bearer "+long, at4(long)).
			in(at8(long).keep("Bearer"), shapeParsed...).redacts(long),

		// Basic, with a password that is there, short, empty, missing, holds ":", is not UTF-8,
		// or is encoded URL-safe.
		shape("Basic", "Basic "+basicPass, at4(basicPass, tok).keep("fake-user")).
			in(at4(basicPass, tok).keep("fake-user", "Basic"), shapeParsed...).redacts(basicPass, tok),
		shape("bAsIc", "bAsIc "+basicPass, at4(basicPass, tok).keep("fake-user")).
			in(at4(basicPass, tok).keep("fake-user", "bAsIc"), shapeParsed...).redacts(basicPass, tok),
		shape("Basic, short password", "Basic "+basicShort, at4(basicShort).keep("fake-user", "Qx7Rv2")).
			in(at4(basicShort, "Qx7Rv2").keep("fake-user"), inCredential),
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
			in(at4("Qx7R").keep("dca"), inCredential),
		// A first half that has a prefix's shape is public by the rule; a URL's value is not read.
		shape("key:secret", "AKIDFAKEEXAMPLE:"+tok, at4("AKIDFAKEEXAMPLE", tok)).
			in(at4(tok).keep("AKIDFAKEEXAMPLE"), shapeParsed...).redacts(tok),
		shape("pair behind a prefix", "dca:gwname="+tok, at4("gwname="+tok)).
			in(at4("gwname="+tok).keep("dca"), inCredential, inHeader, inSettings).
			in(at4(tok).keep("gwname", "dca"), inCookie).redacts("gwname="+tok, tok),
		shape("pair behind Bearer", "Bearer gwname="+tok, at4("gwname="+tok)).
			in(at4("gwname="+tok).keep("Bearer"), inCredential, inHeader, inSettings).
			in(at4(tok).keep("gwname", "Bearer"), inCookie).redacts("gwname="+tok, tok),

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
		shape("long token and a newline", long+"\n", at8(long)).in(at4(long), inQuery),
		shape("two lines", tok+"\n"+oth, at4(tok, oth)),
		shape("PEM private key", pem, at8(pemLine)).in(at4(pemLine), inQuery),

		// A URL as the value. Settings read it by its secrets; a field that holds it counts it.
		shape("URL with secrets", "https://gw-user:"+tok+"@gw.example/v1?token="+oth, at4(tok, oth)).
			in(at4(tok, oth).keep("https", "example"), inSettings),

		// Text outside the token characters.
		shape("non-ASCII", "пароль-"+tok, at4("пароль-"+tok)),
		shape("punctuation", "P@ss!w0rd#Fake1", at4("P@ss!w0rd#Fake1")),
		shape("not UTF-8", "Qx7R\xe9v2Lm9Tz4", at4("Qx7R", "v2Lm9Tz4")).
			not("settings are JSON, which cannot carry a string that is not UTF-8", inSettings).
			notRedactor("NewRedactor reads a credential through JSON, which turns the byte into U+FFFD"),
	}
}

// shapeFields are the fields of an account and a login, each with its kind and how a value gets
// there.
func shapeFields() []struct {
	name, kind string
	secrets    func(v string) []string
} {
	type field = struct {
		name, kind string
		secrets    func(v string) []string
	}
	cred := func(c Credential) []string { return AccountSecrets(Account{Credential: &c}) }
	return []field{
		{"api_key", inCredential, func(v string) []string { return cred(Credential{APIKey: v}) }},
		{"access_token", inCredential, func(v string) []string { return cred(Credential{AccessToken: v}) }},
		{"refresh_token", inCredential, func(v string) []string { return cred(Credential{RefreshToken: v}) }},
		// internal/host/credentials.go passes a login's secret input that is no JSON object so.
		{"login secret input", inCredential, func(v string) []string { return cred(Credential{APIKey: v}) }},
		{"secret header", inHeader, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"X-Api-Key": v}})
		}},
		{"Cookie header", inCookie, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"Cookie": v}})
		}},
		{"cookie header, lower case", inCookie, func(v string) []string {
			return AccountSecrets(Account{Headers: map[string]string{"cookie": v}})
		}},
		{"settings secret", inSettings, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"api_key": v})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings secret, core's rules", inSettings, func(v string) []string {
			return AccountSecretsWith(Account{}, []string{v})
		}},
		{"proxy user", inUserinfo, func(v string) []string {
			u := url.URL{Scheme: "http", User: url.UserPassword(v, "x"), Host: "proxy.example:3128"}
			return AccountSecrets(Account{ProxyURL: u.String()})
		}},
		{"proxy password", inUserinfo, func(v string) []string {
			u := url.URL{Scheme: "http", User: url.UserPassword("x", v), Host: "proxy.example:3128"}
			return AccountSecrets(Account{ProxyURL: u.String()})
		}},
		{"settings URL password", inUserinfo, func(v string) []string {
			u := url.URL{Scheme: "https", User: url.UserPassword("x", v), Host: "gw.example", Path: "/v1"}
			b, _ := json.Marshal(map[string]string{"base_url": u.String()})
			return AccountSecrets(Account{Settings: b})
		}},
		{"settings URL query", inQuery, func(v string) []string {
			b, _ := json.Marshal(map[string]string{"base_url": "https://gw.example/v1?token=" + url.QueryEscape(v)})
			return AccountSecrets(Account{Settings: b})
		}},
		// internal/host/credentials.go passes a login's input that is a URL so.
		{"login URL password", inUserinfo, func(v string) []string {
			u := url.URL{Scheme: "https", User: url.UserPassword("x", v), Host: "gw.example", Path: "/cb"}
			return AccountSecretsWith(Account{}, []string{u.String()})
		}},
		{"login URL query", inQuery, func(v string) []string {
			return AccountSecretsWith(Account{}, []string{"https://gw.example/cb?code=" + url.QueryEscape(v)})
		}},
	}
}

// cutsOf returns the cuts of run that must go at limit n: its start, its middle, its end.
func cutsOf(run string, n int) []string {
	if len(run) <= n {
		return []string{run}
	}
	return []string{run[:n], run[(len(run)-n)/2 : (len(run)-n)/2+n], run[len(run)-n:]}
}

// Every shape against every kind of secret field: what is real secret leaves an error from the cut
// length that applies, a cut just below it stays, and so do the public words. The expected values
// are written by hand in shapeRows.
func TestSecretShapes(t *testing.T) {
	removed := func(text string, secrets []string) bool {
		return RemovePieces("upstream said "+text+" here", secrets...) == "upstream said "+Redacted+" here"
	}
	kept := func(text string, secrets []string) bool {
		in := "upstream said " + text + " here"
		return RemovePieces(in, secrets...) == in
	}
	for _, row := range shapeRows() {
		for _, f := range shapeFields() {
			t.Run(row.name+"/"+f.name, func(t *testing.T) {
				want, ok := row.want[f.kind]
				if !ok {
					if row.cannot[f.kind] == "" {
						t.Fatalf("the table says nothing of %s", f.kind)
					}
					t.Skip(row.cannot[f.kind])
				}
				secrets := f.secrets(row.value)
				for _, limit := range []struct {
					runs []string
					n    int
				}{{want.at4, minPiece}, {want.at8, longPiece}} {
					for _, run := range limit.runs {
						for _, cut := range cutsOf(run, limit.n) {
							if !removed(cut, secrets) {
								t.Errorf("%d-byte cut %q of %q stayed in an error; secrets %q", len(cut), cut, row.value, secrets)
							}
						}
						if below := run[:limit.n-1]; !want.written && !kept(below, secrets) {
							t.Errorf("%q, under the %d-byte limit, was removed; secrets %q", below, limit.n, secrets)
						}
					}
				}
				for _, word := range want.stay {
					if !kept(word, secrets) {
						t.Errorf("public word %q was removed; secrets %q", word, secrets)
					}
				}
				for _, s := range secrets {
					if strings.Trim(s, "=") == "" && strings.Trim(row.value, "=") != "" {
						t.Errorf("padding %q counts as a secret", s)
					}
				}
			})
		}
	}
}

// The Redactor removes a credential's whole value and each part an upstream may echo alone, in
// every credential field, and keeps the public words.
func TestSecretShapesRedactor(t *testing.T) {
	for _, row := range shapeRows() {
		want, ok := row.want[inCredential]
		if !ok || row.noRedactor != "" {
			continue
		}
		for name, c := range map[string]*Credential{
			"api_key":       {Kind: CredentialAPIKey, APIKey: row.value},
			"access_token":  {Kind: CredentialOAuth, AccessToken: row.value},
			"refresh_token": {Kind: CredentialOAuth, RefreshToken: row.value},
		} {
			t.Run(row.name+"/"+name, func(t *testing.T) {
				r := NewRedactor(c)
				if got := r.String("said " + row.value + " here"); strings.Contains(got, row.value) {
					t.Errorf("the whole value stayed: %q", got)
				}
				for _, form := range row.forms {
					if got, wanted := r.String("said "+form+" here"), "said "+Redacted+" here"; got != wanted {
						t.Errorf("part %q: String = %q, want %q", form, got, wanted)
					}
				}
				for _, word := range want.stay {
					in := "said " + word + " here"
					if got := r.String(in); got != in {
						t.Errorf("public word %q: String = %q", word, got)
					}
				}
			})
		}
	}
}

// Which words of a URL count: its user name, password and the values of its parameters named like
// a secret, in the query or the fragment an OAuth redirect carries, split on "&" or ";". The host,
// the scheme and other parameters stay.
func TestSecretShapesURLParts(t *testing.T) {
	const tok, oth = shapeToken, shapeOther
	for _, c := range []struct {
		name, url string
		gone      []string
		stay      []string
	}{
		{"user and password", "https://" + tok + ":" + oth + "@gw.example/v1", []string{tok, oth}, []string{"https", "example"}},
		{"percent-encoded password", "https://x:Qx7R%40v2Lm9@gw.example/v1", []string{"Qx7R", "v2Lm9"}, []string{"example"}},
		{"password holding :", "https://x:Fk3Rv2:" + tok + "@gw.example/", []string{"Fk3Rv2", tok}, []string{"example"}},
		{"password holding =", "https://x:Fk3Rv2=" + tok + "@gw.example/", []string{"Fk3Rv2", tok}, []string{"example"}},
		{"secret query value", "https://gw.example/v1?token=" + tok + "&model=gpt-fake", []string{tok}, []string{"model", "gpt-fake", "example"}},
		{"query value holding =", "https://gw.example/v1?token=Fk3Rv2=" + tok, []string{"Fk3Rv2", tok}, []string{"example"}},
		{"query split on ;", "https://gw.example/v1?model=m;token=" + tok, []string{tok}, []string{"model", "example"}},
		{"fragment of an OAuth redirect", "https://gw.example/cb#access_token=" + tok + "&state=fakestate", []string{tok}, []string{"fakestate", "example"}},
		{"secret value in the query and the fragment", "https://gw.example/cb?code=" + tok + "#id_token=" + oth, []string{tok, oth}, []string{"example"}},
		{"other parameters only", "https://gw.example/v1?model=gpt-fake#section", nil, []string{"model", "gpt-fake", "section", "example"}},
	} {
		for name, secrets := range map[string][]string{
			"settings":  AccountSecrets(Account{Settings: mustJSON(t, map[string]string{"base_url": c.url})}),
			"login URL": AccountSecretsWith(Account{}, []string{c.url}),
		} {
			t.Run(c.name+"/"+name, func(t *testing.T) {
				for _, run := range c.gone {
					in := "upstream said " + run[:minPiece] + " here"
					if got := RemovePieces(in, secrets...); got != "upstream said "+Redacted+" here" {
						t.Errorf("cut %q stayed: %q; secrets %q", run[:minPiece], got, secrets)
					}
				}
				for _, word := range c.stay {
					in := "upstream said " + word + " here"
					if got := RemovePieces(in, secrets...); got != in {
						t.Errorf("word %q: %q; secrets %q", word, got, secrets)
					}
				}
			})
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A run with characters outside the token characters is read whole only for a secret that holds
// such characters: for any other, the error's punctuation still ends a run.
func TestRemovePiecesPunctuationOnlyForSecretsWithIt(t *testing.T) {
	in := "bad key (" + shapeToken[:4] + ") and more!"
	if got, want := RemovePieces(in, shapeToken), "bad key ([redacted]) and more!"; got != want {
		t.Errorf("RemovePieces = %q, want %q", got, want)
	}
	// Quotes and commas end a run even for a secret that holds them, so a JSON error keeps its
	// structure and loses only the word that shares a piece.
	const body = `{"error":"bad","user":"bob"}`
	if got, want := RemovePieces(body, `{"api_key":"Ab3Cd5Ef","user":"svc"}`), `{"error":"bad","[redacted]":"bob"}`; got != want {
		t.Errorf("RemovePieces(%q) = %q, want %q", body, got, want)
	}
	const pw = "P@ss!w0rd#Fake1"
	for in, want := range map[string]string{
		"cut P@ss!w here":        "cut [redacted] here",
		"cut ss!w here":          "cut [redacted] here",
		"cut P@s here":           "cut P@s here",
		"[redacted] stays!":      "[redacted] stays!",
		"nothing of it, friend!": "nothing of it, friend!",
	} {
		if got := RemovePieces(in, pw); got != want {
			t.Errorf("RemovePieces(%q) = %q, want %q", in, got, want)
		}
	}
}
