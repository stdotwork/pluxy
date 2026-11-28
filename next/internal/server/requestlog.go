package server

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/private"
	"github.com/stdotwork/pluxy/next/protocol"
)

type RequestLog struct {
	Dir           string
	All           bool
	ErrorMaxFiles int
	Redact        *protocol.Redactor
	Trim          func()
}

const (
	loggedHead      = 256 << 10
	loggedTail      = 768 << 10
	errorLoggedHead = 32 << 10
	errorLoggedTail = 96 << 10
)

type capture struct {
	mu        sync.Mutex
	head      []byte
	tail      []byte
	at        int
	written   int64
	headLimit int
	tailLimit int
	redact    *protocol.Redactor
}

func (l *RequestLog) newCapture() *capture {
	if l.All {
		return &capture{headLimit: loggedHead, tailLimit: loggedTail, redact: l.Redact}
	}
	return &capture{headLimit: errorLoggedHead, tailLimit: errorLoggedTail, redact: l.Redact}
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

func (c *capture) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := append(slices.Clone(c.tail[c.at:]), c.tail[:c.at]...)
	skipped := c.written - int64(len(c.head)+len(tail))
	if skipped <= 0 {
		return append(slices.Clone(c.head), tail...)
	}
	head, end := trimGap(c.redact, string(c.head), string(tail))
	skipped += int64(len(c.head) - len(head) + len(tail) - len(end))
	return fmt.Appendf([]byte(head), "\n[... %d bytes not logged ...]\n%s", skipped, end)
}

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

type requestEntry struct {
	at         time.Time
	method     string
	url        *url.URL
	client     netip.Addr
	headers    http.Header
	body       []byte
	trace      *trace
	status     int
	rHeaders   http.Header
	reply      *capture
	replyTitle string
	panicked   bool
}

func (e *requestEntry) failed() bool {
	return e.status >= 400 || e.panicked || e.trace != nil && e.trace.failed
}

func (l *RequestLog) write(e *requestEntry) {
	if l == nil || !l.All && !e.failed() {
		return
	}
	root, err := private.Dir{Path: l.Dir}.OpenRoot(true)
	if err != nil {
		log.Printf("request log: %v", err)
		return
	}
	defer root.Close()
	name := logFileName(e.url.Path, e.at, e.trace.id)
	if !l.All {
		name = "error-" + name
	}
	f, tmp, err := private.CreateTemp(root, l.Dir, ".writing-")
	if err != nil {
		log.Printf("request log: %v", err)
		return
	}
	_, err = f.Write(l.format(e))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Link(tmp, name)
	}
	_ = root.Remove(tmp)
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

type LogFile struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

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

var requestLogName = regexp.MustCompile(`^(error-)?[A-Za-z0-9._-]{0,100}-[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{6}-([0-9]{14}-[0-9a-f]{16})\.log$`)

func IsErrorLogName(name string) bool {
	m := requestLogName.FindStringSubmatch(name)
	return m != nil && m[1] != ""
}

func FindRequestLog(dir, id string) string {
	if !traceID.MatchString(id) {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		m := requestLogName.FindStringSubmatch(e.Name())
		if !e.Type().IsRegular() || m == nil || m[2] != id {
			continue
		}
		info, err := e.Info()
		if err == nil && (best == "" || info.ModTime().After(bestMod)) {
			best, bestMod = e.Name(), info.ModTime()
		}
	}
	return best
}

var traceID = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{16}$`)

func sensitiveName(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"authorization", "api-key", "apikey", "api_key", "token", "secret", "cookie", "password"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return n == "key" // Google's key parameter
}

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
	if e.client.IsValid() {
		fmt.Fprintf(&b, "Client IP: %s\n", e.client)
	}
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
	b.WriteString("\n\n")
	if e.trace != nil {
		e.trace.upstream.write(&b)
	}
	fmt.Fprintf(&b, "=== %s ===\n", e.replyTitle)
	if e.status != 0 {
		fmt.Fprintf(&b, "Status: %d\n", e.status)
	}
	writeHeaders(&b, e.rHeaders)
	b.WriteString("\n")
	if e.reply != nil {
		b.Write(e.reply.bytes())
	}
	b.WriteString("\n")
	return []byte(l.Redact.String(b.String()))
}

func keepBody(r *http.Request, body []byte) {
	if t := traceOf(r); t != nil {
		t.body = body
	}
}

func logged(w http.ResponseWriter, r *http.Request, g *Generation, h func(w http.ResponseWriter, r *http.Request)) {
	if g.RequestLog == nil {
		h(w, r)
		return
	}
	at := time.Now()
	r, t := requestTrace(r)
	if g.RequestLog.All {
		r = r.WithContext(t.captureUpstream(r.Context(), g.RequestLog.Redact))
	}
	rec := &recorder{ResponseWriter: w, body: g.RequestLog.newCapture()}
	finished := false
	defer func() {
		g.RequestLog.write(&requestEntry{at: at, method: r.Method, url: r.URL, client: ClientIP(r), headers: r.Header, body: t.body, trace: t,
			status: rec.status, rHeaders: rec.header, reply: rec.body, replyTitle: "RESPONSE", panicked: !finished})
	}()
	h(rec, r)
	finished = true
}

func requestTrace(r *http.Request) (*http.Request, *trace) {
	if t := traceOf(r); t != nil {
		return r, t
	}
	t := newTrace()
	if s := accessStateOf(r); s != nil {
		s.trace = t
	}
	return withTrace(r, t), t
}
