package sse

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MaxEvent = 16 << 20

var ErrEventTooLarge = errors.New("sse: event too large")

type Event struct {
	Name string
	ID   string
	Data string
}

type Reader struct {
	r       *bufio.Reader
	max     int
	afterCR bool
}

func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReader(r), max: MaxEvent} }

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
