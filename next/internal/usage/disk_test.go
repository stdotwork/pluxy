package usage

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func intPtr(n int) *int { return &n }

func TestOpenRefillsFromTodayAndYesterdayAndPersistsV1(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}

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
	writeLine("2026-11-18", Event{Seq: 2, Timestamp: c.now().Add(-14 * time.Hour), Model: "yesterday-kept", KeyID: "k1",
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

	s2, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	p := s2.Events(Filter{}, 0, 100)
	if len(p.Events) != 2 || p.Events[0].Model != "yesterday-kept" || p.Events[1].Model != "today-1" ||
		p.Events[0].Seq != 2 || p.Events[1].Seq != 3 {
		t.Fatalf("refilled events = %+v", p.Events)
	}
	sum24 := s2.Summarize(Filter{Since: c.now().Add(-24 * time.Hour)}, time.Minute, 1)
	if sum24.Requests != 2 || sum24.Tokens.Total != 45 || sum24.Evicted != 0 || sum24.Dropped != 0 {
		t.Fatalf("refilled 24h summary = %+v", sum24)
	}
	sumAll := s2.Summarize(Filter{}, time.Minute, 1)
	if sumAll.Requests != 4 || sumAll.Tokens.Total != 45 {
		t.Fatalf("refilled all-kept summary = %+v", sumAll)
	}
}

func TestServedModelPersistsAndOlderRecordsLoad(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(dir, "2026-11-19.jsonl")
	old := Event{Seq: 1, Timestamp: c.now().Add(-2 * time.Minute), Model: "gpt-4o"}
	line, err := json.Marshal(diskEvent{V: 1, Event: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Timestamp: c.now(), Model: "gpt-4o", ServedModel: "gpt-4o-2024-08-06"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	events := s2.Events(Filter{}, 0, 10).Events
	if len(events) != 2 || events[0].ServedModel != "" || events[1].ServedModel != "gpt-4o-2024-08-06" {
		t.Fatalf("served models after reload = %+v", events)
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

	s.Add(Event{Timestamp: c.now(), Model: "first"})
	select {
	case <-writing:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not start")
	}

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

func TestSeqPersistsAcrossRestarts(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}

	t.Run("continues above stored seq and numbers old unsequenced lines", func(t *testing.T) {
		dir := t.TempDir()
		yesterday := c.now().Add(-24 * time.Hour).UTC().Format("2006-01-02")
		today := c.now().UTC().Format("2006-01-02")

		writeJSONL := func(day string, lines ...Event) {
			path := filepath.Join(dir, day+".jsonl")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			for _, e := range lines {
				b, err := json.Marshal(diskEvent{V: 1, Event: e})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.Write(append(b, '\n')); err != nil {
					t.Fatal(err)
				}
			}
		}
		writeJSONL(yesterday,
			Event{Timestamp: c.now().Add(-30 * time.Hour), Model: "unseq-old"},
			Event{Seq: 10, Timestamp: c.now().Add(-25 * time.Hour), Model: "seq-10-expired"},
		)
		writeJSONL(today,
			Event{Seq: 12, Timestamp: c.now().Add(-2 * time.Hour), Model: "today-seq-12"},
		)

		s, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		seq13 := s.Add(Event{Timestamp: c.now(), Model: "after-start-1"})
		if seq13 != 13 {
			s.Close()
			t.Fatalf("first seq after refill = %d, want 13", seq13)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		c.add(25 * time.Hour)
		s2, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()

		seq14 := s2.Add(Event{Timestamp: c.now(), Model: "after-second-restart"})
		if seq14 != 14 {
			t.Fatalf("seq after second restart (all prior expired) = %d, want 14", seq14)
		}
		p := s2.Events(Filter{}, seq13, 100)
		if len(p.Events) != 1 || p.Events[0].Seq != 14 || p.Events[0].Model != "after-second-restart" {
			t.Fatalf("Events(after=%d) = %+v, want only seq 14", seq13, p.Events)
		}
	})

	t.Run("continues above newest existing day file after an idle gap", func(t *testing.T) {
		dir := t.TempDir()
		fiveDaysAgo := c.now().AddDate(0, 0, -5).UTC().Format("2006-01-02")
		path := filepath.Join(dir, fiveDaysAgo+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for seq := uint64(1); seq <= 3; seq++ {
			b, err := json.Marshal(diskEvent{V: 1, Event: Event{
				Seq:       seq,
				Timestamp: c.now().AddDate(0, 0, -5).Add(time.Duration(seq) * time.Minute),
				Model:     "old-gap",
			}})
			if err != nil {
				f.Close()
				t.Fatal(err)
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				f.Close()
				t.Fatal(err)
			}
		}
		f.Close()

		s, err := openStore(Options{Dir: dir, MaxEvents: 100, MaxAge: 24 * time.Hour}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()

		if seq4 := s.Add(Event{Timestamp: c.now(), Model: "after-gap"}); seq4 != 4 {
			t.Fatalf("first seq after 5-day idle gap = %d, want 4", seq4)
		}
	})
}

func refSummarize(events []Event, f Filter, bucket time.Duration, first time.Time, buckets int) Summary {
	out := Summary{
		BucketSeconds: int64(bucket / time.Second),
		Series:        make([]Bucket, buckets),
	}
	for i := range out.Series {
		out.Series[i].Start = first.Add(time.Duration(i) * bucket).UTC()
	}
	groups := [4]map[string]*Group{{}, {}, {}, {}}
	for i := range events {
		e := &events[i]
		if !f.match(e) {
			continue
		}
		out.Totals.add(e)
		for g, name := range [4]string{e.Model, e.Provider, e.Account, e.KeyID} {
			if name == "" {
				continue
			}
			grp := groups[g][name]
			if grp == nil {
				grp = &Group{Name: name}
				if g == 2 {
					grp.Provider = e.Provider
				}
				groups[g][name] = grp
			}
			grp.add(e)
		}
		if !e.Timestamp.Before(first) {
			if b := int(e.Timestamp.Sub(first) / bucket); b >= 0 && b < buckets {
				out.Series[b].Requests++
				if e.Failed {
					out.Series[b].Failure++
				}
				out.Series[b].Tokens += deref(e.Tokens.Total)
			}
		}
	}
	out.Totals.finish()
	res := [4][]Group{}
	for g := range groups {
		res[g] = []Group{}
		for _, grp := range groups[g] {
			grp.finish()
			res[g] = append(res[g], *grp)
		}
		slices.SortFunc(res[g], func(a, b Group) int {
			return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Name, b.Name))
		})
	}
	out.Models, out.Providers, out.Accounts, out.Keys = res[0], res[1], res[2], res[3]
	return out
}

func assertSummaryMatches(t *testing.T, label string, got, want Summary) {
	t.Helper()
	if got.Totals != want.Totals {
		t.Fatalf("%s totals = %+v, want %+v", label, got.Totals, want.Totals)
	}
	if fmt.Sprint(got.Models) != fmt.Sprint(want.Models) {
		t.Fatalf("%s models = %+v, want %+v", label, got.Models, want.Models)
	}
	if fmt.Sprint(got.Providers) != fmt.Sprint(want.Providers) {
		t.Fatalf("%s providers = %+v, want %+v", label, got.Providers, want.Providers)
	}
	if fmt.Sprint(got.Accounts) != fmt.Sprint(want.Accounts) {
		t.Fatalf("%s accounts = %+v, want %+v", label, got.Accounts, want.Accounts)
	}
	if fmt.Sprint(got.Keys) != fmt.Sprint(want.Keys) {
		t.Fatalf("%s keys = %+v, want %+v", label, got.Keys, want.Keys)
	}
	if got.BucketSeconds != want.BucketSeconds || fmt.Sprint(got.Series) != fmt.Sprint(want.Series) {
		t.Fatalf("%s series (bucket=%d) = %+v, want (bucket=%d) %+v", label, got.BucketSeconds, got.Series, want.BucketSeconds, want.Series)
	}
}

func TestSummariesBuiltOnDemandAndNoClosedRawReads(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	c := &clock{t: now}

	models := []string{"claude-sonnet", "gpt-5", "gemini-pro"}
	providers := []string{"p/claude", "p/openai", "p/gemini"}
	accounts := []string{"acct-claude", "acct-openai", "acct-gemini"}
	keys := []string{"k-alpha", "k-beta", ""}

	var allEvents []Event
	byDay := map[string][]Event{}
	var seq uint64
	for d := 30; d >= 0; d-- {
		dayTime := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -d)
		dayStr := dayTime.Format(dayLayout)
		for h := 0; h < 24; h += 3 {
			ts := dayTime.Add(time.Duration(h)*time.Hour + 15*time.Minute)
			if d == 0 && !ts.Before(now) {
				continue
			}
			seq++
			idx := (d + h) % len(models)
			kIdx := (d*3 + h) % len(keys)
			failed := (d+h)%5 == 0
			in := int64(100 + d + h)
			out := int64(40 + h)
			reas := int64(5 + (h % 4))
			cached := int64(20 + (d % 10))
			cw := int64(7 + (d % 3))
			ev := Event{
				Seq:       seq,
				Timestamp: ts,
				Endpoint:  "/v1/chat/completions",
				Model:     models[idx],
				Provider:  providers[idx],
				Account:   accounts[idx],
				KeyID:     keys[kIdx],
				Failed:    failed,
				LatencyMS: int64(50 + d*2 + h),
				Tokens: TokensOf(&llm.Usage{
					InputTokens:           &in,
					OutputTokens:          &out,
					ReasoningTokens:       &reas,
					CachedInputTokens:     &cached,
					CacheWriteInputTokens: &cw,
				}),
			}
			allEvents = append(allEvents, ev)
			byDay[dayStr] = append(byDay[dayStr], ev)
		}
	}
	for dayStr, evs := range byDay {
		path := filepath.Join(dir, dayStr+daySuffix)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			b, err := json.Marshal(diskEvent{V: 1, Event: ev})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write(append(b, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
	}

	s, err := openStore(Options{Dir: dir, MaxEvents: 1000, MaxAge: 48 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sumDir := filepath.Join(dir, "summary")
	if _, err := os.Stat(sumDir); !os.IsNotExist(err) {
		t.Fatalf("summary dir created eagerly on Open: %v", err)
	}

	f30 := Filter{Since: now.Add(-30 * 24 * time.Hour)}
	got30 := s.Summarize(f30, 0, 0)
	first30 := f30.Since.Truncate(24 * time.Hour)
	want30 := refSummarize(allEvents, f30, 24*time.Hour, first30, int(now.Truncate(24*time.Hour).Sub(first30)/(24*time.Hour))+1)
	assertSummaryMatches(t, "30d initial", got30, want30)

	todayStr := now.UTC().Format(dayLayout)
	if _, err := os.Stat(filepath.Join(sumDir, todayStr+".json")); !os.IsNotExist(err) {
		t.Fatalf("summary written for today %s", todayStr)
	}
	closedSample := filepath.Join(sumDir, now.UTC().AddDate(0, 0, -5).Format(dayLayout)+".json")
	info, err := os.Stat(closedSample)
	if err != nil {
		t.Fatalf("missing closed day summary %s: %v", closedSample, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("summary mode = %o, want 0600", info.Mode().Perm())
	}

	s2, err := openStore(Options{Dir: dir, MaxEvents: 1000, MaxAge: 48 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.onReadRaw = func(day string) {
		if day < todayStr {
			t.Fatalf("opened raw day file %s for closed day after summaries were built", day)
		}
	}

	assertSummaryMatches(t, "30d from disk summaries", s2.Summarize(f30, 0, 0), want30)

	failedTrue := true
	for _, tc := range []struct {
		name string
		f    Filter
	}{
		{"model", Filter{Since: f30.Since, Model: "gpt-5"}},
		{"account", Filter{Since: f30.Since, Account: "acct-claude"}},
		{"key", Filter{Since: f30.Since, KeyID: "k-beta"}},
		{"failed", Filter{Since: f30.Since, Failed: &failedTrue}},
	} {
		got := s2.Summarize(tc.f, 0, 0)
		want := refSummarize(allEvents, tc.f, 24*time.Hour, first30, len(want30.Series))
		assertSummaryMatches(t, "30d "+tc.name, got, want)
	}

	f2d := Filter{Since: now.Add(-48 * time.Hour)}
	got2d := s2.Summarize(f2d, 0, 0)
	first2d := f2d.Since.Truncate(time.Hour)
	want2d := refSummarize(allEvents, f2d, time.Hour, first2d, int(now.Truncate(time.Hour).Sub(first2d)/time.Hour)+1)
	assertSummaryMatches(t, "2d hourly", got2d, want2d)

	s2.onReadRaw = nil
	if err := os.WriteFile(closedSample, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	s3, err := openStore(Options{Dir: dir, MaxEvents: 1000, MaxAge: 48 * time.Hour}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	assertSummaryMatches(t, "30d after corrupt rebuild", s3.Summarize(f30, 0, 0), want30)
}

func TestRetentionKeepsSummariesAndReadsThem(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	c := &clock{t: now}

	oldDay := now.UTC().AddDate(0, 0, -10)
	oldPath := filepath.Join(dir, oldDay.Format(dayLayout)+daySuffix)
	in, out := int64(50), int64(25)
	b, _ := json.Marshal(diskEvent{V: 1, Event: Event{
		Seq: 1, Timestamp: oldDay, Model: "gpt-old", Provider: "p/o", Account: "a-old", KeyID: "k-old",
		Tokens: TokensOf(&llm.Usage{InputTokens: &in, OutputTokens: &out}),
	}})
	if err := os.WriteFile(oldPath, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := openStore(Options{Dir: dir, KeepDays: intPtr(30)}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum := s.Summarize(Filter{Since: now.Add(-30 * 24 * time.Hour)}, 0, 0)
	if sum.Requests != 1 || sum.Tokens.Total != 75 {
		s.Close()
		t.Fatalf("before prune: %+v", sum)
	}
	s.Close()

	s2, err := openStore(Options{Dir: dir, KeepDays: intPtr(3)}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("raw file %s still exists after keep_days=3", oldPath)
	}
	sum2 := s2.Summarize(Filter{Since: now.Add(-30 * 24 * time.Hour)}, 0, 0)
	if sum2.Requests != 1 || sum2.Tokens.Total != 75 || len(sum2.Accounts) != 1 || sum2.Accounts[0].Name != "a-old" {
		t.Fatalf("after prune: summary was lost: %+v", sum2)
	}
}

func TestTodayRingOverflowReadsTodayFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	c := &clock{t: now}

	s, err := openStore(Options{Dir: dir, MaxEvents: 3}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	in, out := int64(10), int64(5)
	for i := range 7 {
		s.Add(Event{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Model:     "gpt-today",
			Tokens:    TokensOf(&llm.Usage{InputTokens: &in, OutputTokens: &out}),
		})
	}
	sum := s.Summarize(Filter{Since: now.Add(-time.Hour)}, 0, 0)
	if sum.Requests != 7 || sum.Tokens.Total != 7*15 {
		t.Fatalf("today overflow summary = %+v, want 7 requests and 105 tokens", sum)
	}
}

func TestLateClosedDayEventInvalidatesSummary(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 0, 5, 0, 0, time.UTC)
	c := &clock{t: now}

	yesterdayTs := time.Date(2026, 11, 18, 23, 50, 0, 0, time.UTC)
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	yPath := filepath.Join(dir, "2026-11-18.jsonl")
	b, _ := json.Marshal(diskEvent{V: 1, Event: Event{Seq: 1, Timestamp: yesterdayTs, Model: "m1"}})
	if err := os.WriteFile(yPath, append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	sum1 := s.Summarize(Filter{Since: now.Add(-48 * time.Hour)}, 0, 0)
	if sum1.Requests != 1 {
		t.Fatalf("initial summary requests = %d, want 1", sum1.Requests)
	}

	s.Add(Event{Timestamp: yesterdayTs.Add(9 * time.Minute), Model: "m2"})
	sum2 := s.Summarize(Filter{Since: now.Add(-48 * time.Hour)}, 0, 0)
	if sum2.Requests != 2 {
		t.Fatalf("after late closed-day event: requests = %d, want 2", sum2.Requests)
	}
}

func appendEvents(t *testing.T, path string, events ...Event) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range events {
		b, err := json.Marshal(diskEvent{V: 1, Event: e})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeriesCountsClosedDayHourTheRangeCutsShort(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.UTC)
	c := &clock{t: now}
	appendEvents(t, filepath.Join(dir, "2026-11-18"+daySuffix),
		Event{Seq: 1, Timestamp: time.Date(2026, 11, 18, 21, 45, 0, 0, time.UTC), Model: "m"},
		Event{Seq: 2, Timestamp: time.Date(2026, 11, 18, 22, 30, 0, 0, time.UTC), Model: "m"},
	)
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sum := s.Summarize(Filter{Since: now.Add(-200 * time.Minute)}, 10*time.Minute, 20)
	if sum.Requests != 2 {
		t.Fatalf("totals = %d requests, want 2", sum.Requests)
	}
	if got := sum.Series[0]; got.Requests != 1 {
		t.Errorf("first step %+v, want the closed 21:00 hour counted there", got)
	}
	if got := sum.Series[1]; got.Requests != 1 {
		t.Errorf("second step %+v, want the closed 22:00 hour counted there", got)
	}
	var inSeries int64
	for _, b := range sum.Series {
		inSeries += b.Requests
	}
	if inSeries != sum.Requests {
		t.Errorf("series counts %d requests, totals %d", inSeries, sum.Requests)
	}
}

func TestSeqSurvivesPrunedDayFiles(t *testing.T) {
	t.Run("after a clean shutdown and a longer idle than retention keeps", func(t *testing.T) {
		dir := t.TempDir()
		c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
		s, err := openStore(Options{Dir: dir}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		for range 3 {
			s.Add(Event{Timestamp: c.now(), Model: "before"})
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		c.add(72 * time.Hour)
		s2, err := openStore(Options{Dir: dir, KeepDays: intPtr(1)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		if seq := s2.Add(Event{Timestamp: c.now(), Model: "after"}); seq != 4 {
			t.Fatalf("first seq after the day files were pruned = %d, want 4", seq)
		}
	})

	t.Run("after a crash, from the newest day file retention would delete", func(t *testing.T) {
		dir := t.TempDir()
		c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
		appendEvents(t, filepath.Join(dir, "2026-11-15"+daySuffix),
			Event{Seq: 9, Timestamp: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC), Model: "old"})

		s, err := openStore(Options{Dir: dir, KeepDays: intPtr(3)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if seq := s.Add(Event{Timestamp: c.now(), Model: "after"}); seq != 10 {
			t.Fatalf("first seq after a crash and a prune = %d, want 10", seq)
		}
	})

	t.Run("after a run whose last day file retention deleted", func(t *testing.T) {
		dir := t.TempDir()
		c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
		s, err := openStore(Options{Dir: dir, KeepDays: intPtr(1)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Add(Event{Timestamp: c.now(), Model: "before"})
		s.Add(Event{Timestamp: c.now(), Model: "before"})
		s.flushWriter()

		c.add(72 * time.Hour)
		s.prune()

		s2, err := openStore(Options{Dir: dir, KeepDays: intPtr(1)}, c.now, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		if seq := s2.Add(Event{Timestamp: c.now(), Model: "after"}); seq != 3 {
			t.Fatalf("first seq after the last day file was pruned = %d, want 3", seq)
		}
	})
}

func TestLateClosedDayEventRebuildsAStaleSummary(t *testing.T) {
	now := time.Date(2026, 11, 19, 0, 5, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 18, 23, 50, 0, 0, time.UTC)
	f := Filter{Since: now.Add(-48 * time.Hour)}

	for _, tc := range []struct {
		name   string
		reopen bool
	}{
		{"from the summary in memory", false},
		{"from the summary file after a restart", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			c := &clock{t: now}
			path := filepath.Join(dir, "2026-11-18"+daySuffix)
			appendEvents(t, path, Event{Seq: 1, Timestamp: yesterday, Model: "m1"})

			s, err := openStore(Options{Dir: dir}, c.now, nil)
			if err != nil {
				t.Fatal(err)
			}
			if sum := s.Summarize(f, 0, 0); sum.Requests != 1 {
				s.Close()
				t.Fatalf("initial summary = %d requests, want 1", sum.Requests)
			}

			appendEvents(t, path, Event{Seq: 2, Timestamp: yesterday.Add(5 * time.Minute), Model: "m2"})
			if tc.reopen {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if s, err = openStore(Options{Dir: dir}, c.now, nil); err != nil {
					t.Fatal(err)
				}
			}
			defer s.Close()
			if sum := s.Summarize(f, 0, 0); sum.Requests != 2 {
				t.Fatalf("summary after the closed day grew = %d requests, want 2", sum.Requests)
			}
		})
	}
}

func TestDiskWriteErrorIsCountedAndLogged(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	var logged lockedBuffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)

	s, err := openStore(Options{Dir: dir}, c.now, func(string, []byte) error {
		return errors.New("no space left on device")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Add(Event{Timestamp: c.now(), Model: "m"})
	s.flushWriter()

	sum := s.Summarize(Filter{}, time.Minute, 1)
	if sum.Requests != 1 {
		t.Errorf("requests = %d, want the event kept in the record", sum.Requests)
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var reply map[string]any
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if got := reply["dropped"]; got != float64(0) {
		t.Errorf("dropped = %v, want 0: a write error is not a full queue", got)
	}
	if got := reply["write_errors"]; got != float64(1) {
		t.Errorf("write_errors = %v, want 1", got)
	}
	if line := logged.String(); !strings.Contains(line, "usage:") || !strings.Contains(line, "no space left on device") {
		t.Errorf("logged %q, want the failed write and its error", line)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestAttemptsAndTriedPersistAndOlderRecordsLoad(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	old := `{"v":1,"seq":1,"timestamp":"2026-11-19T11:58:00Z","endpoint":"/v1/chat/completions","stream":false,"model":"gpt-4o","failed":false,"latency_ms":3,"tokens":{}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-11-19.jsonl"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	tried := []llm.Tried{
		{Account: "acc_11111111", Provider: "openai", Model: "gpt-4o", Result: "rate_limit"},
		{Account: "acc_22222222", Provider: "openai", Model: "gpt-4o", Result: "ok"},
	}
	s.Add(Event{Timestamp: c.now(), Model: "gpt-4o", Attempts: 2, Tried: tried})
	s.Add(Event{Timestamp: c.now(), Model: "gpt-4o", Attempts: 1})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ev := s2.Events(Filter{}, 0, 10).Events
	if len(ev) != 3 || ev[0].Attempts != 0 || ev[0].Tried != nil || ev[1].Attempts != 2 || !slices.Equal(ev[1].Tried, tried) ||
		ev[2].Attempts != 1 || ev[2].Tried != nil {
		t.Fatalf("events after reload = %+v", ev)
	}
}

func TestAverageTimeFromSummaryRowsLeavesOutRequestsThatReachedNoAccount(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	c := &clock{t: now}
	day := now.UTC().AddDate(0, 0, -3)
	var lines []byte
	for i, e := range []Event{
		{Model: "gpt", Account: "a1", LatencyMS: 300},
		{Model: "claude-opus", Failed: true},
		{Model: "claude-opus", Failed: true},
	} {
		e.Seq, e.Timestamp = uint64(i+1), day.Add(time.Duration(i)*time.Minute)
		b, err := json.Marshal(diskEvent{V: 1, Event: e})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, append(b, '\n')...)
	}
	if err := os.WriteFile(filepath.Join(dir, day.Format(dayLayout)+daySuffix), lines, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sum := s.Summarize(Filter{Since: now.Add(-7 * 24 * time.Hour)}, 0, 0)
	if sum.Requests != 3 || sum.Failure != 2 || sum.AverageLatencyMS != 300 {
		t.Fatalf("summary = %+v, want 3 requests, 2 failures and a 300 ms average", sum.Totals)
	}
}
