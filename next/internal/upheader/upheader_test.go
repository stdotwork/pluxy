package upheader

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const value = "fake-header-value-91"

// Check refuses what net/http would refuse or override, and its message never holds a value.
func TestCheck(t *testing.T) {
	for _, c := range []struct {
		h    map[string]string
		want string // part of the error; "" for none
	}{
		{map[string]string{"X-Gateway-Key": value, "User-Agent": "pluxy"}, ""},
		{nil, ""},
		{map[string]string{"host": value}, "header host cannot be set"},
		{map[string]string{"Transfer-Encoding": value}, "header Transfer-Encoding cannot be set"},
		{map[string]string{"X-A": value, "x-a": value}, "are the same header"},
		{map[string]string{"X A": value}, "not a valid header name"},
		{map[string]string{"": value}, "not a valid header name"},
		{map[string]string{"X-A": "  "}, "header X-A has no value"},
		{map[string]string{"X-A": value + "\r\nX-Injected: 1"}, "line break"},
		{map[string]string{"X-A": "a\tb"}, ""},
	} {
		err := Check("headers", c.h)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%v: %v", c.h, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%v: error %v, want one with %q", c.h, err, c.want)
		case err != nil && strings.Contains(err.Error(), value):
			t.Errorf("%v: the error holds the value: %v", c.h, err)
		}
	}
}

// Import trims and drops empty pairs as today's core does, and leaves out the values today's
// core fills in from the client's request.
func TestImport(t *testing.T) {
	got, dynamic, err := Import(map[string]string{" X-Test ": " v ", "": "x", "X-Empty": " ", "X-Copy": "$X-Client",
		"X-Session": "id-$cpa-session-id", "X-Price": "5$"})
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"X-Test": "v", "X-Price": "5$"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Import = %v, want %v", got, want)
	}
	if want := []string{"X-Copy", "X-Session"}; !reflect.DeepEqual(dynamic, want) {
		t.Errorf("dynamic = %v, want %v", dynamic, want)
	}
}

func TestSecrets(t *testing.T) {
	got := Secrets(map[string]string{"Authorization": "a", "X-Api-Key": "b", "X-Title": "c", "Cookie": "d", "X-Passphrase": "e"})
	if want := []string{"a", "d", "b", "e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Secrets = %v, want %v (by name: Authorization, Cookie, X-Api-Key, X-Passphrase)", got, want)
	}
}

// Names that are the same once trimmed or with the letter case ignored are refused, both named and
// no value shown, instead of one pair being kept by chance; a pair dropped for its empty value
// does not clash.
func TestImportRefusesSameHeaderTwice(t *testing.T) {
	for _, h := range []map[string]string{
		{"X-Api-Key": "fake-value-one", " X-Api-Key ": "fake-value-two"},
		{"X-Api-Key": "fake-value-one", "x-api-key": "fake-value-two"},
	} {
		for range 20 { // map order is random: the refusal must not depend on it
			out, _, err := Import(h)
			if err == nil {
				t.Fatalf("Import(%v) = %v, want an error", h, out)
			}
			for _, name := range slices.Collect(maps.Keys(h)) {
				if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
					t.Fatalf("error %q does not name header %q", err, name)
				}
			}
			if strings.Contains(err.Error(), "fake-value") {
				t.Fatalf("error shows a value: %q", err)
			}
		}
	}
	out, _, err := Import(map[string]string{"X-A": " ", "x-a": "v"})
	if want := map[string]string{"x-a": "v"}; err != nil || !reflect.DeepEqual(out, want) {
		t.Errorf("Import with one empty value = %v, %v; want %v", out, err, want)
	}
}

// A secret header's value alone is removed from text only from 8 bytes, as a proxy's password is: a
// shorter one ("1") would also scrub unrelated text. Next to its name, as text shows a header, it
// is always removed.
func TestForms(t *testing.T) {
	got := Forms(map[string]string{"x-api-key": "1", "Authorization": "Bearer abcdefgh", "X-Title": "plain-title", "Cookie": "sid=12345", "X-Token": "12345678"})
	for _, f := range got {
		if f == "1" || strings.Contains(f, "plain-title") {
			t.Errorf("Forms holds %q", f)
		}
	}
	for _, want := range []string{"Bearer abcdefgh", "sid=12345", "12345678", "Authorization: Bearer abcdefgh", `"cookie":"sid=12345"`} {
		if !slices.Contains(got, want) {
			t.Errorf("Forms %q lacks %q", got, want)
		}
	}
	if got[0] != "Authorization: Bearer abcdefgh" || got[len(got)-1] != `"X-API-KEY": "1"` {
		t.Errorf("Forms are not in name order: %q", got)
	}
	// The forms remove each way an error or a log shows the short value next to its name, and
	// leave other 1s.
	text := `model-1 failed: x-api-key=1, X-API-KEY=1, {"X-Api-Key": "1"}, {"x-api-key":"1"}, X-Api-Key: 1, X-Api-Key:1, retry 1`
	for _, f := range got {
		text = strings.ReplaceAll(text, f, "[redacted]")
	}
	if want := `model-1 failed: [redacted], [redacted], {[redacted]}, {[redacted]}, [redacted], [redacted], retry 1`; text != want {
		t.Errorf("redacted text %q, want %q", text, want)
	}
}
