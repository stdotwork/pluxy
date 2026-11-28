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
		return &typeError{data: data, t: e.Type()}
	}
	e.SetZero()
	return json.Unmarshal(data, v)
}

func StdError(err error) error {
	if te, ok := err.(*typeError); ok {
		return te.Unwrap()
	}
	return err
}

type typeError struct {
	data []byte
	t    reflect.Type
	once sync.Once
	err  error
}

func (e *typeError) Error() string { return e.Unwrap().Error() }

func (e *typeError) Unwrap() error {
	e.once.Do(func() { e.err = json.Unmarshal(e.data, reflect.New(e.t).Interface()) })
	return e.err
}

type result int

const (
	decoded result = iota
	mismatch
	other
)

type decodeFunc func(v reflect.Value, data []byte, top bool) result

var decoders sync.Map

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
			return nil
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
			if !v.IsNil() {
				// A repeated key: json.Unmarshal decodes into the slice already there.
				return other
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
						return other
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

func byEncodingJSON(v reflect.Value, data []byte, top bool) result {
	if top || !v.CanAddr() || json.Unmarshal(data, v.Addr().Interface()) != nil {
		return other
	}
	return decoded
}

func objectOf(data []byte, top bool) ([]Member, bool) {
	if top {
		return Object(data)
	}
	if v := Trim(data); len(v) == 0 || v[0] != '{' {
		return nil, false
	}
	return members(data), true
}

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

func plainName(s string) bool {
	for _, c := range []byte(s) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}
