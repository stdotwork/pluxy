package jsonscan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// cases are JSON texts, valid and not, at the edges of what encoding/json accepts.
var cases = []string{
	"", " ", "{", "}", "[", "]", "{}", "[]", " {} ", "\t[ ]\r\n", "{} {}", "{}x", "[],",
	"null", "true", "false", "nul", "truex", "nulll", "True", "NULL",
	"0", "-0", "01", "-01", "1.", ".5", "1.5", "1e5", "1E+5", "1e-5", "1e", "1e+", "-", "+1", "1.5e3.2",
	"123456789012345678901234567890", "1_000", "0x10", "Infinity", "NaN", "--1", "0.0e0",
	"\"\"", "\"a\"", "\"\\\"\"", "\"\\\\\"", "\"\\/\"", "\"\\b\\f\\n\\r\\t\"",
	"\"\\u00e9\"", "\"\\u00E9\"", "\"\\u12g4\"", "\"\\u123\"", "\"\\x\"", "\"\\'\"", "\"\\\"", "\"abc",
	"\"\\ud83d\\ude00\"", "\"\\ud800\"", "\"\\udc00\\ud800\"", "\"\\u0000\"", "\"\\uD83D\\uDE00x\\ud83d\"", "\"a\\ud83d\\n\"", "\"\\ud83d\\ud83d\\ude00\"",
	"\"tab\tin\"", "\"nl\nin\"", "\"\x01\"", "\"\x7f\"", "\"\xff\xfe\"", "\"\xc3\"", "\"é ✓ 😀\"", "\"\xed\xa0\x80\"",
	"{\"a\":1}", "{\"a\":1,}", "{\"a\" 1}", "{a:1}", "{\"a\":1 \"b\":2}", "{\"a\":}", "{,}",
	"{\"a\":1,\"a\":2}", "{\"A\":1,\"a\":2}", "{\"\\u0061\":1}", "{\"\xff\":1}", "{\"a\\\"b\":1}", "{\"\":1}",
	"{ \"a\" : [ 1 , { \"b\" : null } ] , \"c\" : \"d\" }", "[1,2,]", "[,1]", "[1 2]", "[[[]]]", "[{}]",
	"{\"a\":[1,{\"b\":[true,false,null,\"x\",-1.5e-3]}]}", "{\"a\":{\"b\":{\"c\":{}}}}", "\xef\xbb\xbf{}", "{}\x00", "\x00",
}

func nest(n int, open, close string) string {
	return strings.Repeat(open, n) + strings.Repeat(close, n)
}

// allCases adds texts nested to just under, at and just over encoding/json's depth limit.
func allCases() []string {
	c := append([]string(nil), cases...)
	for _, n := range []int{9999, 10000, 10001} {
		c = append(c, nest(n, "[", "]"), "{\"a\":"+nest(n-1, "[", "]")+"}", "["+nest(n-1, "{\"k\":", "}")+"]")
	}
	return c
}

func TestMatchesEncodingJSON(t *testing.T) {
	for _, s := range allCases() {
		check(t, []byte(s))
	}
}

func FuzzMatchesEncodingJSON(f *testing.F) {
	for _, s := range cases {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		check(t, data)
		mems, _ := Object(data)
		for _, m := range mems {
			check(t, m.Value)
		}
	})
}

