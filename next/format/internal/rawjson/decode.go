package rawjson

import "github.com/stdotwork/pluxy/next/internal/jsonscan"

// Unmarshal decodes data into v with the result and the error json.Unmarshal gives, checking
// data once and decoding the values request parsers read without encoding/json's cost (see
// jsonscan.Unmarshal). The json.RawMessage values it sets may be slices of data or copies:
// data must not change while they are in use, a value must not be changed in place, and v must
// point to a zero value.
func Unmarshal(data []byte, v any) error { return jsonscan.Unmarshal(data, v) }
