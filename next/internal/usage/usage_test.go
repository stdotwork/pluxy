package usage

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

func i64(n int64) *int64 { return &n }

// clock is a store's time, moved by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestStore(maxEvents int, maxAge time.Duration) (*Store, *clock) {
	c := &clock{t: time.Date(2026, 11, 9, 12, 0, 30, 0, time.UTC)}
	s := NewStore()
	s.now = c.now
	s.SetLimits(maxEvents, maxAge)
	return s, c
}

func seqs(p Page) []uint64 {
	var out []uint64
	for _, e := range p.Events {
		out = append(out, e.Seq)
	}
	return out
}

func TestTokensOf(t *testing.T) {
	got := TokensOf(&llm.Usage{InputTokens: i64(10), OutputTokens: i64(5), ReasoningTokens: i64(2), CachedInputTokens: i64(4)})
	if *got.Input != 10 || *got.Output != 5 || *got.Reasoning != 2 || *got.Cached != 4 || got.CacheWrite != nil || *got.Total != 15 {
		t.Fatalf("got %+v", got)
	}
	if got := TokensOf(&llm.Usage{OutputTokens: i64(3)}); got.Input != nil || *got.Total != 3 {
		t.Fatalf("output only: %+v", got)
	}
	if got := TokensOf(nil); got != (Tokens{}) {
		t.Fatalf("nil: %+v", got)
	}
	if got := TokensOf(&llm.Usage{CachedInputTokens: i64(1)}); got.Total != nil {
		t.Fatalf("no input or output reported, yet a total: %+v", got)
	}
}

// Reading never removes events; the limit on events pushes out the oldest; after reads only
// newer ones and says when some were lost.
func TestStoreKeepsWhatItIsAskedAndDropsTheOldest(t *testing.T) {
	s, c := newTestStore(3, time.Hour)
	for i := range 5 {
		s.Add(Event{Timestamp: c.now(), Model: fmt.Sprint("m", i)})
	}
	p := s.Events(Filter{}, 0, 100)
	if got := fmt.Sprint(seqs(p)); got != "[3 4 5]" || !p.Lost || p.Next != 5 || p.More {
		t.Fatalf("events %s, lost %v, next %d, more %v", got, p.Lost, p.Next, p.More)
	}
	if again := s.Events(Filter{}, 0, 100); fmt.Sprint(seqs(again)) != "[3 4 5]" {
		t.Fatalf("a second read sees %v: reading removed events", seqs(again))
	}
	p = s.Events(Filter{}, 3, 1)
	if fmt.Sprint(seqs(p)) != "[4]" || !p.More || p.Lost || p.Next != 4 {
		t.Fatalf("page after 3: %+v", p)
	}
	p = s.Events(Filter{}, 5, 10)
	if len(p.Events) != 0 || p.Next != 5 || p.Lost || p.More {
		t.Fatalf("nothing newer: %+v", p)
	}
	if sum := s.Summarize(Filter{}, time.Minute, 1); sum.Evicted != 2 || sum.Requests != 3 {
		t.Fatalf("evicted %d, requests %d", sum.Evicted, sum.Requests)
	}
}

func TestStoreDropsEventsOlderThanItsAgeLimit(t *testing.T) {
	s, c := newTestStore(100, time.Minute)
	s.Add(Event{Timestamp: c.now()})
	c.add(40 * time.Second)
	s.Add(Event{Timestamp: c.now()})
	c.add(30 * time.Second) // the first is now 70s old
	if got := fmt.Sprint(seqs(s.Events(Filter{}, 0, 10))); got != "[2]" {
		t.Fatalf("events %s", got)
	}
	c.add(time.Hour)
	p := s.Events(Filter{}, 0, 10)
	if len(p.Events) != 0 || !p.Lost {
		t.Fatalf("all expired: %+v", p)
	}
}

func TestStoreLimitsChange(t *testing.T) {
	s, c := newTestStore(10, time.Hour)
	for range 10 {
		s.Add(Event{Timestamp: c.now()})
	}
	s.SetLimits(4, time.Hour)
	if got := fmt.Sprint(seqs(s.Events(Filter{}, 0, 100))); got != "[7 8 9 10]" {
		t.Fatalf("after lowering: %s", got)
	}
	s.SetLimits(6, time.Hour)
	for range 3 {
		s.Add(Event{Timestamp: c.now()})
	}
	if got := fmt.Sprint(seqs(s.Events(Filter{}, 0, 100))); got != "[8 9 10 11 12 13]" {
		t.Fatalf("after raising: %s", got)
	}
	s.SetLimits(MaxMaxEvents*2, 0)
	if s.maxEvents != MaxMaxEvents || s.maxAge != DefaultMaxAge {
		t.Fatalf("limits %d %v", s.maxEvents, s.maxAge)
	}
}

