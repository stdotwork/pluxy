package configdoc

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A list of lists of keys below a key named like a secret holds secrets at every depth.
func TestRedactNestedKeyLists(t *testing.T) {
	src := `{"access": {"api_keys": [["fake-nested-a", "fake-nested-b"], [["fake-nested-c"]]]}}`
	_, back := redactChecked(t, src, false, []string{"fake-nested-a", "fake-nested-b", "fake-nested-c"})
	if back != src {
		t.Errorf("round trip = %s", back)
	}
}

// In account settings, everything below a key named like a secret is a secret, however deep,
// and the rest stays as it is.
func TestRedactSettings(t *testing.T) {
	r := NewRedactor()
	src := `{"base_url":"https://one.example/v1","password":{"primary":"fake-object","spare":[1234]},` +
		`"api_key":[{"inner":"fake-in-list"}],"headers":{"Authorization":"fake-header"},"retries":2}`
	hidden := []string{"fake-object", "1234", "fake-in-list", "fake-header"}
	out, err := r.RedactSettings([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hidden {
		if strings.Contains(string(out), h) {
			t.Errorf("%s is in the redacted settings: %s", h, out)
		}
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["base_url"] != "https://one.example/v1" || got["retries"] != 2.0 {
		t.Errorf("redacted settings = %s, want base_url and retries kept", out)
	}
	back, err := r.RestoreSettings(out, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	_ = json.Unmarshal(back, &a)
	_ = json.Unmarshal([]byte(src), &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("restored = %s, want %s", back, src)
	}
}

// In settings, a placeholder goes back only where its secret is: in its own place, or within its
// own list in any order; in a config document too (TestRestorePlacesEachSecretInItsOwnSlot).
func TestRestoreSettingsKeepsSecretsInPlace(t *testing.T) {
	r := NewRedactor()
	cur := []byte(`{"api_key":"fake-a","password":"fake-b","api_keys":["fake-c","fake-d"]}`)
	a, b, c, d := r.Placeholder("fake-a"), r.Placeholder("fake-b"), r.Placeholder("fake-c"), r.Placeholder("fake-d")
	swapped := []byte(`{"api_key":"` + b + `","password":"` + a + `"}`)
	if _, err := r.RestoreSettings(swapped, cur); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrStale) {
		t.Errorf("swapped placeholders: err = %v, want ErrInvalid only", err)
	}
	if _, err := r.RestoreSettings([]byte(`{"api_keys":"`+c+`"}`), cur); !errors.Is(err, ErrInvalid) {
		t.Errorf("a list item's placeholder outside its list: err = %v, want ErrInvalid", err)
	}
	out, err := r.RestoreSettings([]byte(`{"api_keys":["`+d+`","`+c+`"],"api_key":"`+a+`"}`), cur)
	if err != nil || string(out) != `{"api_keys":["fake-d","fake-c"],"api_key":"fake-a"}` {
		t.Errorf("reordered list = %s, %v", out, err)
	}
	if _, err := r.RestoreSettings([]byte(`{"api_key":"pluxy-redacted-000000000000000000000000"}`), cur); !errors.Is(err, ErrStale) {
		t.Errorf("unknown placeholder at a secret key: err = %v, want ErrStale", err)
	}
	if out, err := r.Restore(swapped, cur, false); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrStale) {
		t.Errorf("config restore of swapped placeholders = %s, %v; want ErrInvalid only", out, err)
	}
}

// A key that is the empty string is a key like any other: below a secret key, its value is a
// secret too.
func TestRedactEmptyKey(t *testing.T) {
	r := NewRedactor()
	for _, c := range []struct {
		src    string
		strict []bool
	}{
		{`{"password":{"":"fake-empty-key"}}`, []bool{true}},
		{`{"headers":{"Authorization":{"":"fake-empty-key"}}}`, []bool{true, false}},
		{`{"api_keys":[[{"":"fake-empty-key"}]]}`, []bool{true, false}},
		{`{"credential":{"kind":"api_key","":"fake-empty-key"}}`, []bool{true, false}},
	} {
		for _, strict := range c.strict {
			out, err := r.redact([]byte(c.src), false, strict)
			if err != nil || strings.Contains(string(out), "fake-empty-key") {
				t.Errorf("redact(%s, strict %v) = %s, %v", c.src, strict, out, err)
			}
		}
	}
}

// A placeholder in a list item goes back only in that same list: not in the same-named list of
// another item.
func TestRestoreSettingsKeepsListItemsInTheirList(t *testing.T) {
	r := NewRedactor()
	cur := []byte(`{"targets":[{"name":"one","api_keys":["fake-a","fake-c"]},{"name":"two","api_keys":["fake-b"]}]}`)
	a, b, c := r.Placeholder("fake-a"), r.Placeholder("fake-b"), r.Placeholder("fake-c")
	moved := []byte(`{"targets":[{"name":"one","api_keys":["` + c + `"]},{"name":"two","api_keys":["` + a + `","` + b + `"]}]}`)
	if _, err := r.RestoreSettings(moved, cur); !errors.Is(err, ErrInvalid) {
		t.Errorf("a placeholder moved to another item's list: err = %v, want ErrInvalid", err)
	}
	out, err := r.RestoreSettings([]byte(`{"targets":[{"name":"one","api_keys":["`+c+`","`+a+`"]},{"name":"two","api_keys":["`+b+`"]}]}`), cur)
	if want := `{"targets":[{"name":"one","api_keys":["fake-c","fake-a"]},{"name":"two","api_keys":["fake-b"]}]}`; err != nil || string(out) != want {
		t.Errorf("reordered list = %s, %v; want %s", out, err, want)
	}
	nested := []byte(`{"api_keys":[["fake-a"],["fake-b"]]}`)
	if _, err := r.RestoreSettings([]byte(`{"api_keys":[["`+b+`"],["`+a+`"]]}`), nested); !errors.Is(err, ErrInvalid) {
		t.Errorf("a placeholder moved to another inner list: err = %v, want ErrInvalid", err)
	}
}

// The same text as a number and as a string is one placeholder, and each goes back with its own
// type.
func TestRestoreSettingsKeepsEachType(t *testing.T) {
	r := NewRedactor()
	for _, src := range []string{
		`{"password":{"numeric":1234,"text":"1234"}}`,
		`{"password":{"text":"1234","numeric":1234}}`,
		`{"password":["1234",1234]}`,
	} {
		out, err := r.RedactSettings([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		back, err := r.RestoreSettings(out, []byte(src))
		if err != nil || string(back) != src {
			t.Errorf("round trip of %s = %s, %v", src, back, err)
		}
	}
}

// A number and a string with the same text have different placeholders in settings, so each
// keeps its type wherever its own placeholder goes, whatever else in the list changed.
func TestRestoreSettingsKeepsTypeOfEachPlaceholder(t *testing.T) {
	r := NewRedactor()
	cur := []byte(`{"password":["1234",1234]}`)
	out, err := r.RedactSettings(cur)
	if err != nil {
		t.Fatal(err)
	}
	var shown struct{ Password []string }
	if err := json.Unmarshal(out, &shown); err != nil || len(shown.Password) != 2 || shown.Password[0] == shown.Password[1] {
		t.Fatalf("redacted = %s, %v; want two different placeholders", out, err)
	}
	text, number := shown.Password[0], shown.Password[1]
	for sent, want := range map[string]string{
		`{"password":["replacement","` + number + `"]}`:  `{"password":["replacement",1234]}`,
		`{"password":["` + number + `","` + text + `"]}`: `{"password":[1234,"1234"]}`,
		`{"password":["` + text + `"]}`:                  `{"password":["1234"]}`,
	} {
		if back, err := r.RestoreSettings([]byte(sent), cur); err != nil || string(back) != want {
			t.Errorf("restore %s = %s, %v; want %s", sent, back, err, want)
		}
	}
}

// A string that spells a number's type and value is still a string: its placeholder is not the
// number's, in any order or place, and it does not keep a placeholder from the number alive.
func TestSettingsPlaceholdersOfTypedValuesAreTheirOwn(t *testing.T) {
	r := NewRedactor()
	for _, src := range []string{
		`{"password":[1234,"!!int\u00001234"]}`,
		`{"password":["!!int\u00001234",1234]}`,
		`{"password":[1.2,"!!float\u00001.2"]}`,
		`{"password":{"n":1234,"s":"!!int\u00001234"}}`,
	} {
		out, err := r.RedactSettings([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		var shown map[string]any
		if err := json.Unmarshal(out, &shown); err != nil {
			t.Fatal(err)
		}
		seen := map[any]bool{}
		var each func(v any)
		each = func(v any) {
			switch v := v.(type) {
			case []any:
				for _, c := range v {
					each(c)
				}
			case map[string]any:
				for _, c := range v {
					each(c)
				}
			default:
				if seen[v] {
					t.Errorf("redacted %s = %s: two secrets share a placeholder", src, out)
				}
				seen[v] = true
			}
		}
		each(shown["password"])
		if back, err := r.RestoreSettings(out, []byte(src)); err != nil || string(back) != src {
			t.Errorf("round trip of %s = %s, %v", src, back, err)
		}
	}
	// The number's placeholder, sent after the number became that string, matches no secret.
	before, err := r.RedactSettings([]byte(`{"password":1234}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RestoreSettings(before, []byte(`{"password":"!!int\u00001234"}`)); !errors.Is(err, ErrStale) {
		t.Errorf("a number's placeholder after the number changed to a string: err = %v, want ErrStale", err)
	}
}
