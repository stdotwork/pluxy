package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFiles(t *testing.T) (*Files, *clock) {
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 0, 0, time.Local)}
	l := New(filepath.Join(t.TempDir(), "logs"))
	l.now = c.now
	return l, c
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestWriteFormatsLinesOnlyWhileOn(t *testing.T) {
	l, c := newFiles(t)
	fmt.Fprint(l, "pluxy: before\n")
	if _, err := os.Stat(l.Dir()); !os.IsNotExist(err) {
		t.Fatalf("the folder exists while logging to file is off: %v", err)
	}
	if _, err := l.Read(0, 10); err != ErrOff {
		t.Fatalf("read while off: %v", err)
	}
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: config revision 1 active (start)\n")
	c.t = c.t.Add(1500 * time.Millisecond)
	fmt.Fprint(l, "pluxy: warning: no client_keys set\nsecond line\n")
	b, _ := os.ReadFile(filepath.Join(l.Dir(), Main))
	want := "[2026-11-09 12:00:00.000] [info ] config revision 1 active (start)\n" +
		"[2026-11-09 12:00:01.500] [warn ] warning: no client_keys set\n" +
		"[2026-11-09 12:00:01.500] [info ] second line\n"
	if string(b) != want {
		t.Fatalf("main.log:\n%s\nwant\n%s", b, want)
	}
	if err := l.Configure(false, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: after\n")
	if b2, _ := os.ReadFile(filepath.Join(l.Dir(), Main)); string(b2) != want {
		t.Fatalf("written while off:\n%s", b2)
	}
}

func TestRotateAndTrim(t *testing.T) {
	l, c := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 1<<20) // about 1 MiB per write
	for range 25 {
		c.t = c.t.Add(time.Second)
		fmt.Fprintln(l, line)
	}
	got := names(t, l.Dir())
	if len(got) != 3 || got[2] != Main || !strings.HasPrefix(got[0], "main-2026-11-09T") {
		t.Fatalf("after 25 MiB: %v", got)
	}
	for _, n := range got {
		info, _ := os.Stat(filepath.Join(l.Dir(), n))
		if info.Size() > MaxFileSize {
			t.Fatalf("%s is %d bytes, over %d", n, info.Size(), MaxFileSize)
		}
	}
	// A request log, older than everything, goes first under a cap; main.log never goes.
	old := filepath.Join(l.Dir(), "v1-chat-completions-old.log")
	os.WriteFile(old, []byte("old"), 0o600)
	os.Chtimes(old, c.t.Add(-time.Hour), c.t.Add(-time.Hour))
	if err := l.Configure(true, 17); err != nil {
		t.Fatal(err)
	}
	got = names(t, l.Dir())
	if slices.Contains(got, "v1-chat-completions-old.log") || len(got) != 2 || got[1] != Main {
		t.Fatalf("after a 17 MB cap (9, 9 and 7 MiB files): %v", got)
	}
	l.Configure(true, 1)
	if got = names(t, l.Dir()); !slices.Equal(got, []string{Main}) {
		t.Fatalf("after a 1 MB cap: %v (main.log stays whatever its size)", got)
	}
}

