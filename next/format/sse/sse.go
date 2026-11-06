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
	r   *bufio.Reader
	max int
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
				ev = Event{}
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

// readLine reads one line without its terminator (LF, CRLF or CR followed by LF), counting bytes
// against the event limit.
func (r *Reader) readLine(size *int) (string, error) {
	var buf []byte
	for {
		chunk, err := r.r.ReadSlice('\n')
		*size += len(chunk)
		if *size > r.max {
			return "", ErrEventTooLarge
		}
		buf = append(buf, chunk...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		return strings.TrimRight(string(buf), "\r\n"), nil
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
