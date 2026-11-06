package extconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const schemaText = `{
  "type": "object",
  "properties": {
    "greeting": {"type": "string", "title": "Greeting", "maxLength": 5, "default": "hi"},
    "interval": {"type": "integer", "minimum": 1, "maximum": 10, "default": 2},
    "ratio": {"type": "number", "minimum": 0, "maximum": 1},
    "unit": {"type": "string", "enum": ["a", "b"]},
    "on": {"type": "boolean", "default": false},
    "token": {"type": "string", "writeOnly": true},
    "name": {"type": "string"}
  },
  "required": ["name"]
}`

func mustParse(t *testing.T, text string) *Schema {
	t.Helper()
	s, err := Parse(json.RawMessage(text))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseKeepsFieldOrder(t *testing.T) {
	s := mustParse(t, schemaText)
	var names []string
	for _, f := range s.Fields {
		names = append(names, f.Name)
	}
	if want := []string{"greeting", "interval", "ratio", "unit", "on", "token", "name"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("fields %v, want %v", names, want)
	}
	if f, _ := s.Field("name"); !f.Required {
		t.Fatal("name is not required")
	}
	if !strings.Contains(string(s.Raw()), `"greeting"`) {
		t.Fatal("raw schema lost")
	}
}

func TestParseRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"not an object":       `[]`,
		"unknown top keyword": `{"type": "object", "properties": {"a": {"type": "string"}}, "$ref": "x"}`,
		"not type object":     `{"type": "array", "properties": {"a": {"type": "string"}}}`,
		"no fields":           `{"type": "object", "properties": {}}`,
		"nested object":       `{"type": "object", "properties": {"a": {"type": "object"}}}`,
		"pattern":             `{"type": "object", "properties": {"a": {"type": "string", "pattern": "x"}}}`,
		"bound on a string":   `{"type": "object", "properties": {"a": {"type": "string", "minimum": 1}}}`,
		"enum on a number":    `{"type": "object", "properties": {"a": {"type": "integer", "enum": ["1"]}}}`,
		"bad field name":      `{"type": "object", "properties": {"A-b": {"type": "string"}}}`,
		"duplicate field":     `{"type": "object", "properties": {"a": {"type": "string"}, "a": {"type": "boolean"}}}`,
		"required unknown":    `{"type": "object", "properties": {"a": {"type": "string"}}, "required": ["b"]}`,
		"bad default":         `{"type": "object", "properties": {"a": {"type": "integer", "maximum": 3, "default": 4}}}`,
		"null default":        `{"type": "object", "properties": {"a": {"type": "string", "default": null}}}`,
		"default off enum":    `{"type": "object", "properties": {"a": {"type": "string", "enum": ["x"], "default": "y"}}}`,
		"min above max":       `{"type": "object", "properties": {"a": {"type": "number", "minimum": 3, "maximum": 1}}}`,
		"duplicate enum":      `{"type": "object", "properties": {"a": {"type": "string", "enum": ["x", "x"]}}}`,
	} {
		if _, err := Parse(json.RawMessage(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func values(t *testing.T, text string) Values {
	t.Helper()
	var v Values
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMergeChecksEveryField(t *testing.T) {
	s := mustParse(t, schemaText)
	for change, want := range map[string]string{
		`{"name": "n", "greeting": "toolong"}`: "greeting must be at most 5 characters",
		`{"name": "n", "interval": 11}`:        "interval must be at most 10",
		`{"name": "n", "interval": 1.5}`:       "interval must be a whole number",
		`{"name": "n", "interval": "3"}`:       "interval must be a number",
		`{"name": "n", "ratio": -0.5}`:         "ratio must be at least 0",
		`{"name": "n", "unit": "c"}`:           "unit must be one of a, b",
		`{"name": "n", "on": "yes"}`:           "on must be true or false",
		`{"name": "n", "other": 1}`:            "other is not a setting of this extension",
		`{"greeting": "x"}`:                    "name must have a value",
	} {
		_, err := s.Merge(Values{}, values(t, change))
		if err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", change, err, want)
		}
	}
}

func TestMergeNullClearsAndViewHidesSecrets(t *testing.T) {
	s := mustParse(t, schemaText)
	v, err := s.Merge(Values{"stale": json.RawMessage(`1`)}, values(t, `{"name": "n", "interval": 7, "token": "secret-value"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v["stale"]; ok {
		t.Fatal("a field the schema no longer has was kept")
	}
	view := s.View(v)
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), "secret-value") {
		t.Fatalf("view shows the secret: %s", b)
	}
	if want := `{"values":{"greeting":"hi","interval":7,"name":"n","on":false},"set":["token"]}`; string(b) != want {
		t.Fatalf("view %s, want %s", b, want)
	}
	v, err = s.Merge(v, values(t, `{"interval": null, "token": null}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(s.View(v))
	if want := `{"values":{"greeting":"hi","interval":2,"name":"n","on":false},"set":[]}`; string(b) != want {
		t.Fatalf("after clearing: %s, want %s", b, want)
	}
}

func TestMergeChecksKeptValuesAgainstANewSchema(t *testing.T) {
	s := mustParse(t, schemaText)
	if _, err := s.Merge(Values{"name": json.RawMessage(`"n"`), "interval": json.RawMessage(`50`)}, values(t, `{"on": true}`)); err == nil || err.Error() != "interval must be at most 10" {
		t.Fatalf("kept out-of-range value: %v", err)
	}
}

func TestStoreSavesPrivatelyAndRefusesWithoutSaving(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(filepath.Join(dir, "config"))
	s := mustParse(t, schemaText)
	if v, err := st.Load("pub/ext"); err != nil || len(v) != 0 {
		t.Fatalf("empty store: %v %v", v, err)
	}
	if _, err := st.Change("pub/ext", s, values(t, `{"name": "n", "token": "t"}`)); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config", "pub", "ext.json")
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode %v", info.Mode().Perm())
	}
	_, err = st.Change("pub/ext", s, values(t, `{"interval": 99}`))
	var invalid *Invalid
	if !errors.As(err, &invalid) {
		t.Fatalf("refused change: %v", err)
	}
	got, err := st.Load("pub/ext")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Values{"name": json.RawMessage(`"n"`), "token": json.RawMessage(`"t"`)}); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a refused change: %s", got)
	}
	for _, id := range []string{"../x", "pub/../../x", "pub", "/abs", "pub/a/b"} {
		if _, err := st.Load(id); err == nil {
			t.Errorf("Load(%q) accepted", id)
		}
	}
}
