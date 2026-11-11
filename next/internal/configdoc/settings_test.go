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
// own list in any order. A config document's placeholder may still move between secret keys.
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
	if out, err := r.Restore(swapped, cur, false); err != nil || string(out) != `{"api_key":"fake-b","password":"fake-a"}` {
		t.Errorf("config restore of moved placeholders = %s, %v; want them accepted", out, err)
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
