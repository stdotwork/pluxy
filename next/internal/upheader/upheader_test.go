package upheader

import (
	"reflect"
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
	got, dynamic := Import(map[string]string{" X-Test ": " v ", "": "x", "X-Empty": " ", "X-Copy": "$X-Client",
		"X-Session": "id-$cpa-session-id", "X-Price": "5$"})
	if want := map[string]string{"X-Test": "v", "X-Price": "5$"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Import = %v, want %v", got, want)
	}
	if want := []string{"X-Copy", "X-Session"}; !reflect.DeepEqual(dynamic, want) {
		t.Errorf("dynamic = %v, want %v", dynamic, want)
	}
}

func TestSecrets(t *testing.T) {
	got := Secrets(map[string]string{"Authorization": "a", "X-Api-Key": "b", "X-Title": "c", "Cookie": "d"})
	if want := []string{"a", "d", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Secrets = %v, want %v (by name: Authorization, Cookie, X-Api-Key)", got, want)
	}
}