func TestReadPagesAndTails(t *testing.T) {
	l, c := newFiles(t)
	l.Configure(true, 0)
	start := c.t
	for i := range 10 {
		c.t = start.Add(time.Duration(i) * time.Second)
		fmt.Fprintf(l, "pluxy: line %d\n", i)
	}
	// Rotate by hand partway, so lines span two files.
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	for i := 10; i < 15; i++ {
		c.t = start.Add(time.Duration(i) * time.Second)
		fmt.Fprintf(l, "pluxy: line %d\nmore of line %d\n", i, i)
	}
	text := func(r Lines) string {
		var out []string
		for _, s := range r.Lines {
			out = append(out, s[strings.Index(s, "] [")+10:])
		}
		return strings.Join(out, ",")
	}
	r, err := l.Read(0, 3)
	if err != nil || text(r) != "more of line 13,line 14,more of line 14" || r.Count != 3 {
		t.Fatalf("tail 3: %q %v", text(r), err)
	}
	if r.Latest != float64(start.Add(14*time.Second).UnixMilli())/1000 {
		t.Fatalf("latest %v", r.Latest)
	}
	after := float64(start.Add(8*time.Second).UnixMilli()) / 1000
	r, _ = l.Read(after, 3)
	if text(r) != "line 9,line 10,more of line 10" {
		t.Fatalf("after line 8, 3 lines: %q", text(r))
	}
	r, _ = l.Read(r.Latest, 100)
	if r.Count != 8 || !strings.HasPrefix(text(r), "line 11,") {
		t.Fatalf("next page: %q", text(r))
	}
	r2, _ := l.Read(r.Latest, 100)
	if r2.Count != 0 || r2.Latest != r.Latest || r2.Lines == nil {
		t.Fatalf("nothing newer: %+v", r2)
	}

	removed, err := l.Clear()
	if err != nil || removed != 1 {
		t.Fatalf("clear: %d %v", removed, err)
	}
	if r, _ := l.Read(0, 100); r.Count != 0 {
		t.Fatalf("after clear: %v", r.Lines)
	}
	fmt.Fprint(l, "pluxy: after clear\n")
	if r, _ := l.Read(0, 100); r.Count != 1 {
		t.Fatalf("written after clear: %v", r.Lines)
	}
}

// A reader that pages forward with Latest never misses a line: not one written in the same
// millisecond as the newest line it read, and not one a limit cut off from a write's other lines.
func TestPagingForwardLosesNothing(t *testing.T) {
	l, c := newFiles(t) // the clock never moves, so every write is in the same millisecond
	l.Configure(true, 0)
	fmt.Fprint(l, "pluxy: first\n")
	r, _ := l.Read(0, 10)
	if r.Count != 1 {
		t.Fatalf("first read: %v", r.Lines)
	}
	fmt.Fprint(l, "pluxy: second\n")
	next, _ := l.Read(r.Latest, 10)
	if next.Count != 1 || !strings.HasSuffix(next.Lines[0], "second") {
		t.Fatalf("a line written in the millisecond of the newest line read: %v", next.Lines)
	}
	if next.Latest <= r.Latest {
		t.Fatalf("latest %v did not move past %v", next.Latest, r.Latest)
	}

	// A clock that goes back does not put a line before the one read.
	c.t = c.t.Add(-time.Hour)
	fmt.Fprint(l, "pluxy: third\n")
	if back, _ := l.Read(next.Latest, 10); back.Count != 1 || !strings.HasSuffix(back.Lines[0], "third") {
		t.Fatalf("a line written after the clock went back: %v", back.Lines)
	}

	// One write's lines share a time: a limit that ends inside them returns the rest too.
	l2, _ := newFiles(t)
	l2.Configure(true, 0)
	fmt.Fprint(l2, "pluxy: a\nb\nc\n")
	fmt.Fprint(l2, "pluxy: d\n")
	var got []string
	after := 1.0 // before every line; 0 would read the newest line only
	for range 10 {
		page, _ := l2.Read(after, 1)
		if page.Count == 0 {
			break
		}
		for _, s := range page.Lines {
			got = append(got, s[len(s)-1:])
		}
		after = page.Latest
	}
	if strings.Join(got, "") != "abcd" {
		t.Fatalf("paging by 1 line read %q, want abcd", got)
	}
}

