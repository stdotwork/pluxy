package usage

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	recordVersion       = 1
	summaryVersion      = 1
	defaultCloseTimeout = 5 * time.Second
	dayLayout           = "2006-01-02"
	daySuffix           = ".jsonl"
	summarySubdir       = "summary"
	summarySuffix       = ".json"
)

// ErrCloseTimeout is returned by Close when the disk writer has not drained its queue within
// the shutdown deadline.
var ErrCloseTimeout = errors.New("usage: writer drain timed out on close")

// Options configure a usage Store opened with Open.
type Options struct {
	// Dir is the folder holding daily UTC JSONL files (YYYY-MM-DD.jsonl). Empty keeps events in
	// memory only.
	Dir string
	// MaxEvents and MaxAge bound the in-memory ring; zero values take the defaults.
	MaxEvents int
	MaxAge    time.Duration
	// KeepDays is how many UTC days of day files are kept; nil defaults to DefaultKeepDays (90),
	// and 0 keeps everything.
	KeepDays *int
	// QueueSize bounds the disk writer's queue; zero takes DefaultQueueSize (4096).
	QueueSize int
}

type diskEvent struct {
	V int `json:"v"`
	Event
}

type summaryKey struct {
	Hour     int    `json:"hour"`
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	KeyID    string `json:"key_id,omitempty"`
	Failed   bool   `json:"failed,omitempty"`
}

type summaryRow struct {
	summaryKey
	Requests   int64 `json:"requests"`
	LatencyMS  int64 `json:"latency_ms,omitempty"`
	Input      int64 `json:"input_tokens,omitempty"`
	Output     int64 `json:"output_tokens,omitempty"`
	Reasoning  int64 `json:"reasoning_tokens,omitempty"`
	Cached     int64 `json:"cached_tokens,omitempty"`
	CacheWrite int64 `json:"cache_creation_tokens,omitempty"`
	Total      int64 `json:"total_tokens,omitempty"`
}

type daySummaryFile struct {
	V      int          `json:"v"`
	Day    string       `json:"day"`
	Oldest *time.Time   `json:"oldest,omitempty"`
	Rows   []summaryRow `json:"rows"`
}

// Open returns a Store backed by opts.Dir (when non-empty): it prunes day files older than
// KeepDays, refills the in-memory ring from yesterday's and today's UTC files, and starts one
// writer goroutine draining a bounded queue.
func Open(opts Options) (*Store, error) {
	return openStore(opts, time.Now, nil)
}

func openStore(opts Options, now func() time.Time, writeLine func(day string, line []byte) error) (*Store, error) {
	if now == nil {
		now = time.Now
	}
	maxEvents := opts.MaxEvents
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	maxEvents = min(maxEvents, MaxMaxEvents)
	maxAge := opts.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	keepDays := DefaultKeepDays
	if opts.KeepDays != nil {
		keepDays = max(0, *opts.KeepDays)
	}
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}

	s := &Store{
		maxEvents:    maxEvents,
		maxAge:       maxAge,
		now:          now,
		dir:          opts.Dir,
		closeTimeout: defaultCloseTimeout,
		writeLine:    writeLine,
	}
	s.keepDays.Store(int64(keepDays))

	if s.dir != "" {
		if info, err := os.Stat(s.dir); err == nil && !info.IsDir() {
			return nil, fmt.Errorf("usage: %s is not a directory", s.dir)
		}
		initialDay := s.now().UTC().Format(dayLayout)
		s.prune()
		if err := s.refill(); err != nil {
			return nil, err
		}
		s.summaries = map[string]*daySummaryFile{}
		s.writeCh = make(chan Event, queueSize)
		s.flushCh = make(chan chan struct{}, 16)
		s.pruneCh = make(chan struct{}, 1)
		s.done = make(chan struct{})
		go s.writerLoop(initialDay)
	}
	return s, nil
}

// SetKeepDays updates how many UTC days of usage files are kept; 0 keeps everything.
func (s *Store) SetKeepDays(days int) {
	if days < 0 {
		days = DefaultKeepDays
	}
	prev := s.keepDays.Swap(int64(days))
	if days > 0 && (prev == 0 || int64(days) < prev) && s.pruneCh != nil {
		select {
		case s.pruneCh <- struct{}{}:
		default:
		}
	}
}

