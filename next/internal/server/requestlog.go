package server

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// RequestLog says which client requests core writes to files in its logs folder, as today's
// core does: every request while request-log is on; while it is off, each request that failed,
// as error-<name>.log, keeping the newest ErrorMaxFiles of those.
type RequestLog struct {
	Dir string
	// All is today's request-log.
	All bool
	// ErrorMaxFiles is how many error logs are kept (today's error-logs-max-files); 0 keeps all.
	ErrorMaxFiles int
	// Redact removes the config's secrets (client keys, the management key, the accounts'
	// credentials) from everything a request log holds.
	Redact *protocol.Redactor
	// Trim applies the logs folder's size cap; nil: none.
	Trim func()
}

// How much of a reply a request log keeps: its start and its end, so the error that ends a long
// stream is kept. Every request keeps them until it ends, as any of them may fail, so with
// request-log off (error logs only) a request keeps less.
const (
	loggedHead      = 256 << 10
	loggedTail      = 768 << 10
	errorLoggedHead = 32 << 10
	errorLoggedTail = 96 << 10
)

// capture keeps the start and the end of what is written to it, and counts the rest.
type capture struct {
	mu        sync.Mutex
	head      []byte
	tail      []byte // a ring of tailLimit bytes once full
	at        int    // where the ring's oldest byte is
	written   int64
	headLimit int
	tailLimit int
}

// newCapture returns a capture for l's request logs.
func (l *RequestLog) newCapture() *capture {
	if l.All {
		return &capture{headLimit: loggedHead, tailLimit: loggedTail}
	}
	return &capture{headLimit: errorLoggedHead, tailLimit: errorLoggedTail}
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.written += int64(n)
	if room := c.headLimit - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	for len(p) > 0 {
		if len(c.tail) < c.tailLimit {
			k := min(c.tailLimit-len(c.tail), len(p))
			c.tail = append(c.tail, p[:k]...)
			p = p[k:]
			continue
		}
		k := copy(c.tail[c.at:], p)
		c.at = (c.at + k) % c.tailLimit
		p = p[k:]
	}
	return n, nil
}

// bytes returns what was kept, with a note where bytes in the middle were left out.
func (c *capture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := slices.Clone(c.head)
	kept := int64(len(c.head) + len(c.tail))
	if skipped := c.written - kept; skipped > 0 {
		out = fmt.Appendf(out, "\n[... %d bytes not logged ...]\n", skipped)
	}
	return append(append(out, c.tail[c.at:]...), c.tail[:c.at]...)
}

// recorder is the client's ResponseWriter, keeping the reply's status, headers and body for
// the request log.
type recorder struct {
	http.ResponseWriter
	status int
	header http.Header
	body   *capture
}

