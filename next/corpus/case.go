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
	// Then lists further requests the client sends after Client, one at a time, each once the
	// reply to the one before has ended: for behaviour that shows across requests, such as which
	// account each request goes to or a cooldown a failure leaves. Not WebSocket requests.
	Then []ClientRequest `json:"then,omitempty"`
	// AccountModels, when set, are the models the provider account lists in both cores. By
	// default the old core lists the request's model (it serves only listed models) and the
	// new core lists none (it serves any model).
	AccountModels []string `json:"account_models,omitempty"`
	// Accounts is how many provider accounts both cores have, all pointed at the fake
	// upstream, each with its own key (see UpstreamKeys). Default 1.
	Accounts int `json:"accounts,omitempty"`
	// LabelAccounts replaces each account's upstream key in the requests the upstream received
	// with "account-1", "account-2" and so on, numbered in the order the accounts were first
	// used, so a case can compare which account each request went to although today's core
	// orders equal accounts differently on each run.
	LabelAccounts bool `json:"label_accounts,omitempty"`
	// NewCoreConfig holds settings added to the new core's config (top-level keys of its JSON
	// config), for a behaviour that today's core has by default and the new core only when
	// configured (such as CORS). Today's core is run as usual.
	NewCoreConfig map[string]json.RawMessage `json:"new_core_config,omitempty"`
	// Upstream lists the fake upstream's replies, one per upstream request, in order.
	Upstream []Reply `json:"upstream,omitempty"`
	// Volatile lists observation paths whose values change from run to run; they are masked
	// before any comparison.
	Volatile []string `json:"volatile,omitempty"`
	// Unordered lists observation paths of arrays whose order is not compared (today's core
	// lists models in no fixed order): after masking, each is sorted by its elements' JSON.
	Unordered []string `json:"unordered,omitempty"`
	// OldCore and NewCore add settings to each core's generated config for this case, in that
	// core's own config format.
	OldCore *CoreOverride `json:"old_core,omitempty"`
	NewCore *CoreOverride `json:"new_core,omitempty"`
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
	// WebSocket, when set, makes the request a WebSocket handshake (GET with an upgrade) and
	// scripts what the client does on the socket. It is used instead of a body.
	WebSocket []ClientStep `json:"websocket,omitempty"`
}

// ClientStep is one step of a WebSocket client's script; set exactly one field. After the last
// step the client closes the socket normally, unless a step already closed or dropped it.
type ClientStep struct {
	// Send sends a JSON value as one text message; SendText sends literal text.
	Send     json.RawMessage `json:"send,omitempty"`
	SendText *string         `json:"send_text,omitempty"`
	// Read reads messages until one whose type is Read, or until the socket closes. "turn"
	// stands for any message that ends a turn: response.completed, response.done,
	// response.incomplete, response.failed or error.
	Read string `json:"read,omitempty"`
	// AwaitDrop waits until the fake upstream's WebSocket that is upstream[n] (counting from 0) has
	// dropped its connection and the core has closed its side of it, so the core knows the
	// upstream is gone. The upstream drops right after its last message, but the client does not
	// wait for that: without this step, whether the core has noticed by the client's next
	// message depends on timing.
	AwaitDrop *int `json:"await_drop,omitempty"`
	// Close closes the socket with this close code.
	Close int `json:"close,omitempty"`
	// Drop drops the connection without a close message.
	Drop bool `json:"drop,omitempty"`
}

// CoreOverride adds settings to a core's generated config. Objects merge key by key; any other
// value replaces what is there, and null removes it.
type CoreOverride struct {
	// Config merges into the config at the top level.
	Config map[string]any `json:"config,omitempty"`
	// Account merges into each provider account: for the old core each group of its api-keys
	// block and each auth file, for the new core each account.
	Account map[string]any `json:"account,omitempty"`
	// Accounts merges its n-th entry into the n-th account, after Account: for the new core
	// its n-th account, for the old core the n-th key of its api-keys block (the n-th entry of
	// a section with one entry per key, or the n-th of a group's keys). For settings that differ
	// between accounts, such as a weight.
	Accounts []map[string]any `json:"accounts,omitempty"`
}

// Merge returns base with over merged into it (see CoreOverride). base is changed in place.
func Merge(base, over map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for k, v := range over {
		switch v := v.(type) {
		case nil:
			delete(base, k)
		case map[string]any:
			cur, _ := base[k].(map[string]any)
			base[k] = Merge(cur, v)
		default:
			base[k] = v
		}
	}
	return base
}