// Close flushes queued events to disk up to a short shutdown deadline and stops the writer.
func (s *Store) Close() error {
	if s.writeCh == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.writeCh)
	}
	s.mu.Unlock()

	timeout := s.closeTimeout
	if timeout <= 0 {
		timeout = defaultCloseTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return nil
	case <-timer.C:
		return ErrCloseTimeout
	}
}

// refill loads yesterday's and today's UTC JSONL files into the in-memory ring.
func (s *Store) refill() error {
	now := s.now()
	nowUTC := now.UTC()
	cutoff := now.Add(-s.maxAge)
	days := []string{
		nowUTC.AddDate(0, 0, -1).Format(dayLayout),
		nowUTC.Format(dayLayout),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var prevRec time.Time
	for _, day := range days {
		path := filepath.Join(s.dir, day+daySuffix)
		err := readDayFile(path, func(e Event) {
			if e.Seq > s.seq {
				s.seq = e.Seq
			} else {
				s.seq++
				e.Seq = s.seq
			}
			rec := e.Timestamp
			if e.LatencyMS > 0 {
				rec = rec.Add(time.Duration(e.LatencyMS) * time.Millisecond)
			}
			if !prevRec.IsZero() && rec.Before(prevRec) {
				rec = prevRec
			}
			prevRec = rec
			if rec.Before(cutoff) {
				s.markIncomplete(e)
				return
			}
			e.recorded = rec
			s.appendRing(e, false)
		})
		if err != nil {
			return err
		}
	}
	s.openSeq = s.seq
	return nil
}

// readDayFile streams valid v=1 records from path, skipping any missing file or torn line.
func readDayFile(path string, fn func(Event)) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("usage: %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64<<10)
	for {
		b, err := r.ReadBytes('\n')
		line := bytes.TrimSpace(b)
		if len(line) > 0 {
			var row diskEvent
			if uerr := json.Unmarshal(line, &row); uerr == nil && row.V == recordVersion {
				fn(row.Event)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func (s *Store) writerLoop(initialDay string) {
	defer close(s.done)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	w := &dayWriter{dir: s.dir, lastPrunedDay: initialDay, writeLine: s.writeLine, prune: s.prune}
	defer w.close()

	for {
		select {
		case ev, ok := <-s.writeCh:
			if !ok {
				return
			}
			if err := s.writeEvent(w, ev); err != nil {
				s.dropped.Add(1)
			}
		case done := <-s.flushCh:
			for draining := true; draining; {
				select {
				case ev, ok := <-s.writeCh:
					if !ok {
						close(done)
						return
					}
					if err := s.writeEvent(w, ev); err != nil {
						s.dropped.Add(1)
					}
				default:
					draining = false
				}
			}
			close(done)
		case <-s.pruneCh:
			w.close()
			s.prune()
		case <-ticker.C:
			w.close()
			s.prune()
		}
	}
}

func (s *Store) writeEvent(w *dayWriter, e Event) error {
	t := e.Timestamp
	if t.IsZero() {
		t = e.recorded
	}
	day := t.UTC().Format(dayLayout)
	b, err := json.Marshal(diskEvent{V: recordVersion, Event: e})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := w.append(day, b); err != nil {
		return err
	}
	if day < s.now().UTC().Format(dayLayout) {
		s.invalidateClosedSummary(day)
	}
	if e.Seq > s.writtenSeq.Load() {
		s.writtenSeq.Store(e.Seq)
	}
	return nil
}

type dayWriter struct {
	dir           string
	day           string
	file          *os.File
	lastPrunedDay string
	writeLine     func(day string, line []byte) error
	prune         func()
}

func (w *dayWriter) close() {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
		w.day = ""
	}
}

func (w *dayWriter) append(day string, line []byte) error {
	if w.writeLine != nil {
		return w.writeLine(day, line)
	}
	if w.file == nil || w.day != day {
		w.close()
		if w.lastPrunedDay != "" && w.lastPrunedDay != day && w.prune != nil {
			w.prune()
		}
		w.lastPrunedDay = day
		f, err := openAppendFile(w.dir, filepath.Join(w.dir, day+daySuffix))
		if err != nil {
			return err
		}
		w.file, w.day = f, day
	}
	n, err := w.file.Write(line)
	if err != nil || n != len(line) {
		w.close()
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	return nil
}

func openAppendFile(dir, path string) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("usage: %s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
			return nil, mkErr
		}
		_ = os.Chmod(dir, 0o700)
		f, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err == nil {
		var at os.FileInfo
		if at, err = os.Lstat(path); err == nil && (!at.Mode().IsRegular() || !os.SameFile(st, at)) {
			err = fmt.Errorf("usage: %s is not a regular file", path)
		}
	}
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	// If a prior crash left a partial last line without a trailing newline, terminate it once
	// so the next appended event starts on a line of its own.
	if st.Size() > 0 && !endsWithNewline(path) {
		_, _ = f.Write([]byte{'\n'})
	}
	return f, nil
}

func endsWithNewline(path string) bool {
	r, err := os.Open(path)
	if err != nil {
		return true
	}
	defer r.Close()
	if _, err := r.Seek(-1, io.SeekEnd); err != nil {
		return true
	}
	var buf [1]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return true
	}
	return buf[0] == '\n'
}

// prune deletes UTC day files older than keepDays when keepDays > 0.
func (s *Store) prune() {
	if s.dir == "" {
		return
	}
	keepDays := int(s.keepDays.Load())
	if keepDays <= 0 {
		return
	}
	nowUTC := s.now().UTC()
	today := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	cutoff := today.AddDate(0, 0, -(keepDays - 1))

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		stem, ok := strings.CutSuffix(name, daySuffix)
		if !ok || len(stem) != len(dayLayout) {
			continue
		}
		day, err := time.Parse(dayLayout, stem)
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

func (s *Store) flushWriter() {
	if s.writeCh == nil {
		return
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		select {
		case <-s.done:
		case <-time.After(200 * time.Millisecond):
		}
		return
	}
	ack := make(chan struct{})
	select {
	case s.flushCh <- ack:
	case <-s.done:
		return
	case <-time.After(200 * time.Millisecond):
		return
	}
	select {
	case <-ack:
	case <-s.done:
	case <-time.After(200 * time.Millisecond):
	}
}

func (s *Store) invalidateClosedSummary(day string) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	if s.summaries != nil {
		delete(s.summaries, day)
	}
	_ = os.Remove(filepath.Join(s.dir, summarySubdir, day+summarySuffix))
}

func (s *Store) listClosedDays(f Filter, todayDay string) []string {
	daySet := map[string]struct{}{}
	addFromDir := func(dir, suffix string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || !e.Type().IsRegular() {
				continue
			}
			stem, ok := strings.CutSuffix(e.Name(), suffix)
			if !ok || len(stem) != len(dayLayout) || stem >= todayDay {
				continue
			}
			dayTime, err := time.Parse(dayLayout, stem)
			if err != nil {
				continue
			}
			dayEnd := dayTime.AddDate(0, 0, 1)
			if (f.Since.IsZero() || dayEnd.After(f.Since)) && (f.Until.IsZero() || dayTime.Before(f.Until)) {
				daySet[stem] = struct{}{}
			}
		}
	}
	addFromDir(s.dir, daySuffix)
	addFromDir(filepath.Join(s.dir, summarySubdir), summarySuffix)
	days := make([]string, 0, len(daySet))
	for d := range daySet {
		days = append(days, d)
	}
	slices.Sort(days)
	return days
}

func (s *Store) needsClosedFlush(days []string) bool {
	if len(days) == 0 {
		return false
	}
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	for _, day := range days {
		if s.summaries[day] != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.dir, summarySubdir, day+summarySuffix)); err != nil {
			return true
		}
	}
	return false
}

