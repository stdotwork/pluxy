package geminigenerate

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Redacted stands where MaskProject takes out a project or an account: the text core's own
// redaction uses.
const Redacted = protocol.Redacted

// Google's errors name the Google Cloud project or account an upstream account belongs to: in the
// message ("The consumer 'projects/123' is suspended."), and in the details, where ErrorInfo's
// metadata (consumer, containerInfo, activationUrl), QuotaFailure's subject, ResourceInfo's owner
// and resource_name, and Help's links carry it. A client holding a pluxy key must not learn it.
// The details themselves stay: clients read RetryInfo and QuotaFailure.

// namePatterns are the ways text names a project or an account. Group 1 is the name.
var namePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bprojects/([\w-]+(?:\.[\w-]+)*)`),
	regexp.MustCompile(`(?i)\bproject:([\w-]+(?:\.[\w-]+)*)`),
	regexp.MustCompile(`(?i)\bproject(?:_?id|_?number)?=([\w.-]+)`),
	regexp.MustCompile(`(?i)\bproject (?:number |id )?(\d+)\b`),
	regexp.MustCompile("(?i)\\bproject ['\"`]([^'\"`\\s]+)['\"`]"),
	regexp.MustCompile(`(?i)\b(?:user|serviceAccount):([A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)`),
}

// serviceAccountProject finds the project in a service account's address.
var serviceAccountProject = regexp.MustCompile(`@([a-z][a-z0-9-]*)\.iam\.gserviceaccount\.com$`)

// MaskProject returns e without the Google project or account its message and its raw body name.
// known are identifiers the caller knows belong to the upstream account (its project, a service
// account's project and address); the structured fields of Google's error details and the text
// patterns above add the rest. Each is replaced, as a whole token, in the message and in every
// string value of the raw body (an object, or the array a Google API wraps one in); the keys,
// numbers, types, reasons, quota facts and everything else stay, and a body with nothing to mask
// keeps its bytes. e itself is returned when nothing changed; otherwise a copy, and e is not
// touched. Call it after the error has been classified: it changes no code, scope or delay.
func MaskProject(e *llm.Error, known ...string) *llm.Error {
	if e == nil {
		return nil
	}
	d := newNames(known)
	rawOK := len(e.Raw) > 0 && json.Valid(e.Raw)
	d.learnString(e.Message)
	if rawOK {
		d.learnJSON(e.Raw)
	}
	msg := d.maskString(e.Message)
	raw := e.Raw
	if rawOK {
		raw = d.maskJSON(e.Raw)
	}
	if msg == e.Message && bytes.Equal(raw, e.Raw) {
		return e
	}
	c := *e
	c.Message, c.Raw = msg, raw
	return &c
}

// MaskBody does the same for a body a client gets as it is, such as an error sent inside a
// reply: JSON when it is JSON, otherwise text. A body with nothing to mask is returned itself.
func MaskBody(body []byte, known ...string) []byte {
	d := newNames(known)
	if json.Valid(body) {
		d.learnJSON(body)
		return d.maskJSON(body)
	}
	s := string(body)
	d.learnString(s)
	if m := d.maskString(s); m != s {
		return []byte(m)
	}
	return body
}

