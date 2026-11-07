package corpus

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Some upstreams (Devin's API server) speak Connect (connectrpc.com) with protobuf messages,
// which are not JSON. A case scripts such a reply as Connect frames whose messages are written in
// JSON (Reply.Connect, ConnectFrame), and the corpus decodes a recorded protobuf request into the
// same JSON form, with the provider's ProtoSchema, so it is compared like any other body.
//
// The JSON form of a message is an object keyed by field number. A string is a length-delimited
// field holding text, a number a varint, an object a nested message, {"double": x} a 64-bit
// float, {"fixed32": n} a 32-bit fixed value, {"bytes": "base64"} length-delimited bytes that are
// not text, and an array a field that repeats. Each wire type has its own form, so a field written
// with the wrong one does not compare equal.

// ConnectFrame is one frame of a scripted Connect reply: a data frame with Message, or the end
// frame with End, its JSON trailer ({} for a clean end, {"error": {"code", "message"}}).
type ConnectFrame struct {
	Message json.RawMessage `json:"message,omitempty"`
	End     json.RawMessage `json:"end,omitempty"`
}

// ProtoSchema says how to decode a provider's protobuf requests: which fields hold nested
// messages and which repeat, each named by its path of field numbers ("3.6" is field 6 of a
// message in field 3). Any other length-delimited field is text, or bytes when it is not UTF-8.
type ProtoSchema struct {
	Messages []string
	Repeated []string
}

// encodeConnect writes a scripted Connect reply's frames.
func encodeConnect(frames []ConnectFrame) ([]byte, error) {
	var out bytes.Buffer
	for i, f := range frames {
		flags, payload := byte(0), []byte{}
		switch {
		case len(f.End) > 0:
			flags = 2
			var c bytes.Buffer
			if err := json.Compact(&c, f.End); err != nil {
				return nil, fmt.Errorf("connect[%d].end: %w", i, err)
			}
			payload = c.Bytes()
		case len(f.Message) > 0:
			var v any
			dec := json.NewDecoder(bytes.NewReader(f.Message))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				return nil, fmt.Errorf("connect[%d].message: %w", i, err)
			}
			m, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("connect[%d].message: not an object", i)
			}
			var err error
			if payload, err = encodeMessage(m); err != nil {
				return nil, fmt.Errorf("connect[%d].message: %w", i, err)
			}
		default:
			return nil, fmt.Errorf("connect[%d]: set message or end", i)
		}
		var h [5]byte
		h[0] = flags
		binary.BigEndian.PutUint32(h[1:], uint32(len(payload)))
		out.Write(h[:])
		out.Write(payload)
	}
	return out.Bytes(), nil
}

// encodeMessage writes a message from its JSON form, fields in ascending number order.
func encodeMessage(m map[string]any) ([]byte, error) {
	nums := make([]int, 0, len(m))
	for k := range m {
		n, err := strconv.Atoi(k)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("field %q is not a field number", k)
		}
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var out []byte
	for _, n := range nums {
		v := m[strconv.Itoa(n)]
		vals, repeated := v.([]any)
		if !repeated {
			vals = []any{v}
		}
		for _, val := range vals {
			var err error
			if out, err = encodeField(out, n, val); err != nil {
				return nil, fmt.Errorf("field %d: %w", n, err)
			}
		}
	}
	return out, nil
}

