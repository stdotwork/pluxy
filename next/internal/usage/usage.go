package usage

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	DefaultMaxEvents = 10000
	MaxMaxEvents     = 200000
	DefaultMaxAge    = 24 * time.Hour
	MaxMaxAge        = 30 * 24 * time.Hour
	DefaultQueueSize = 4096
	DefaultKeepDays  = 90
	MaxKeepDays      = 1000000
	maxSeriesBuckets = 1440
)

type Tokens struct {
	Input      *int64 `json:"input_tokens,omitempty"`
	Output     *int64 `json:"output_tokens,omitempty"`
	Reasoning  *int64 `json:"reasoning_tokens,omitempty"`
	Cached     *int64 `json:"cached_tokens,omitempty"`
	CacheWrite *int64 `json:"cache_creation_tokens,omitempty"`
	Total      *int64 `json:"total_tokens,omitempty"`
}

func TokensOf(u *llm.Usage) Tokens {
	if u == nil {
		return Tokens{}
	}
	t := Tokens{Input: u.InputTokens, Output: u.OutputTokens, Reasoning: u.ReasoningTokens,
		Cached: u.CachedInputTokens, CacheWrite: u.CacheWriteInputTokens}
	if t.Input != nil || t.Output != nil {
		total := deref(t.Input) + deref(t.Output)
		t.Total = &total
	}
	return t
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

type Event struct {
	Seq           uint64        `json:"seq"`
	ID            string        `json:"id,omitempty"`
	Timestamp     time.Time     `json:"timestamp"`
	Endpoint      string        `json:"endpoint"`
	Stream        bool          `json:"stream"`
	Model         string        `json:"model"`
	UpstreamModel string        `json:"upstream_model,omitempty"`
	ServedModel   string        `json:"served_model,omitempty"`
	Provider      string        `json:"provider,omitempty"`
	Account       string        `json:"account,omitempty"`
	ChainStep     int           `json:"chain_step,omitempty"`
	Why           string        `json:"why,omitempty"`
	Attempts      int           `json:"attempts,omitempty"`
	Tried         []llm.Tried   `json:"tried,omitempty"`
	LeftOut       []llm.LeftOut `json:"left_out,omitempty"`
	KeyID         string        `json:"key_id,omitempty"`
	Label         string        `json:"label,omitempty"`
	ClientIP      string        `json:"client_ip,omitempty"`
	Failed        bool          `json:"failed"`
	Status        int           `json:"status,omitempty"`
	ErrorCode     string        `json:"error_code,omitempty"`
	LatencyMS     int64         `json:"latency_ms"`
	Tokens        Tokens        `json:"tokens"`

	recorded time.Time
}

type Filter struct {
	Since, Until time.Time
	Model        string
	Provider     string
	Account      string
	KeyID        string
	Failed       *bool
}

func (f Filter) match(e *Event) bool {
	return (f.Since.IsZero() || !e.Timestamp.Before(f.Since)) && (f.Until.IsZero() || e.Timestamp.Before(f.Until)) &&
		(f.Model == "" || f.Model == e.Model) && (f.Provider == "" || f.Provider == e.Provider) &&
		(f.Account == "" || f.Account == e.Account) && (f.KeyID == "" || f.KeyID == e.KeyID) &&
		(f.Failed == nil || *f.Failed == e.Failed)
}

type Store struct {
	mu        sync.Mutex
	ring      []Event
	head, n   int
	seq       uint64
	evicted   uint64
	maxEvents int
	maxAge    time.Duration
	now       func() time.Time

	dir             string
	keepDays        atomic.Int64
	dropped         atomic.Uint64
	writeErrs       atomic.Uint64
	writeCh         chan Event
	flushCh         chan chan struct{}
	pruneCh         chan struct{}
	done            chan struct{}
	closed          bool
	closeTimeout    time.Duration
	writeLine       func(day string, line []byte) error
	onReadRaw       func(day string)
	openSeq         uint64
	queuedSeq       atomic.Uint64
	writtenSeq      atomic.Uint64
	closedQueuedSeq atomic.Uint64
	incompleteDay   map[string]bool

	sumMu     sync.Mutex
	summaries map[string]*daySummaryFile
}

func (s *Store) SetLimits(maxEvents int, maxAge time.Duration) {
	if maxEvents <= 0 {
		maxEvents = DefaultMaxEvents
	}
	maxEvents = min(maxEvents, MaxMaxEvents)
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxEvents != s.maxEvents {
		kept := s.events()
		if len(kept) > maxEvents {
			cut := len(kept) - maxEvents
			for i := range cut {
				s.markIncomplete(kept[i])
			}
			s.evicted += uint64(cut)
			kept = kept[cut:]
		}
		s.ring = append(make([]Event, 0, min(maxEvents, max(len(kept), 64))), kept...)
		s.head, s.n = 0, len(kept)
	}
	s.maxEvents, s.maxAge = maxEvents, maxAge
	s.expire()
}

func (s *Store) events() []Event {
	out := make([]Event, 0, s.n)
	for i := range s.n {
		out = append(out, s.ring[(s.head+i)%len(s.ring)])
	}
	return out
}

func (s *Store) expire() {
	cutoff := s.now().Add(-s.maxAge)
	for s.n > 0 && s.ring[s.head].recorded.Before(cutoff) {
		s.markIncomplete(s.ring[s.head])
		s.ring[s.head] = Event{}
		s.head = (s.head + 1) % len(s.ring)
		s.n--
		s.evicted++
	}
}

func (s *Store) Add(e Event) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	s.seq++
	e.Seq = s.seq
	e.recorded = s.now()
	if s.n > 0 {
		if last := s.ring[(s.head+s.n-1)%len(s.ring)].recorded; e.recorded.Before(last) {
			e.recorded = last
		}
	}
	s.appendRing(e, true)
	if s.writeCh != nil && !s.closed {
		select {
		case s.writeCh <- e:
			s.queuedSeq.Store(e.Seq)
			todayDay := e.recorded.UTC().Format(dayLayout)
			t := e.Timestamp
			if t.IsZero() {
				t = e.recorded
			}
			if t.UTC().Format(dayLayout) < todayDay {
				s.closedQueuedSeq.Store(e.Seq)
			}
		default:
			s.dropped.Add(1)
		}
	}
	return e.Seq
}