// Rotations in the same millisecond keep every file, read back in order; a link at main.log is
// refused; one write never makes a file larger than the limit.
func TestRotationEdges(t *testing.T) {
	l, _ := newFiles(t) // the clock never moves
	l.Configure(true, 0)
	for i := range 3 {
		fmt.Fprintf(l, "pluxy: file %d\n", i)
		l.mu.Lock()
		l.rotate()
		l.mu.Unlock()
	}
	fmt.Fprint(l, "pluxy: file 3\n")
	if got := names(t, l.Dir()); len(got) != 4 {
		t.Fatalf("files %v", got)
	}
	r, _ := l.Read(0, 10)
	var order []string
	for _, line := range r.Lines {
		order = append(order, line[len(line)-6:])
	}
	if strings.Join(order, ",") != "file 0,file 1,file 2,file 3" {
		t.Fatalf("order %v", order)
	}

	fmt.Fprintln(l, strings.Repeat("y", MaxFileSize+100))
	info, _ := os.Stat(filepath.Join(l.Dir(), Main))
	if info.Size() > MaxFileSize {
		t.Fatalf("main.log is %d bytes after one large write", info.Size())
	}

	l2, _ := newFiles(t)
	os.MkdirAll(l2.Dir(), 0o700)
	target := filepath.Join(t.TempDir(), "elsewhere.log")
	if err := os.Symlink(target, filepath.Join(l2.Dir(), Main)); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := l2.Configure(true, 0); err == nil {
		t.Fatal("a link at main.log was followed")
	}
	fmt.Fprint(l2, "pluxy: secret line\n")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the link's target was written: %v", err)
	}
}

func TestClearReportsUnremovableRotatedFile(t *testing.T) {
	l, c := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: first\n")
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	c.t = c.t.Add(time.Second)
	fmt.Fprint(l, "pluxy: second\n")
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	c.t = c.t.Add(time.Second)
	fmt.Fprint(l, "pluxy: current\n")

	got := names(t, l.Dir())
	if len(got) != 3 {
		t.Fatalf("setup files: %v", got)
	}
	bad, okName := got[0], got[1]

	prev := beforeRemove
	t.Cleanup(func() { beforeRemove = prev })
	beforeRemove = func(dir, name string) {
		if name != bad {
			return
		}
		// Replace the regular file with a non-empty directory so os.Remove fails even as root.
		p := filepath.Join(dir, name)
		_ = os.Remove(p)
		_ = os.Mkdir(p, 0o700)
		_ = os.WriteFile(filepath.Join(p, "keep"), []byte("x"), 0o600)
	}

	removed, err := l.Clear()
	if err == nil {
		t.Fatalf("Clear succeeded (removed=%d), want error naming %s", removed, bad)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (other rotated file still deleted)", removed)
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("error %q does not name %s", err, bad)
	}
	if strings.Contains(err.Error(), l.Dir()) {
		t.Errorf("error %q leaks full path %s", err, l.Dir())
	}
	if strings.Contains(err.Error(), okName) || strings.Contains(err.Error(), Main) {
		t.Errorf("error %q names a file that succeeded", err)
	}
	if _, err := os.Stat(filepath.Join(l.Dir(), okName)); !os.IsNotExist(err) {
		t.Errorf("%s was not deleted: %v", okName, err)
	}
	if b, err := os.ReadFile(filepath.Join(l.Dir(), Main)); err != nil || len(b) != 0 {
		t.Errorf("main.log after Clear: len=%d err=%v, want empty", len(b), err)
	}
}

