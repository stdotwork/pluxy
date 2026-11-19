package usage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func intPtr(n int) *int { return &n }

func TestOpenRefillsFromTodayAndYesterdayAndPersistsV1(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}

	// Seed a file from two days ago (ignored by refill, still within default 90-day retention),
	// yesterday (within 24h maxAge), and today.
	writeLine := func(day string, e Event) {
		b, err := json.Marshal(diskEvent{V: 1, Event: e})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, day+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	writeLine("2026-11-17", Event{Seq: 1, Timestamp: c.now().Add(-48 * time.Hour), Model: "two-days-ago"})
	writeLine("2026-11-18", Event{Seq: 1, Timestamp: c.now().Add(-26 * time.Hour), Model: "yesterday-expired"})
	writeLine("2026-11-18", Event{Seq: 2, Timestamp: c.now().Add(-6 * time.Hour), Model: "yesterday-kept", KeyID: "k1",
		Tokens: TokensOf(&llm.Usage{InputTokens: i64(10), OutputTokens: i64(5)})})

	s, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Timestamp: c.now(), Model: "today-1", KeyID: "k1",
		Tokens: TokensOf(&llm.Usage{InputTokens: i64(20), OutputTokens: i64(10)})})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	todayPath := filepath.Join(dir, "2026-11-19.jsonl")
	info, err := os.Stat(todayPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(todayPath)
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &line); err != nil || line.V != 1 {
		t.Fatalf("today line = %s (%v), want v=1", raw, err)
	}

	// Reopen and verify yesterday-kept + today-1 are both present with monotonic Seq.
	s2, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p := s2.Events(Filter{}, 0, 100)
	if len(p.Events) != 2 || p.Events[0].Model != "yesterday-kept" || p.Events[1].Model != "today-1" ||
		p.Events[0].Seq != 1 || p.Events[1].Seq != 2 {
		t.Fatalf("refilled events = %+v", p.Events)
	}
	sum := s2.Summarize(Filter{}, time.Minute, 1)
	if sum.Requests != 2 || sum.Tokens.Total != 45 || sum.Evicted != 0 || sum.Dropped != 0 {
		t.Fatalf("refilled summary = %+v", sum)
	}
}

func TestRefillSkipsTornLastLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		tail string
	}{
		{"without newline", `{"v":1,"seq":2,"timestamp":"2026-11-19T11:00:00Z","model":"torn`},
		{"with newline", "{\"v\":1,\"seq\":2,\"timestamp\":\"2026-11-19T11:00:00Z\",\"model\":\"torn\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
			path := filepath.Join(dir, "2026-11-19.jsonl")
			good, err := json.Marshal(diskEvent{V: 1, Event: Event{
				Seq:       1,
				Timestamp: c.now().Add(-time.Minute),
				Endpoint:  "/v1/chat/completions",
				Model:     "good-model",
			}})
			if err != nil {
				t.Fatal(err)
			}
			content := append(good, '\n')
			content = append(content, tc.tail...)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}

			s, err := openStore(Options{Dir: dir}, c.now, nil)
			if err != nil {
				t.Fatal(err)
			}
			p := s.Events(Filter{}, 0, 100)
			if len(p.Events) != 1 || p.Events[0].Model != "good-model" {
				t.Fatalf("events after torn tail = %+v", p.Events)
			}

			// Writing a new event after reopening must land cleanly on its own line.
			s.Add(Event{Timestamp: c.now(), Endpoint: "/v1/chat/completions", Model: "after-crash"})
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			s2, err := openStore(Options{Dir: dir}, c.now, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			p2 := s2.Events(Filter{}, 0, 100)
			if len(p2.Events) != 2 || p2.Events[0].Model != "good-model" || p2.Events[1].Model != "after-crash" {
				t.Fatalf("events after append+reopen = %+v", p2.Events)
			}
		})
	}
}

func TestFullQueueDropsAndCountsWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}

	writing := make(chan struct{}, 1)
	release := make(chan struct{})
	s, err := openStore(Options{Dir: dir}, c.now, func(_ string, _ []byte) error {
		select {
		case writing <- struct{}{}:
		default:
		}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.closeTimeout = 2 * time.Second

	// First event enters writeLine and blocks the single writer goroutine.
	s.Add(Event{Timestamp: c.now(), Model: "first"})
	select {
	case <-writing:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not start")
	}

	// Next DefaultQueueSize (4096) events fill the bounded queue; extra events must drop at once.
	const extra = 17
	done := make(chan struct{})
	go func() {
		for i := range DefaultQueueSize + extra {
			s.Add(Event{Timestamp: c.now(), Model: fmt.Sprintf("m-%d", i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Add blocked when the queue filled")
	}

	sum := s.Summarize(Filter{}, time.Minute, 1)
	if sum.Dropped != extra {
		close(release)
		t.Fatalf("Dropped = %d, want %d", sum.Dropped, extra)
	}

	close(release)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionDeletesOldFilesAndZeroKeepsAll(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}

	t.Run("deletes older than keep_days at start and daily", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "summary"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, day := range []string{"2026-11-14", "2026-11-15", "2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19"} {
			if err := os.WriteFile(filepath.Join(dir, day+".jsonl"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}

		// keep_days: 3 keeps 2026-11-17, 2026-11-18, 2026-11-19; deletes 2026-11-14, 15, 16 at start.
		s, err := openStore(Options{Dir: dir, KeepDays: intPtr(3)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		for _, gone := range []string{"2026-11-14.jsonl", "2026-11-15.jsonl", "2026-11-16.jsonl"} {
			if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
				t.Errorf("%s still exists after start prune: %v", gone, err)
			}
		}
		for _, kept := range []string{"2026-11-17.jsonl", "2026-11-18.jsonl", "2026-11-19.jsonl", "notes.txt", "summary"} {
			if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
				t.Errorf("%s missing: %v", kept, err)
			}
		}

		// Advance clock by 1 day and write a new day's event (triggering daily retention on rollover).
		c.add(24 * time.Hour)
		s.Add(Event{Timestamp: c.now(), Model: "next-day"})
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "2026-11-17.jsonl")); !os.IsNotExist(err) {
			t.Errorf("2026-11-17.jsonl still exists after day rollover: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "2026-11-20.jsonl")); err != nil {
			t.Errorf("2026-11-20.jsonl missing: %v", err)
		}
	})

	t.Run("keep_days 0 keeps everything", func(t *testing.T) {
		dir := t.TempDir()
		old := "2020-01-01.jsonl"
		if err := os.WriteFile(filepath.Join(dir, old), []byte(`{"v":1}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := openStore(Options{Dir: dir, KeepDays: intPtr(0)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.prune()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, old))
		if err != nil || !strings.Contains(string(raw), `"v":1`) {
			t.Fatalf("old file lost with keep_days 0: %v %q", err, raw)
		}
	})
}
