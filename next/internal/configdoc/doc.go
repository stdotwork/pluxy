// Package configdoc reads and edits core's config file as a document: the JSON config, or a
// config.yaml with today's core's key names. An edit keeps what it does not touch: key order,
// and in a config.yaml its comments, as today's core keeps them when it saves. It also redacts
// the secrets a document holds, for the management API, and puts them back when an edited
// document comes back (redact.go).
package configdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalid marks a document that does not parse or an edit that does not fit it.
var ErrInvalid = errors.New("invalid config document")

// Doc is a config file's contents as a tree of nodes. A JSON file is read into the same kind of
// tree as a config.yaml, so both are walked and edited by the same code.
type Doc struct {
	// YAML is set for a config.yaml; otherwise the document is JSON.
	YAML bool
	src  []byte
	file *yaml.Node // YAML: the document node, which holds the comments around the root
	root *yaml.Node // the top-level mapping
	// spans are where each JSON scalar's text is in src.
	spans  map[*yaml.Node]span
	edited bool
}

// span is a range of bytes in a document's source.
type span struct{ start, end int }

// Parse reads a config file's contents; isYAML says it is a config.yaml.
func Parse(b []byte, isYAML bool) (*Doc, error) {
	d := &Doc{YAML: isYAML, src: b}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, fmt.Errorf("%w: the config file is empty", ErrInvalid)
	}
	if isYAML {
		var n yaml.Node
		if err := yaml.Unmarshal(b, &n); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if n.Kind != yaml.DocumentNode || len(n.Content) == 0 {
			return nil, fmt.Errorf("%w: the config file is empty", ErrInvalid)
		}
		d.file, d.root = &n, n.Content[0]
	} else {
		root, spans, err := parseJSON(b)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		d.root, d.spans = root, spans
	}
	if d.root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: the config file must hold a mapping of settings", ErrInvalid)
	}
	return d, nil
}

// parseJSON reads a JSON document into nodes, keeping the order of keys and where each
// scalar's text is. Duplicate keys are kept; Get finds the last, as encoding/json reads it.
func parseJSON(b []byte) (*yaml.Node, map[*yaml.Node]span, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	spans := map[*yaml.Node]span{}
	// next returns where the next token starts: after white space and the separators the
	// decoder has not consumed yet.
	next := func() int {
		off := int(dec.InputOffset())
		for off < len(b) && strings.IndexByte(" \t\r\n,:", b[off]) >= 0 {
			off++
		}
		return off
	}
	var value func() (*yaml.Node, error)
	value = func() (*yaml.Node, error) {
		from := next()
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var n *yaml.Node
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				for dec.More() {
					k, err := dec.Token()
					if err != nil {
						return nil, err
					}
					v, err := value()
					if err != nil {
						return nil, err
					}
					n.Content = append(n.Content, scalar("!!str", k.(string)), v)
				}
			case '[':
				n = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
				for dec.More() {
					v, err := value()
					if err != nil {
						return nil, err
					}
					n.Content = append(n.Content, v)
				}
			default:
				return nil, fmt.Errorf("unexpected %q", t)
			}
			if _, err := dec.Token(); err != nil { // the closing delimiter
				return nil, err
			}
			return n, nil
		case string:
			n = scalar("!!str", t)
		case json.Number:
			tag := "!!int"
			if strings.ContainsAny(t.String(), ".eE") {
				tag = "!!float"
			}
			n = scalar(tag, t.String())
		case bool:
			n = scalar("!!bool", fmt.Sprint(t))
		case nil:
			n = scalar("!!null", "null")
		}
		spans[n] = span{from, int(dec.InputOffset())}
		return n, nil
	}
	root, err := value()
	if err != nil {
		return nil, nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, nil, errors.New("unexpected data after the config object")
	}
	return root, spans, nil
}

func scalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// String returns a string node.
func String(s string) *yaml.Node { return scalar("!!str", s) }

// Int returns an integer node.
func Int(n int64) *yaml.Node { return scalar("!!int", fmt.Sprint(n)) }

