package rawjson

import (
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
	"github.com/stdotwork/pluxy/next/llm"
)

func Object(raw []byte) map[string]json.RawMessage {
	members, ok := jsonscan.Object(raw)
	if !ok {
		return nil
	}
	m := make(map[string]json.RawMessage, len(members))
	for _, mem := range members {
		k, ok := jsonscan.Unquote(mem.Key)
		if !ok {
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			return m
		}
		m[k] = mem.Value
	}
	return m
}

func Rest(m map[string]json.RawMessage, modelled ...string) json.RawMessage {
	rest := map[string]json.RawMessage{}
	for k, v := range m {
		if !slices.Contains(modelled, k) {
			rest[k] = v
		}
	}
	return Encode(rest)
}

func Encode(m map[string]json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func Origin(format, path string, m map[string]json.RawMessage, modelled []string, nested map[string]json.RawMessage) *llm.Origin {
	rest := map[string]json.RawMessage{}
	for k, v := range m {
		if !slices.Contains(modelled, k) {
			rest[k] = v
		}
	}
	for k, v := range nested {
		if len(v) > 0 {
			rest[k] = v
		}
	}
	return &llm.Origin{Format: format, Path: path, Raw: Encode(rest)}
}

func Merge(obj []byte, raw json.RawMessage) []byte {
	extra := Object(raw)
	if len(extra) == 0 {
		return obj
	}
	m := Object(obj)
	if m == nil {
		return obj
	}
	for k, v := range extra {
		cur, ok := m[k]
		switch {
		case !ok:
			m[k] = v
		case Object(cur) != nil && Object(v) != nil:
			m[k] = Merge(cur, v)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return obj
	}
	return b
}

func Losses(o *llm.Origin, to string) []llm.Loss {
	if o == nil || o.Format == to || len(o.Raw) == 0 {
		return nil
	}
	m := Object(o.Raw)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]llm.Loss, 0, len(keys))
	for _, k := range keys {
		out = append(out, llm.Loss{Kind: llm.LossDropped, Path: Join(o.Path, k), Detail: "no place for it in " + to})
	}
	return out
}

func Text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

func With(modelled []string, extra ...string) []string {
	return slices.Concat(modelled, extra)
}

func Join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
