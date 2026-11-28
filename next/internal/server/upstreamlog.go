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

type upstreamLog struct {
	mu          sync.Mutex
	first, last []byte
	between     int
	redact      *protocol.Redactor
}

func (t *trace) captureUpstream(ctx context.Context, redact *protocol.Redactor) context.Context {
	t.upstream = &upstreamLog{redact: redact}
	return host.WithCapture(ctx, t.upstream.add)
}

func (u *upstreamLog) add(account string, d protocol.Diagnostic) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i, x := range d.Upstream {
		if i > 0 && i == len(d.Upstream)-1 && u.first != nil {
			u.between += d.UpstreamLeftOut
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

const (
	loggedHeaderBytes = 16 << 10
	loggedFieldBytes  = 8 << 10
)

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

func oneLine(s string) string {
	if len(s) > loggedFieldBytes {
		return fmt.Sprintf("[%d bytes not logged]", len(s))
	}
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(s)
}

func secretUpstreamName(name string) bool {
	return sensitiveName(name) || protocol.SecretName(name)
}

const unloggedURL = "[URL not logged]"

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

func writeBody(b *bytes.Buffer, c protocol.CapturedBody, redact *protocol.Redactor) {
	if c.BodyStopped {
		defer b.WriteString("\n[... the body was not read past here ...]")
	}
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
		text := head + end
		if c.BodyStopped {
			text = trimStop(redact, text)
		}
		b.WriteString(text)
		return
	}
	if c.BodyStopped && end != "" {
		end = trimStop(redact, end)
	}
	h, e := trimGap(redact, head, end)
	skipped += int64(len(head) - len(h) + len(end) - len(e))
	b.WriteString(h)
	fmt.Fprintf(b, "\n[... %d bytes not logged ...]\n", skipped)
	b.WriteString(e)
}

func trimStop(r *protocol.Redactor, s string) string {
	n := gapWindow(r)
	q := len(s) - min(n, len(s))
	if i := strings.LastIndexByte(s, '\n'); i >= q {
		q = i + 1
	}
	h, _ := r.TrimCut(s[:q], "")
	q = len(strings.TrimRightFunc(h, secretChar))
	if n > 0 {
		q = keepWhole(r, s, q, n, true)
	}
	return s[:q]
}

const gapBytesPerFormByte = 12

func gapWindow(r *protocol.Redactor) int {
	return gapBytesPerFormByte * r.LongestForm(func(f string) bool {
		return strings.IndexFunc(f, func(c rune) bool { return !secretChar(c) }) >= 0
	})
}

func trimGap(r *protocol.Redactor, head, end string) (string, string) {
	n := gapWindow(r)
	q, p := len(head)-min(n, len(head)), min(n, len(end))
	h, e := r.TrimCut(head[:q], end[p:])
	q, p = len(strings.TrimRightFunc(h, secretChar)), len(end)-len(strings.TrimLeftFunc(e, secretChar))
	if n > 0 {
		q, p = keepWhole(r, head, q, n, true), keepWhole(r, end, p, n, false)
	}
	return head[:q], end[p:]
}

func keepWhole(r *protocol.Redactor, s string, at, n int, head bool) int {
	all := 0
	if !head {
		all = len(s)
	}
	for range 4 {
		a, b := max(0, at-n), min(len(s), at+n)
		whole := r.String(s[a:b])
		keptRaw := s[at:b]
		if head {
			keptRaw = s[a:at]
		}
		kept := r.String(keptRaw)
		if gaveUp(whole, s[a:b]) || gaveUp(kept, keptRaw) {
			return all
		}
		if head {
			if strings.HasPrefix(whole, kept) {
				return at
			}
			d := 0
			for d < len(kept) && d < len(whole) && kept[d] == whole[d] {
				d++
			}
			if part := kept[d:]; strings.HasSuffix(s[a:at], part) && part != "" {
				at -= len(part)
			} else {
				at = a
			}
			at = len(strings.TrimRightFunc(s[:at], secretChar))
		} else {
			if strings.HasSuffix(whole, kept) {
				return at
			}
			d := 0
			for d < len(kept) && d < len(whole) && kept[len(kept)-1-d] == whole[len(whole)-1-d] {
				d++
			}
			if part := kept[:len(kept)-d]; strings.HasPrefix(s[at:b], part) && part != "" {
				at += len(part)
			} else {
				at = b
			}
			at = len(s) - len(strings.TrimLeftFunc(s[at:], secretChar))
		}
	}
	return all
}

func gaveUp(out, raw string) bool { return out == protocol.Redacted && raw != protocol.Redacted }

func secretChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`-_.~+/=%\`, r)
}
