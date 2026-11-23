package jsonscan

import (
	"bytes"
	"encoding"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

// Unmarshal decodes data into v with the result and the error json.Unmarshal gives. It checks
// data once and decodes structs, strings, booleans, numbers, json.RawMessage, and slices, maps
// and pointers of those itself; a value of any other type (json.Number among them) it hands to
// json.Unmarshal, as it does all of data when it meets text, a type or a name it does not decode
// exactly as json.Unmarshal would. The json.RawMessage values it decodes itself are slices of
// data (each with no room after it, so appending to one copies it); those json.Unmarshal sets
// are copies. Callers must rely on neither: data must not change while the values are in use,
// and a value must not be changed in place. v must point to a zero value; otherwise it goes to
// json.Unmarshal whole.
func Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return json.Unmarshal(data, v)
	}
	e := rv.Elem()
	dec := decoderFor(e.Type())
	if dec == nil || !e.IsZero() {
		return json.Unmarshal(data, v)
	}
	switch dec(e, data, true) {
	case decoded:
		return nil
	case mismatch:
		// The value at the top is of another JSON type than v: json.Unmarshal leaves v as it
		// is and fails with a type error. Callers often try one type and then another, so the
		// error's text is made only when it is read.
		return &typeError{data: data, t: e.Type()}
	}
	e.SetZero()
	return json.Unmarshal(data, v)
}

// StdError returns json.Unmarshal's own error where err is the error Unmarshal gives for a value
// of another JSON type than v, which only wraps it (and makes it when read): for a caller that
// passes the error on and must give the one json.Unmarshal gives, type and all. Any other error
// it returns as it is.
func StdError(err error) error {
	if te, ok := err.(*typeError); ok {
		return te.Unwrap()
	}
	return err
}

// typeError is json.Unmarshal's error for data of another JSON type than t, made when read.
type typeError struct {
	data []byte
	t    reflect.Type
	once sync.Once
	err  error
}

func (e *typeError) Error() string { return e.Unwrap().Error() }

// Unwrap returns json.Unmarshal's own error, so errors.As finds a *json.UnmarshalTypeError.
func (e *typeError) Unwrap() error {
	e.once.Do(func() { e.err = json.Unmarshal(e.data, reflect.New(e.t).Interface()) })
	return e.err
}

type result int

const (
	decoded  result = iota
	mismatch        // the top value is of a JSON type the target cannot hold; nothing was set
	other           // anything else: json.Unmarshal decides
)

// decodeFunc decodes data into v. At the top, data is the whole text, not yet checked;
// below it, one valid JSON value.
type decodeFunc func(v reflect.Value, data []byte, top bool) result

var decoders sync.Map // reflect.Type → decodeFunc, nil for types Unmarshal leaves to json.Unmarshal

func decoderFor(t reflect.Type) decodeFunc {
	if d, ok := decoders.Load(t); ok {
		return d.(decodeFunc)
	}
	d := build(t, map[reflect.Type]bool{})
	decoders.Store(t, d)
	return d
}

