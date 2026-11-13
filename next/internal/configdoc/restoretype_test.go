package configdoc

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

// The same secret written as a number in one place and as a string in another goes back to each
// place as it was written there, also after its placeholder moved within its list.
func TestRestoreKeepsEachPlacesType(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		isYAML    bool
		// want is what the file holds once the two items of its list are swapped.
		want string
	}{
		{"json", `{"api_key": 1234, "secret_key": "1234", "access": {"api_keys": ["1234", 1234]}}`, false,
			`"api_keys": [1234, "1234"]`},
		{"yaml", "api-key: 1234\nsecret-key: \"1234\"\naccess:\n  api-keys: [\"1234\", 1234]\n", true,
			`api-keys: [1234, "1234"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRedactor()
			red, err := r.Redact([]byte(tc.src), tc.isYAML)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(red), "1234") {
				t.Fatalf("the redacted document holds the secret:\n%s", red)
			}
			back, err := r.Restore(red, []byte(tc.src), tc.isYAML)
			if err != nil || string(back) != tc.src {
				t.Errorf("round trip = %s, %v; want %s", back, err, tc.src)
			}
			// Swap the list's two items, its last two placeholders: each goes back with its type.
			at := regexp.MustCompile(PlaceholderPrefix+`[0-9a-f]{24}`).FindAllStringIndex(string(red), -1)
			if len(at) != 4 {
				t.Fatalf("want 4 placeholders in the redacted document:\n%s", red)
			}
			s, x, y := string(red), at[2], at[3]
			moved := s[:x[0]] + s[y[0]:y[1]] + s[x[1]:y[0]] + s[x[0]:x[1]] + s[y[1]:]
			back, err = r.Restore([]byte(moved), []byte(tc.src), tc.isYAML)
			if err != nil || !strings.Contains(string(back), tc.want) {
				t.Errorf("moved placeholder restores to %s, %v; want it to hold %s", back, err, tc.want)
			}
		})
	}
}

// In a config file too, a string that spells a number's type and value is still a string: its
// placeholder is not the number's, in either order, and it does not keep a placeholder from the
// number alive.
func TestConfigPlaceholdersOfTypedValuesAreTheirOwn(t *testing.T) {
	r := NewRedactor()
	for _, tc := range []struct {
		src    string
		isYAML bool
	}{
		{`{"api_keys": [1234, "!!int\u00001234"]}`, false},
		{`{"api_keys": ["!!int\u00001234", 1234]}`, false},
		{"api-keys: [1234, \"!!int\\x001234\"]\n", true},
		{"api-keys: [\"!!int\\x001234\", 1234]\n", true},
	} {
		out, err := r.Redact([]byte(tc.src), tc.isYAML)
		if err != nil {
			t.Fatal(err)
		}
		if got := regexp.MustCompile(PlaceholderPrefix+`[0-9a-f]{24}`).FindAllString(string(out), -1); len(got) != 2 || got[0] == got[1] {
			t.Errorf("redacted %q = %s: want two different placeholders", tc.src, out)
		}
		if back, err := r.Restore(out, []byte(tc.src), tc.isYAML); err != nil || string(back) != tc.src {
			t.Errorf("round trip of %q = %q, %v", tc.src, back, err)
		}
	}
	// The number's placeholder, sent after the number became that string, matches no secret.
	for _, tc := range []struct {
		before, now string
		isYAML      bool
	}{
		{`{"api_key": 1234}`, `{"api_key": "!!int\u00001234"}`, false},
		{"api-key: 1234\n", "api-key: \"!!int\\x001234\"\n", true},
	} {
		red, err := r.Redact([]byte(tc.before), tc.isYAML)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Restore(red, []byte(tc.now), tc.isYAML); !errors.Is(err, ErrStale) {
			t.Errorf("a number's placeholder after the number changed to a string (%q): err = %v, want ErrStale", tc.now, err)
		}
	}
}
