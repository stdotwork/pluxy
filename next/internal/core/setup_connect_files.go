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

// What Connect writes into a client tool's settings file and what Disconnect takes out again, as
// the desktop app's writers for the tool do (next/app-tools/claude-code and codex, with the
// transactions library's merges): Connect sets the settings the tool's setup text holds
// (ClientSnippetFor) and keeps everything else in the file; Disconnect gives back the file as it
// was when it still holds what Connect wrote, and otherwise puts back, one setting at a time, each
// setting Connect changed that nobody changed since (the writers' Clear).

// toolFormat is how one kind of settings file is changed.
type toolFormat struct {
	// apply is before (nil for no file) with the settings of snippet, the setup text's file for
	// it, set. It refuses a file the tool's writer would refuse.
	apply func(before []byte, snippet string) ([]byte, error)
	// clear is current with what apply changed from original to applied put back where current
	// still holds what apply wrote; nil for no file.
	clear func(original, applied, current []byte) ([]byte, error)
	// token is the client key the file holds, empty for none.
	token func(b []byte) string
}

// toolFormats are the files Connect can write, by the path the setup text names them by.
var toolFormats = map[string]toolFormat{
	"~/.claude/settings.json": {apply: applyClaudeSettings, clear: clearClaudeSettings, token: claudeToken},
	"~/.codex/config.toml":    {apply: applyCodexConfig, clear: clearCodexConfig, token: codexToken},
}

// errNotOurs refuses a file Connect will not change, with why.
type errNotOurs struct{ why string }

func (e *errNotOurs) Error() string { return e.why }

func refuseFile(format string, a ...any) error { return &errNotOurs{fmt.Sprintf(format, a...)} }

// ---- JSON (Claude Code's settings.json) ---------------------------------------------------------

// jsonField is one member of a JSON object, its value as written.
type jsonField struct {
	key string
	val json.RawMessage
}

// jsonObj is a JSON object with its members in the order of the file, so a change keeps the
// person's order and every value they wrote as they wrote it.
type jsonObj []jsonField

// parseJSONObj reads b as one strict JSON object: a byte-order mark, anything but an object, a
// key named twice or text after it is refused.
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

// sub is the object at k: absent is an empty object, true; anything else but an object is false.
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

// render writes o as JSON indented by two spaces, with a final newline, the values as written.
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

// claudeSettings reads Claude Code's settings as its writer's parse does: no file or only white
// space is no settings; otherwise one JSON object whose $schema, if any, is Claude Code's and whose
// env, if any, is an object of strings.
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

// claudeDropped are the variables Connect takes out of env: an API key there would be sent in
// place of the client key.
var claudeDropped = []string{"ANTHROPIC_API_KEY"}

// applyClaudeSettings sets what snippet (the setup text's settings.json) holds in before: each of
// its objects merged member by member into the file's, every other member set.
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

// clearClaudeSettings is the writers' Clear for settings.json: original when current is what
// Connect wrote; otherwise current with each member Connect changed (at the top and one object
// down) given back its original value, or taken out, where current still holds Connect's value.
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
				continue // the person made it something else: theirs now
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

// putBack gives cur's member k its value in orig (or takes it out) when apply changed it and cur
// still holds what apply wrote. It reports whether it changed cur.
func putBack(cur *jsonObj, orig, app jsonObj, k string) bool {
	ov, oHas := orig.get(k)
	av, aHas := app.get(k)
	cv, cHas := cur.get(k)
	if oHas == aHas && (!oHas || sameJSON(ov, av)) {
		return false // apply did not change it
	}
	if cHas != aHas || cHas && !sameJSON(cv, av) {
		return false // changed since: the person's now
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

// ---- TOML (Codex's config.toml) -----------------------------------------------------------------

// tomlLine kinds.
const (
	tomlOther  = iota // blank, comment
	tomlHeader        // [table]
	tomlArray         // [[array of tables]]
	tomlKV            // key = value, maybe over several lines
)

// tomlStmt is one statement of a TOML file: a table header, or a key and its value, which may run
// over several lines (a multi-line string, array or inline table).
type tomlStmt struct {
	kind       int
	start, end int    // the lines it takes, [start, end)
	table      string // the table it is in (a header's own name), dotted, unquoted
	key        string // a key's name in its table, dotted, unquoted
	value      string // a one-line key's value as written, without its comment
	comment    string // a one-line key's comment, with its #, or empty
}

// tomlDoc is a TOML file as lines and the statements they hold. It is not a TOML parser: it
// finds where each statement starts and ends (strings, arrays and inline tables over several
// lines included) so a change can replace or add whole statements and leave the rest as it is.
type tomlDoc struct {
	lines []string // without their line ends
	crlf  bool     // the file ends its lines with \r\n
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

// tomlValueEnd finds where the value that starts at lines[i][at:] ends: the line after its last
// one, and whether it took one line. It follows strings (basic, literal and both multi-line
// kinds), arrays and inline tables, and stops at a comment outside them.
func tomlValueEnd(lines []string, i, at int) (int, bool, error) {
	depth := 0
	inMulti := "" // `"""` or `'''` while inside one
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

// find is the key statement for table.key, or -1.
func (d *tomlDoc) find(table, key string) int {
	return slices.IndexFunc(d.stmts, func(s tomlStmt) bool { return s.kind == tomlKV && s.table == table && s.key == key })
}

// header is the header statement of table, or -1.
func (d *tomlDoc) header(table string) int {
	return slices.IndexFunc(d.stmts, func(s tomlStmt) bool { return s.kind == tomlHeader && s.table == table })
}

// lastKV is the line after the last key statement in table (from its header to the next), or
// the line after its header when it has none; -1 when the root table has no keys.
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

// reparse reads the document again after its lines changed.
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

// setKV makes table.key = value: the statement replaced where it is (keeping a one-line
// comment), else added after the table's last key, or for a new table at the end of the file.
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

// delKV takes out table.key's statement, if any.
func (d *tomlDoc) delKV(table, key string) error {
	if i := d.find(table, key); i >= 0 {
		d.splice(d.stmts[i].start, d.stmts[i].end)
		return d.reparse()
	}
	return nil
}

// value is table.key's value as written (a one-line statement's), and whether it is there.
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

// codexProvider is the table of pluxy's provider in Codex's config.
const codexProvider = "model_providers.pluxy"

// codexDropped are the provider's keys Connect takes out: either would have Codex send another
// credential than the bearer token.
var codexDropped = []string{"env_key", "requires_openai_auth"}

// codexDoc reads Codex's config for a change: refused when config.rs would refuse it
// (parseCodexConfigTOML) or when it holds pluxy's provider in a form this editor does not change
// (dotted keys or an inline table in place of the [model_providers.pluxy] table, or that table
// twice).
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
		// Keys of the provider's own sub-tables ([model_providers.pluxy.capabilities]) are fine.
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

// tomlEntry is one setting of the setup text: its table, key and value as written.
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

// applyCodexConfig sets each setting of snippet (the setup text's config.toml) in before: a root
// key after the file's last root key, so before its first table, and the provider's keys in its
// table. The provider's other credentials are taken out.
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

// codexKeys are the settings Connect sets or takes out, by table.
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

// clearCodexConfig is the writers' Clear for config.toml: original when current is what Connect
// wrote; otherwise current with each key Connect set or took out given back its original value,
// or taken out, where current still holds what Connect left there. A provider table Connect
// added is taken out once it has no keys left.
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
		// The blank line Connect put before the table it added, when the table ended the file.
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
