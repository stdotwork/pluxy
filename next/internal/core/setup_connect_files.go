package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

type toolFormat struct {
	apply func(before []byte, snippet string) ([]byte, error)
	clear func(original, applied, current []byte) ([]byte, error)
	token func(b []byte) string
}

var toolFormats = map[string]toolFormat{
	"~/.claude/settings.json": {apply: applyClaudeSettings, clear: clearClaudeSettings, token: claudeToken},
	"~/.codex/config.toml":    {apply: applyCodexConfig, clear: clearCodexConfig, token: codexToken},
}

type errNotOurs struct{ why string }

func (e *errNotOurs) Error() string { return e.why }

func refuseFile(format string, a ...any) error { return &errNotOurs{fmt.Sprintf(format, a...)} }

type jsonField struct {
	key string
	val json.RawMessage
}

type jsonObj []jsonField

func parseJSONObj(b []byte) (jsonObj, error) {
	if bytes.HasPrefix(b, []byte(utf8BOM)) {
		return nil, errors.New("it starts with a byte-order mark")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("it is not a JSON object")
	}
	var o jsonObj
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, errors.New("it is not valid JSON")
		}
		k, _ := t.(string)
		if _, dup := o.get(k); dup {
			return nil, fmt.Errorf("it names %q twice", k)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, errors.New("it is not valid JSON")
		}
		o = append(o, jsonField{k, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("it is not valid JSON")
	}
	if _, err := dec.Token(); err == nil || dec.More() {
		return nil, errors.New("it has text after the JSON object")
	}
	if !json.Valid(b) {
		return nil, errors.New("it is not valid JSON")
	}
	return o, nil
}

func (o jsonObj) get(k string) (json.RawMessage, bool) {
	for _, f := range o {
		if f.key == k {
			return f.val, true
		}
	}
	return nil, false
}

func (o *jsonObj) set(k string, v json.RawMessage) {
	for i := range *o {
		if (*o)[i].key == k {
			(*o)[i].val = v
			return
		}
	}
	*o = append(*o, jsonField{k, v})
}

func (o *jsonObj) del(k string) {
	*o = slices.DeleteFunc(*o, func(f jsonField) bool { return f.key == k })
}

func (o jsonObj) sub(k string) (jsonObj, bool, error) {
	v, ok := o.get(k)
	if !ok {
		return nil, false, nil
	}
	if !isJSONObject(v) {
		return nil, true, errNotObject
	}
	s, err := parseJSONObj(v)
	return s, true, err
}

var errNotObject = errors.New("not an object")

func isJSONObject(v json.RawMessage) bool {
	t := bytes.TrimLeft(v, " \t\r\n")
	return len(t) > 0 && t[0] == '{'
}

func (o jsonObj) render() []byte {
	var b bytes.Buffer
	o.write(&b, "")
	b.WriteByte('\n')
	return b.Bytes()
}