// check compares every function of the package on data with what encoding/json does.
func check(t *testing.T, data []byte) {
	t.Helper()
	name := string(data)
	if len(name) > 40 {
		name = name[:40] + "..."
	}
	valid := json.Valid(data)
	if got := Valid(data); got != valid {
		t.Fatalf("Valid(%q) = %v, json.Valid says %v", name, got, valid)
	}
	if got, want := Trim(data), bytes.Trim(data, " \t\r\n"); valid && !bytes.Equal(got, want) {
		t.Fatalf("Trim(%q) = %q, want %q", name, got, want)
	}

	// Object: the members of a valid object, as json.Unmarshal into a map reads them (the
	// last of duplicate names), each value exactly the bytes json.RawMessage gets.
	mems, ok := Object(data)
	var m map[string]json.RawMessage
	err := json.Unmarshal(data, &m)
	if want := err == nil && m != nil; ok != want {
		t.Fatalf("Object(%q) ok = %v, want %v (unmarshal error %v)", name, ok, want, err)
	}
	if ok {
		got := map[string]json.RawMessage{}
		for _, mem := range mems {
			var k string
			if err := json.Unmarshal(mem.Key, &k); err != nil {
				t.Fatalf("Object(%q): key %q does not decode: %v", name, mem.Key, err)
			}
			if cap(mem.Key) != len(mem.Key) || cap(mem.Value) != len(mem.Value) {
				t.Fatalf("Object(%q): member slices have room after them", name)
			}
			if text, ok := Unquote(mem.Key); ok && text != k {
				t.Fatalf("Unquote(%q) = %q, want %q", mem.Key, text, k)
			}
			got[k] = mem.Value
		}
		if len(got) != len(m) {
			t.Fatalf("Object(%q): %d names, want %d", name, len(got), len(m))
		}
		for k, v := range m {
			if !bytes.Equal(got[k], v) {
				t.Fatalf("Object(%q)[%q] = %q, want %q", name, k, got[k], v)
			}
		}
	}

	// members splits a valid object as Object does, without checking it.
	if ok {
		unchecked := members(data)
		if len(unchecked) != len(mems) {
			t.Fatalf("members(%q): %d members, Object found %d", name, len(unchecked), len(mems))
		}
		for i := range mems {
			if !bytes.Equal(unchecked[i].Key, mems[i].Key) || !bytes.Equal(unchecked[i].Value, mems[i].Value) {
				t.Fatalf("members(%q)[%d] = %q: %q, Object found %q: %q", name, i, unchecked[i].Key, unchecked[i].Value, mems[i].Key, mems[i].Value)
			}
		}
	} else {
		_ = members(data) // must not panic
	}

	// Array: the elements of a valid array, as json.Unmarshal into []json.RawMessage reads
	// them.
	elems, ok := Array(data)
	var a []json.RawMessage
	err = json.Unmarshal(data, &a)
	if want := err == nil && a != nil; ok != want {
		t.Fatalf("Array(%q) ok = %v, want %v (unmarshal error %v)", name, ok, want, err)
	}
	if ok {
		if len(elems) != len(a) {
			t.Fatalf("Array(%q): %d elements, want %d", name, len(elems), len(a))
		}
		for i := range a {
			if !bytes.Equal(elems[i], a[i]) || cap(elems[i]) != len(elems[i]) {
				t.Fatalf("Array(%q)[%d] = %q, want %q", name, i, elems[i], a[i])
			}
		}
	}

	if ok {
		unchecked := elements(data)
		if len(unchecked) != len(elems) {
			t.Fatalf("elements(%q): %d elements, Array found %d", name, len(unchecked), len(elems))
		}
		for i := range elems {
			if !bytes.Equal(unchecked[i], elems[i]) {
				t.Fatalf("elements(%q)[%d] = %q, Array found %q", name, i, unchecked[i], elems[i])
			}
		}
	} else {
		_ = elements(data)
	}

	// Unquote: a valid string decodes as json.Unmarshal decodes it, or is left to it.
	if v := Trim(data); valid && IsString(v) {
		var want string
		if err := json.Unmarshal(v, &want); err != nil {
			t.Fatalf("json.Unmarshal(%q): %v", name, err)
		}
		if got, ok := Unquote(v); ok && got != want {
			t.Fatalf("Unquote(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestUnquote(t *testing.T) {
	for _, s := range []string{"\"\xff\"", "\"\\ud800\"", "\"\\ud800x\"", "\"\\ude00\\ud83d\"", "\"\\ud83d\\u0041\""} {
		if _, ok := Unquote([]byte(s)); ok {
			t.Errorf("Unquote(%q) decoded it; encoding/json replaces it", s)
		}
	}
	for s, want := range map[string]string{
		"\"a\\\"b\\\\c\\/d\\u00e9\\n\"": "a\"b\\c/dé\n",
		"\"plain ✓\"":                   "plain ✓",
		"\"a\\ud83d\\ude00b\"":          "a😀b",
		"\"\"":                          "",
	} {
		if got, ok := Unquote([]byte(s)); !ok || got != want {
			t.Errorf("Unquote(%q) = %q, %v; want %q", s, got, ok, want)
		}
	}
}

// plainEnd finds the first byte a string does not hold as it is wherever it falls in a word of
// eight, after any run of other bytes, high bytes included.
func TestPlainEnd(t *testing.T) {
	for _, special := range []byte{'"', '\\', 0x00, 0x1f} {
		for n := range 70 {
			for _, fill := range []byte{'a', 0xff, 0x80, 0x20, '!', '#', '[', ']'} {
				data := append(bytes.Repeat([]byte{fill}, n), special, 'z', '"')
				if got := plainEnd(data, 0); got != n {
					t.Fatalf("plainEnd(%q) = %d, want %d", data, got, n)
				}
			}
		}
	}
	if got := plainEnd([]byte("abcdefghijklmnop"), 3); got != 16 {
		t.Errorf("plainEnd of plain text = %d, want its length", got)
	}
}

func BenchmarkValidString(b *testing.B) {
	s := []byte("\"" + strings.Repeat("func handle(w http.ResponseWriter) { return } // padding line\\n", 1600) + "\"")
	b.SetBytes(int64(len(s)))
	for b.Loop() {
		if !Valid(s) {
			b.Fatal("not valid")
		}
	}
}
