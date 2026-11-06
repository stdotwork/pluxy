// Package extconfig is an extension's settings: the schema its manifest declares (a small part
// of JSON Schema, next/ui/extension-pages/README.md), checking values against it, and the store
// core keeps them in.
package extconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits of a schema and its values.
const (
	MaxFields      = 50
	MaxString      = 4096
	maxTitle       = 100
	maxDescription = 500
	maxEnum        = 100
	maxEnumValue   = 200
)

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Field is one setting.
type Field struct {
	Name        string
	Type        string // string, integer, number or boolean
	Title       string
	Description string
	Default     json.RawMessage // nil when absent
	Enum        []string
	MinLength   *int
	MaxLength   *int
	Minimum     *float64
	Maximum     *float64
	WriteOnly   bool
	Required    bool
}

// Schema is a parsed settings schema.
type Schema struct {
	Fields []Field
	raw    json.RawMessage
}

// Raw returns the schema as the manifest gave it, keys in their order.
func (s *Schema) Raw() json.RawMessage { return s.raw }

// Field returns the field called name.
func (s *Schema) Field(name string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

var (
	topKeys   = []string{"type", "properties", "required"}
	fieldKeys = map[string][]string{
		"string": {"type", "title", "description", "default", "writeOnly", "enum", "minLength", "maxLength"},
		// writeOnly is for secrets, which are text.
		"integer": {"type", "title", "description", "default", "minimum", "maximum"},
		"number":  {"type", "title", "description", "default", "minimum", "maximum"},
		"boolean": {"type", "title", "description", "default"},
	}
)

// Parse reads and checks a schema. Any keyword the contract does not list is refused, so a
// schema never means something core does not check.
func Parse(raw json.RawMessage) (*Schema, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, errors.New("schema must be a JSON object")
	}
	for k := range top {
		if !slices.Contains(topKeys, k) {
			return nil, fmt.Errorf("schema: keyword %q is not supported", k)
		}
	}
	var typ string
	if err := json.Unmarshal(top["type"], &typ); err != nil || typ != "object" {
		return nil, errors.New(`schema: type must be "object"`)
	}
	names, props, err := orderedObject(top["properties"])
	if err != nil {
		return nil, fmt.Errorf("schema: properties: %w", err)
	}
	if len(names) == 0 || len(names) > MaxFields {
		return nil, fmt.Errorf("schema: properties must hold 1 to %d fields", MaxFields)
	}
	s := &Schema{raw: append(json.RawMessage(nil), raw...)}
	for i, name := range names {
		if !fieldName.MatchString(name) {
			return nil, fmt.Errorf("schema: field name %q is not 1 to 64 lower-case letters, digits and '_' starting with a letter", name)
		}
		f, err := parseField(name, props[i])
		if err != nil {
			return nil, fmt.Errorf("schema: %s: %w", name, err)
		}
		s.Fields = append(s.Fields, f)
	}
	if r, ok := top["required"]; ok {
		var req []string
		if err := json.Unmarshal(r, &req); err != nil {
			return nil, errors.New("schema: required must be a list of field names")
		}
		for _, name := range req {
			i := slices.IndexFunc(s.Fields, func(f Field) bool { return f.Name == name })
			if i < 0 {
				return nil, fmt.Errorf("schema: required names %q, which is not a field", name)
			}
			s.Fields[i].Required = true
		}
	}
	return s, nil
}

// orderedObject returns a JSON object's keys in order and their values. A key may appear once.
func orderedObject(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("must be an object")
	}
	var names []string
	var values []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name := t.(string) // object keys are strings
		if slices.Contains(names, name) {
			return nil, nil, fmt.Errorf("%q appears twice", name)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
		values = append(values, v)
	}
	return names, values, nil
}

