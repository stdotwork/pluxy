package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/protocol"
)

// upstreamLog holds what a request's extensions sent upstream and got back, for its request log
// (protocol minor version 13): the first exchange and the last, across every account the request
// tried, counting the ones between. Each is formatted, within bounds, as it arrives.
type upstreamLog struct {
	mu          sync.Mutex
	first, last []byte
	between     int
	redact      *protocol.Redactor // the config's secrets, which a cut body could split in two (trimGap)
}

// captureUpstream returns ctx asking the request's calls to capture their upstream exchanges
// into t's request log, whose secrets are redact's.
func (t *trace) captureUpstream(ctx context.Context, redact *protocol.Redactor) context.Context {
	t.upstream = &upstreamLog{redact: redact}
	return host.WithCapture(ctx, t.upstream.add)
}

// add takes the exchanges of one call on account. It is a host.CaptureFunc.
func (u *upstreamLog) add(account string, d protocol.Diagnostic) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, x := range d.Upstream {
		if i > 0 && i == len(d.Upstream)-1 && u.first != nil {
			u.between += d.UpstreamLeftOut // the call left them out before its last
		}
		b := formatExchange(account, x, u.redact)
		if u.first == nil {
			u.first = b
			continue
		}
		if u.last != nil {
			u.between++
		}
		u.last = b
	}
}

// write appends the logged exchanges to b.
func (u *upstreamLog) write(b *bytes.Buffer) {
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	b.Write(u.first)
	if u.between > 0 {
		fmt.Fprintf(b, "[... %d upstream exchanges not logged ...]\n\n", u.between)
	}
	b.Write(u.last)
}

// Bounds on what one exchange's log holds besides its bodies (which keep protocol.CapturedHead
// and protocol.CapturedEnd bytes): its header lines, and each one-line field.
const (
	loggedHeaderBytes = 16 << 10
	loggedFieldBytes  = 8 << 10
)

// formatExchange writes one exchange as an UPSTREAM REQUEST and an UPSTREAM RESPONSE section.
// What the extension sent is held to the bounds, whatever it sent: secrets by name are masked
// again here, and core's own redaction runs over the whole file when it is written.
func formatExchange(account string, x protocol.UpstreamExchange, redact *protocol.Redactor) []byte {
	var b bytes.Buffer
	b.WriteString("=== UPSTREAM REQUEST ===\n")
	fmt.Fprintf(&b, "Account: %s\n", oneLine(account))
	writeTime(&b, x.Request.TimeMs)
	fmt.Fprintf(&b, "URL: %s\nMethod: %s\n", oneLine(upstreamURL(x.Request.URL)), oneLine(x.Request.Method))
	writeUpstreamHeaders(&b, x.Request.Headers, x.Request.HeadersLeftOut)
	b.WriteString("\n")
	writeBody(&b, x.Request.CapturedBody, redact)
	b.WriteString("\n\n=== UPSTREAM RESPONSE ===\n")
	switch r := x.Response; {
	case r != nil:
		writeTime(&b, r.TimeMs)
		fmt.Fprintf(&b, "Status: %d\n", r.Status)
		writeUpstreamHeaders(&b, r.Headers, r.HeadersLeftOut)
		b.WriteString("\n")
		if r.Status == http.StatusSwitchingProtocols {
			b.WriteString("[the connection switched protocols: its messages are not logged]")
		} else {
			writeBody(&b, r.CapturedBody, redact)
		}
	case x.Error != "":
		fmt.Fprintf(&b, "Error: %s\n", oneLine(x.Error))
	default:
		b.WriteString("No response had come when the call ended.\n")
	}
	b.WriteString("\n\n")
	return b.Bytes()
}

func writeTime(b *bytes.Buffer, ms int64) {
	if ms > 0 {
		fmt.Fprintf(b, "Timestamp: %s\n", time.UnixMilli(ms).Format(time.RFC3339Nano))
	}
}

// oneLine keeps s to one line of at most loggedFieldBytes, so nothing an extension sends can
// start a section of its own.
func oneLine(s string) string {
	if len(s) > loggedFieldBytes {
		return fmt.Sprintf("[%d bytes not logged]", len(s))
	}
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s)
}