// Bool returns a boolean node.
func Bool(b bool) *yaml.Node { return scalar("!!bool", fmt.Sprint(b)) }

// isNull reports whether n is absent or an empty value (`key:` with nothing after it).
func isNull(n *yaml.Node) bool {
	return n == nil || (n.Kind == yaml.ScalarNode && n.Tag == "!!null")
}

// child returns the value of key in mapping m: the last one, when the key is there twice.
func child(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	var v *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if keyName(m.Content[i]) == key {
			v = m.Content[i+1]
		}
	}
	return v
}

// Get returns the node at path, or nil when there is none.
func (d *Doc) Get(path ...string) *yaml.Node {
	n := d.root
	for _, k := range path {
		if n = child(n, k); n == nil {
			return nil
		}
	}
	return n
}

// Set puts v at path, adding the mappings on the way that are missing or empty. A value it
// replaces keeps its comments.
func (d *Doc) Set(v *yaml.Node, path ...string) error {
	m := d.root
	for i, k := range path {
		last := i == len(path)-1
		cur := child(m, k)
		switch {
		case last && cur != nil:
			v.HeadComment, v.LineComment, v.FootComment = cur.HeadComment, cur.LineComment, cur.FootComment
			*cur = *v
		case last:
			m.Content = append(m.Content, String(k), v)
		case cur == nil:
			cur = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, String(k), cur)
		case isNull(cur):
			*cur = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: cur.HeadComment, LineComment: cur.LineComment, FootComment: cur.FootComment}
		case cur.Kind != yaml.MappingNode:
			return fmt.Errorf("%w: %s is not a mapping", ErrInvalid, strings.Join(path[:i+1], "."))
		}
		m = cur
	}
	d.edited = true
	return nil
}

// Delete removes the key at path, if it is there.
func (d *Doc) Delete(path ...string) {
	m := d.Get(path[:len(path)-1]...)
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	key := path[len(path)-1]
	for i := len(m.Content) - 2; i >= 0; i -= 2 {
		if keyName(m.Content[i]) == key {
			m.Content = slices.Delete(m.Content, i, i+2)
			d.edited = true
		}
	}
}

// Bytes returns the document's contents: the source as read when nothing changed, else the
// tree written out. A config.yaml keeps its comments and key order, with two-space indents
// and without blank lines, as today's core writes it; a JSON file keeps its key order and the
// indent it had.
func (d *Doc) Bytes() ([]byte, error) {
	if !d.edited {
		return d.src, nil
	}
	return d.encode()
}

func (d *Doc) encode() ([]byte, error) {
	if d.YAML {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(d.file); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	var buf bytes.Buffer
	if err := encodeJSON(&buf, d.root); err != nil {
		return nil, err
	}
	indent := jsonIndent(d.src)
	if indent == "" {
		return append(buf.Bytes(), '\n'), nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// jsonIndent returns the indent of the first indented line of a JSON document: empty for a
// document on one line.
func jsonIndent(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n"))[1:] {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) > 0 && len(trimmed) < len(line) {
			return string(line[:len(line)-len(trimmed)])
		}
	}
	return ""
}

func encodeJSON(buf *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.MappingNode:
		buf.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(jsonString(n.Content[i].Value))
			buf.WriteByte(':')
			if err := encodeJSON(buf, n.Content[i+1]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case yaml.SequenceNode:
		buf.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeJSON(buf, c); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int", "!!float", "!!bool":
			if !json.Valid([]byte(n.Value)) {
				return fmt.Errorf("%w: %q is not a JSON value", ErrInvalid, n.Value)
			}
			buf.WriteString(n.Value)
		case "!!null":
			buf.WriteString("null")
		default:
			buf.WriteString(jsonString(n.Value))
		}
	default:
		return fmt.Errorf("%w: a JSON config cannot hold YAML node kind %d", ErrInvalid, n.Kind)
	}
	return nil
}

// jsonString returns s as a JSON string, which is also a YAML double-quoted scalar.
func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}