func parseField(name string, raw json.RawMessage) (Field, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return Field{}, errors.New("must be an object")
	}
	f := Field{Name: name}
	if err := json.Unmarshal(m["type"], &f.Type); err != nil || fieldKeys[f.Type] == nil {
		return Field{}, errors.New(`type must be "string", "integer", "number" or "boolean"`)
	}
	for k := range m {
		if !slices.Contains(fieldKeys[f.Type], k) {
			return Field{}, fmt.Errorf("keyword %q is not supported for a %s", k, f.Type)
		}
	}
	text := func(key string, limit int, into *string) error {
		v, ok := m[key]
		if !ok {
			return nil
		}
		if err := json.Unmarshal(v, into); err != nil || utf8.RuneCountInString(*into) > limit {
			return fmt.Errorf("%s must be a string of at most %d characters", key, limit)
		}
		return nil
	}
	if err := text("title", maxTitle, &f.Title); err != nil {
		return Field{}, err
	}
	if err := text("description", maxDescription, &f.Description); err != nil {
		return Field{}, err
	}
	if v, ok := m["writeOnly"]; ok {
		if err := json.Unmarshal(v, &f.WriteOnly); err != nil {
			return Field{}, errors.New("writeOnly must be true or false")
		}
	}
	count := func(key string) (*int, error) {
		v, ok := m[key]
		if !ok {
			return nil, nil
		}
		var n int
		if err := json.Unmarshal(v, &n); err != nil || n < 0 || n > MaxString {
			return nil, fmt.Errorf("%s must be a whole number from 0 to %d", key, MaxString)
		}
		return &n, nil
	}
	bound := func(key string) (*float64, error) {
		v, ok := m[key]
		if !ok {
			return nil, nil
		}
		var n float64
		if err := json.Unmarshal(v, &n); err != nil || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%s must be a number", key)
		}
		return &n, nil
	}
	var err error
	if f.MinLength, err = count("minLength"); err != nil {
		return Field{}, err
	}
	if f.MaxLength, err = count("maxLength"); err != nil {
		return Field{}, err
	}
	if f.MinLength != nil && f.MaxLength != nil && *f.MinLength > *f.MaxLength {
		return Field{}, errors.New("minLength is above maxLength")
	}
	if f.Minimum, err = bound("minimum"); err != nil {
		return Field{}, err
	}
	if f.Maximum, err = bound("maximum"); err != nil {
		return Field{}, err
	}
	if f.Minimum != nil && f.Maximum != nil && *f.Minimum > *f.Maximum {
		return Field{}, errors.New("minimum is above maximum")
	}
	if v, ok := m["enum"]; ok {
		if err := json.Unmarshal(v, &f.Enum); err != nil || len(f.Enum) == 0 || len(f.Enum) > maxEnum {
			return Field{}, fmt.Errorf("enum must be a list of 1 to %d strings", maxEnum)
		}
		for i, e := range f.Enum {
			if utf8.RuneCountInString(e) > maxEnumValue || slices.Contains(f.Enum[:i], e) {
				return Field{}, fmt.Errorf("enum values must be unique strings of at most %d characters", maxEnumValue)
			}
		}
	}
	if v, ok := m["default"]; ok {
		if string(bytes.TrimSpace(v)) == "null" {
			return Field{}, errors.New("default must not be null")
		}
		if err := f.Check(v); err != nil {
			return Field{}, fmt.Errorf("default %w", err)
		}
		f.Default = compact(v)
	}
	return f, nil
}

// Check reports why v is not a value of f, as "must ..."; nil when it is one.
func (f Field) Check(v json.RawMessage) error {
	switch f.Type {
	case "boolean":
		var b bool
		if json.Unmarshal(v, &b) != nil {
			return errors.New("must be true or false")
		}
	case "string":
		var s string
		if json.Unmarshal(v, &s) != nil {
			return errors.New("must be text")
		}
		n := utf8.RuneCountInString(s)
		if n > MaxString {
			return fmt.Errorf("must be at most %d characters", MaxString)
		}
		if f.MaxLength != nil && n > *f.MaxLength {
			return fmt.Errorf("must be at most %d characters", *f.MaxLength)
		}
		if f.MinLength != nil && n < *f.MinLength {
			return fmt.Errorf("must be at least %d characters", *f.MinLength)
		}
		if f.Enum != nil && !slices.Contains(f.Enum, s) {
			return fmt.Errorf("must be one of %s", strings.Join(f.Enum, ", "))
		}
	case "integer", "number":
		n, err := exact(v)
		if err != nil {
			return err
		}
		if f.Type == "integer" && (!n.IsInt() || n.Cmp(maxSafeInt) > 0 || n.Cmp(minSafeInt) < 0) {
			return errors.New("must be a whole number from -(2^53) to 2^53")
		}
		if f.Minimum != nil && n.Cmp(new(big.Float).SetFloat64(*f.Minimum)) < 0 {
			return fmt.Errorf("must be at least %s", num(*f.Minimum))
		}
		if f.Maximum != nil && n.Cmp(new(big.Float).SetFloat64(*f.Maximum)) > 0 {
			return fmt.Errorf("must be at most %s", num(*f.Maximum))
		}
	}
	return nil
}

