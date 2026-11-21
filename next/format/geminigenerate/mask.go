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

// Google's errors name the Google Cloud project, organization, folder, billing account or account
// an upstream account belongs to: in the message ("The consumer 'projects/123' is suspended."),
// and in the details, where ErrorInfo's metadata (consumer, containerInfo, activationUrl),
// QuotaFailure's subject, ResourceInfo's owner and resource_name, and Help's links carry it.
// A client holding a pluxy key must not learn it. The details themselves stay: clients read
// RetryInfo and QuotaFailure.

// namePattern is a way text names a project, an organization, a folder, a billing account or a
// person. Group 1 is the name; ok, when set, tells a name from a word that happens to follow.
type namePattern struct {
	re *regexp.Regexp
	ok func(name string) bool
}

const nameToken = `([\w%-]+(?:\.[\w%-]+)*)`

var namePatterns = []namePattern{
	{re: regexp.MustCompile(`(?i)\b(?:projects|organizations|folders|billingAccounts)/` + nameToken)},
	{re: regexp.MustCompile(`(?i)\b(?:project(?:_?id|_?number)?|organization|folder|billingAccount):` + nameToken)},
	{re: regexp.MustCompile(`(?i)\bproject(?:_?id|_?number)?=([\w.-]+)`)},
	{re: regexp.MustCompile(`(?i)\bproject (?:number |id )?(\d+)\b`)},
	{re: regexp.MustCompile("(?i)\\bproject ['\"`]([^'\"`\\s]+)['\"`]")},
	{re: regexp.MustCompile(`(?i)\b(?:user|serviceAccount|group):([A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)`)},
	// Any service account's address, whatever the account's own key says.
	{re: regexp.MustCompile(`([A-Za-z0-9._%+-]+@(?:[a-z][a-z0-9-]*\.iam|developer)\.gserviceaccount\.com)`)},
	// "Permission denied on resource project my-proj-1." or "... resource project myproject.": a
	// project ID after the words, which prose ("resource project has been denied") must not be
	// taken for. An ID is all digits, or at least six characters with a hyphen or a digit, or 6 to
	// 30 lowercase letters, digits and hyphens followed by end of text, trailing spaces, newline,
	// \r\n, " (", or ., ; : quote or ).
	{re: regexp.MustCompile(`(?i:\bresource project) ([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)`), ok: looksLikeProjectID},
	{re: regexp.MustCompile("(?i:\\bresource project) ([a-z0-9](?:[a-z0-9-]*[a-z0-9])?)(?:\\s*$|\\r?\\n| \\(|[.,;:'\"`)])"), ok: func(id string) bool {
		return len(id) >= 6 && len(id) <= 30
	}},
}

func looksLikeProjectID(id string) bool {
	if strings.Trim(id, "0123456789") == "" {
		return true
	}
	return len(id) >= 6 && strings.ContainsAny(id, "-0123456789")
}

// serviceAccountProject finds the project in a service account's address.
var serviceAccountProject = regexp.MustCompile(`@([a-z][a-z0-9-]*)\.iam\.gserviceaccount\.com$`)

// MaskProject returns e without the Google project or account its message and its raw body name.
// known are identifiers the caller knows belong to the upstream account (its project, a service
// account's project and address); the structured fields of Google's error details and the text
// patterns above add the rest. A non-empty QuotaFailure violation's subject and ResourceInfo's
// owner, found by the detail's @type, go whole in their own field, whatever they say; one that
// looks like an identifier (contains :, / or @) or a project ID is also masked where another
// string repeats it, while a plain word (such as "global") stays elsewhere. A PreconditionFailure's subject, and the
// quota metric, ID, value and dimensions, stay. Each collected name is replaced as a whole token
// in the message and in every string value of the raw body (an object, or the array a Google API
// wraps one in); the keys, numbers, types, reasons, quota facts and everything else stay, and a
// body with nothing to mask keeps its bytes. e itself is returned when nothing changed; otherwise
// a copy, and e is not touched. Call it after the error has been classified: it changes no code,
// scope or delay.
func MaskProject(e *llm.Error, known ...string) *llm.Error {
	if e == nil {
		return nil
	}
	d := newNames(known)
	rawOK := len(e.Raw) > 0 && json.Valid(e.Raw)
	d.learnString(e.Message)
	var whole map[int]bool
	if rawOK {
		whole = d.learnJSON(e.Raw)
	}
	msg := d.maskString(e.Message)
	raw := e.Raw
	if rawOK {
		raw = d.maskJSON(e.Raw, whole)
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
		whole := d.learnJSON(body)
		return d.maskJSON(body, whole)
	}
	s := string(body)
	d.learnString(s)
	if m := d.maskString(s); m != s {
		return []byte(m)
	}
	return body
}