// Reply is one scripted upstream reply.
type Reply struct {
	Status  int               `json:"status,omitempty"` // default 200
	Headers map[string]string `json:"headers,omitempty"`
	// One of Body (a JSON value), BodyText (literal bytes), SSE (a stream) or Connect (a
	// Connect stream of protobuf messages, see connect.go).
	Body     json.RawMessage `json:"body,omitempty"`
	BodyText *string         `json:"body_text,omitempty"`
	SSE      []SSEEvent      `json:"sse,omitempty"`
	Connect  []ConnectFrame  `json:"connect,omitempty"`
	// Abort drops the connection after the scripted body instead of ending the reply cleanly.
	Abort bool `json:"abort,omitempty"`
	// Hold keeps the reply open after the scripted body until the core ends the request, as an
	// upstream that is still generating does.
	Hold bool `json:"hold,omitempty"`
	// WebSocket accepts the request as a WebSocket handshake and scripts the upstream's side
	// of the socket. It is used instead of a status and body; a request that is not a handshake
	// gets a 500 reply.
	WebSocket []UpstreamStep `json:"websocket,omitempty"`
}

// UpstreamStep is one step of the fake upstream's side of a WebSocket; set exactly one field.
// Every message the core sends is recorded, whenever it arrives. After the last step the fake
// upstream keeps reading until the core closes the socket.
type UpstreamStep struct {
	// Receive waits for the next message from the core that no earlier Receive took.
	Receive bool `json:"receive,omitempty"`
	// Send sends a JSON value as one text message.
	Send json.RawMessage `json:"send,omitempty"`
	// Close closes the socket with a close message.
	Close *CloseFrame `json:"close,omitempty"`
	// Drop drops the connection without a close message.
	Drop bool `json:"drop,omitempty"`
}

// CloseFrame is a WebSocket close message.
type CloseFrame struct {
	Code   int    `json:"code"`
	Reason string `json:"reason,omitempty"`
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
	// Path is an observation path (see README.md). Without Want or Absent it covers only the
	// difference at Path itself, and only when neither side is an object or array. With Want or
	// Absent it also covers the differences below Path, because the new core's whole value at
	// Path is pinned. A "*" segment matches any key or index.
	Path string `json:"path"`
	// Kind is "intended" (the new core differs on purpose) or "gap" (the new core does not
	// match yet; a later slice builds it or the owner decides).
	Kind string `json:"kind"`
	// Reason says why the difference is there. Required.
	Reason string `json:"reason"`
	// Want, when set, is the value the new core must give at Path, in full. Required when the
	// exception covers an object or array on either side.
	Want json.RawMessage `json:"want,omitempty"`
	// Absent, when true, says the new core must give no value at Path. Used instead of Want
	// where the new core drops what the old core sends.
	Absent bool `json:"absent,omitempty"`
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
	if c.Accounts < 0 {
		return fmt.Errorf("accounts must not be negative")
	}
	if len(c.Client.Body) > 0 && c.Client.BodyText != nil {
		return fmt.Errorf("client: set body or body_text, not both")
	}
	if len(c.Client.WebSocket) > 0 && (len(c.Client.Body) > 0 || c.Client.BodyText != nil) {
		return fmt.Errorf("client: a websocket script has no body")
	}
	for i, t := range c.Then {
		switch {
		case t.Path == "":
			return fmt.Errorf("then[%d].path is required", i)
		case len(t.WebSocket) > 0:
			return fmt.Errorf("then[%d]: only the first request may be a websocket", i)
		case len(t.Body) > 0 && t.BodyText != nil:
			return fmt.Errorf("then[%d]: set body or body_text, not both", i)
		}
	}
	for i, s := range c.Client.WebSocket {
		if n := count(len(s.Send) > 0, s.SendText != nil, s.Read != "", s.AwaitDrop != nil, s.Close != 0, s.Drop); n != 1 {
			return fmt.Errorf("client.websocket[%d]: set exactly one of send, send_text, read, await_drop, close or drop", i)
		}
	}
	for i, r := range c.Upstream {
		for j, s := range r.WebSocket {
			if n := count(s.Receive, len(s.Send) > 0, s.Close != nil, s.Drop); n != 1 {
				return fmt.Errorf("upstream[%d].websocket[%d]: set exactly one of receive, send, close or drop", i, j)
			}
		}
		if len(r.WebSocket) > 0 && (r.Status != 0 || len(r.Body) > 0 || r.BodyText != nil || len(r.SSE) > 0 || len(r.Connect) > 0 || r.Abort || r.Hold) {
			return fmt.Errorf("upstream[%d]: a websocket reply has no status, body or sse", i)
		}
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
		if len(r.Connect) > 0 {
			n++
			if _, err := encodeConnect(r.Connect); err != nil {
				return fmt.Errorf("upstream[%d]: %w", i, err)
			}
		}
		if n > 1 {
			return fmt.Errorf("upstream[%d]: set one of body, body_text, sse or connect", i)
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

func count(conds ...bool) int {
	n := 0
	for _, c := range conds {
		if c {
			n++
		}
	}
	return n
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
	if len(e.Want) > 0 && e.Absent {
		return fmt.Errorf("set want or absent, not both")
	}
	if len(e.Want) > 0 {
		if _, err := decodeValue(e.Want); err != nil {
			return fmt.Errorf("want: %w", err)
		}
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
