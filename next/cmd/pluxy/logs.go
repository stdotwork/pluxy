package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/management"
)

// stdoutIsTerminal reports whether w, a command's standard output, is a terminal. It is not
// invocation.tty, which describes standard input.
var stdoutIsTerminal = func(w io.Writer) bool {
	r, ok := w.(io.Reader) // an *os.File, which isInteractive checks
	return ok && isInteractive(r)
}

var logsErrorsCommand = command{words: "logs errors", args: []string{"name"}, optional: true,
	routes:  []string{"GET /request-error-logs", "GET /request-error-logs/{name}"},
	summary: "the error logs, or one error log's text",
	run: func(x *invocation) error {
		if len(x.args) == 1 {
			return x.logFile("/request-error-logs/" + url.PathEscape(x.args[0]))
		}
		var r management.ErrorLogsReply
		return x.call(http.MethodGet, "/request-error-logs", nil, nil, &r, func() {
			if len(r.Files) == 0 {
				x.printf("no error logs\n")
				return
			}
			rows := [][]string{{"NAME", "BYTES", "MODIFIED"}}
			for _, f := range r.Files {
				rows = append(rows, []string{f.Name, strconv.FormatInt(f.Size, 10), time.Unix(f.Modified, 0).Local().Format(time.DateTime)})
			}
			x.table(rows)
		})
	}}

var logsRequestCommand = command{words: "logs request", args: []string{"id"},
	routes:  []string{"GET /request-log-by-id/{id}"},
	summary: "the request log of the request with a trace ID",
	run: func(x *invocation) error {
		return x.logFile("/request-log-by-id/" + url.PathEscape(x.args[0]))
	}}

var logsClearCommand = command{words: "logs clear", routes: []string{"DELETE /logs"},
	summary: "empty main.log and delete its rotated files; request logs and error logs stay",
	run: func(x *invocation) error {
		var r management.LogsCleared
		return x.call(http.MethodDelete, "/logs", nil, nil, &r, func() {
			rotated := fmt.Sprintf("%d rotated log files deleted", r.Removed)
			if r.Removed == 1 {
				rotated = "1 rotated log file deleted"
			}
			x.printf("logs cleared: main.log emptied, %s; request logs and error logs are kept\n", rotated)
		})
	}}

// logFile prints a log file core serves at path. With --json it is core's bounded JSON reply
// (a file over 2 MiB is core's 413 log_too_large), unchanged. Otherwise the plain download is
// copied to standard output as it arrives, whatever its size; on a terminal its control
// characters are shown escaped, since it holds text clients and upstreams sent.
func (x *invocation) logFile(path string) error {
	if x.json {
		return x.call(http.MethodGet, path, url.Values{"format": {"json"}}, nil, nil, nil)
	}
	if !stdoutIsTerminal(x.stdout) {
		return x.client.Stream(x.ctx, http.MethodGet, path, nil, x.stdout)
	}
	w := &escapeWriter{w: x.stdout}
	err := x.client.Stream(x.ctx, http.MethodGet, path, nil, w)
	if ferr := w.flush(); err == nil {
		err = ferr
	}
	return err
}

// escapeWriter writes what it is given with the characters a terminal would act on shown as
// text: the C0 controls and DEL other than newline and tab (or including them when
// escapeWhitespace is set, for a single table cell) as \xNN (so a logged escape sequence
// cannot move the cursor, retitle the window or rewrite what is shown), the C1 controls, which
// some terminals act on, as \uNNNN, and bytes that are not UTF-8 as \xNN. Everything else is
// written as it is. A character cut in two by a Write is held until its rest comes.
type escapeWriter struct {
	w                io.Writer
	hold             []byte
	escapeWhitespace bool
}

// escapeTableCell runs s through escapeWriter with tabs and newlines also escaped, as \x09 and
// \x0a. With the vertical tab and form feed escapeWriter always escapes, that covers every byte
// text/tabwriter acts on, so a table cell cannot add a column or a row on a terminal.
func escapeTableCell(s string) string {
	var b bytes.Buffer
	w := &escapeWriter{w: &b, escapeWhitespace: true}
	_, _ = io.WriteString(w, s)
	_ = w.flush()
	return b.String()
}

func (e *escapeWriter) Write(p []byte) (int, error) {
	data := append(e.hold, p...)
	e.hold = nil
	var out bytes.Buffer
	for i := 0; i < len(data); {
		b := data[i]
		switch {
		case (!e.escapeWhitespace && (b == '\n' || b == '\t')) || (b >= 0x20 && b < 0x7f):
			out.WriteByte(b)
			i++
		case b < utf8.RuneSelf:
			fmt.Fprintf(&out, `\x%02x`, b)
			i++
		case !utf8.FullRune(data[i:]):
			e.hold = append([]byte(nil), data[i:]...)
			i = len(data)
		default:
			r, size := utf8.DecodeRune(data[i:])
			switch {
			case r == utf8.RuneError && size == 1:
				fmt.Fprintf(&out, `\x%02x`, b)
			case r >= 0x80 && r <= 0x9f:
				fmt.Fprintf(&out, `\u%04x`, r)
			default:
				out.Write(data[i : i+size])
			}
			i += size
		}
	}
	if _, err := e.w.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// flush writes the bytes of a character the file ended inside, as bytes.
func (e *escapeWriter) flush() error {
	var out strings.Builder
	for _, b := range e.hold {
		fmt.Fprintf(&out, `\x%02x`, b)
	}
	e.hold = nil
	_, err := io.WriteString(e.w, out.String())
	return err
}

// usageExportPage is how many events one call reads: the most core gives.
const usageExportPage = 1000

// usageFilterFlags registers the shared usage event filter flags for usage events and usage export.
func usageFilterFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.since, "since", "", "from this time (RFC 3339) or this long ago (such as 24h or 7d)")
	fs.StringVar(&o.until, "until", "", "up to this time (RFC 3339)")
	fs.StringVar(&o.model, "model", "", "only this model")
	fs.StringVar(&o.account, "account", "", "only this account")
	fs.StringVar(&o.provider, "provider", "", "only this provider (extension)")
	fs.StringVar(&o.clientKey, "client-key", "", "only this client key ID")
	fs.StringVar(&o.failed, "failed", "", "only failed requests (true) or only successful ones (false)")
}