func (o jsonObj) write(b *bytes.Buffer, indent string) {
	if len(o) == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{\n")
	inner := indent + "  "
	for i, f := range o {
		k, _ := json.Marshal(f.key)
		b.WriteString(inner)
		b.Write(k)
		b.WriteString(": ")
		var v bytes.Buffer
		if err := json.Indent(&v, f.val, inner, "  "); err != nil {
			v.Reset()
			v.Write(f.val)
		}
		b.Write(bytes.TrimRight(v.Bytes(), " \t\r\n"))
		if i < len(o)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(indent)
	b.WriteByte('}')
}

func claudeSettings(b []byte) (jsonObj, error) {
	if blank(b) {
		return jsonObj{}, nil
	}
	root, err := parseJSONObj(b)
	if err != nil {
		return nil, refuseFile("pluxy does not change ~/.claude/settings.json: %v", err)
	}
	if v, ok := root.get("$schema"); ok {
		var s string
		if json.Unmarshal(v, &s) != nil || s != claudeCodeSchema {
			return nil, refuseFile("pluxy does not change ~/.claude/settings.json: it names another schema than Claude Code's")
		}
	}
	env, _, err := root.sub("env")
	if err != nil {
		return nil, refuseFile("pluxy does not change ~/.claude/settings.json: its env is not an object of strings")
	}
	for _, f := range env {
		var s string
		if json.Unmarshal(f.val, &s) != nil {
			return nil, refuseFile("pluxy does not change ~/.claude/settings.json: its env is not an object of strings")
		}
	}
	return root, nil
}

// env: an API key there would be sent in place of the client key.
var claudeDropped = []string{"ANTHROPIC_API_KEY"}

func applyClaudeSettings(before []byte, snippet string) ([]byte, error) {
	root, err := claudeSettings(before)
	if err != nil {
		return nil, err
	}
	want, err := parseJSONObj([]byte(snippet))
	if err != nil {
		return nil, fmt.Errorf("the setup text for Claude Code is not a JSON object: %v", err)
	}
	for _, f := range want {
		if !isJSONObject(f.val) {
			root.set(f.key, f.val)
			continue
		}
		have, _, err := root.sub(f.key)
		if err != nil {
			return nil, refuseFile("pluxy does not change ~/.claude/settings.json: its %s is not an object", f.key)
		}
		add, err := parseJSONObj(f.val)
		if err != nil {
			return nil, err
		}
		for _, m := range add {
			have.set(m.key, m.val)
		}
		if f.key == "env" {
			for _, k := range claudeDropped {
				have.del(k)
			}
		}
		root.set(f.key, have.render())
	}
	return root.render(), nil
}

func clearClaudeSettings(original, applied, current []byte) ([]byte, error) {
	if bytes.Equal(current, applied) {
		return original, nil
	}
	cur, err := claudeSettings(current)
	if err != nil {
		return nil, err
	}
	orig, err := parseOrEmpty(original)
	if err != nil {
		return nil, err
	}
	app, err := parseOrEmpty(applied)
	if err != nil {
		return nil, err
	}
	for _, k := range unionKeys(orig, app) {
		ov, oHas := orig.get(k)
		av, aHas := app.get(k)
		if aHas && isJSONObject(av) && (!oHas || isJSONObject(ov)) {
			osub, _, _ := orig.sub(k)
			asub, _, _ := app.sub(k)
			cs, cHas, err := cur.sub(k)
			if err != nil {
				continue
			}
			changed := false
			for _, m := range unionKeys(osub, asub) {
				if putBack(&cs, osub, asub, m) {
					changed = true
				}
			}
			switch {
			case !changed:
			case len(cs) == 0 && !oHas:
				cur.del(k)
			case cHas || len(cs) > 0:
				cur.set(k, cs.render())
			}
			continue
		}
		putBack(&cur, orig, app, k)
	}
	if original == nil && len(cur) == 0 {
		return nil, nil
	}
	return cur.render(), nil
}

func putBack(cur *jsonObj, orig, app jsonObj, k string) bool {
	ov, oHas := orig.get(k)
	av, aHas := app.get(k)
	cv, cHas := cur.get(k)
	if oHas == aHas && (!oHas || sameJSON(ov, av)) {
		return false
	}
	if cHas != aHas || cHas && !sameJSON(cv, av) {
		return false
	}
	if oHas {
		cur.set(k, ov)
	} else {
		cur.del(k)
	}
	return true
}

func parseOrEmpty(b []byte) (jsonObj, error) {
	if blank(b) {
		return jsonObj{}, nil
	}
	return parseJSONObj(b)
}

func unionKeys(a, b jsonObj) []string {
	var keys []string
	for _, o := range []jsonObj{a, b} {
		for _, f := range o {
			if !slices.Contains(keys, f.key) {
				keys = append(keys, f.key)
			}
		}
	}
	return keys
}

func claudeToken(b []byte) string {
	root, err := claudeSettings(b)
	if err != nil {
		return ""
	}
	env, _, _ := root.sub("env")
	v, ok := env.get("ANTHROPIC_AUTH_TOKEN")
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

const (
	tomlOther = iota
	tomlHeader
	tomlArray
	tomlKV
)

type tomlStmt struct {
	kind       int
	start, end int
	table      string
	key        string
	value      string
	comment    string
}

type tomlDoc struct {
	lines []string
	crlf  bool
	stmts []tomlStmt
}

func parseTOMLDoc(b []byte) (*tomlDoc, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("it is not UTF-8 text")
	}
	s := string(b)
	d := &tomlDoc{crlf: strings.Contains(s, "\r\n")}
	s = strings.TrimSuffix(s, "\n")
	if s != "" || len(b) > 0 {
		d.lines = strings.Split(s, "\n")
	}
	for i := range d.lines {
		d.lines[i] = strings.TrimSuffix(d.lines[i], "\r")
	}
	if len(b) == 0 {
		d.lines = nil
	}
	table := ""
	for i := 0; i < len(d.lines); {
		line := strings.TrimSpace(d.lines[i])
		switch {
		case line == "" || line[0] == '#':
			d.stmts = append(d.stmts, tomlStmt{kind: tomlOther, start: i, end: i + 1, table: table})
			i++
		case line[0] == '[':
			kind := tomlHeader
			body := strings.TrimSpace(stripTOMLComment(line))
			open, close := "[", "]"
			if strings.HasPrefix(body, "[[") {
				kind, open, close = tomlArray, "[[", "]]"
			}
			if !strings.HasSuffix(body, close) {
				return nil, fmt.Errorf("line %d is not a table header TOML takes", i+1)
			}
			table = normalizeTOMLPath(strings.TrimSuffix(strings.TrimPrefix(body, open), close))
			d.stmts = append(d.stmts, tomlStmt{kind: kind, start: i, end: i + 1, table: table})
			i++
		default:
			eq := indexUnquoted(d.lines[i], '=')
			if eq < 0 {
				return nil, fmt.Errorf("line %d is not a key and value", i+1)
			}
			end, oneLine, err := tomlValueEnd(d.lines, i, eq+1)
			if err != nil {
				return nil, err
			}
			st := tomlStmt{kind: tomlKV, start: i, end: end, table: table, key: normalizeTOMLPath(d.lines[i][:eq])}
			if oneLine {
				rest := d.lines[i][eq+1:]
				val := stripTOMLComment(rest)
				st.value = strings.TrimSpace(val)
				st.comment = strings.TrimSpace(rest[len(val):])
			}
			d.stmts = append(d.stmts, st)
			i = end
		}
	}
	return d, nil
}

func tomlValueEnd(lines []string, i, at int) (int, bool, error) {
	depth := 0
	inMulti := ""
	for l := i; l < len(lines); l++ {
		s := lines[l]
		j := 0
		if l == i {
			j = at
		}
	scan:
		for j < len(s) {
			if inMulti != "" {
				if inMulti == `"""` && s[j] == '\\' {
					j += 2
					continue
				}
				if strings.HasPrefix(s[j:], inMulti) {
					j += 3
					for j < len(s) && s[j] == inMulti[0] { // up to two more quotes belong to the string
						j++
					}
					inMulti = ""
					continue
				}
				j++
				continue
			}
			switch c := s[j]; {
			case strings.HasPrefix(s[j:], `"""`) || strings.HasPrefix(s[j:], `'''`):
				inMulti = s[j : j+3]
				j += 3
			case c == '"' || c == '\'':
				k := j + 1
				for k < len(s) && s[k] != c {
					if c == '"' && s[k] == '\\' {
						k++
					}
					k++
				}
				if k >= len(s) {
					return 0, false, fmt.Errorf("line %d has a string that does not end", l+1)
				}
				j = k + 1
			case c == '#':
				break scan
			case c == '[' || c == '{':
				depth++
				j++
			case c == ']' || c == '}':
				depth--
				if depth < 0 {
					return 0, false, fmt.Errorf("line %d closes a bracket it did not open", l+1)
				}
				j++
			default:
				j++
			}
		}
		if depth == 0 && inMulti == "" {
			return l + 1, l == i, nil
		}
	}
	return 0, false, fmt.Errorf("the value on line %d does not end", i+1)
}

func (d *tomlDoc) find(table, key string) int {
	return slices.IndexFunc(d.stmts, func(s tomlStmt) bool { return s.kind == tomlKV && s.table == table && s.key == key })
}

func (d *tomlDoc) header(table string) int {
	return slices.IndexFunc(d.stmts, func(s tomlStmt) bool { return s.kind == tomlHeader && s.table == table })
}

func (d *tomlDoc) lastKV(table string) int {
	end := -1
	if table != "" {
		h := d.header(table)
		if h < 0 {
			return -1
		}
		end = d.stmts[h].end
	}
	for _, s := range d.stmts {
		if s.kind == tomlKV && s.table == table {
			end = s.end
		}
	}
	return end
}

func (d *tomlDoc) bytes() []byte {
	if len(d.lines) == 0 {
		return nil
	}
	nl := "\n"
	if d.crlf {
		nl = "\r\n"
	}
	return []byte(strings.Join(d.lines, nl) + nl)
}

func (d *tomlDoc) reparse() error {
	n, err := parseTOMLDoc(d.bytes())
	if err != nil {
		return err
	}
	n.crlf = d.crlf
	*d = *n
	return nil
}

func (d *tomlDoc) splice(start, end int, with ...string) {
	d.lines = slices.Concat(d.lines[:start], with, d.lines[end:])
}

func (d *tomlDoc) setKV(table, key, value string) error {
	line := key + " = " + value
	if i := d.find(table, key); i >= 0 {
		st := d.stmts[i]
		if st.end-st.start == 1 && st.comment != "" {
			line += " " + st.comment
		}
		indent := d.lines[st.start][:len(d.lines[st.start])-len(strings.TrimLeft(d.lines[st.start], " \t"))]
		d.splice(st.start, st.end, indent+line)
		return d.reparse()
	}
	at := d.lastKV(table)
	switch {
	case at >= 0:
		d.splice(at, at, line)
	case table == "":
		d.splice(0, 0, line)
	default:
		var add []string
		if len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) != "" {
			add = append(add, "")
		}
		add = append(add, "["+table+"]", line)
		d.splice(len(d.lines), len(d.lines), add...)
	}
	return d.reparse()
}

func (d *tomlDoc) delKV(table, key string) error {
	if i := d.find(table, key); i >= 0 {
		d.splice(d.stmts[i].start, d.stmts[i].end)
		return d.reparse()
	}
	return nil
}

func (d *tomlDoc) value(table, key string) (string, bool) {
	i := d.find(table, key)
	if i < 0 {
		return "", false
	}
	st := d.stmts[i]
	if st.end-st.start != 1 {
		return strings.Join(d.lines[st.start:st.end], "\n"), true
	}
	return st.value, true
}

const codexProvider = "model_providers.pluxy"

// Either would have Codex send another credential than the bearer token.
var codexDropped = []string{"env_key", "requires_openai_auth"}

func codexDoc(b []byte) (*tomlDoc, error) {
	if parseCodexConfigTOML(b).refused {
		return nil, refuseFile("pluxy does not change ~/.codex/config.toml: Codex's writer would refuse it (a model_providers or agents entry of another kind, or a wire_api other than \"responses\")")
	}
	d, err := parseTOMLDoc(b)
	if err != nil {
		return nil, refuseFile("pluxy does not change ~/.codex/config.toml: %v", err)
	}
	headers := 0
	for _, s := range d.stmts {
		full := s.key
		if s.table != "" {
			full = s.table + "." + s.key
		}
		inProvider := s.table == codexProvider || strings.HasPrefix(s.table, codexProvider+".")
		switch {
		case s.kind == tomlHeader && s.table == codexProvider:
			headers++
		case s.kind == tomlKV && !inProvider &&
			(full == codexProvider || strings.HasPrefix(full, codexProvider+".") || full == "model_providers"):
			return nil, refuseFile("pluxy does not change ~/.codex/config.toml: it sets pluxy's provider in another form than a [%s] table", codexProvider)
		}
	}
	if headers > 1 {
		return nil, refuseFile("pluxy does not change ~/.codex/config.toml: it has the [%s] table twice", codexProvider)
	}
	return d, nil
}

type tomlEntry struct{ table, key, value string }

func snippetEntries(snippet string) ([]tomlEntry, error) {
	d, err := parseTOMLDoc([]byte(snippet))
	if err != nil {
		return nil, fmt.Errorf("the setup text for Codex is not TOML this editor reads: %v", err)
	}
	var out []tomlEntry
	for _, s := range d.stmts {
		if s.kind == tomlKV {
			if s.end-s.start != 1 {
				return nil, errors.New("the setup text for Codex has a value over several lines")
			}
			out = append(out, tomlEntry{s.table, s.key, s.value})
		}
	}
	return out, nil
}

func applyCodexConfig(before []byte, snippet string) ([]byte, error) {
	d, err := codexDoc(before)
	if err != nil {
		return nil, err
	}
	entries, err := snippetEntries(snippet)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := d.setKV(e.table, e.key, e.value); err != nil {
			return nil, fmt.Errorf("changing ~/.codex/config.toml: %v", err)
		}
	}
	for _, k := range codexDropped {
		if err := d.delKV(codexProvider, k); err != nil {
			return nil, fmt.Errorf("changing ~/.codex/config.toml: %v", err)
		}
	}
	out := d.bytes()
	if p := parseCodexConfigTOML(out); p.refused || p.modelProvider != "pluxy" || !p.hasBaseURL || p.bearerToken == "" {
		return nil, errors.New("changing ~/.codex/config.toml did not give a config Codex reads as pluxy's; nothing was written")
	}
	return out, nil
}