// IsGoogleErrorShape reports whether raw is a Google error: an object with an "error" whose code
// is a number and whose status is a name such as RESOURCE_EXHAUSTED, alone or as the one element
// of an array (as Google's OpenAI-compatible API sends it), or such an error object by itself (as
// a Chat Completions decoder keeps the one a stream carried).
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
		Error json.RawMessage `json:"error"`
		errorShape
	}
	if json.Unmarshal(raw, &b) != nil {
		return false
	}
	if len(b.Error) > 0 {
		var e errorShape
		return json.Unmarshal(b.Error, &e) == nil && e.ok()
	}
	return b.errorShape.ok()
}

// errorShape is the members of a Google error object that tell it from another vendor's.
type errorShape struct {
	Code   json.RawMessage `json:"code"`
	Status string          `json:"status"`
}

func (e errorShape) ok() bool {
	return e.Status != "" && len(e.Code) > 0 && e.Code[0] != '"' && json.Unmarshal(e.Code, new(json.Number)) == nil
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
	for _, p := range namePatterns {
		for _, m := range p.re.FindAllStringSubmatch(s, -1) {
			if p.ok == nil || p.ok(m[1]) {
				d.addProject(m[1])
			}
		}
	}
}

// learnJSON adds the names the string values of raw hold and the ones its structured fields give,
// and returns the byte offsets of non-empty QuotaFailure violation subjects and ResourceInfo
// owners in raw so those fields can be masked whole without affecting other fields.
func (d *names) learnJSON(raw []byte) map[int]bool {
	mapStrings(raw, func(_ int, s string) string { d.learnString(s); return s })
	var whole map[int]bool
	d.learnFields(raw, 0, len(raw), &whole)
	return whole
}

// projectKeys are the members of Google's error details that hold a project, by their names
// without case, underscores or hyphens.
var projectKeys = map[string]bool{
	"consumer": true, "containerinfo": true, "project": true, "projectid": true, "projectnumber": true,
}

