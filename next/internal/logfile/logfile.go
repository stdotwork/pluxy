// Package logfile writes core's log to files in its logs folder, as today's core does with
// logging-to-file: main.log, rotated at a fixed size, with a cap on the folder's total size,
// and reads the lines back for the management API.
package logfile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Main is the file the log is written to; rotated files are named main-<time>.log.
const Main = "main.log"

// MaxFileSize is the size at which main.log is rotated: 10 MB, as in today's core.
const MaxFileSize = 10 << 20

// timeLayout is how a line's time is written: today's core's, to the millisecond.
const timeLayout = "2006-01-02 15:04:05.000"

// rotatedLayout names a rotated file by the time it was rotated; names sort in time order.
const rotatedLayout = "2006-01-02T15-04-05.000"

// ErrOff is returned when the log is read while logging to file is off.
var ErrOff = errors.New("logging to file is off")

// Files is core's logs folder: the log file it writes while logging to file is on, and the
// other log files kept there. It is safe for concurrent use.
type Files struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	on       bool
	maxTotal int64 // bytes; 0 is no cap
	f        *os.File
	size     int64
}

// New returns the logs folder at dir, with logging to file off.
func New(dir string) *Files { return &Files{dir: dir, now: time.Now} }

// Dir returns the logs folder.
func (l *Files) Dir() string { return l.dir }

// On reports whether logging to file is on.
func (l *Files) On() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.on
}

// Configure turns logging to file on or off and sets the cap on the folder's total size in
// megabytes (0: none). Turning it on creates the folder and opens main.log.
func (l *Files) Configure(on bool, maxTotalMB int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxTotal = int64(maxTotalMB) << 20
	if !on {
		l.on = false
		if l.f != nil {
			err := l.f.Close()
			l.f = nil
			return err
		}
		return nil
	}
	l.on = true
	if l.f == nil {
		if err := l.open(); err != nil {
			l.on = false
			return err
		}
	}
	l.trim()
	return nil
}

func (l *Files) open() error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(l.dir, Main), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, st.Size()
	return nil
}

// Write writes log output to main.log while logging to file is on: each line with its time and
// level in front, as today's core writes them ("[2026-11-09 12:00:00.000] [info ] ..."), the
// program's "pluxy: " prefix taken off. It never fails, so a full disk does not stop the log
// on standard error.
func (l *Files) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on || l.f == nil {
		return len(p), nil
	}
	var b bytes.Buffer
	stamp := l.now().Format(timeLayout)
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = strings.TrimPrefix(line, "pluxy: ")
		level := "info "
		if strings.HasPrefix(line, "warning:") {
			level = "warn "
		}
		fmt.Fprintf(&b, "[%s] [%s] %s\n", stamp, level, line)
	}
	if l.size > 0 && l.size+int64(b.Len()) > MaxFileSize {
		l.rotate()
	}
	if l.f != nil {
		n, _ := l.f.Write(b.Bytes())
		l.size += int64(n)
	}
	return len(p), nil
}

// rotate renames main.log after the time and starts a new one. l.mu is held.
func (l *Files) rotate() {
	l.f.Close()
	l.f = nil
	name := "main-" + l.now().UTC().Format(rotatedLayout) + ".log"
	_ = os.Rename(filepath.Join(l.dir, Main), filepath.Join(l.dir, name))
	if err := l.open(); err != nil {
		return // logged nowhere: the log itself is what failed
	}
	l.trim()
}

// Trim deletes the oldest log files until the folder is within its size cap.
func (l *Files) Trim() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trim()
}

// trim deletes the oldest .log files but main.log, by modification time, until the folder's
// .log files are within the cap, as today's core's cleaner does. l.mu is held.
func (l *Files) trim() {
	if l.maxTotal <= 0 {
		return
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		if e.Name() != Main {
			files = append(files, file{e.Name(), info.Size(), info.ModTime()})
		}
	}
	slices.SortFunc(files, func(a, b file) int { return a.mod.Compare(b.mod) })
	for _, f := range files {
		if total <= l.maxTotal {
			return
		}
		if os.Remove(filepath.Join(l.dir, f.name)) == nil {
			total -= f.size
		}
	}
}

