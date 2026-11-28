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

const Redacted = protocol.Redacted

type namePattern struct {
	re *regexp.Regexp
	ok func(name string) bool
}

const nameToken = `([\w%-]+(?:\.[\w%-]+)*)`

var namePatterns = []namePattern{
	{re: regexp.MustCompile(`(?i)\b(?:projects|organizations|folders|billingAccounts)/` + nameToken)},
	{re: regexp.MustCompile(`(?i)\b(?:project(?:_?id|_?number)?|organization|folder|billingAccount):` + nameToken)},
	{re: regexp.MustCompile(`(?i)\bproject(?:_?id|_?number)?=` + nameToken)},
	{re: regexp.MustCompile(`(?i)\bproject (?:number |id )?(\d+)\b`)},
	{re: regexp.MustCompile("(?i)\\bproject ['\"`]([^'\"`\\s]+)['\"`]")},
	{re: regexp.MustCompile(`(?i)\b(?:user|serviceAccount|group):([A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+)`)},
	{re: regexp.MustCompile(`([A-Za-z0-9._%+-]+@(?:[a-z][a-z0-9-]*\.iam|developer)\.gserviceaccount\.com)`)},
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

var serviceAccountProject = regexp.MustCompile(`@([a-z][a-z0-9-]*)\.iam\.gserviceaccount\.com$`)

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

func MaskReply(data []byte, event string, known ...string) []byte {
	if event == "error" {
		return MaskBody(data, known...)
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
		masked := MaskBody(orig, known...)
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

func collectAllErrorSpans(raw []byte, pos, end int, spans *[][2]int) int {
	i := skipWS(raw, pos)
	if i >= end {
		return end
	}
	switch raw[i] {
	case '{':
		i++
		for {
			i = skipWS(raw, i)
			if i >= end {
				return end
			}
			if raw[i] == '}' {
				return i + 1
			}
			ke := skipString(raw, i)
			var k string
			_ = json.Unmarshal(raw[i:ke], &k)
			i = skipWS(raw, ke)
			if i >= end || raw[i] != ':' {
				return i
			}
			vs := skipWS(raw, i+1)
			ve := end
			if vs < end {
				if k == "error" && (raw[vs] == '{' || raw[vs] == '"') {
					ve = skipValue(raw, vs)
					*spans = append(*spans, [2]int{vs, ve})
				} else if raw[vs] == '{' || raw[vs] == '[' {
					ve = collectAllErrorSpans(raw, vs, end, spans)
				} else {
					ve = skipValue(raw, vs)
				}
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else if i < end && raw[i] == '}' {
				return i + 1
			} else {
				return i
			}
		}
	case '[':
		i++
		for {
			vs := skipWS(raw, i)
			if vs >= end {
				return end
			}
			if raw[vs] == ']' {
				return vs + 1
			}
			var ve int
			if raw[vs] == '{' || raw[vs] == '[' {
				ve = collectAllErrorSpans(raw, vs, end, spans)
			} else {
				ve = skipValue(raw, vs)
			}
			if ve <= vs {
				return end
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else if i < end && raw[i] == ']' {
				return i + 1
			} else {
				return i
			}
		}
	default:
		return skipValue(raw, i)
	}
}

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

type errorShape struct {
	Code   json.RawMessage `json:"code"`
	Status string          `json:"status"`
}

func (e errorShape) ok() bool {
	return e.Status != "" && len(e.Code) > 0 && e.Code[0] != '"' && json.Unmarshal(e.Code, new(json.Number)) == nil
}

type names struct {
	list    []string
	seen    map[string]bool
	matcher *nameMatcher
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

func (d *names) addProject(v string) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "projects/"), "project:")
	if i := strings.IndexByte(v, '/'); i >= 0 {
		v = v[:i]
	}
	d.add(v)
}

func (d *names) learnString(s string) {
	for _, p := range namePatterns {
		for _, m := range p.re.FindAllStringSubmatch(s, -1) {
			if p.ok == nil || p.ok(m[1]) {
				d.addProject(m[1])
			}
		}
	}
}

func (d *names) learnJSON(raw []byte) map[int]bool {
	mapStrings(raw, func(_ int, s string) string { d.learnString(s); return s })
	var whole map[int]bool
	d.learnFields(raw, 0, len(raw), 0, &whole)
	return whole
}

const maxFieldDepth = 64

var projectKeys = map[string]bool{
	"consumer": true, "containerinfo": true, "project": true, "projectid": true, "projectnumber": true,
}

func (d *names) learnFields(raw []byte, pos, end, depth int, whole *map[int]bool) int {
	i := skipWS(raw, pos)
	if i >= end {
		return end
	}
	if depth >= maxFieldDepth {
		return skipValue(raw, i)
	}
	switch raw[i] {
	case '[':
		i++
		for {
			i = skipWS(raw, i)
			if i >= end {
				return end
			}
			if raw[i] == ']' {
				return i + 1
			}
			next := d.learnFields(raw, i, end, depth+1, whole)
			if next <= i {
				return end
			}
			i = skipWS(raw, next)
			if i < end && raw[i] == ',' {
				i++
			} else if i < end && raw[i] == ']' {
				return i + 1
			} else {
				return i
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
			if i >= end {
				i = end
				break
			}
			if raw[i] == '}' {
				i++
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
			ve := end
			if vs < end {
				switch raw[vs] {
				case '"':
					ve = skipString(raw, vs)
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
					ve = d.learnFields(raw, vs, end, depth+1, whole)
					if k == "violations" && raw[vs] == '[' {
						violRanges = append(violRanges, [2]int{vs, ve})
					}
				default:
					ve = skipValue(raw, vs)
				}
			}
			i = skipWS(raw, ve)
			if i < end && raw[i] == ',' {
				i++
			} else if i < end && raw[i] == '}' {
				i++
				break
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
		if hasRname && strings.HasSuffix(strings.ToLower(rtype), "project") {
			d.addProject(rname)
		}
		return i
	default:
		return skipValue(raw, i)
	}
}

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
		var ve int
		if raw[i] == '{' {
			j := i + 1
			for {
				j = skipWS(raw, j)
				if j >= end {
					j = end
					break
				}
				if raw[j] == '}' {
					j++
					break
				}
				ke := skipString(raw, j)
				var k string
				_ = json.Unmarshal(raw[j:ke], &k)
				j = skipWS(raw, ke)
				if j >= end || raw[j] != ':' {
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
				if j < end && raw[j] == ',' {
					j++
				} else if j < end && raw[j] == '}' {
					j++
					break
				} else {
					break
				}
			}
			ve = j
		} else {
			ve = skipValue(raw, i)
		}
		if ve <= i {
			return
		}
		i = skipWS(raw, ve)
		if i < end && raw[i] == ',' {
			i++
		} else {
			return
		}
	}
}

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

func isType(typ, name string) bool {
	return typ == name || strings.HasSuffix(typ, "."+name)
}

func normalKey(k string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(k))
}

func (d *names) maskString(s string) string {
	out := s
	for _, p := range namePatterns {
		out = replaceGroup(out, p)
	}
	if len(d.list) > 0 {
		if d.matcher == nil || len(d.matcher.list) != len(d.list) {
			sort.SliceStable(d.list, func(i, j int) bool { return len(d.list[i]) > len(d.list[j]) })
			d.matcher = newNameMatcher(d.list)
		}
		out = d.matcher.mask(out)
	}
	return widen(s, out)
}

func (d *names) maskJSON(raw []byte, whole map[int]bool) []byte {
	return mapStrings(raw, func(off int, s string) string {
		if whole[off] {
			return Redacted
		}
		return d.maskString(s)
	})
}

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

var (
	domainScopedID = regexp.MustCompile(`(?i)(?:\b(?:projects|organizations|folders|billingAccounts)/|` +
		`\b(?:project(?:_?id|_?number)?|organization|folder|billingAccount):|\bproject(?:_?id|_?number)?=)` +
		`(?:[\w%-]+\.)+[\w%-]+:([\w%-]+)`)
	markedID    = regexp.MustCompile(regexp.QuoteMeta(Redacted) + `:([\w%-]+)`)
	dottedValue = regexp.MustCompile(`(?i)\bproject(?:_?id|_?number)?=(?:(\.+[\w%-]+(?:\.+[\w%-]+)*(?::[\w%-]+)?)|` +
		regexp.QuoteMeta(Redacted) + `(\.+[\w%-]+(?:\.+[\w%-]+)*))`)
)

func widen(s, out string) string {
	if strings.IndexByte(s, ':') >= 0 {
		var ids map[string]bool
		for _, m := range domainScopedID.FindAllStringSubmatch(s, -1) {
			if ids == nil {
				ids = map[string]bool{}
			}
			ids[m[1]] = true
		}
		if ids != nil {
			out = replaceMarked(out, ids)
		}
	}
	if strings.IndexByte(out, '=') >= 0 && strings.IndexByte(out, '.') >= 0 {
		if idx := dottedValue.FindAllStringSubmatchIndex(out, -1); len(idx) > 0 {
			var b strings.Builder
			last := 0
			for _, m := range idx {
				if m[2] >= 0 {
					b.WriteString(out[last:m[2]])
					b.WriteString(Redacted)
					last = m[3]
				} else {
					b.WriteString(out[last:m[4]])
					last = m[5]
				}
			}
			b.WriteString(out[last:])
			out = b.String()
		}
	}
	return out
}

func replaceMarked(out string, ids map[string]bool) string {
	idx := markedID.FindAllStringSubmatchIndex(out, -1)
	var b strings.Builder
	last := 0
	for _, m := range idx {
		if !ids[out[m[2]:m[3]]] {
			continue
		}
		b.WriteString(out[last : m[2]-1])
		last = m[3]
	}
	if last == 0 {
		return out
	}
	b.WriteString(out[last:])
	return b.String()
}

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