func (s *Store) loadDaySummaries(days []string) []*daySummaryFile {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	out := make([]*daySummaryFile, 0, len(days))
	for _, day := range days {
		if ds := s.loadOrBuildDaySummaryLocked(day); ds != nil {
			out = append(out, ds)
		}
	}
	return out
}

func (s *Store) loadOrBuildDaySummaryLocked(day string) *daySummaryFile {
	if ds := s.summaries[day]; ds != nil {
		return ds
	}
	sumPath := filepath.Join(s.dir, summarySubdir, day+summarySuffix)
	if raw, err := os.ReadFile(sumPath); err == nil {
		var ds daySummaryFile
		if uerr := json.Unmarshal(raw, &ds); uerr == nil && ds.V == summaryVersion && ds.Day == day && ds.Rows != nil {
			s.summaries[day] = &ds
			return &ds
		}
	}
	rawPath := filepath.Join(s.dir, day+daySuffix)
	if _, err := os.Lstat(rawPath); err != nil {
		return nil
	}
	if s.onReadRaw != nil {
		s.onReadRaw(day)
	}
	var oldest *time.Time
	rowsMap := map[summaryKey]*summaryRow{}
	if err := readDayFile(rawPath, func(e Event) {
		if !e.Timestamp.IsZero() && (oldest == nil || e.Timestamp.Before(*oldest)) {
			cp := e.Timestamp
			oldest = &cp
		}
		k := summaryKey{
			Hour:     e.Timestamp.UTC().Hour(),
			Model:    e.Model,
			Provider: e.Provider,
			Account:  e.Account,
			KeyID:    e.KeyID,
			Failed:   e.Failed,
		}
		r := rowsMap[k]
		if r == nil {
			r = &summaryRow{summaryKey: k}
			rowsMap[k] = r
		}
		r.Requests++
		r.LatencyMS += e.LatencyMS
		r.Input += deref(e.Tokens.Input)
		r.Output += deref(e.Tokens.Output)
		r.Reasoning += deref(e.Tokens.Reasoning)
		r.Cached += deref(e.Tokens.Cached)
		r.CacheWrite += deref(e.Tokens.CacheWrite)
		r.Total += deref(e.Tokens.Total)
	}); err != nil {
		return nil
	}
	rows := make([]summaryRow, 0, len(rowsMap))
	for _, r := range rowsMap {
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b summaryRow) int {
		return cmp.Or(
			cmp.Compare(a.Hour, b.Hour),
			cmp.Compare(a.Model, b.Model),
			cmp.Compare(a.Provider, b.Provider),
			cmp.Compare(a.Account, b.Account),
			cmp.Compare(a.KeyID, b.KeyID),
			cmp.Compare(boolInt(a.Failed), boolInt(b.Failed)),
		)
	})
	ds := &daySummaryFile{V: summaryVersion, Day: day, Oldest: oldest, Rows: rows}
	_ = writeSummaryFileAtomic(filepath.Join(s.dir, summarySubdir), sumPath, ds)
	s.summaries[day] = ds
	return ds
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func writeSummaryFileAtomic(sumDir, finalPath string, ds *daySummaryFile) error {
	b, err := json.Marshal(ds)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(sumDir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(sumDir, 0o700)
	tmp, err := os.CreateTemp(sumDir, ds.Day+".json.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if n, err := tmp.Write(b); err != nil || n != len(b) {
		cleanup()
		if err != nil {
			return err
		}
		return io.ErrShortWrite
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (s *Store) collectTodayEvents(todayDay string, todayStart time.Time, incompleteToday bool, openSeq uint64, ringEvents []Event) []Event {
	if !incompleteToday {
		return ringEvents
	}
	rawPath := filepath.Join(s.dir, todayDay+daySuffix)
	if _, err := os.Lstat(rawPath); err != nil {
		return ringEvents
	}
	if s.onReadRaw != nil {
		s.onReadRaw(todayDay)
	}
	var out []Event
	var maxFileSeq uint64
	if err := readDayFile(rawPath, func(e Event) {
		if e.Seq > maxFileSeq {
			maxFileSeq = e.Seq
		}
		if !e.Timestamp.Before(todayStart) {
			out = append(out, e)
		}
	}); err != nil {
		return ringEvents
	}
	for _, e := range ringEvents {
		if e.Seq > openSeq && e.Seq > maxFileSeq {
			out = append(out, e)
		}
	}
	return out
}