// logFiles returns main.log's rotated files, oldest first, then main.log. l.mu is held.
func (l *Files) logFiles() []string {
	entries, _ := os.ReadDir(l.dir)
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasPrefix(n, "main-") && strings.HasSuffix(n, ".log") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return append(names, Main)
}

// Lines is what Read returns.
type Lines struct {
	Lines []string `json:"lines"`
	// Count is the number of lines returned.
	Count int `json:"line-count"`
	// Latest is the time of the newest line returned, in Unix seconds with milliseconds; pass it
	// as after to read only newer lines. With no line returned, it is after.
	Latest float64 `json:"latest-timestamp"`
}

// line is a log line and the time it was written.
type line struct {
	text string
	at   float64
}

// Read returns log lines, oldest first: with after (Unix seconds) above 0, the oldest limit
// lines written after it, so a reader can page forward with Latest; with after 0, the newest
// limit lines. A line with no time of its own (a message that spans lines) counts as written
// with the line before it.
func (l *Files) Read(after float64, limit int) (Lines, error) {
	l.mu.Lock()
	if !l.on {
		l.mu.Unlock()
		return Lines{}, ErrOff
	}
	names := l.logFiles()
	l.mu.Unlock()

	// Newest file first, stopping at a file that starts at or before after, or (reading the
	// newest lines) once limit lines are in hand.
	var chunks [][]line
	have := 0
	for i := len(names) - 1; i >= 0; i-- {
		lines, first, err := readFile(filepath.Join(l.dir, names[i]), after)
		if err != nil {
			continue // rotated or cleared while reading
		}
		chunks = append(chunks, lines)
		have += len(lines)
		if after > 0 && first > 0 && first <= after || after <= 0 && have >= limit {
			break
		}
	}
	var all []line
	for i := len(chunks) - 1; i >= 0; i-- {
		all = append(all, chunks[i]...)
	}
	if len(all) > limit {
		if after > 0 {
			all = all[:limit]
		} else {
			all = all[len(all)-limit:]
		}
	}
	out := Lines{Lines: make([]string, len(all)), Count: len(all), Latest: max(after, 0)}
	for i, ln := range all {
		out.Lines[i] = ln.text
		out.Latest = max(out.Latest, ln.at)
	}
	return out, nil
}

// readFile returns the lines of the file at path written after after, and the time of its
// first line (0 when it has none with a time).
func readFile(path string, after float64) (lines []line, first float64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var cur float64
	for sc.Scan() {
		text := sc.Text()
		if t, ok := lineTime(text); ok {
			cur = t
			if first == 0 {
				first = t
			}
		}
		if cur > after {
			lines = append(lines, line{text, cur})
		}
	}
	// A line too long for the scanner ends the read; what came before stands.
	return lines, first, nil
}

// lineTime reads the time a line starts with, in Unix seconds.
func lineTime(line string) (float64, bool) {
	if len(line) < len(timeLayout)+2 || line[0] != '[' || line[len(timeLayout)+1] != ']' {
		return 0, false
	}
	t, err := time.ParseInLocation(timeLayout, line[1:len(timeLayout)+1], time.Local)
	if err != nil {
		return 0, false
	}
	return float64(t.UnixMilli()) / 1000, true
}

// Clear empties main.log and deletes its rotated files, and returns how many files it deleted.
// Request logs and error logs stay.
func (l *Files) Clear() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on {
		return 0, ErrOff
	}
	removed := 0
	for _, n := range l.logFiles() {
		if n == Main {
			continue
		}
		if os.Remove(filepath.Join(l.dir, n)) == nil {
			removed++
		}
	}
	if l.f != nil {
		if err := l.f.Truncate(0); err != nil {
			return removed, err
		}
		l.size = 0
	}
	return removed, nil
}