func encodeField(out []byte, n int, v any) ([]byte, error) {
	tag := func(wire int) []byte { return binary.AppendUvarint(out, uint64(n)<<3|uint64(wire)) }
	delimited := func(b []byte) []byte {
		o := binary.AppendUvarint(tag(2), uint64(len(b)))
		return append(o, b...)
	}
	switch x := v.(type) {
	case string:
		return delimited([]byte(x)), nil
	case json.Number:
		u, err := strconv.ParseUint(x.String(), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("varint %s: %w", x, err)
		}
		return binary.AppendUvarint(tag(0), u), nil
	case map[string]any:
		if d, ok := x["double"].(json.Number); ok && len(x) == 1 {
			f, err := d.Float64()
			if err != nil {
				return nil, err
			}
			return binary.LittleEndian.AppendUint64(tag(1), math.Float64bits(f)), nil
		}
		if d, ok := x["fixed32"].(json.Number); ok && len(x) == 1 {
			u, err := strconv.ParseUint(d.String(), 10, 32)
			if err != nil {
				return nil, fmt.Errorf("fixed32 %s: %w", d, err)
			}
			return binary.LittleEndian.AppendUint32(tag(5), uint32(u)), nil
		}
		if s, ok := x["bytes"].(string); ok && len(x) == 1 {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return nil, err
			}
			return delimited(b), nil
		}
		b, err := encodeMessage(x)
		if err != nil {
			return nil, err
		}
		return delimited(b), nil
	}
	return nil, fmt.Errorf("value %v has no protobuf form", v)
}

// decodeConnectRequest decodes a request body of one Connect data frame (or, for a unary call,
// a bare message) into the JSON form, or returns false when it is not one.
func decodeConnectRequest(body []byte, enveloped bool, schema *ProtoSchema) (any, bool) {
	msg := body
	if enveloped {
		if len(body) < 5 || body[0] != 0 || int(binary.BigEndian.Uint32(body[1:5])) != len(body)-5 {
			return nil, false
		}
		msg = body[5:]
	}
	v, err := decodeMessage(msg, "", schema)
	if err != nil {
		return nil, false
	}
	return v, true
}

func decodeMessage(b []byte, path string, schema *ProtoSchema) (map[string]any, error) {
	out := map[string]any{}
	for len(b) > 0 {
		t, k := binary.Uvarint(b)
		if k <= 0 || t>>3 == 0 {
			return nil, fmt.Errorf("bad tag")
		}
		b = b[k:]
		n, wire := int(t>>3), int(t&7)
		fpath := strconv.Itoa(n)
		if path != "" {
			fpath = path + "." + fpath
		}
		var v any
		switch wire {
		case 0:
			u, k := binary.Uvarint(b)
			if k <= 0 {
				return nil, fmt.Errorf("bad varint")
			}
			v, b = json.Number(strconv.FormatUint(u, 10)), b[k:]
		case 1:
			if len(b) < 8 {
				return nil, fmt.Errorf("short fixed64")
			}
			f := math.Float64frombits(binary.LittleEndian.Uint64(b))
			v, b = map[string]any{"double": json.Number(strconv.FormatFloat(f, 'g', -1, 64))}, b[8:]
		case 2:
			l, k := binary.Uvarint(b)
			if k <= 0 || l > uint64(len(b)-k) {
				return nil, fmt.Errorf("bad length")
			}
			data := b[k : k+int(l)]
			b = b[k+int(l):]
			switch {
			case slices.Contains(schema.Messages, fpath):
				m, err := decodeMessage(data, fpath, schema)
				if err != nil {
					return nil, err
				}
				v = m
			case utf8.Valid(data):
				v = string(data)
			default:
				v = map[string]any{"bytes": base64.StdEncoding.EncodeToString(data)}
			}
		case 5:
			if len(b) < 4 {
				return nil, fmt.Errorf("short fixed32")
			}
			v, b = map[string]any{"fixed32": json.Number(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10))}, b[4:]
		default:
			return nil, fmt.Errorf("wire type %d", wire)
		}
		key := strconv.Itoa(n)
		if slices.Contains(schema.Repeated, fpath) {
			list, _ := out[key].([]any)
			out[key] = append(list, v)
			continue
		}
		out[key] = v
	}
	return out, nil
}

// isConnectProto reports whether a request's content type is protobuf over Connect: enveloped
// for a streaming call, bare for a unary one.
func isConnectProto(contentType string) (proto, enveloped bool) {
	ct, _, _ := strings.Cut(contentType, ";")
	switch strings.TrimSpace(ct) {
	case "application/connect+proto":
		return true, true
	case "application/proto":
		return true, false
	}
	return false, false
}