// addUsageFilterQuery adds the shared usage event filter flags to q.
func addUsageFilterQuery(q url.Values, o opts) {
	for k, v := range map[string]string{
		"since":    o.since,
		"until":    o.until,
		"model":    o.model,
		"account":  o.account,
		"provider": o.provider,
		"key":      o.clientKey,
		"failed":   o.failed,
	} {
		if v != "" {
			q.Set(k, v)
		}
	}
}

var usageExportCommand = command{words: "usage export", routes: []string{"GET /usage/events"},
	summary: "the recorded requests as CSV on standard output, newest first, as the panel's Export writes them",
	flags:   usageFilterFlags,
	run: func(x *invocation) error {
		if x.json {
			return usageError{"usage export writes CSV; for the recorded requests as JSON use pluxy usage events --json"}
		}
		q := url.Values{"limit": {strconv.Itoa(usageExportPage)}}
		addUsageFilterQuery(q, x.opts)
		// Oldest first with after, until the last page. Core's Lost says events newer than after
		// have left its record: on the first page, requests gone before this export began; on a
		// later page, requests pushed out between two pages. Either way the export cannot hold
		// them, and the panel says so too. Lost ignores the filters, so they may not be ones the
		// filters would have kept.
		var events []usage.Event
		var after uint64
		lost, noted := false, false
		for {
			q.Set("after", strconv.FormatUint(after, 10))
			var r management.UsageEventsReply
			if _, err := x.client.Call(x.ctx, http.MethodGet, "/usage/events", q, nil, &r); err != nil {
				return err
			}
			if !r.Enabled && !noted {
				noted = true
				x.eprintf("usage is not recorded (usage-statistics-enabled is off)\n")
			}
			lost = lost || r.Lost
			events = append(events, r.Events...)
			if !r.More {
				break
			}
			if r.Next <= after {
				return errors.New("core's reply to GET /usage/events did not advance: the export stopped")
			}
			after = r.Next
		}
		if lost {
			x.eprintf("pluxy: some older requests are no longer available, so this export may not include them\n")
		}
		// Names in the record come from clients, so on a terminal they go through escapeWriter,
		// and a row ends in a plain newline there, since it would show a CR as \x0d. Piped, the
		// bytes are the panel's CSV.
		terminal := stdoutIsTerminal(x.stdout)
		eol := "\r\n"
		if terminal {
			eol = "\n"
		}
		var out bytes.Buffer
		writeCSVRow(&out, eol, "Time", "Model", "Provider", "Account", "Client key", "Failed", "Status", "Latency", "Tokens", "Trace ID")
		for i := len(events) - 1; i >= 0; i-- {
			e := events[i]
			status, tokens := "", ""
			if e.Status != 0 {
				status = strconv.Itoa(e.Status)
			}
			if e.Tokens.Total != nil {
				tokens = strconv.FormatInt(*e.Tokens.Total, 10)
			}
			writeCSVRow(&out, eol, e.Timestamp.Format(time.RFC3339Nano), e.Model, e.Provider, e.Account, e.KeyID,
				strconv.FormatBool(e.Failed), status, strconv.FormatInt(e.LatencyMS, 10), tokens, e.ID)
		}
		if !terminal {
			_, err := x.stdout.Write(out.Bytes())
			return err
		}
		w := &escapeWriter{w: x.stdout}
		if _, err := w.Write(out.Bytes()); err != nil {
			return err
		}
		return w.flush()
	}}

func writeCSVRow(b *bytes.Buffer, eol string, fields ...string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(csvField(f))
	}
	b.WriteString(eol)
}

// csvField encodes a cell as CSV, neutralizing a spreadsheet formula before quoting. It is
// csvField of next/ui/kit/csv.ts, which the panel's Export uses: a cell that starts, after
// JavaScript's whitespace, with =, +, - or @ gets a ' in front, and one holding a quote, comma,
// CR or LF is quoted with its quotes doubled.
func csvField(s string) string {
	if t := strings.TrimLeftFunc(s, isJSSpace); t != "" && strings.ContainsRune("=+-@", rune(t[0])) {
		s = "'" + s
	}
	if strings.ContainsAny(s, "\",\r\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// isJSSpace is what a JavaScript regular expression's \s matches.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
