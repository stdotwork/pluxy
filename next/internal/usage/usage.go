// Package usage keeps the record of the requests core served: one typed event per client
// request, with what it used. An in-memory ring holds the recent window within limits; when
// opened with a directory, events are also appended asynchronously to one JSONL file per UTC
// day and refilled at start from today's and yesterday's files. Reading the record never
// removes anything from it.
package usage

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Defaults and bounds of the store's limits.
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

// Tokens is what one request used, as its upstream reported it. A field that is absent was not
// reported, which is different from zero.
type Tokens struct {
	// Input is every input token, cached ones included.
	Input *int64 `json:"input_tokens,omitempty"`
	// Output is every output token, reasoning included.
	Output *int64 `json:"output_tokens,omitempty"`
	// Reasoning is the output tokens spent on reasoning.
	Reasoning *int64 `json:"reasoning_tokens,omitempty"`
	// Cached is the input tokens read from a cache.
	Cached *int64 `json:"cached_tokens,omitempty"`
	// CacheWrite is the input tokens written to a cache.
	CacheWrite *int64 `json:"cache_creation_tokens,omitempty"`
	// Total is input plus output, when either was reported.
	Total *int64 `json:"total_tokens,omitempty"`
}

// TokensOf returns the tokens u reports; nil u reports none.
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

// Event is one client request core served, as the usage record keeps it. It never holds a
// client key or a credential: KeyID names the client key without revealing it.
type Event struct {
	// Seq numbers events in the order they were recorded, from 1; a reader passes the last one
	// it has as after to read only newer ones.
	Seq uint64 `json:"seq"`
	// ID is the request's trace ID (the X-CPA-TRACE-ID reply header), or empty when the request
	// never reached an account.
	ID string `json:"id,omitempty"`
	// Timestamp is when the request arrived.
	Timestamp time.Time `json:"timestamp"`
	// Endpoint is the client API path, such as /v1/chat/completions.
	Endpoint string `json:"endpoint"`
	Stream   bool   `json:"stream"`
	// Model is the model as the client asked for it; UpstreamModel the account's name for it,
	// when that differs (an alias, a prefix); ServedModel the model the upstream reply says
	// served it, when the reply names one.
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	ServedModel   string `json:"served_model,omitempty"`
	// Provider is the extension the last attempt went to, Account its account.
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	// ChainStep is the step of the model's fallback chain the last attempt was for, from 1;
	// absent for a model with no chain.
	ChainStep int `json:"chain_step,omitempty"`
	// KeyID identifies the client key the request came with, as the management API lists client
	// keys; empty with no client keys set.
	KeyID string `json:"key_id,omitempty"`
	// ClientIP is the address the request came from: the connection's peer, or the client a
	// trusted proxy names (trusted_proxies).
	ClientIP string `json:"client_ip,omitempty"`
	// Failed is true when the request did not end with a complete reply.
	Failed bool `json:"failed"`
	// Status is the HTTP status of the reply; absent when none was sent (the client went away
	// first). A streamed reply that fails after it began keeps its 200, with Failed set.
	Status int `json:"status,omitempty"`
	// ErrorCode is the error's code when the request failed, such as rate_limit or cancelled.
	ErrorCode string `json:"error_code,omitempty"`
	// LatencyMS is how long the request took, from arrival to its last event.
	LatencyMS int64  `json:"latency_ms"`
	Tokens    Tokens `json:"tokens"`

	// recorded is when the store added the event. Events are kept in the order they were
	// recorded, so the age limit is measured from it.
	recorded time.Time
}

// Filter selects events. A zero field selects every event.
type Filter struct {
	// Since and Until bound Timestamp: Since included, Until excluded.
	Since, Until time.Time
	Model        string
	Provider     string
	Account      string
	KeyID        string
	// Failed, when set, selects failed (true) or successful (false) events.
	Failed *bool
}

func (f Filter) match(e *Event) bool {
	return (f.Since.IsZero() || !e.Timestamp.Before(f.Since)) && (f.Until.IsZero() || e.Timestamp.Before(f.Until)) &&
		(f.Model == "" || f.Model == e.Model) && (f.Provider == "" || f.Provider == e.Provider) &&
		(f.Account == "" || f.Account == e.Account) && (f.KeyID == "" || f.KeyID == e.KeyID) &&
		(f.Failed == nil || *f.Failed == e.Failed)
}

// Store is the usage record: a ring of at most its maximum number of events, none recorded longer
// ago than its age limit. It is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	ring      []Event // capacity maxEvents; events in order from head, n of them
	head, n   int
	seq       uint64
	evicted   uint64 // events pushed out by the limits, ever
	maxEvents int
	maxAge    time.Duration
	now       func() time.Time

	dir             string
	keepDays        atomic.Int64
	dropped         atomic.Uint64
	writeCh         chan Event
	flushCh         chan chan struct{}
	pruneCh         chan struct{}
	done            chan struct{}
	closed          bool
	closeTimeout    time.Duration
	writeLine       func(day string, line []byte) error // test hook; nil writes to dir
	onReadRaw       func(day string)                    // test hook; called when opening a raw .jsonl file
	openSeq         uint64
	queuedSeq       atomic.Uint64
	writtenSeq      atomic.Uint64
	closedQueuedSeq atomic.Uint64
	incompleteDay   map[string]bool

	sumMu     sync.Mutex
	summaries map[string]*daySummaryFile
}

