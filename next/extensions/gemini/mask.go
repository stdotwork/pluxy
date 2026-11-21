package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A client holding a pluxy key must not learn which Google project or account the owner's upstream
// account is. Google names it in its errors, in the message and in the details; the details stay
// (clients read RetryInfo and QuotaFailure) and what names the project is masked
// (geminigenerate.MaskProject).
//
// masked wraps the handlers whose errors reach clients: Generate, CountTokens and Endpoint.
// Login, Check, Quota and Models are the owner's, who reads them to find which project needs an
// API enabled, and are not wrapped. An error is masked after the handler classified it, so its
// code, scope and delays are the ones the handler chose.
func masked(h sdk.Handler) sdk.Handler {
	return func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
		err := h(ctx, call, out)
		if err == nil {
			return nil
		}
		known := knownNames(call.Account)
		var e *llm.Error
		if errors.As(err, &e) {
			return geminigenerate.MaskProject(e, known...)
		}
		// Any other error becomes an internal error with its text, which can hold a request URL
		// that names the project.
		m := geminigenerate.MaskProject(&llm.Error{Code: llm.CodeInternal, Message: err.Error()}, known...)
		if m.Message == err.Error() {
			return err
		}
		if ctx.Err() != nil {
			m.Code = llm.CodeCancelled
		}
		return m
	}
}

// knownNames are what the account itself says is its Google project or identity: the project of
// its Vertex settings, and its service account key's project and address.
func knownNames(a protocol.Account) []string {
	var known []string
	if len(a.Settings) > 0 {
		var s struct {
			Vertex *struct {
				Project string `json:"project"`
			} `json:"vertex"`
		}
		if json.Unmarshal(a.Settings, &s) == nil && s.Vertex != nil {
			known = append(known, s.Vertex.Project)
		}
	}
	if c := a.Credential; c != nil && c.Kind == CredentialServiceAccount && len(c.Data) > 0 {
		var k struct {
			ProjectID   string `json:"project_id"`
			ClientEmail string `json:"client_email"`
		}
		if json.Unmarshal(c.Data, &k) == nil {
			known = append(known, k.ProjectID, k.ClientEmail)
		}
	}
	return known
}

// maskReply masks a reply or an event an Interactions client gets as the upstream sent it: the
// data of an event named "error" is masked whole. When the reply's status (at the top level or in
// a top-level object member such as interaction) is "failed", the value of every "error" member at
// any depth, inside arrays too, is masked. Any other reply keeps today's rule: only the value of an
// "error" member (an object or a string) at the top level of the JSON or one object level down,
// never inside an array. Everything outside those values keeps its bytes, and a reply with nothing
// to mask is returned as it is.
func maskReply(data []byte, event string, known []string) []byte {
	if event == "error" {
		return geminigenerate.MaskBody(data, known...)
	}
	if !bytes.Contains(data, []byte(`"error"`)) || !json.Valid(data) {
		return data
	}
	var spans [][2]int
	if isFailedReply(data) {
		collectAllErrorSpans(data, 0, len(data), &spans)
	} else {
		collectErrorSpans(data, 0, len(data), 0, &spans)
	}
	if len(spans) == 0 {
		return data
	}
	var out []byte
	last := 0
	for _, sp := range spans {
		orig := data[sp[0]:sp[1]]
		masked := geminigenerate.MaskBody(orig, known...)
		if !bytes.Equal(masked, orig) {
			if out == nil {
				out = make([]byte, 0, len(data)+16)
			}
			out = append(out, data[last:sp[0]]...)
			out = append(out, masked...)
			last = sp[1]
		}
	}
	if out == nil {
		return data
	}
	return append(out, data[last:]...)
}

func isFailedReply(raw []byte) bool {
	if !bytes.Contains(raw, []byte(`"failed"`)) {
		return false
	}
	i := skipWS(raw, 0)
	if i >= len(raw) || raw[i] != '{' {
		return false
	}
	i++
	for {
		i = skipWS(raw, i)
		if i >= len(raw) || raw[i] == '}' {
			return false
		}
		ke := skipString(raw, i)
		var k string
		_ = json.Unmarshal(raw[i:ke], &k)
		i = skipWS(raw, ke)
		if i >= len(raw) || raw[i] != ':' {
			return false
		}
		vs := skipWS(raw, i+1)
		ve := skipValue(raw, vs)
		if vs < ve {
			if k == "status" && isStatusFailed(raw[vs:ve]) {
				return true
			}
			if raw[vs] == '{' && hasStatusFailed(raw[vs:ve]) {
				return true
			}
		}
		i = skipWS(raw, ve)
		if i < len(raw) && raw[i] == ',' {
			i++
		} else {
			return false
		}
	}
}