func TestClearIgnoresAlreadyGoneRotatedFile(t *testing.T) {
	l, c := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: first\n")
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	c.t = c.t.Add(time.Second)
	fmt.Fprint(l, "pluxy: second\n")
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	c.t = c.t.Add(time.Second)
	fmt.Fprint(l, "pluxy: current\n")

	got := names(t, l.Dir())
	if len(got) != 3 {
		t.Fatalf("setup files: %v", got)
	}
	gone, stay := got[0], got[1]

	prev := beforeRemove
	t.Cleanup(func() { beforeRemove = prev })
	beforeRemove = func(dir, name string) {
		if name == gone {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}

	removed, err := l.Clear()
	if err != nil {
		t.Fatalf("Clear with already-gone rotated file: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (only %s was removed by Clear)", removed, stay)
	}
	if _, err := os.Stat(filepath.Join(l.Dir(), stay)); !os.IsNotExist(err) {
		t.Errorf("%s was not deleted: %v", stay, err)
	}
	if b, err := os.ReadFile(filepath.Join(l.Dir(), Main)); err != nil || len(b) != 0 {
		t.Errorf("main.log after Clear: len=%d err=%v, want empty", len(b), err)
	}
}

func TestClearEmptiesMainWhenHandleNilAndRotationStillWorks(t *testing.T) {
	l, c := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: line before failed rotation\n")

	// Simulate a failed rotation that left l.f nil while l.on is true and main.log still has lines.
	l.mu.Lock()
	_ = l.f.Close()
	l.f = nil
	l.size = MaxFileSize - 10
	l.mu.Unlock()

	removed, err := l.Clear()
	if err != nil || removed != 0 {
		t.Fatalf("Clear with nil handle: removed=%d err=%v", removed, err)
	}
	if b, err := os.ReadFile(filepath.Join(l.Dir(), Main)); err != nil || len(b) != 0 {
		t.Fatalf("main.log still holds %d bytes (err=%v), want 0", len(b), err)
	}
	l.mu.Lock()
	size := l.size
	l.mu.Unlock()
	if size != 0 {
		t.Fatalf("tracked size = %d, want 0", size)
	}

	// Missing main.log while l.f is nil is not a failure.
	if err := os.Remove(filepath.Join(l.Dir(), Main)); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	l.size = 1234
	l.mu.Unlock()
	if removed, err := l.Clear(); err != nil || removed != 0 {
		t.Fatalf("Clear with missing main.log: removed=%d err=%v", removed, err)
	}
	l.mu.Lock()
	size = l.size
	l.mu.Unlock()
	if size != 0 {
		t.Fatalf("tracked size after missing main.log = %d, want 0", size)
	}

	// Reopen, write a small line (must not immediately rotate), then write past MaxFileSize to rotate.
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(time.Second)
	fmt.Fprint(l, "pluxy: after clear\n")
	if got := names(t, l.Dir()); !slices.Equal(got, []string{Main}) {
		t.Fatalf("small write after clear rotated unexpectedly: %v", got)
	}
	if r, err := l.Read(0, 10); err != nil || r.Count != 1 {
		t.Fatalf("read after clear: count=%d err=%v", r.Count, err)
	}
	chunk := strings.Repeat("a", 1<<20)
	for range 12 {
		c.t = c.t.Add(time.Second)
		fmt.Fprintln(l, chunk)
	}
	got := names(t, l.Dir())
	if len(got) < 2 || got[len(got)-1] != Main {
		t.Fatalf("later writes did not rotate: %v", got)
	}
	info, err := os.Stat(filepath.Join(l.Dir(), Main))
	if err != nil || info.Size() > MaxFileSize {
		t.Fatalf("main.log after later rotation: size=%d err=%v", info.Size(), err)
	}
}

func TestClearReportsUnemptiableMainWhenHandleNil(t *testing.T) {
	l, _ := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: first\n")
	l.mu.Lock()
	l.rotate()
	_ = l.f.Close()
	l.f = nil
	l.mu.Unlock()

	// Replace main.log with a directory so emptying it fails even as root.
	mainPath := filepath.Join(l.Dir(), Main)
	if err := os.Remove(mainPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mainPath, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := l.Clear()
	if err == nil {
		t.Fatalf("Clear succeeded (removed=%d), want error naming %s", removed, Main)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if !strings.Contains(err.Error(), Main) {
		t.Errorf("error %q does not name %s", err, Main)
	}
	if strings.Contains(err.Error(), l.Dir()) {
		t.Errorf("error %q leaks full path %s", err, l.Dir())
	}
}