// IsGoogleErrorShape reports whether raw is a Google error body: an object with an "error" whose
// code is a number and whose status is a name such as RESOURCE_EXHAUSTED, alone or as the one
// element of an array (as Google's OpenAI-compatible API sends it).
func IsGoogleErrorShape(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil || len(list) == 0 {
			return false
		}
		raw = list[0]
	}
	var b struct {
		Error struct {
			Code   json.RawMessage `json:"code"`
			Status string          `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Error.Status == "" {
		return false
	}
	var n json.Number
	return json.Unmarshal(b.Error.Code, &n) == nil && len(b.Error.Code) > 0 && b.Error.Code[0] != '"'
}

// names holds what to mask.
type names struct {
	list []string
	seen map[string]bool
}

func newNames(known []string) *names {
	d := &names{seen: map[string]bool{}}
	for _, k := range known {
		d.add(k)
	}
	return d
}

func (d *names) add(id string) {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, Redacted) || d.seen[id] {
		return
	}
	d.seen[id] = true
	d.list = append(d.list, id)
	if m := serviceAccountProject.FindStringSubmatch(id); m != nil {
		d.add(m[1])
	}
}

// addProject adds a project named as a resource ("projects/123", "project:123" or a bare ID).
func (d *names) addProject(v string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "projects/"), "project:")
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	d.add(v)
}

// learnString adds the names text holds in the forms of namePatterns.
func (d *names) learnString(s string) {
	for _, re := range namePatterns {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			d.addProject(m[1])
		}
	}
}

// learnJSON adds the names the string values of raw hold and the ones its structured fields give.
func (d *names) learnJSON(raw []byte) {
	mapStrings(raw, func(s string) string { d.learnString(s); return s })
	var v any
	if json.Unmarshal(raw, &v) == nil {
		d.learnFields(v)
	}
}

// projectKeys are the members of Google's error details that hold a project, by their names
// without case, underscores or hyphens.
var projectKeys = map[string]bool{
	"consumer": true, "containerinfo": true, "project": true, "projectid": true, "projectnumber": true,
}

func (d *names) learnFields(v any) {
	switch v := v.(type) {
	case []any:
		for _, e := range v {
			d.learnFields(e)
		}
	case map[string]any:
		for k, e := range v {
			if s, ok := e.(string); ok && projectKeys[normalKey(k)] {
				d.addProject(s)
			}
			d.learnFields(e)
		}
		// ResourceInfo names a project resource by its ID alone.
		typ, _ := firstString(v, "resource_type", "resourceType")
		if name, ok := firstString(v, "resource_name", "resourceName"); ok && strings.HasSuffix(strings.ToLower(typ), "project") {
			d.addProject(name)
		}
	}
}

func normalKey(k string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			return s, true
		}
	}
	return "", false
}

// maskString replaces the names in s: the pattern forms, then each known name as a whole token.
func (d *names) maskString(s string) string {
	for _, re := range namePatterns {
		s = replaceGroup(s, re)
	}
	sort.SliceStable(d.list, func(i, j int) bool { return len(d.list[i]) > len(d.list[j]) })
	for _, id := range d.list {
		s = replaceToken(s, id)
	}
	return s
}

func (d *names) maskJSON(raw []byte) []byte { return mapStrings(raw, d.maskString) }

// replaceGroup replaces group 1 of each match of re in s.
func replaceGroup(s string, re *regexp.Regexp) string {
	idx := re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		b.WriteString(s[last:m[2]])
		b.WriteString(Redacted)
		last = m[3]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isTokenByte(c byte) bool { return isAlnum(c) || c == '_' || c == '-' }

// replaceToken replaces id in s where it stands alone: not next to a letter, digit, underscore
// or hyphen, nor to a dot that has a letter or digit beyond it (so 12 leaves 123, 12.5 and a
// model name such as gemini-2.5-flash alone).
func replaceToken(s, id string) string {
	if id == "" || !strings.Contains(s, id) {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		k := strings.Index(s[i:], id)
		if k < 0 {
			break
		}
		k += i
		end := k + len(id)
		before := k == 0 || !isTokenByte(s[k-1]) && !(s[k-1] == '.' && k >= 2 && isAlnum(s[k-2]))
		after := end == len(s) || !isTokenByte(s[end]) && !(s[end] == '.' && end+1 < len(s) && isAlnum(s[end+1]))
		if before && after {
			b.WriteString(s[i:k])
			b.WriteString(Redacted)
			i = end
		} else {
			b.WriteString(s[i : k+1])
			i = k + 1
		}
	}
	b.WriteString(s[i:])
	return b.String()
}

// mapStrings calls f on each string value (not key) of the JSON text raw and returns raw with the
// values f changed written again; everything else, key order, numbers and spacing, is the
// original bytes. raw itself comes back when nothing changed.
func mapStrings(raw []byte, f func(string) string) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			i++
			continue
		}
		j := i + 1
		for j < len(raw) && raw[j] != '"' {
			if raw[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(raw) {
			break
		}
		end := j + 1
		k := end
		for k < len(raw) && (raw[k] == ' ' || raw[k] == '\t' || raw[k] == '\n' || raw[k] == '\r') {
			k++
		}
		if !(k < len(raw) && raw[k] == ':') {
			var s string
			if json.Unmarshal(raw[i:end], &s) == nil {
				if m := f(s); m != s {
					if out == nil {
						out = make([]byte, 0, len(raw)+16)
					}
					out = append(out, raw[last:i]...)
					out = append(out, encodeString(m)...)
					last = end
				}
			}
		}
		i = end
	}
	if out == nil {
		return raw
	}
	return append(out, raw[last:]...)
}

func encodeString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}