func hasStatusFailed(obj []byte) bool {
	i := skipWS(obj, 0)
	if i >= len(obj) || obj[i] != '{' {
		return false
	}
	i++
	for {
		i = skipWS(obj, i)
		if i >= len(obj) || obj[i] == '}' {
			return false
		}
		ke := skipString(obj, i)
		var k string
		_ = json.Unmarshal(obj[i:ke], &k)
		i = skipWS(obj, ke)
		if i >= len(obj) || obj[i] != ':' {
			return false
		}
		vs := skipWS(obj, i+1)
		ve := skipValue(obj, vs)
		if vs < ve && k == "status" && isStatusFailed(obj[vs:ve]) {
			return true
		}
		i = skipWS(obj, ve)
		if i < len(obj) && obj[i] == ',' {
			i++
		} else {
			return false
		}
	}
}

func isStatusFailed(val []byte) bool {
	var s string
	return json.Unmarshal(val, &s) == nil && s == "failed"
}

// collectAllErrorSpans appends the [start, end) byte ranges of "error" member values (objects or
// strings) at any depth, descending into arrays and objects.
func collectAllErrorSpans(raw []byte, start, end int, spans *[][2]int) {
	i := skipWS(raw, start)
	if i >= end {
		return
	}
	switch raw[i] {
	case '{':
		i++
		for {
			i = skipWS(raw, i)
			if i >= end || raw[i] == '}' {
				return
			}
			ke := skipString(raw, i)
			var k string
			_ = json.Unmarshal(raw[i:ke], &k)
			i = skipWS(raw, ke)
			if i >= end || raw[i] != ':' {
				return
			}
			vs := skipWS(raw, i+1)
			ve := skipValue(raw, vs)
			if vs < ve {
				if k == "error" && (raw[vs] == '{' || raw[vs] == '"') {
					*spans = append(*spans, [2]int{vs, ve})
				} else if raw[vs] == '{' || raw[vs] == '[' {
					collectAllErrorSpans(raw, vs, ve, spans)
				}
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else {
				return
			}
		}
	case '[':
		i++
		for {
			vs := skipWS(raw, i)
			if vs >= end || raw[vs] == ']' {
				return
			}
			ve := skipValue(raw, vs)
			if vs < ve {
				if raw[vs] == '{' || raw[vs] == '[' {
					collectAllErrorSpans(raw, vs, ve, spans)
				}
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else {
				return
			}
		}
	}
}

// collectErrorSpans appends the [start, end) byte ranges of "error" member values that are JSON
// objects or strings at depth 0 (top-level object) or depth 1 (one object level down), never
// descending into arrays or beyond depth 1.
func collectErrorSpans(raw []byte, start, end, depth int, spans *[][2]int) {
	i := skipWS(raw, start)
	if i >= end || raw[i] != '{' {
		return
	}
	i++
	for {
		i = skipWS(raw, i)
		if i >= end || raw[i] == '}' {
			return
		}
		ke := skipString(raw, i)
		var k string
		_ = json.Unmarshal(raw[i:ke], &k)
		i = skipWS(raw, ke)
		if i >= end || raw[i] != ':' {
			return
		}
		vs := skipWS(raw, i+1)
		ve := skipValue(raw, vs)
		if vs < ve {
			switch {
			case k == "error":
				if raw[vs] == '{' || raw[vs] == '"' {
					*spans = append(*spans, [2]int{vs, ve})
				}
			case depth == 0 && raw[vs] == '{':
				collectErrorSpans(raw, vs, ve, 1, spans)
			}
		}
		i = skipWS(raw, ve)
		if i < end && raw[i] == ',' {
			i++
		} else {
			return
		}
	}
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

func skipString(b []byte, i int) int {
	if i >= len(b) || b[i] != '"' {
		return i
	}
	i++
	for i < len(b) {
		if b[i] == '\\' {
			i += 2
			continue
		}
		if b[i] == '"' {
			return i + 1
		}
		i++
	}
	return len(b)
}

func skipValue(b []byte, i int) int {
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		depth := 1
		i++
		for i < len(b) && depth > 0 {
			switch b[i] {
			case '"':
				i = skipString(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
			i++
		}
		return i
	default:
		for i < len(b) {
			switch b[i] {
			case ' ', '\t', '\n', '\r', ',', '}', ']':
				return i
			}
			i++
		}
		return i
	}
}
