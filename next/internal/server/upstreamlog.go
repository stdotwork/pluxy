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

// gapBytesPerFormByte is how many bytes beside a cut trimGap leaves out for each byte of the
// longest form of a secret that holds a character other than a key's: more than a spelling of
// the secret with escapes of up to 11 bytes for each of its bytes takes, the bound a streamed
// body's redaction uses for what it holds back of one. The redaction removes spellings with
// deeper escapes too (a percent escape nested 6 times takes 13 bytes for a byte); the window does
// not hold a part of one of those that the gap cut.
const gapBytesPerFormByte = 12

// gapWindow returns how many bytes beside a cut trimGap leaves out for r's secrets: 12 for each
// byte of the longest form of r's that holds a character other than a key's (secretChar), or 0
// when no form does. A secret made only of key characters, such as an API key or a token, is
// spelled only with them in every escape the redaction decodes, so the run of key characters
// removes any part of it beside a cut, whatever its length.
func gapWindow(r *protocol.Redactor) int {
	return gapBytesPerFormByte * r.LongestForm(func(f string) bool {
		return strings.IndexFunc(f, func(c rune) bool { return !secretChar(c) }) >= 0
	})
}

// trimGap returns head and end, the start and the end of a body with bytes left out between them,
// without what beside the gap could be part of a secret cut in two, which the redaction cannot
// recognise as neither part is the whole secret. What it returns is the text as it was, ready to
// be redacted, so the bytes it leaves out count as they are. On each side it leaves out:
//   - a window of gapWindow(r) bytes, which holds every part of a secret with a character other
//     than a key's that the gap cut, in a spelling of up to 11 bytes for each of its bytes;
//   - a part of at least 4 bytes of a form of one of r's secrets that the window's edge cuts
//     (Redactor.TrimCut), then the run of key characters (secretChar) there, which holds every
//     part of a secret made of them that the gap cut, whatever its length;
//   - where the edge still cuts through a secret that the redaction finds whole in the text the
//     side had, that secret's part (keepWhole).
//
// So the edge cuts through no run of key characters and no secret the redaction finds whole
// within the window around it, and what is left of the side is redacted as the whole side would
// have been; where keepWhole cannot make it so, all of the side goes.
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

// keepWhole returns at, a place in s to cut s at, moved so that it cuts through no secret of r's
// that the redaction finds whole in s: to the start of one, keeping s[:at] (head), or to its
// end, keeping s[at:] (the end). A secret that the place cuts through spells out in at most n
// bytes on either side of it, so only those are redacted, with the cut and without it: where no
// secret is cut, the redacted text without the cut starts (head) or ends with the redacted kept
// part, and where one is, they differ from where the secret's part begins.
//
// A move goes back over the secret's part as it stands in s when the redaction left that part as
// it was, and a whole n bytes otherwise (the redaction changed its length, so its length in s is
// not known; a secret cut there starts within them). After each move the run of key characters
// before the place (head) or after it goes too, so the place never stands inside a secret made
// of them, however long, such as a token. Where the redaction gives up on the text around the
// place (Redacted for all of it), or the place still cuts through a secret after 4 moves, it
// leaves out all of the side.
func keepWhole(r *protocol.Redactor, s string, at, n int, head bool) int {
	all := 0 // what leaves out all of the side
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

// gaveUp reports whether out, the redaction of raw, is the redaction giving up on raw: Redacted
// for all of it, which holds escapes that decode too many ways.
func gaveUp(out, raw string) bool { return out == protocol.Redacted && raw != protocol.Redacted }

// secretChar reports whether r can be part of a secret as text carries one: a key, a token, and
// their base64, URL and JSON escapes.
func secretChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`-_.~+/=%\`, r)
}