func (d *names) learnFields(raw []byte, start, end int, whole *map[int]bool) {
	i := skipWS(raw, start)
	if i >= end {
		return
	}
	switch raw[i] {
	case '[':
		i++
		for {
			i = skipWS(raw, i)
			if i >= end || raw[i] == ']' {
				return
			}
			ve := skipValue(raw, i)
			d.learnFields(raw, i, ve, whole)
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else {
				return
			}
		}
	case '{':
		i++
		var (
			typ          string
			rtype, rname string
			hasRtype     bool
			hasRname     bool
			ownerOffsets []int
			ownerVals    []string
			violRanges   [][2]int
		)
		for {
			i = skipWS(raw, i)
			if i >= end || raw[i] == '}' {
				break
			}
			ke := skipString(raw, i)
			var k string
			_ = json.Unmarshal(raw[i:ke], &k)
			i = skipWS(raw, ke)
			if i >= end || raw[i] != ':' {
				break
			}
			vs := skipWS(raw, i+1)
			ve := skipValue(raw, vs)
			if vs < ve {
				switch raw[vs] {
				case '"':
					var s string
					if json.Unmarshal(raw[vs:ve], &s) == nil {
						if projectKeys[normalKey(k)] {
							d.addProject(s)
						}
						switch k {
						case "@type":
							typ = s
						case "resource_type":
							rtype, hasRtype = s, true
						case "resourceType":
							if !hasRtype {
								rtype = s
							}
						case "resource_name":
							rname, hasRname = s, true
						case "resourceName":
							if !hasRname {
								rname, hasRname = s, true
							}
						case "owner":
							ownerOffsets = append(ownerOffsets, vs)
							ownerVals = append(ownerVals, s)
						}
					}
				case '{', '[':
					if k == "violations" && raw[vs] == '[' {
						violRanges = append(violRanges, [2]int{vs, ve})
					}
					d.learnFields(raw, vs, ve, whole)
				}
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else {
				break
			}
		}
		if isType(typ, "ResourceInfo") {
			for idx, s := range ownerVals {
				d.recordWhole(ownerOffsets[idx], s, whole)
			}
		}
		if isType(typ, "QuotaFailure") {
			for _, r := range violRanges {
				d.learnQuotaSubjects(raw, r[0], r[1], whole)
			}
		}
		// ResourceInfo names a project resource by its ID alone.
		if hasRname && strings.HasSuffix(strings.ToLower(rtype), "project") {
			d.addProject(rname)
		}
	}
}

// learnQuotaSubjects records the byte offset of each non-empty "subject" string inside the
// violation objects of a QuotaFailure's violations array.
func (d *names) learnQuotaSubjects(raw []byte, start, end int, whole *map[int]bool) {
	i := skipWS(raw, start)
	if i >= end || raw[i] != '[' {
		return
	}
	i++
	for {
		i = skipWS(raw, i)
		if i >= end || raw[i] == ']' {
			return
		}
		ve := skipValue(raw, i)
		if i < ve && raw[i] == '{' {
			j := i + 1
			for {
				j = skipWS(raw, j)
				if j >= ve || raw[j] == '}' {
					break
				}
				ke := skipString(raw, j)
				var k string
				_ = json.Unmarshal(raw[j:ke], &k)
				j = skipWS(raw, ke)
				if j >= ve || raw[j] != ':' {
					break
				}
				vs := skipWS(raw, j+1)
				vEnd := skipValue(raw, vs)
				if k == "subject" && vs < vEnd && raw[vs] == '"' {
					var s string
					if json.Unmarshal(raw[vs:vEnd], &s) == nil {
						d.recordWhole(vs, s, whole)
					}
				}
				j = skipWS(raw, vEnd)
				if j < ve && raw[j] == ',' {
					j++
				} else {
					break
				}
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

// recordWhole marks the string at off to be replaced whole in its own field, and adds s to the
// token list when it looks like an identifier (contains :, / or @ alongside an alphanumeric
// character) or a project ID (looksLikeProjectID) so repetitions in the message or other strings
// are masked as whole tokens. Empty values are skipped.
func (d *names) recordWhole(off int, s string, whole *map[int]bool) {
	if strings.TrimSpace(s) == "" {
		return
	}
	if *whole == nil {
		*whole = map[int]bool{}
	}
	(*whole)[off] = true
	if looksLikeIdentifier(s) || looksLikeProjectID(s) {
		d.add(s)
	}
}

func looksLikeIdentifier(s string) bool {
	if !strings.ContainsAny(s, ":/@") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if isAlnum(s[i]) {
			return true
		}
	}
	return false
}

// isType reports whether a detail's @type URL names the google.rpc message called name.
func isType(typ, name string) bool {
	return typ == name || strings.HasSuffix(typ, "."+name)
}

func normalKey(k string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
}

// maskString replaces the names in s: the pattern forms, then each known name as a whole token.
func (d *names) maskString(s string) string {
	for _, p := range namePatterns {
		s = replaceGroup(s, p)
	}
	sort.SliceStable(d.list, func(i, j int) bool { return len(d.list[i]) > len(d.list[j]) })
	for _, id := range d.list {
		s = replaceToken(s, id)
	}
	return s
}

func (d *names) maskJSON(raw []byte, whole map[int]bool) []byte {
	return mapStrings(raw, func(off int, s string) string {
		if whole[off] {
			return Redacted
		}
		return d.maskString(s)
	})
}

// replaceGroup replaces group 1 of each match of p in s.
func replaceGroup(s string, p namePattern) string {
	idx := p.re.FindAllStringSubmatchIndex(s, -1)
	if len(idx) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range idx {
		if p.ok != nil && !p.ok(s[m[2]:m[3]]) {
			continue
		}
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

// mapStrings calls f with the starting byte offset and decoded text of each string value (not key)
// of the JSON text raw and returns raw with the values f changed written again; everything else,
// key order, numbers and spacing, is the original bytes. raw itself comes back when nothing
// changed.
func mapStrings(raw []byte, f func(int, string) string) []byte {
	var out []byte
	last := 0
	for i := 0; i < len(raw); {
		if raw[i] != '"' {
			i++
			continue
		}
		end := skipString(raw, i)
		if end == i || end > len(raw) || raw[end-1] != '"' {
			break
		}
		k := skipWS(raw, end)
		if !(k < len(raw) && raw[k] == ':') {
			var s string
			if json.Unmarshal(raw[i:end], &s) == nil {
				if m := f(i, s); m != s {
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

func encodeString(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}