// NewStore returns an empty, in-memory-only store with the default limits.
func NewStore() *Store {
	return &Store{maxEvents: DefaultMaxEvents, maxAge: DefaultMaxAge, now: time.Now}
}

// SetLimits changes the store's limits; zero values take the defaults. Lowering a limit drops the
// oldest events at once.
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
		// The ring grows as events arrive (append below), so a large limit costs nothing until
		// it is used.
		s.ring = append(make([]Event, 0, min(maxEvents, max(len(kept), 64))), kept...)
		s.head, s.n = 0, len(kept)
	}
	s.maxEvents, s.maxAge = maxEvents, maxAge
	s.expire()
}

// events returns the events in order, as a new slice. s.mu is held.
func (s *Store) events() []Event {
	out := make([]Event, 0, s.n)
	for i := range s.n {
		out = append(out, s.ring[(s.head+i)%len(s.ring)])
	}
	return out
}

// expire drops events recorded longer ago than the age limit. Events are kept in the order they
// were recorded, so these are at the front. s.mu is held.
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

// Add records e, numbering it, and returns its Seq. When backed by a directory, e is queued
// without blocking for the disk writer; if the queue is full, e is dropped from disk and
// counted in Summary.Dropped.
func (s *Store) Add(e Event) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	s.seq++
	e.Seq = s.seq
	// A clock set back must not put an event behind one recorded before it.
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

// appendRing inserts e at the end of the ring. s.mu is held.
func (s *Store) appendRing(e Event, countEviction bool) {
	switch {
	case s.n < len(s.ring):
		s.ring[(s.head+s.n)%len(s.ring)] = e
		s.n++
	case len(s.ring) < s.maxEvents:
		// Grow: put the events in order first, so the ring's order survives the append.
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

// Page is a page of events.
type Page struct {
	Events []Event `json:"events"`
	// Next is the Seq to pass as after for the next page: the last event's, or after itself when
	// the page is empty.
	Next uint64 `json:"next"`
	// More is true when newer matching events are kept beyond this page.
	More bool `json:"more"`
	// Lost is true when events newer than after were pushed out before this read: a reader that
	// polls with after missed some.
	Lost bool `json:"lost"`
}

// Events returns, oldest first, up to limit events that match f with Seq above after.
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

// EventsBefore returns, newest first, up to limit events that match f with Seq below before
// (or all kept events when before is 0).
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

// Totals is what a set of events adds up to.
type Totals struct {
	Requests int64 `json:"total_requests"`
	Success  int64 `json:"success_count"`
	Failure  int64 `json:"failure_count"`
	// SuccessRate is Success over Requests, from 0 to 1; 0 with no requests.
	SuccessRate float64 `json:"success_rate"`
	// AverageLatencyMS is the mean latency; 0 with no requests.
	AverageLatencyMS int64       `json:"average_latency_ms"`
	Tokens           TokenTotals `json:"tokens"`

	latency int64
}

// TokenTotals adds up the tokens events reported; one that reported none adds nothing.
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
	t.latency += e.LatencyMS
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
	t.latency += r.LatencyMS
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
		t.AverageLatencyMS = t.latency / t.Requests
	}
}

// Group is the totals of the events that share one model, provider, account or client key.
type Group struct {
	Name string `json:"name"`
	// Provider is an account's extension (in the accounts breakdown only).
	Provider string `json:"provider,omitempty"`
	Totals
}

// Bucket is the totals of the events that arrived within one step of the time series.
type Bucket struct {
	Start    time.Time `json:"start"`
	Requests int64     `json:"requests"`
	Failure  int64     `json:"failure_count"`
	Tokens   int64     `json:"total_tokens"`
}

// Summary is what the events that match a filter add up to, with breakdowns and a time series.
type Summary struct {
	Totals
	By        string  `json:"by,omitempty"`
	Breakdown []Group `json:"breakdown,omitzero"`
	Models    []Group `json:"models"`
	Providers []Group `json:"providers"`
	Accounts  []Group `json:"accounts"`
	Keys      []Group `json:"keys"`
	// Series counts the events in buckets of BucketSeconds, oldest first, ending with the
	// bucket that holds now; buckets with no events are included.
	BucketSeconds int64    `json:"bucket_seconds"`
	Series        []Bucket `json:"series"`
	// Oldest is the arrival time of the oldest event the store keeps, matching or not; absent
	// when it keeps none. Events before it were pushed out by the limits or never recorded.
	Oldest *time.Time `json:"oldest,omitempty"`
	// Evicted counts the events the limits have pushed out since core started.
	Evicted uint64 `json:"evicted"`
	// Dropped counts the events dropped since core started because the disk writer's queue was full.
	Dropped uint64 `json:"dropped"`
}

// Summarize adds up the events that match f, and counts them in a time series of buckets steps
// of bucket. When bucket or buckets is 0, Summarize picks defaults for the range: 1m / 60 when no
// range is given, else 1h for ranges up to 48 hours and 24h beyond, spanning the range.
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
			if !hourStart.Before(first) {
				if b := int(hourStart.Sub(first) / bucket); b >= 0 && b < buckets {
					sum.Series[b].Requests += r.Requests
					if r.Failed {
						sum.Series[b].Failure += r.Requests
					}
					sum.Series[b].Tokens += r.Total
				}
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
		// Most requests first, then by name, so the order is stable.
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
			// HTTP callers compute f.Since from time.Now() moments before Summarize reads s.now().
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
