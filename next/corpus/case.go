// Package corpus is the differential test corpus: cases taken from today's core (`core/`), each
// replayed against the new core and compared with what the old core does, at the HTTP seam.
//
// A case is one JSON file under cases/<client format>/<provider>/. It holds the client request,
// the replies a fake upstream server gives, the old core's recorded observation and a list of
// exceptions: differences from the old core that are known, each with a reason. See README.md.
package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Case is one corpus case file.
type Case struct {
	// Description says what behaviour the case pins down.
	Description string `json:"description"`
	// Source names where the behaviour comes from in today's core: tests, code or issues.
	Source []string `json:"source,omitempty"`
	// Client is the request the client sends.
	Client ClientRequest `json:"client"`
	// AccountModels, when set, are the models the provider account lists in both cores. By
	// default the old core lists the request's model (it serves only listed models) and the
	// new core lists none (it serves any model).
	AccountModels []string `json:"account_models,omitempty"`
	// Upstream lists the fake upstream's replies, one per upstream request, in order.
	Upstream []Reply `json:"upstream,omitempty"`
	// Volatile lists observation paths whose values change from run to run; they are masked
	// before any comparison.
	Volatile []string `json:"volatile,omitempty"`
	// Exceptions lists known differences between the new core and the old one.
	Exceptions []Exception `json:"exceptions,omitempty"`
	// Recorded is the old core's observation, written by a record run.
	Recorded json.RawMessage `json:"recorded_old_core,omitempty"`

	// Set by Load.
	Name     string `json:"-"` // file name without .json
	Format   string `json:"-"` // client format directory, for example openai-chat
	Provider string `json:"-"` // provider directory, for example openai-compatible
	Path     string `json:"-"`
}

// ClientRequest is the request the corpus client sends to a core.
type ClientRequest struct {
	Method string `json:"method,omitempty"` // default POST
	Path   string `json:"path"`
	// Headers are sent as given. The runner adds Content-Type: application/json and a fixed
	// User-Agent unless set here.
	Headers map[string]string `json:"headers,omitempty"`
	// Auth is "client-key" (default: the corpus client key as a bearer token), "none", or
	// any other value, sent as the bearer token.
	Auth string `json:"auth,omitempty"`
	// Body is a JSON body. BodyText is a literal body, for requests that are not valid JSON.
	Body     json.RawMessage `json:"body,omitempty"`
	BodyText *string         `json:"body_text,omitempty"`
}

// Reply is one scripted upstream reply.
type Reply struct {
	Status  int               `json:"status,omitempty"` // default 200
	Headers map[string]string `json:"headers,omitempty"`
	// One of Body (a JSON value), BodyText (literal bytes) or SSE (a stream).
	Body     json.RawMessage `json:"body,omitempty"`
	BodyText *string         `json:"body_text,omitempty"`
	SSE      []SSEEvent      `json:"sse,omitempty"`
	// Abort drops the connection after the scripted body instead of ending the reply cleanly.
	Abort bool `json:"abort,omitempty"`
}

