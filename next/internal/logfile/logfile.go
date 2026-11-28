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

const Main = "main.log"

const MaxFileSize = 10 << 20

const timeLayout = "2006-01-02 15:04:05.000"

const rotatedLayout = "2006-01-02T15-04-05.000"

var (
	afterCreate  = func(dir string) {}
	beforeRotate = func(dir string) {}
	beforeRemove = func(dir, name string) {}
)

var ErrOff = errors.New("logging to file is off")

type Files struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	on       bool
	maxTotal int64
	f        *os.File
	size     int64
	lastMs   int64
}

func New(dir string) *Files { return &Files{dir: dir, now: time.Now} }

func (l *Files) Dir() string { return l.dir }

func (l *Files) On() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.on
}

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
	root, err := private.Dir{Path: l.dir}.OpenRoot(true)
	if err != nil {
		return err
	}
	defer root.Close()
	path := filepath.Join(l.dir, Main)
	if info, err := root.Lstat(Main); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
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

func (l *Files) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on || l.f == nil {
		return len(p), nil
	}
	var b bytes.Buffer
	ms := max(l.now().UnixMilli(), l.lastMs+1)
	l.lastMs = ms
	stamp := time.UnixMilli(ms).Format(timeLayout)
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		line = strings.TrimPrefix(line, "pluxy: ")
		level := "info "
		switch {
		case strings.HasPrefix(line, "error:"):
			level = "error"
		case strings.HasPrefix(line, "warning:"):
			level = "warn "
		}
		fmt.Fprintf(&b, "[%s] [%s] %s\n", stamp, level, line)
	}
	if b.Len() > MaxFileSize {
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

func (l *Files) rotate() {
	l.f.Close()
	l.f = nil
	beforeRotate(l.dir)
	root, err := private.Dir{Path: l.dir}.OpenRoot(false)
	if err != nil {
		l.open()
		return
	}
	defer root.Close()
	stamp := l.now().UTC().Format(rotatedLayout)
	name := "main-" + stamp + ".log"
	for i := 1; ; i++ {
		if _, err := root.Lstat(name); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("main-%s-%d.log", stamp, i)
	}
	if err := root.Rename(Main, name); err != nil {
		l.open()
		return
	}
	if err := l.open(); err != nil {
		return
	}
	l.trim()
}

func (l *Files) Trim() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.trim()
}

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

func (l *Files) logFiles() []string {
	entries, _ := os.ReadDir(l.dir)
	var names []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasPrefix(n, "main-") && strings.HasSuffix(n, ".log") {
			names = append(names, n)
		}
	}
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

type Lines struct {
	Lines  []string `json:"lines"`
	Count  int      `json:"line-count"`
	Latest float64  `json:"latest-timestamp"`
}

type line struct {
	text string
	at   float64
}

func (l *Files) Read(after float64, limit int) (Lines, error) {
	l.mu.Lock()
	if !l.on {
		l.mu.Unlock()
		return Lines{}, ErrOff
	}
	names := l.logFiles()
	l.mu.Unlock()

	var chunks [][]line
	have := 0
	for i := len(names) - 1; i >= 0; i-- {
		lines, first, err := readFile(filepath.Join(l.dir, names[i]), after)
		if err != nil {
			continue
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
	return lines, first, nil
}

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
