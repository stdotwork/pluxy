package configdoc

import (
	"encoding/json"
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