// SSEEvent is one server-sent event the fake upstream writes. Data is a JSON value written in
// compact form; a JSON string is written as its text (so "[DONE]" is written as [DONE]).
// Raw is written byte for byte, for malformed streams.
type SSEEvent struct {
	Event   string          `json:"event,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Comment *string         `json:"comment,omitempty"`
	Raw     *string         `json:"raw,omitempty"`
}

// Exception is a known difference between the new core and the old one at Path.
type Exception struct {
	// Path is an observation path (see README.md). It covers every difference at or below it.
	// A "*" segment matches any key or index.
	Path string `json:"path"`
	// Kind is "intended" (the new core differs on purpose) or "gap" (the new core does not
	// match yet; a later slice builds it or the owner decides).
	Kind string `json:"kind"`
	// Reason says why the difference is there. Required.
	Reason string `json:"reason"`
	// Want, when set, is the value the new core must give at Path.
	Want json.RawMessage `json:"want,omitempty"`
}

const (
	KindIntended = "intended"
	KindGap      = "gap"
)

// Global holds settings that apply to every case: cases/corpus.json.
type Global struct {
	Description string      `json:"description,omitempty"`
	Volatile    []string    `json:"volatile,omitempty"`
	Exceptions  []Exception `json:"exceptions,omitempty"`
}

// LoadGlobal reads cases/corpus.json.
func LoadGlobal(dir string) (*Global, error) {
	b, err := os.ReadFile(filepath.Join(dir, "corpus.json"))
	if err != nil {
		return nil, err
	}
	var g Global
	if err := strictUnmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("corpus.json: %w", err)
	}
	for i, e := range g.Exceptions {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("corpus.json: exceptions[%d]: %w", i, err)
		}
	}
	return &g, nil
}

// LoadCases reads every case under dir, sorted by format, provider and name.
func LoadCases(dir string) ([]*Case, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*", "*", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []*Case
	for _, p := range paths {
		c, err := LoadCase(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// LoadCase reads and checks one case file.
func LoadCase(path string) (*Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Case
	if err := strictUnmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	c.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	c.Provider = filepath.Base(filepath.Dir(path))
	c.Format = filepath.Base(filepath.Dir(filepath.Dir(path)))
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Case) validate() error {
	if c.Description == "" {
		return fmt.Errorf("description is required")
	}
	if c.Client.Path == "" {
		return fmt.Errorf("client.path is required")
	}
	if len(c.Client.Body) > 0 && c.Client.BodyText != nil {
		return fmt.Errorf("client: set body or body_text, not both")
	}
	for i, r := range c.Upstream {
		n := 0
		if len(r.Body) > 0 {
			n++
		}
		if r.BodyText != nil {
			n++
		}
		if len(r.SSE) > 0 {
			n++
		}
		if n > 1 {
			return fmt.Errorf("upstream[%d]: set one of body, body_text or sse", i)
		}
	}
	for i, e := range c.Exceptions {
		if err := e.validate(); err != nil {
			return fmt.Errorf("exceptions[%d]: %w", i, err)
		}
	}
	for _, p := range c.Volatile {
		if _, err := parsePath(p); err != nil {
			return fmt.Errorf("volatile %q: %w", p, err)
		}
	}
	return nil
}

func (e Exception) validate() error {
	if _, err := parsePath(e.Path); err != nil {
		return fmt.Errorf("path %q: %w", e.Path, err)
	}
	if e.Kind != KindIntended && e.Kind != KindGap {
		return fmt.Errorf("kind must be %q or %q, got %q", KindIntended, KindGap, e.Kind)
	}
	if strings.TrimSpace(e.Reason) == "" {
		return fmt.Errorf("reason is required")
	}
	return nil
}

// SaveRecorded writes obs into the case file as its recorded old-core observation. The rest of
// the file keeps its keys, order and values; the whole file is laid out by FormatJSON.
func (c *Case) SaveRecorded(obs any) error {
	b, err := os.ReadFile(c.Path)
	if err != nil {
		return err
	}
	root, err := parseOrdered(b)
	if err != nil {
		return err
	}
	if root.keys == nil {
		return fmt.Errorf("case file is not a JSON object")
	}
	rec, err := json.Marshal(obs)
	if err != nil {
		return err
	}
	recNode, err := parseOrdered(rec)
	if err != nil {
		return err
	}
	root.set("recorded_old_core", recNode)
	return os.WriteFile(c.Path, root.format(), 0o644)
}

func strictUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// FormatJSON lays out a JSON document the way case files are written: two-space indent, key
// order kept, any object or array that fits in lineWidth characters on one line, and each
// stream event (items of "sse" and "events" arrays) on a line of its own.
func FormatJSON(b []byte) ([]byte, error) {
	n, err := parseOrdered(b)
	if err != nil {
		return nil, err
	}
	return n.format(), nil
}

const lineWidth = 100

// node is a parsed JSON value that keeps object key order.
type node struct {
	keys   []string         // object keys in order; nil unless an object
	fields map[string]*node // object fields
	items  []*node          // array items
	array  bool
	scalar json.RawMessage // compact scalar
	// eventList marks an "sse" or "events" array, whose items are written one per line.
	eventList bool
}

func parseOrdered(b []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	n, err := parseNode(dec)
	if err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return n, nil
}

func parseNode(dec *json.Decoder) (*node, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		n := &node{keys: []string{}, fields: map[string]*node{}}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k := kt.(string)
			v, err := parseNode(dec)
			if err != nil {
				return nil, err
			}
			n.set(k, v)
		}
		_, err := dec.Token()
		return n, err
	case json.Delim('['):
		n := &node{array: true}
		for dec.More() {
			v, err := parseNode(dec)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, v)
		}
		_, err := dec.Token()
		return n, err
	}
	return &node{scalar: marshalPlain(t)}, nil
}

func (n *node) set(k string, v *node) {
	if _, ok := n.fields[k]; !ok {
		n.keys = append(n.keys, k)
	}
	n.fields[k] = v
}

func (n *node) compact(buf *bytes.Buffer) {
	switch {
	case n.keys != nil:
		buf.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb := marshalPlain(k)
			buf.Write(kb)
			buf.WriteByte(':')
			n.fields[k].compact(buf)
		}
		buf.WriteByte('}')
	case n.array:
		buf.WriteByte('[')
		for i, v := range n.items {
			if i > 0 {
				buf.WriteByte(',')
			}
			v.compact(buf)
		}
		buf.WriteByte(']')
	default:
		buf.Write(n.scalar)
	}
}

func (n *node) format() []byte {
	var buf bytes.Buffer
	n.write(&buf, "", 0, false)
	buf.WriteByte('\n')
	return buf.Bytes()
}

// write writes n at the given indent; used is how many characters the line already holds.
// oneLine writes n compactly whatever its width.
func (n *node) write(buf *bytes.Buffer, indent string, used int, oneLine bool) {
	var c bytes.Buffer
	n.compact(&c)
	if oneLine || (n.keys == nil && !n.array) || used+c.Len() <= lineWidth || (len(n.keys) == 0 && len(n.items) == 0) {
		buf.Write(c.Bytes())
		return
	}
	inner := indent + "  "
	if n.array {
		buf.WriteString("[\n")
		for i, v := range n.items {
			buf.WriteString(inner)
			v.write(buf, inner, len(inner), n.eventList)
			if i < len(n.items)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "]")
		return
	}
	buf.WriteString("{\n")
	for i, k := range n.keys {
		kb := marshalPlain(k)
		buf.WriteString(inner)
		buf.Write(kb)
		buf.WriteString(": ")
		v := n.fields[k]
		v.eventList = v.array && (k == "sse" || k == "events")
		v.write(buf, inner, len(inner)+len(kb)+2, false)
		if i < len(n.keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(indent + "}")
}

// marshalPlain encodes v as JSON without escaping <, > and &, so case files stay readable.
func marshalPlain(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