func (s *Store) appendRing(e Event, countEviction bool) {
	switch {
	case s.n < len(s.ring):
		s.ring[(s.head+s.n)%len(s.ring)] = e
		s.n++
	case len(s.ring) < s.maxEvents:
		if s.head != 0 {
			s.ring = s.events()
			s.head = 0
		}
		s.ring = append(s.ring, e)
		s.n++
	default:
		s.markIncomplete(s.ring[s.head])
		s.ring[s.head] = e
		s.head = (s.head + 1) % len(s.ring)
		if countEviction {
			s.evicted++
		}
	}
}

func (s *Store) markIncomplete(e Event) {
	if s.dir == "" {
		return
	}
	t := e.Timestamp
	if t.IsZero() {
		t = e.recorded
	}
	if t.IsZero() {
		return
	}
	if s.incompleteDay == nil {
		s.incompleteDay = map[string]bool{}
	}
	s.incompleteDay[t.UTC().Format(dayLayout)] = true
}

type Page struct {
	Events []Event `json:"events"`
	Next   uint64  `json:"next"`
	More   bool    `json:"more"`
	Lost   bool    `json:"lost"`
}

func (s *Store) Events(f Filter, after uint64, limit int) Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	p := Page{Events: []Event{}, Next: after}
	if s.n > 0 {
		p.Lost = s.ring[s.head].Seq-1 > after
	} else {
		p.Lost = s.seq > after
	}
	for i := range s.n {
		e := &s.ring[(s.head+i)%len(s.ring)]
		if e.Seq <= after || !f.match(e) {
			continue
		}
		if len(p.Events) == limit {
			p.More = true
			break
		}
		p.Events = append(p.Events, *e)
		p.Next = e.Seq
	}
	return p
}

func (s *Store) EventsBefore(f Filter, before uint64, limit int) Page {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	p := Page{Events: []Event{}, Next: before}
	for i := s.n - 1; i >= 0; i-- {
		e := &s.ring[(s.head+i)%len(s.ring)]
		if (before > 0 && e.Seq >= before) || !f.match(e) {
			continue
		}
		if len(p.Events) == limit {
			p.More = true
			break
		}
		p.Events = append(p.Events, *e)
		p.Next = e.Seq
	}
	return p
}

type Totals struct {
	Requests         int64       `json:"total_requests"`
	Success          int64       `json:"success_count"`
	Failure          int64       `json:"failure_count"`
	SuccessRate      float64     `json:"success_rate"`
	AverageLatencyMS int64       `json:"average_latency_ms"`
	Tokens           TokenTotals `json:"tokens"`

	latency, timed int64
}