func (w *recorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.header = w.ResponseWriter.Header().Clone()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the client's writer to flush it.
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// requestEntry is one request as its request log writes it.
type requestEntry struct {
	at       time.Time
	method   string
	url      *url.URL
	headers  http.Header
	body     []byte
	trace    *trace
	status   int
	rHeaders http.Header
	reply    []byte
	// replyTitle names the reply's section: RESPONSE, or WEBSOCKET MESSAGES for a turn.
	replyTitle string
}

// failed reports whether the request counts as failed for an error log, as in today's core: a
// reply status of 400 or more, or a failure after the reply began. A client that went away is
// not a failure.
func (e *requestEntry) failed() bool {
	return e.status >= 400 || e.trace != nil && e.trace.failed
}

// write writes e to its request log file, if e is to be logged.
func (l *RequestLog) write(e *requestEntry) {
	if l == nil || !l.All && !e.failed() {
		return
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		log.Printf("request log: %v", err)
		return
	}
	name := logFileName(e.url.Path, e.at, e.trace.id)
	if !l.All {
		name = "error-" + name
	}
	// Always a new file: an existing name, or a link planted under it, is never written through.
	f, err := os.OpenFile(filepath.Join(l.Dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		log.Printf("request log: %v", err)
		return
	}
	_, err = f.Write(l.format(e))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		log.Printf("request log: %v", err)
		return
	}
	if !l.All {
		l.keepNewestErrors()
	}
	if l.Trim != nil {
		l.Trim()
	}
}

// logFileName names a request's log file as today's core does, by its path and time, ending in
// its trace ID: v1-chat-completions-2026-11-09T120000-<trace>.log.
func logFileName(path string, at time.Time, id string) string {
	var b strings.Builder
	for _, r := range strings.Trim(path, "/") {
		if b.Len() >= 100 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return fmt.Sprintf("%s-%s-%s.log", b.String(), at.Format("2006-01-02T150405"), id)
}

// keepNewestErrors deletes the oldest error logs beyond ErrorMaxFiles.
func (l *RequestLog) keepNewestErrors() {
	if l.ErrorMaxFiles <= 0 {
		return
	}
	files, err := ErrorLogs(l.Dir)
	if err != nil {
		return
	}
	for _, f := range files[min(l.ErrorMaxFiles, len(files)):] {
		_ = os.Remove(filepath.Join(l.Dir, f.Name))
	}
}

// LogFile is a request log file as the management API lists it.
type LogFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Modified is when it was last written, in Unix seconds.
	Modified int64 `json:"modified"`
}

// ErrorLogs returns the error logs in dir, newest first.
func ErrorLogs(dir string) ([]LogFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogFile{}, nil
		}
		return nil, err
	}
	files := []LogFile{}
	mod := map[string]time.Time{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !IsErrorLogName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, LogFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime().Unix()})
		mod[e.Name()] = info.ModTime()
	}
	slices.SortFunc(files, func(a, b LogFile) int {
		if c := mod[b.Name].Compare(mod[a.Name]); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	return files, nil
}

// IsErrorLogName reports whether name is an error log's: error-<...>.log, with no path in it.
func IsErrorLogName(name string) bool {
	return strings.HasPrefix(name, "error-") && strings.HasSuffix(name, ".log") && !strings.ContainsAny(name, `/\`)
}

// FindRequestLog returns the name of the newest request log in dir, plain or error, of the
// request with trace ID id; "" when there is none.
func FindRequestLog(dir, id string) string {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), "-"+id+".log") {
			continue
		}
		info, err := e.Info()
		if err == nil && (best == "" || info.ModTime().After(bestMod)) {
			best, bestMod = e.Name(), info.ModTime()
		}
	}
	return best
}

// sensitiveName reports whether a header or query parameter name may carry a secret, as today's
// core judges it, plus cookies and Google's key parameter.
func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"authorization", "api-key", "apikey", "api_key", "token", "secret", "cookie", "password"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return n == "key"
}

// maskValue hides a secret header value, keeping an authorization scheme such as Bearer.
func maskValue(name, v string) string {
	if scheme, _, ok := strings.Cut(v, " "); ok && strings.Contains(strings.ToLower(name), "authorization") && scheme != "" {
		return scheme + " " + protocol.Redacted
	}
	return protocol.Redacted
}

func writeHeaders(b *bytes.Buffer, h http.Header) {
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range h {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, v := range h[name] {
			if sensitiveName(name) {
				v = maskValue(name, v)
			}
			fmt.Fprintf(b, "%s: %s\n", name, v)
		}
	}
}

// format writes e in today's core's sections, with every secret it can find removed: header
// values and query parameters by name, and the config's secrets anywhere.
func (l *RequestLog) format(e *requestEntry) []byte {
	u := *e.url
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			if sensitiveName(k) {
				q[k] = []string{protocol.Redacted}
			}
		}
		u.RawQuery = q.Encode()
	}
	var b bytes.Buffer
	b.WriteString("=== REQUEST INFO ===\n")
	fmt.Fprintf(&b, "URL: %s\nMethod: %s\nTimestamp: %s\n", u.RequestURI(), e.method, e.at.Format(time.RFC3339Nano))
	if e.trace != nil {
		fmt.Fprintf(&b, "Trace ID: %s\n", e.trace.id)
		if e.trace.account != "" {
			fmt.Fprintf(&b, "Account: %s\n", e.trace.account)
		}
	}
	b.WriteString("\n=== HEADERS ===\n")
	writeHeaders(&b, e.headers)
	b.WriteString("\n=== REQUEST BODY ===\n")
	b.Write(e.body)
	fmt.Fprintf(&b, "\n\n=== %s ===\n", e.replyTitle)
	if e.status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", e.status)
	}
	writeHeaders(&b, e.rHeaders)
	b.WriteString("\n")
	b.Write(e.reply)
	b.WriteString("\n")
	return []byte(l.Redact.String(b.String()))
}

// keepBody keeps the request's body, once read, for its request log.
func keepBody(r *http.Request, body []byte) {
	if t := traceOf(r); t != nil {
		t.body = body
	}
}

// logged runs h for a client request and writes the request's log when the generation's
// request log calls for it.
func logged(w http.ResponseWriter, r *http.Request, g *Generation, h func(w http.ResponseWriter, r *http.Request)) {
	if g.RequestLog == nil {
		h(w, r)
		return
	}
	at := time.Now()
	t := newTrace()
	r = withTrace(r, t)
	rec := &recorder{ResponseWriter: w, body: g.RequestLog.newCapture()}
	h(rec, r)
	g.RequestLog.write(&requestEntry{at: at, method: r.Method, url: r.URL, headers: r.Header, body: t.body, trace: t,
		status: rec.status, rHeaders: rec.header, reply: rec.body.bytes(), replyTitle: "RESPONSE"})
}

// requestTrace returns r's trace, adding a new one when it has none.
func requestTrace(r *http.Request) (*http.Request, *trace) {
	if t := traceOf(r); t != nil {
		return r, t
	}
	t := newTrace()
	return withTrace(r, t), t
}