var codexKeys = []tomlEntry{
	{table: "", key: "model_provider"},
	{table: "", key: "model"},
	{table: codexProvider, key: "name"},
	{table: codexProvider, key: "base_url"},
	{table: codexProvider, key: "wire_api"},
	{table: codexProvider, key: "experimental_bearer_token"},
	{table: codexProvider, key: "env_key"},
	{table: codexProvider, key: "requires_openai_auth"},
}

func clearCodexConfig(original, applied, current []byte) ([]byte, error) {
	if bytes.Equal(current, applied) {
		return original, nil
	}
	cur, err := codexDoc(current)
	if err != nil {
		return nil, err
	}
	orig, err := parseTOMLDoc(original)
	if err != nil {
		return nil, err
	}
	app, err := parseTOMLDoc(applied)
	if err != nil {
		return nil, err
	}
	for _, e := range codexKeys {
		ov, oHas := orig.value(e.table, e.key)
		av, aHas := app.value(e.table, e.key)
		cv, cHas := cur.value(e.table, e.key)
		if oHas == aHas && ov == av || cHas != aHas || cv != av {
			continue
		}
		if oHas {
			if e.table != "" && cur.header(e.table) < 0 {
				continue
			}
			err = cur.setKV(e.table, e.key, ov)
		} else {
			err = cur.delKV(e.table, e.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if h := cur.header(codexProvider); h >= 0 && orig.header(codexProvider) < 0 && cur.lastKV(codexProvider) == cur.stmts[h].end {
		start := cur.stmts[h].start
		cur.splice(start, cur.stmts[h].end)
		if start == len(cur.lines) && start > 0 && strings.TrimSpace(cur.lines[start-1]) == "" {
			cur.splice(start-1, start)
		}
		if err := cur.reparse(); err != nil {
			return nil, err
		}
	}
	out := cur.bytes()
	if original == nil && blank(out) {
		return nil, nil
	}
	return out, nil
}

func codexToken(b []byte) string { return parseCodexConfigTOML(b).bearerToken }
