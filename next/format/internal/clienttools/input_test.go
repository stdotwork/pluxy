package clienttools

import "testing"

func TestCustomInput(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
		ok               bool
	}{
		{"plain", `{"input":"abc"}`, "abc", true},
		{"white space", " {\n \"input\" : \"a\" }\n", "a", true},
		{"escapes", `{"input":"a\n\t\"\\\/é"}`, "a\n\t\"\\/é", true},
		{"surrogate pair", `{"input":"😀"}`, "😀", true},
		{"cut off", `{"input":"abc`, "abc", true},
		{"empty", ``, "", true},
		{"empty object", ` {} `, "", true},
		{"lone low surrogate", `{"input":"\ude00"}`, `{"input":"\ude00"}`, false},
		{"high surrogate without low", `{"input":"\ud83dx"}`, `{"input":"\ud83dx"}`, false},
		{"bad escape", `{"input":"\q"}`, `{"input":"\q"}`, false},
		{"raw control character", "{\"input\":\"a\nb\"}", "{\"input\":\"a\nb\"}", false},
		{"other field", `{"text":"a"}`, `{"text":"a"}`, false},
		{"field after", `{"input":"a","b":1}`, `{"input":"a","b":1}`, false},
		{"text after", `{"input":"a"} x`, `{"input":"a"} x`, false},
		{"two closes", `{"input":"a"}}`, `{"input":"a"}}`, false},
		{"not a string", `{"input":1}`, `{"input":1}`, false},
		{"invalid UTF-8", "{\"input\":\"\xff\"}", "{\"input\":\"\xff\"}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CustomInput(tc.args)
			if got != tc.want || ok != tc.ok {
				t.Errorf("CustomInput(%q) = %q, %v, want %q, %v", tc.args, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Every way of cutting the arguments in two gives the same text.
func TestInputDecoderFragments(t *testing.T) {
	const args = `{ "input" : "a\néé😀z" }`
	const want = "a\néé😀z"
	for i := 0; i <= len(args); i++ {
		var d InputDecoder
		got := d.Push(args[:i]) + d.Push(args[i:])
		if got != want || d.failed {
			t.Errorf("cut at %d: %q, failed %v, want %q", i, got, d.failed, want)
		}
	}
}