// secretUpstreamName reports whether an upstream header or query parameter's value is secret by
// its name: by the request log's rule or the protocol's.
func secretUpstreamName(name string) bool {
	return sensitiveName(name) || protocol.SecretName(name)
}

// unloggedURL stands for a URL that does not parse, which cannot be told apart from its secrets.
const unloggedURL = "[URL not logged]"

// upstreamURL is raw without user name and password, secret query values masked. A query that
// does not parse (a bad escape, a ";") is masked whole, and a URL that does not parse is not
// logged.
func upstreamURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return unloggedURL
	}
	u.User = nil
	if u.RawQuery == "" {
		return u.String()
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		u.RawQuery = protocol.Redacted
		return u.String()
	}
	masked := false
	for k := range q {
		if secretUpstreamName(k) {
			q[k] = []string{protocol.Redacted}
			masked = true
		}
	}
	if masked {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// writeUpstreamHeaders writes h as writeHeaders does, masking values by secretUpstreamName, up
// to loggedHeaderBytes, then a line counting the header lines left out, those the extension left
// out (leftOut) and those cut here.
func writeUpstreamHeaders(b *bytes.Buffer, h map[string][]string, leftOut int) {
	written, left := 0, max(0, leftOut)
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range h {
			if !yield(k) {
				return
			}
		}
	}) {
		for _, v := range h[name] {
			if secretUpstreamName(name) {
				v = maskValue(name, v)
			}
			line := oneLine(name) + ": " + oneLine(v) + "\n"
			if written+len(line) > loggedHeaderBytes {
				left++
				continue
			}
			written += len(line)
			b.WriteString(line)
		}
	}
	if left > 0 {
		fmt.Fprintf(b, "[... %d header lines not logged ...]\n", left)
	}
}

// writeBody writes a captured body as the client side's capture does: all of it, or its start and
// its end with a line saying how many bytes were left out between them. A secret of redact's cut
// in two at the gap would not be recognised by the redaction, so what could be part of one on
// each side of the gap is left out too (trimGap).
func writeBody(b *bytes.Buffer, c protocol.CapturedBody, redact *protocol.Redactor) {
	head, end := c.Body, c.BodyEnd
	if end == "" && len(head) > protocol.CapturedHead+protocol.CapturedEnd {
		head, end = head[:protocol.CapturedHead], head[len(head)-protocol.CapturedEnd:]
	}
	head = head[:min(len(head), protocol.CapturedHead+protocol.CapturedEnd)]
	if end != "" && len(head) > protocol.CapturedHead {
		head = head[:protocol.CapturedHead]
	}
	end = end[max(0, len(end)-protocol.CapturedEnd):]
	kept := int64(len(head) + len(end))
	skipped := max(c.BodySize, int64(len(c.Body)+len(c.BodyEnd))) - kept
	if skipped <= 0 {
		b.WriteString(head)
		b.WriteString(end)
		return
	}
	h, e := trimGap(redact, head, end)
	skipped += int64(len(head) - len(h) + len(end) - len(e))
	b.WriteString(h)
	fmt.Fprintf(b, "\n[... %d bytes not logged ...]\n", skipped)
	b.WriteString(e)
}

// trimGap returns head and end, the start and the end of a body with bytes left out between them,
// without what beside the gap could be part of a secret cut in two, which the redaction cannot
// recognise as neither part is the whole secret: on each side, the run of characters a key or a
// token is spelled with, escapes included (secretChar), and a part of at least 4 bytes of one of
// r's secrets in a form r knows, whatever its characters (Redactor.TrimCut), until neither finds
// more.
func trimGap(r *protocol.Redactor, head, end string) (string, string) {
	for {
		h, e := r.TrimCut(strings.TrimRightFunc(head, secretChar), strings.TrimLeftFunc(end, secretChar))
		if len(h) == len(head) && len(e) == len(end) {
			return h, e
		}
		head, end = h, e
	}
}

// secretChar reports whether r can be part of a secret as text carries one: a key, a token, and
// their base64, URL and JSON escapes.
func secretChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`-_.~+/=%\`, r)
}