var (
	rawMessageType      = reflect.TypeFor[json.RawMessage]()
	numberType          = reflect.TypeFor[json.Number]()
	unmarshalerType     = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

// scalar checks data at the top (it must be one valid JSON value) and returns it without the
// whitespace around it, or ok false.
func scalar(data []byte, top bool) ([]byte, bool) {
	if top && !Valid(data) {
		return nil, false
	}
	return Trim(data), true
}

func build(t reflect.Type, building map[reflect.Type]bool) decodeFunc {
	if t == rawMessageType {
		return func(v reflect.Value, data []byte, top bool) result {
			data, ok := scalar(data, top)
			if !ok {
				return other
			}
			v.SetBytes(data[:len(data):len(data)])
			return decoded
		}
	}
	// json.Number is a string that json.Unmarshal fills from numbers, and from strings only
	// when they hold one: encoding/json decodes it.
	if t == numberType || building[t] || reflect.PointerTo(t).Implements(unmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return nil
	}
	building[t] = true
	defer delete(building, t)
	switch t.Kind() {
	case reflect.String:
		return func(v reflect.Value, data []byte, top bool) result {
			data, ok := scalar(data, top)
			switch {
			case !ok:
				return other
			case IsNull(data):
				return decoded
			case !IsString(data):
				return mismatchAt(top)
			}
			s, ok := Unquote(data)
			if !ok {
				return other
			}
			v.SetString(s)
			return decoded
		}
	case reflect.Bool:
		return func(v reflect.Value, data []byte, top bool) result {
			data, ok := scalar(data, top)
			switch {
			case !ok:
				return other
			case IsNull(data):
			case string(data) == "true":
				v.SetBool(true)
			case string(data) == "false":
				v.SetBool(false)
			default:
				return mismatchAt(top)
			}
			return decoded
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return func(v reflect.Value, data []byte, top bool) result {
			data, ok := scalar(data, top)
			switch {
			case !ok:
				return other
			case IsNull(data):
				return decoded
			case !isNumber(data):
				return mismatchAt(top)
			}
			n, err := strconv.ParseInt(string(data), 10, 64)
			if err != nil || v.OverflowInt(n) {
				return other
			}
			v.SetInt(n)
			return decoded
		}
	case reflect.Float32, reflect.Float64:
		return func(v reflect.Value, data []byte, top bool) result {
			data, ok := scalar(data, top)
			switch {
			case !ok:
				return other
			case IsNull(data):
				return decoded
			case !isNumber(data):
				return mismatchAt(top)
			}
			f, err := strconv.ParseFloat(string(data), t.Bits())
			if err != nil || v.OverflowFloat(f) {
				return other
			}
			v.SetFloat(f)
			return decoded
		}
	case reflect.Pointer:
		elem := build(t.Elem(), building)
		if elem == nil {
			return nil
		}
		return func(v reflect.Value, data []byte, top bool) result {
			if trimmed := Trim(data); IsNull(trimmed) {
				if top && !Valid(data) {
					return other
				}
				v.SetZero()
				return decoded
			}
			if v.IsNil() {
				v.Set(reflect.New(t.Elem()))
			}
			if r := elem(v.Elem(), data, top); r != decoded {
				return other // json.Unmarshal leaves the new value in place: let it decide
			}
			return decoded
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil // base64 text
		}
		elem := build(t.Elem(), building)
		if elem == nil {
			return nil
		}
		return func(v reflect.Value, data []byte, top bool) result {
			elems, ok := arrayOf(data, top)
			if !ok {
				return notContainer(v, data, top)
			}
			s := reflect.MakeSlice(t, len(elems), len(elems))
			for i, el := range elems {
				if elem(s.Index(i), el, false) != decoded {
					return other
				}
			}
			v.Set(s)
			return decoded
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String || t.Key() != reflect.TypeFor[string]() {
			return nil
		}
		elem := build(t.Elem(), building)
		if elem == nil {
			return nil
		}
		return func(v reflect.Value, data []byte, top bool) result {
			members, ok := objectOf(data, top)
			if !ok {
				return notContainer(v, data, top)
			}
			if v.IsNil() {
				v.Set(reflect.MakeMapWithSize(t, len(members)))
			}
			for _, m := range members {
				k, ok := Unquote(m.Key)
				if !ok {
					return other
				}
				ev := reflect.New(t.Elem()).Elem()
				if elem(ev, m.Value, false) != decoded {
					return other
				}
				v.SetMapIndex(reflect.ValueOf(k), ev)
			}
			return decoded
		}
	case reflect.Struct:
		fields, ok := structFields(t, building)
		if !ok {
			return nil
		}
		return func(v reflect.Value, data []byte, top bool) result {
			members, ok := objectOf(data, top)
			if !ok {
				return notContainer(v, data, top)
			}
			for _, m := range members {
				f := fields.find(m.Key)
				if f == nil {
					if bytes.IndexByte(m.Key, '\\') >= 0 {
						return other // an escaped name: json.Unmarshal matches it
					}
					continue
				}
				if f.dec(v.Field(f.index), m.Value, false) != decoded {
					return other
				}
			}
			return decoded
		}
	}
	return nil
}

// byEncodingJSON decodes a field of a type Unmarshal does not decode itself with json.Unmarshal,
// which gives what decoding it as part of the struct gives when it succeeds; when it fails, all
// of the text goes to json.Unmarshal.
func byEncodingJSON(v reflect.Value, data []byte, top bool) result {
	if top || !v.CanAddr() || json.Unmarshal(data, v.Addr().Interface()) != nil {
		return other
	}
	return decoded
}

// objectOf returns the members of data: checked at the top, split without a check below it,
// where the text was checked with the value holding it.
func objectOf(data []byte, top bool) ([]Member, bool) {
	if top {
		return Object(data)
	}
	if v := Trim(data); len(v) == 0 || v[0] != '{' {
		return nil, false
	}
	return members(data), true
}

// arrayOf is objectOf for arrays.
func arrayOf(data []byte, top bool) ([][]byte, bool) {
	if top {
		return Array(data)
	}
	if v := Trim(data); len(v) == 0 || v[0] != '[' {
		return nil, false
	}
	return elements(data), true
}

func mismatchAt(top bool) result {
	if top {
		return mismatch
	}
	return other
}

// notContainer is the result for data that is not the array or object a slice, map or struct
// needs: null sets a slice or map to nil and leaves a struct as it is, as json.Unmarshal does.
func notContainer(v reflect.Value, data []byte, top bool) result {
	if top && !Valid(data) {
		return other
	}
	if IsNull(Trim(data)) {
		if v.Kind() == reflect.Slice || v.Kind() == reflect.Map {
			v.SetZero()
		}
		return decoded
	}
	return mismatchAt(top)
}

func isNumber(data []byte) bool {
	return len(data) > 0 && (data[0] == '-' || '0' <= data[0] && data[0] <= '9')
}

type field struct {
	name  string
	index int
	dec   decodeFunc
}

type fieldList []field

// find returns the field a member's name (as written) goes to, as json.Unmarshal matches it:
// the field of that exact name, else one whose name is the same but for letter case. A name with
// escapes matches nothing here.
func (fs fieldList) find(key []byte) *field {
	name := key[1 : len(key)-1]
	if bytes.IndexByte(name, '\\') >= 0 {
		return nil
	}
	for i := range fs {
		if string(name) == fs[i].name {
			return &fs[i]
		}
	}
	for i := range fs {
		if bytes.EqualFold(name, []byte(fs[i].name)) {
			return &fs[i]
		}
	}
	return nil
}

// structFields returns the fields json.Unmarshal fills in t, or ok false for a struct whose
// fields it maps in ways this package does not follow: embedded fields, the string option, a
// name json.Unmarshal would not take as written, or two names alike but for letter case.
func structFields(t reflect.Type, building map[reflect.Type]bool) (fieldList, bool) {
	var fs fieldList
	for i := range t.NumField() {
		sf := t.Field(i)
		if sf.Anonymous {
			return nil, false
		}
		if !sf.IsExported() {
			continue
		}
		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(","+opts+",", ",string,") || !plainName(name) {
			return nil, false
		}
		if name == "" {
			name = sf.Name
		}
		for _, f := range fs {
			if strings.EqualFold(f.name, name) {
				return nil, false
			}
		}
		dec := build(sf.Type, building)
		if dec == nil {
			dec = byEncodingJSON
		}
		fs = append(fs, field{name: name, index: i, dec: dec})
	}
	return fs, true
}

// plainName reports whether a tag's name is one json.Unmarshal takes as written: ASCII letters,
// digits and the punctuation request fields use.
func plainName(s string) bool {
	for _, c := range []byte(s) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
