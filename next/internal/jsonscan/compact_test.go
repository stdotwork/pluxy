package jsonscan

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzAppendCompact checks AppendCompact against json.Compact: the same text for valid input, and
// nothing appended for input that is not valid.
func FuzzAppendCompact(f *testing.F) {
	for _, s := range []string{
		``, ` `, `1`, ` 1 `, "\t[1 , 2]\n", `{"a" : "b c" , "d":[ ] }`, `"a <b> &   "`, "\"a b\xff\"",
		`{"a":1,}`, `[1 2]`, `{"a":"x`, `"a\"b" "c"`, `{"a":"\ud800\u00zz"}`, "\"a\nb\"", ` {} {} `, `nul`, `-0.5e+3 `,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		var want bytes.Buffer
		wantErr := json.Compact(&want, src)
		got, ok := AppendCompact([]byte("pre"), src)
		if ok != (wantErr == nil) {
			t.Fatalf("%q: ok %v, json.Compact error %v", src, ok, wantErr)
		}
		if ok && string(got) != "pre"+want.String() {
			t.Fatalf("%q: got %q, want %q", src, got, "pre"+want.String())
		}
		if !ok && string(got) != "pre" {
			t.Fatalf("%q: appended %q to dst for invalid text", src, got)
		}
	})
}