func TestSummarize(t *testing.T) {
	s, c := newTestStore(100, time.Hour)
	add := func(ago time.Duration, model, provider, account, key string, failed bool, latency int64, in, out int64) {
		e := Event{Timestamp: c.now().Add(-ago), Model: model, Provider: provider, Account: account, KeyID: key,
			Failed: failed, LatencyMS: latency}
		if in >= 0 {
			e.Tokens = TokensOf(&llm.Usage{InputTokens: i64(in), OutputTokens: i64(out), ReasoningTokens: i64(1)})
		}
		s.Add(e)
	}
	add(5*time.Minute, "gpt", "p/openai", "a1", "k1", false, 100, 10, 5)
	add(90*time.Second, "gpt", "p/openai", "a2", "k1", true, 300, -1, 0)
	add(10*time.Second, "claude", "p/claude", "a3", "k2", false, 200, 20, 10)
	add(0, "gpt", "p/openai", "a1", "", false, 400, 1, 1)

	sum := s.Summarize(Filter{}, time.Minute, 3)
	if sum.Requests != 4 || sum.Success != 3 || sum.Failure != 1 || sum.SuccessRate != 0.75 || sum.AverageLatencyMS != 250 {
		t.Fatalf("totals %+v", sum.Totals)
	}
	if tk := sum.Tokens; tk.Input != 31 || tk.Output != 16 || tk.Total != 47 || tk.Reasoning != 3 {
		t.Fatalf("tokens %+v", tk)
	}
	if len(sum.Models) != 2 || sum.Models[0].Name != "gpt" || sum.Models[0].Requests != 3 || sum.Models[0].Failure != 1 {
		t.Fatalf("models %+v", sum.Models)
	}
	if len(sum.Accounts) != 3 || sum.Accounts[0].Name != "a1" || sum.Accounts[0].Provider != "p/openai" || sum.Accounts[0].Requests != 2 {
		t.Fatalf("accounts %+v", sum.Accounts)
	}
	if len(sum.Keys) != 2 || sum.Keys[0].Name != "k1" || sum.Keys[0].Requests != 2 {
		t.Fatalf("keys %+v (an event with no key is in no key's group)", sum.Keys)
	}
	if len(sum.Providers) != 2 || sum.Providers[0].Name != "p/openai" {
		t.Fatalf("providers %+v", sum.Providers)
	}
	// Now is 12:00:30; buckets of a minute: 11:58, 11:59, 12:00. The event 5 minutes ago is
	// before the series; 90s ago is 11:59:00; 10s ago and now are in 12:00.
	want := []Bucket{
		{Start: time.Date(2026, 11, 9, 11, 58, 0, 0, time.UTC)},
		{Start: time.Date(2026, 11, 9, 11, 59, 0, 0, time.UTC), Requests: 1, Failure: 1},
		{Start: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), Requests: 2, Tokens: 32},
	}
	if fmt.Sprint(sum.Series) != fmt.Sprint(want) || sum.BucketSeconds != 60 {
		t.Fatalf("series %v, want %v", sum.Series, want)
	}
	if sum.Oldest == nil || !sum.Oldest.Equal(c.now().Add(-5*time.Minute)) {
		t.Fatalf("oldest %v", sum.Oldest)
	}

	failed := false
	f := s.Summarize(Filter{Model: "gpt", Failed: &failed, Since: c.now().Add(-time.Minute)}, time.Minute, 1)
	if f.Requests != 1 || f.Tokens.Total != 2 {
		t.Fatalf("filtered %+v", f.Totals)
	}
	if empty := s.Summarize(Filter{Account: "none"}, time.Minute, 2); empty.Requests != 0 || empty.SuccessRate != 0 ||
		len(empty.Models) != 0 || empty.Models == nil || len(empty.Series) != 2 {
		t.Fatalf("no match %+v", empty)
	}
}

func TestStoreConcurrentUse(t *testing.T) {
	s, c := newTestStore(50, time.Hour)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				s.Add(Event{Timestamp: c.now(), Model: fmt.Sprint(g)})
				if i%10 == 0 {
					s.Events(Filter{}, 0, 10)
					s.Summarize(Filter{}, time.Second, 5)
				}
			}
		}()
	}
	wg.Wait()
	p := s.Events(Filter{}, 0, 1000)
	if len(p.Events) != 50 || p.Events[49].Seq != 1600 {
		t.Fatalf("%d events, last %d", len(p.Events), p.Events[len(p.Events)-1].Seq)
	}
}
