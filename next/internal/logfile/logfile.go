// Package logfile writes core's log to files in its logs folder, as today's core does with
// logging-to-file: main.log, rotated at a fixed size, with a cap on the folder's total size,
// and reads the lines back for the management API.
package logfile

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/private"
)

// Main is the file the log is written to; rotated files are named main-<time>.log.
const Main = "main.log"

// MaxFileSize is the size at which main.log is rotated: 10 MB, as in today's core.
const MaxFileSize = 10 << 20

// timeLayout is how a line's time is written: today's core's, to the millisecond.
const timeLayout = "2006-01-02 15:04:05.000"

// rotatedLayout names a rotated file by the time it was rotated; names sort in time order.
const rotatedLayout = "2006-01-02T15-04-05.000"

// afterCreate runs in open between making main.log and opening it, beforeRotate in rotate
// after main.log is closed, and beforeRemove in Clear before removing a rotated file. Tests
// remove files, or swap them, there.
var (
	afterCreate  = func(dir string) {}
	beforeRotate = func(dir string) {}
	beforeRemove = func(dir, name string) {}
)

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
	lastMs   int64 // the time of the last write, in Unix milliseconds
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

// open checks the logs folder, making it when it is missing (private.Dir.Open), and opens
// main.log in it for appending. The folder and a new main.log can be read only by core's user
// (and, on Windows, SYSTEM); a rotated file keeps main.log's access.
func (l *Files) open() error {
	root, err := private.Dir{Path: l.dir}.OpenRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	path := filepath.Join(l.dir, Main)
	// main.log must be a file of its own: a link there would send the log somewhere else. The
	// file opened must be the regular file at the path, so a link swapped in while opening is
	// refused too.
	if info, err := root.Lstat(Main); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	// main.log is made private (private.Create) and then opened for appending without
	// O_CREATE, so one removed in between is made private again, not made with the folder's
	// list.
	var f *os.File
	for range 3 {
		if f, err = private.Create(root, l.dir, Main); err == nil {
			f.Close()
		} else if !os.IsExist(err) {
			return err
		}
		afterCreate(l.dir)
		if f, err = root.OpenFile(Main, os.O_WRONLY|os.O_APPEND, 0); !os.IsNotExist(err) {
			break
		}
	}
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err == nil {
		var at os.FileInfo
		if at, err = root.Lstat(Main); err == nil && (!at.Mode().IsRegular() || !os.SameFile(st, at)) {
			err = fmt.Errorf("%s is not a regular file", path)
		}
	}
	if err == nil {
		// One another account owns (put there before the folder was private) could be read by
		// it.
		var ok bool
		var owner string
		if ok, owner, err = private.Owner(f); err == nil && !ok {
			err = fmt.Errorf("%s is owned by %s, not by this user; move it away", path, owner)
		}
	}
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
	// Each write gets a time of its own, later than the write before it, even in the same
	// millisecond or after the clock went back: a reader that pages forward with the newest
	// time it read (after) must never skip a line written later.
	ms := max(l.now().UnixMilli(), l.lastMs+1)
	l.lastMs = ms
	stamp := time.UnixMilli(ms).Format(timeLayout)
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = strings.TrimPrefix(line, "pluxy: ")
		level := "info "
		if strings.HasPrefix(line, "warning:") {
			level = "warn "
		}
		fmt.Fprintf(&b, "[%s] [%s] %s\n", stamp, level, line)
	}
	if b.Len() > MaxFileSize {
		// One write never makes a file larger than the limit.
		b.Truncate(MaxFileSize - 64)
		b.WriteString("\n[... the rest of this log message was cut]\n")
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

// rotate renames main.log after the time and starts a new one. The rename is made in the
// folder as checked again (private.Dir.OpenRoot), so a folder swapped for a link since main.log
// was opened is not renamed through. l.mu is held.
func (l *Files) rotate() {
	l.f.Close()
	l.f = nil
	beforeRotate(l.dir)
	root, err := private.Dir{Path: l.dir}.OpenRoot(false)
	if err != nil {
		l.open() // refused again, most likely; logging to file stops then
		return
	}
	defer root.Close()
	// Two rotations in one millisecond must not give the same name: a rename replaces the file.
	stamp := l.now().UTC().Format(rotatedLayout)
	name := "main-" + stamp + ".log"
	for i := 1; ; i++ {
		if _, err := root.Lstat(name); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("main-%s-%d.log", stamp, i)
	}
	if err := root.Rename(Main, name); err != nil {
		// Keep writing to main.log past the limit rather than lose lines.
		l.open()
		return
	}
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
	// By rotation time, then by the number a second rotation in the same millisecond got.
	key := func(n string) (string, int) {
		n = strings.TrimSuffix(strings.TrimPrefix(n, "main-"), ".log")
		if len(n) <= len(rotatedLayout) {
			return n, 0
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(n[len(rotatedLayout):], "-"))
		return n[:len(rotatedLayout)], i
	}
	slices.SortFunc(names, func(a, b string) int {
		ka, ia := key(a)
		kb, ib := key(b)
		return cmp.Or(strings.Compare(ka, kb), cmp.Compare(ia, ib))
	})
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
// lines written after it, so a reader can page forward with Latest and miss none; with after 0,
// the newest limit lines. A line with no time of its own (a message that spans lines) counts as
// written with the line before it. Every write has a time of its own (see Write), so a page
// that ends inside one write's lines is extended to the write's last line: it can hold a few
// more than limit.
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
			// Lines of one write share a time, and the reader continues after the newest time it
			// got: keep the lines of that write that the limit cut off.
			for limit < len(all) && all[limit].at == all[limit-1].at {
				limit++
			}
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

// Clear empties main.log and deletes its rotated files, and returns how many rotated files it
// deleted. It tries every rotated file and main.log even if one fails, and returns an error
// naming each file it could not delete or empty. Request logs and error logs stay.
func (l *Files) Clear() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on {
		return 0, ErrOff
	}
	removed := 0
	var notDeleted []string
	for _, n := range l.logFiles() {
		if n == Main {
			continue
		}
		beforeRemove(l.dir, n)
		if err := os.Remove(filepath.Join(l.dir, n)); err == nil {
			removed++
		} else if !os.IsNotExist(err) {
			notDeleted = append(notDeleted, n)
		}
	}
	// A failed rotation can leave no file open. open holds main.log to the folder's checks (no
	// link, this user's file), which a truncate by path would not.
	mainFailed := false
	if l.f == nil && l.open() != nil {
		mainFailed = true
	}
	if l.f != nil {
		if err := l.f.Truncate(0); err != nil {
			mainFailed = true
		} else {
			l.size = 0
		}
	}
	var parts []string
	if len(notDeleted) > 0 {
		parts = append(parts, "could not delete "+strings.Join(notDeleted, ", "))
	}
	if mainFailed {
		parts = append(parts, "could not empty "+Main)
	}
	if len(parts) > 0 {
		return removed, errors.New(strings.Join(parts, "; "))
	}
	return removed, nil
}