// The integers every JSON reader holds exactly.
var (
	maxSafeInt = new(big.Float).SetInt64(1 << 53)
	minSafeInt = new(big.Float).SetInt64(-(1 << 53))
)

// exact reads a JSON number at a precision far beyond float64, so a value just past a bound or
// just off a whole number is not rounded onto it.
func exact(v json.RawMessage) (*big.Float, error) {
	var n json.Number
	text := string(bytes.TrimSpace(v))
	if len(text) > 100 || json.Unmarshal(v, &n) != nil || !strings.ContainsAny(text[:1], "-0123456789") {
		return nil, errors.New("must be a number")
	}
	f, _, err := big.ParseFloat(n.String(), 10, 1024, big.ToNearestEven)
	if err != nil || f.IsInf() {
		return nil, errors.New("must be a number")
	}
	return f, nil
}

func num(f float64) string { return fmt.Sprintf("%g", f) }

func compact(v json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}

// Values are settings by field name, each a JSON value.
type Values map[string]json.RawMessage

// Stored is what core keeps of an extension's settings: the values, and which of them were
// saved as secrets, so a later schema that drops writeOnly never shows them.
type Stored struct {
	Values    Values   `json:"values"`
	WriteOnly []string `json:"write_only,omitempty"`
}

// kept returns the stored values this schema may use: fields it declares, and no secret it no
// longer treats as one.
func (s *Schema) kept(stored Stored) Values {
	out := Values{}
	for _, f := range s.Fields {
		v, ok := stored.Values[f.Name]
		if ok && (f.WriteOnly || !slices.Contains(stored.WriteOnly, f.Name)) {
			out[f.Name] = v
		}
	}
	return out
}

// Merge applies change to stored: a field set to null goes back to its default. It returns the
// result, keeping only fields the schema declares, or the first problem with it as
// "<field> must ...".
func (s *Schema) Merge(stored Stored, change Values) (Stored, error) {
	next := s.kept(stored)
	for name, v := range change {
		f, ok := s.Field(name)
		if !ok {
			return Stored{}, fmt.Errorf("%s is not a setting of this extension", name)
		}
		if string(bytes.TrimSpace(v)) == "null" {
			delete(next, name)
			continue
		}
		if err := f.Check(v); err != nil {
			return Stored{}, fmt.Errorf("%s %w", name, err)
		}
		next[name] = compact(v)
	}
	out := Stored{Values: next}
	for _, f := range s.Fields {
		v, ok := next[f.Name]
		if !ok {
			if f.Required && f.Default == nil {
				return Stored{}, fmt.Errorf("%s must have a value", f.Name)
			}
			continue
		}
		// A value kept from an earlier version of the schema is checked again too.
		if err := f.Check(v); err != nil {
			return Stored{}, fmt.Errorf("%s %w", f.Name, err)
		}
		if f.WriteOnly {
			out.WriteOnly = append(out.WriteOnly, f.Name)
		}
	}
	return out, nil
}

// View is what core shows of an extension's settings: never a writeOnly value.
type View struct {
	// Values holds every field with a value or a default, except writeOnly fields.
	Values Values `json:"values"`
	// Set names the writeOnly fields that have a value.
	Set []string `json:"set"`
}

// View returns what may be shown of stored.
func (s *Schema) View(stored Stored) View {
	kept := s.kept(stored)
	v := View{Values: Values{}, Set: []string{}}
	for _, f := range s.Fields {
		val, ok := kept[f.Name]
		if f.WriteOnly {
			if ok {
				v.Set = append(v.Set, f.Name)
			}
			continue
		}
		if !ok {
			val = f.Default
		}
		if val != nil {
			v.Values[f.Name] = val
		}
	}
	return v
}
