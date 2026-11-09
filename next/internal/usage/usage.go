// Package usage keeps a bounded, in-memory record of the requests core served: one typed event
// per client request, with what it used. Reading the record never removes anything from it;
// events leave only when the store is full or they are older than its age limit. Nothing is kept
// across restarts.
package usage

import (
	"cmp"
	"slices"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Defaults and bounds of the store's limits.
const (
	DefaultMaxEvents = 10000
	MaxMaxEvents     = 200000
	DefaultMaxAge    = 24 * time.Hour
	MaxMaxAge        = 30 * 24 * time.Hour
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
	// when that differs (an alias, a prefix).
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model,omitempty"`
	// Provider is the extension the last attempt went to, Account its account.
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
	// KeyID identifies the client key the request came with, as the management API lists client
	// keys; empty with no client keys set.
	KeyID string `json:"key_id,omitempty"`
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

// Store is the usage record: a ring of at most its maximum number of events, none older than its
// age limit. It is safe for concurrent use.
type Store struct {
	mu        sync.Mutex
	ring      []Event // capacity maxEvents; events in order from head, n of them
	head, n   int
	seq       uint64
	evicted   uint64 // events pushed out by the limits, ever
	maxEvents int
	maxAge    time.Duration
	now       func() time.Time
}

// NewStore returns an empty store with the default limits.
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
			s.evicted += uint64(len(kept) - maxEvents)
			kept = kept[len(kept)-maxEvents:]
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

// expire drops events older than the age limit from the front. Events are kept in the order
// they ended, which is about the order they arrived, so this stops at the first event inside
// the limit. s.mu is held.
func (s *Store) expire() {
	cutoff := s.now().Add(-s.maxAge)
	for s.n > 0 && s.ring[s.head].Timestamp.Before(cutoff) {
		s.ring[s.head] = Event{}
		s.head = (s.head + 1) % len(s.ring)
		s.n--
		s.evicted++
	}
}

// Add records e, numbering it, and returns its Seq.
func (s *Store) Add(e Event) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	s.seq++
	e.Seq = s.seq
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
		s.ring[s.head] = e
		s.head = (s.head + 1) % len(s.ring)
		s.evicted++
	}
	return e.Seq
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
		p.Lost = s.ring[s.head].Seq > after+1
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
}

// Summarize adds up the events that match f, and counts them over the last buckets steps of
// bucket for the time series.
func (s *Store) Summarize(f Filter, bucket time.Duration, buckets int) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	now := s.now()
	sum := Summary{BucketSeconds: int64(bucket / time.Second), Series: make([]Bucket, buckets), Evicted: s.evicted}
	// Buckets start at whole multiples of the step, so a chart polled again lines up.
	last := now.Truncate(bucket)
	first := last.Add(-time.Duration(buckets-1) * bucket)
	for i := range sum.Series {
		sum.Series[i].Start = first.Add(time.Duration(i) * bucket).UTC()
	}
	groups := [4]map[string]*Group{{}, {}, {}, {}}
	for i := range s.n {
		e := &s.ring[(s.head+i)%len(s.ring)]
		if i == 0 || e.Timestamp.Before(*sum.Oldest) {
			t := e.Timestamp
			sum.Oldest = &t
		}
		if !f.match(e) {
			continue
		}
		sum.Totals.add(e)
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
			if b := int(e.Timestamp.Sub(first) / bucket); b < buckets {
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
