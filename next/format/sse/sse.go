// Package sse reads and writes server-sent events as the WHATWG HTML standard defines them:
// lines grouped into events by a blank line, data lines joined with newlines, comments ignored.
// A network read is not an event; the reader assembles events across reads.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxEvent is the default limit on one event's size.
const MaxEvent = 16 << 20

// ErrEventTooLarge is returned when an event exceeds the reader's limit.
var ErrEventTooLarge = errors.New("sse: event too large")

// Event is one server-sent event.
type Event struct {
	Name string // the "event" field; empty means "message"
	ID   string
	Data string
}

// Reader reads events from a stream.
type Reader struct {
	r       *bufio.Reader
	max     int
	afterCR bool // the last line ended with CR, so a following LF belongs to it
}

// NewReader returns a Reader with the MaxEvent limit.
func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReader(r), max: MaxEvent} }

// Next returns the next event. At the end of the stream it returns io.EOF; an event cut off by the
// end of the stream is discarded, as the standard says.
func (r *Reader) Next() (Event, error) {
	var ev Event
	var data bytes.Buffer
	hasData := false
	size := 0
	for {
		line, err := r.readLine(&size)
		if err != nil {
			return Event{}, err
		}
		if line == "" {
			if !hasData {
				// An event with no data (only comments or other fields) dispatches nothing,
				// and its bytes do not count toward the next event.
				ev = Event{}
				size = 0
				continue
			}
			ev.Data = data.String()
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		case "event":
			ev.Name = value
		case "id":
			ev.ID = value
		}
	}
}

// readLine reads one line without its terminator (CRLF, LF or CR), counting bytes against the
// event limit. A line ended by CR returns at once; an LF that follows it is skipped on the next
// read, so a live stream never waits for a byte to tell CR from CRLF.
func (r *Reader) readLine(size *int) (string, error) {
	var line []byte
	for {
		if r.r.Buffered() == 0 {
			if _, err := r.r.Peek(1); err != nil {
				if err == io.EOF && len(line) > 0 {
					return "", io.ErrUnexpectedEOF
				}
				return "", err
			}
		}
		buf, _ := r.r.Peek(r.r.Buffered())
		if r.afterCR {
			r.afterCR = false
			if buf[0] == '\n' {
				_, _ = r.r.Discard(1)
				continue
			}
		}
		i := bytes.IndexAny(buf, "\r\n")
		if i < 0 {
			*size += len(buf)
			if *size > r.max {
				return "", ErrEventTooLarge
			}
			line = append(line, buf...)
			_, _ = r.r.Discard(len(buf))
			continue
		}
		*size += i + 1
		if *size > r.max {
			return "", ErrEventTooLarge
		}
		line = append(line, buf[:i]...)
		r.afterCR = buf[i] == '\r'
		_, _ = r.r.Discard(i + 1)
		return string(line), nil
	}
}

// WriteData writes one event with a single data line. data must not contain newlines.
func WriteData(w io.Writer, data []byte) error {
	if bytes.ContainsAny(data, "\r\n") {
		return fmt.Errorf("sse: data contains a newline")
	}
	buf := make([]byte, 0, len(data)+8)
	buf = append(buf, "data: "...)
	buf = append(buf, data...)
	buf = append(buf, "\n\n"...)
	_, err := w.Write(buf)
	return err
}
