package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

// asciiJSON is s as a JSON string with every character beyond ASCII written as a \u escape.
func asciiJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x7f:
			b.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// Whatever the fragments split, the text comes out whole and in order, with every character
// complete when it is handed out.
func TestInputDecoderFragments(t *testing.T) {
	texts := []string{
		"",
		"plain",
		"*** Begin Patch\n*** Add File: a.go\n+package a\n*** End Patch\n",
		"quote \" slash \\ solidus / tab \t cr \r control \x01 \x1f",
		"héllo 中文 😀 end",
		"<html> & </html>",
		"\u2028\u2029 and \u007f",
	}
	for _, text := range texts {
		for name, args := range map[string]string{
			"escaped":    asciiJSON(text),
			"compact":    func() string { b, _ := json.Marshal(text); return string(b) }(),
			"solidus":    strings.ReplaceAll(asciiJSON(text), "/", `\/`),
			"upper case": strings.NewReplacer(`\u4e2d`, `\u4E2D`, `\u00e9`, `\u00E9`).Replace(asciiJSON(text)),
		} {
			for _, whole := range []string{`{"input":` + args + `}`, " \n{ \"input\" :\t" + args + " }\n "} {
				for cut := 0; cut <= len(whole); cut++ {
					var d inputDecoder
					var got strings.Builder
					for _, frag := range []string{whole[:cut], whole[cut:]} {
						out, err := d.Push(frag)
						if err != nil {
							t.Fatalf("%q split at %d (%s): %v", whole, cut, name, err)
						}
						if strings.ContainsRune(out, '\uFFFD') && !strings.ContainsRune(text, '\uFFFD') {
							t.Fatalf("%q split at %d: a broken character in %q", whole, cut, out)
						}
						got.WriteString(out)
					}
					if got.String() != text || !d.complete() {
						t.Fatalf("%q split at %d (%s): read %q (complete %v), want %q", whole, cut, name, got.String(), d.complete(), text)
					}
					input, tail, err := d.Finish(whole, true)
					if err != nil || input != text || tail != "" {
						t.Fatalf("%q: Finish = %q, %q, %v", whole, input, tail, err)
					}
				}
				// One byte at a time.
				var d inputDecoder
				var got strings.Builder
				for i := 0; i < len(whole); i++ {
					out, err := d.Push(whole[i : i+1])
					if err != nil {
						t.Fatalf("%q byte %d: %v", whole, i, err)
					}
					got.WriteString(out)
				}
				if got.String() != text {
					t.Fatalf("%q byte by byte: read %q, want %q", whole, got.String(), text)
				}
			}
		}
	}
}

func TestInputDecoderRefuses(t *testing.T) {
	for name, args := range map[string]string{
		"array":             `[]`,
		"string":            `"x"`,
		"empty object":      `{}`,
		"other key":         `{"patch":"x"}`,
		"escaped key":       `{"inp\u0075t":"x"}`,
		"key twice":         `{"input":"x","input":"y"}`,
		"number":            `{"input":1}`,
		"null":              `{"input":null}`,
		"no colon":          `{"input" "x"}`,
		"raw newline":       "{\"input\":\"a\nb\"}",
		"bad escape":        `{"input":"\x"}`,
		"short unicode":     `{"input":"\u12"}`,
		"not hex":           `{"input":"\u12zz"}`,
		"lone high":         `{"input":"\ud83dx"}`,
		"high then not low": `{"input":"\ud83d\u0041"}`,
		"lone low":          `{"input":"\ude00"}`,
		"invalid utf-8":     "{\"input\":\"\xff\"}",
		"after the object":  `{"input":"x"}{`,
		"two closings":      `{"input":"x"}}`,
	} {
		var d inputDecoder
		_, err := d.Push(args)
		if err == nil {
			err = func() error { _, _, err := d.Finish(args, true); return err }()
		}
		if err == nil {
			t.Errorf("%s: %q was accepted", name, args)
		}
	}
	// A cut-off object is not complete.
	var d inputDecoder
	if _, err := d.Push(`{"input":"x`); err != nil || d.complete() {
		t.Errorf("cut-off object: error %v, complete %v", err, d.complete())
	}
	if _, _, err := d.Finish(`{"input":"x`, true); err == nil {
		t.Error("Finish accepted a cut-off object")
	}
}

// The final arguments may carry more than the fragments did, never less or something else.
func TestInputDecoderFinish(t *testing.T) {
	var d inputDecoder
	if out, err := d.Push(`{"input":"ab`); err != nil || out != "ab" {
		t.Fatalf("Push = %q, %v", out, err)
	}
	if input, tail, err := d.Finish(`{"input":"abcd"}`, true); err != nil || input != "abcd" || tail != "cd" {
		t.Errorf("Finish = %q, %q, %v; want abcd with the tail cd", input, tail, err)
	}
	var e inputDecoder
	_, _ = e.Push(`{"input":"ab`)
	if _, _, err := e.Finish(`{"input":"xy"}`, true); err == nil {
		t.Error("Finish accepted final arguments that do not start with the text sent")
	}
	// With no fragments, the whole text is the input and there is no tail to send.
	var f inputDecoder
	if input, tail, err := f.Finish(`{"input":"whole"}`, true); err != nil || input != "whole" || tail != "" {
		t.Errorf("Finish without fragments = %q, %q, %v", input, tail, err)
	}
}
