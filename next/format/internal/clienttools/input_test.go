package clienttools

import "testing"

func TestCustomInput(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
		ok               bool
	}{
		{"plain", `{"input":"abc"}`, "abc", true},
		{"empty text", `{"input":""}`, "", true},
		{"white space", " {\n \"input\" : \"a\" }\n", "a", true},
		{"escapes", `{"input":"a\n\t\"\\\/\u00e9"}`, "a\n\t\"\\/é", true},
		{"surrogate pair", `{"input":"\ud83d\ude00"}`, "😀", true},
		{"cut off in the text", `{"input":"abc`, "", false},
		{"cut off after the text", `{"input":"abc"`, "", false},
		{"cut off in an escape", `{"input":"a\`, "", false},
		{"cut off in the name", `{"inp`, "", false},
		{"nothing", ``, "", false},
		{"empty object", ` {} `, "", false},
		{"lone low surrogate", `{"input":"\ude00"}`, "", false},
		{"high surrogate without low", `{"input":"\ud83dx"}`, "", false},
		{"bad escape", `{"input":"\q"}`, "", false},
		{"raw control character", "{\"input\":\"a\nb\"}", "", false},
		{"other field", `{"text":"a"}`, "", false},
		{"field after", `{"input":"a","b":1}`, "", false},
		{"text after", `{"input":"a"} x`, "", false},
		{"two closes", `{"input":"a"}}`, "", false},
		{"second object", `{"input":"a"}{"input":"b"}`, "", false},
		{"not a string", `{"input":1}`, "", false},
		{"invalid UTF-8", "{\"input\":\"\xff\"}", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CustomInput(tc.args)
			if got != tc.want || ok != tc.ok {
				t.Errorf("CustomInput(%q) = %q, %v, want %q, %v", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestInputDecoderFragments(t *testing.T) {
	const args = `{ "input" : "a\néé😀z" }`
	const want = "a\néé😀z"
	for i := 0; i <= len(args); i++ {
		var d InputDecoder
		got := d.Push(args[:i]) + d.Push(args[i:])
		if got != want || !d.Whole() {
			t.Errorf("cut at %d: %q, whole %v, want %q", i, got, d.Whole(), want)
		}
	}
}