type TokenTotals struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_tokens"`
	Cached     int64 `json:"cached_tokens"`
	CacheWrite int64 `json:"cache_creation_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (t *Totals) add(e *Event) {
	t.Requests++
	if e.Failed {
		t.Failure++
	} else {
		t.Success++
	}
	if e.Account != "" {
		t.latency += e.LatencyMS
		t.timed++
	}
	k := e.Tokens
	t.Tokens.Input += deref(k.Input)
	t.Tokens.Output += deref(k.Output)
	t.Tokens.Reasoning += deref(k.Reasoning)
	t.Tokens.Cached += deref(k.Cached)
	t.Tokens.CacheWrite += deref(k.CacheWrite)
	t.Tokens.Total += deref(k.Total)
}

func (t *Totals) addRow(r summaryRow) {
	t.Requests += r.Requests
	if r.Failed {
		t.Failure += r.Requests
	} else {
		t.Success += r.Requests
	}
	if r.Account != "" {
		t.latency += r.LatencyMS
		t.timed += r.Requests
	}
	t.Tokens.Input += r.Input
	t.Tokens.Output += r.Output
	t.Tokens.Reasoning += r.Reasoning
	t.Tokens.Cached += r.Cached
	t.Tokens.CacheWrite += r.CacheWrite
	t.Tokens.Total += r.Total
}

func (t *Totals) finish() {
	if t.Requests > 0 {
		t.SuccessRate = float64(t.Success) / float64(t.Requests)
	}
	if t.timed > 0 {
		t.AverageLatencyMS = t.latency / t.timed
	}
}

type Group struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Totals
}

type Bucket struct {
	Start    time.Time `json:"start"`
	Requests int64     `json:"requests"`
	Failure  int64     `json:"failure_count"`
	Tokens   int64     `json:"total_tokens"`
}

type Summary struct {
	Totals
	By            string     `json:"by,omitempty"`
	Breakdown     []Group    `json:"breakdown,omitzero"`
	Models        []Group    `json:"models"`
	Providers     []Group    `json:"providers"`
	Accounts      []Group    `json:"accounts"`
	Keys          []Group    `json:"keys"`
	BucketSeconds int64      `json:"bucket_seconds"`
	Series        []Bucket   `json:"series"`
	Oldest        *time.Time `json:"oldest,omitempty"`
	Evicted       uint64     `json:"evicted"`
	Dropped       uint64     `json:"dropped"`
	WriteErrors   uint64     `json:"write_errors"`
}

func (s *Store) Summarize(f Filter, bucket time.Duration, buckets int) Summary {
	s.mu.Lock()
	s.expire()
	now := s.now()
	nowUTC := now.UTC()
	todayStart := time.Date(nowUTC.Year(), nowUTC.Month(), nowUTC.Day(), 0, 0, 0, 0, time.UTC)
	todayDay := todayStart.Format(dayLayout)

	dir := s.dir
	evicted := s.evicted
	openSeq := s.openSeq
	incompleteToday := s.incompleteDay[todayDay]
	for d := range s.incompleteDay {
		if d < todayDay {
			delete(s.incompleteDay, d)
		}
	}
	var ringEvents []Event
	if dir == "" {
		ringEvents = s.events()
	} else {
		for i := range s.n {
			e := s.ring[(s.head+i)%len(s.ring)]
			if !e.Timestamp.Before(todayStart) {
				ringEvents = append(ringEvents, e)
			}
		}
	}
	s.mu.Unlock()

	var oldest *time.Time
	updateOldest := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if oldest == nil || t.Before(*oldest) {
			cp := t
			oldest = &cp
		}
	}

	var daySummaries []*daySummaryFile
	var todayEvents []Event
	if dir == "" {
		todayEvents = ringEvents
		for i := range todayEvents {
			updateOldest(todayEvents[i].Timestamp)
		}
	} else {
		closedDays := s.listClosedDays(f, todayDay)
		if (incompleteToday && s.writtenSeq.Load() < s.queuedSeq.Load()) ||
			s.writtenSeq.Load() < s.closedQueuedSeq.Load() ||
			s.needsClosedFlush(closedDays) {
			s.flushWriter()
		}
		if len(closedDays) > 0 {
			daySummaries = s.loadDaySummaries(closedDays)
			for _, ds := range daySummaries {
				if ds.Oldest != nil {
					updateOldest(*ds.Oldest)
				}
			}
		}
		if f.Until.IsZero() || f.Until.After(todayStart) {
			todayEvents = s.collectTodayEvents(todayDay, todayStart, incompleteToday, openSeq, ringEvents)
			for i := range todayEvents {
				updateOldest(todayEvents[i].Timestamp)
			}
		}
	}

	bucket, buckets, first := resolveSeriesWindow(f, nowUTC, oldest, bucket, buckets)
	sum := Summary{
		BucketSeconds: int64(bucket / time.Second),
		Series:        make([]Bucket, buckets),
		Oldest:        oldest,
		Evicted:       evicted,
		Dropped:       s.dropped.Load(),
		WriteErrors:   s.writeErrs.Load(),
	}
	for i := range sum.Series {
		sum.Series[i].Start = first.Add(time.Duration(i) * bucket).UTC()
	}

	groups := [4]map[string]*Group{{}, {}, {}, {}}
	groupFor := func(g int, name, provider string) *Group {
		grp := groups[g][name]
		if grp == nil {
			grp = &Group{Name: name}
			if g == 2 {
				grp.Provider = provider
			}
			groups[g][name] = grp
		}
		return grp
	}

	for _, ds := range daySummaries {
		dayStart, err := time.Parse(dayLayout, ds.Day)
		if err != nil {
			continue
		}
		for _, r := range ds.Rows {
			hourStart := dayStart.Add(time.Duration(r.Hour) * time.Hour)
			hourEnd := hourStart.Add(time.Hour)
			if !f.matchSummaryRow(r, hourStart, hourEnd) {
				continue
			}
			sum.Totals.addRow(r)
			for g, name := range [4]string{r.Model, r.Provider, r.Account, r.KeyID} {
				if name == "" {
					continue
				}
				groupFor(g, name, r.Provider).addRow(r)
			}
			b := -1
			switch {
			case !hourStart.Before(first):
				b = int(hourStart.Sub(first) / bucket)
			case hourEnd.After(first):
				b = 0
			}
			if b >= 0 && b < buckets {
				sum.Series[b].Requests += r.Requests
				if r.Failed {
					sum.Series[b].Failure += r.Requests
				}
				sum.Series[b].Tokens += r.Total
			}
		}
	}

	for i := range todayEvents {
		e := &todayEvents[i]
		if !f.match(e) {
			continue
		}
		sum.Totals.add(e)
		for g, name := range [4]string{e.Model, e.Provider, e.Account, e.KeyID} {
			if name == "" {
				continue
			}
			groupFor(g, name, e.Provider).add(e)
		}
		if !e.Timestamp.Before(first) {
			if b := int(e.Timestamp.Sub(first) / bucket); b >= 0 && b < buckets {
				sum.Series[b].Requests++
				if e.Failed {
					sum.Series[b].Failure++
				}
				sum.Series[b].Tokens += deref(e.Tokens.Total)
			}
		}
	}

	sum.Totals.finish()
	out := [4][]Group{}
	for g := range groups {
		out[g] = []Group{}
		for _, grp := range groups[g] {
			grp.finish()
			out[g] = append(out[g], *grp)
		}
		slices.SortFunc(out[g], func(a, b Group) int {
			return cmp.Or(cmp.Compare(b.Requests, a.Requests), cmp.Compare(a.Name, b.Name))
		})
	}
	sum.Models, sum.Providers, sum.Accounts, sum.Keys = out[0], out[1], out[2], out[3]
	return sum
}

func (f Filter) matchSummaryRow(r summaryRow, hourStart, hourEnd time.Time) bool {
	return (f.Since.IsZero() || hourEnd.After(f.Since)) &&
		(f.Until.IsZero() || hourStart.Before(f.Until)) &&
		(f.Model == "" || f.Model == r.Model) &&
		(f.Provider == "" || f.Provider == r.Provider) &&
		(f.Account == "" || f.Account == r.Account) &&
		(f.KeyID == "" || f.KeyID == r.KeyID) &&
		(f.Failed == nil || *f.Failed == r.Failed)
}

func resolveSeriesWindow(f Filter, nowUTC time.Time, oldest *time.Time, bucket time.Duration, buckets int) (time.Duration, int, time.Time) {
	if f.Since.IsZero() && f.Until.IsZero() {
		if bucket <= 0 {
			bucket = time.Minute
		}
		if buckets <= 0 {
			buckets = 60
		}
		last := nowUTC.Truncate(bucket)
		return bucket, buckets, last.Add(-time.Duration(buckets-1) * bucket)
	}
	end := f.Until.UTC()
	if end.IsZero() {
		end = nowUTC
	}
	start := f.Since.UTC()
	if start.IsZero() && oldest != nil {
		start = oldest.UTC()
	}
	if start.IsZero() || start.After(end) {
		start = end
	}
	if bucket <= 0 {
		maxHourly := 48 * time.Hour
		if f.Until.IsZero() {
			maxHourly += 500 * time.Millisecond
		}
		if end.Sub(start) <= maxHourly {
			bucket = time.Hour
		} else {
			bucket = 24 * time.Hour
		}
	}
	if buckets <= 0 {
		lastTime := end
		if !f.Until.IsZero() && end.After(start) {
			lastTime = end.Add(-time.Nanosecond)
		}
		first := start.Truncate(bucket)
		last := lastTime.Truncate(bucket)
		if last.Before(first) {
			last = first
		}
		n := int(last.Sub(first)/bucket) + 1
		if n > maxSeriesBuckets {
			n = maxSeriesBuckets
			first = last.Add(-time.Duration(n-1) * bucket)
		}
		return bucket, n, first
	}
	last := end.Truncate(bucket)
	return bucket, buckets, last.Add(-time.Duration(buckets-1) * bucket)
}
