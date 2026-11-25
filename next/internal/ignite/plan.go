package ignite

import (
	"maps"
	"math"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// readEvery is how long after a reading with nothing to wait for (no window to start) the
// account is read again; a window that runs is read again once it resets, and one that waits
// when its wait ends.
const readEvery = time.Hour

// Window states, as View shows them.
const (
	// StateRunning: the window runs; it is started again once it resets.
	StateRunning = "running"
	// StateWaiting: the window has reset and is started at Next: when the hours start, the
	// account can serve again, a full window resets or a retry is due.
	StateWaiting = "waiting"
	// StateStarting: the request was sent; the next reading shows whether the window started.
	StateStarting = "starting"
	// StatePaused: failures paused the window's ignition until Next, the next start of the hours.
	StatePaused = "paused"
)

// Ref names an account ignition runs for.
type Ref struct {
	ID, Extension string
}

// Health is what Plan needs to know of an account's routing state: whether it can serve now, and
// else until when it cannot (zero: not known).
type Health struct {
	OK    bool
	Until time.Time
	Why   string
}

// Fire is a window Plan says to start now, with the model the settings name for it ("" for a
// small one the account serves).
type Fire struct {
	Window Window
	Model  string
}

// Failure is why an ignition request failed. Hard is true for a failure retrying soon will not
// mend, such as a refused key or a model the account does not have: it pauses the window at once.
type Failure struct {
	Message string
	Hard    bool
}

// Planner keeps, for each account ignition runs for, when to read its usage next and the state
// of each window it starts. Core reads the accounts Due names, passes each reading to Plan, sends
// the requests Plan returns and passes each outcome to Done. Its methods take the time, so a test
// drives it with a clock of its own; they are safe to call from several goroutines.
type Planner struct {
	mu       sync.Mutex
	cfg      Config
	accounts map[string]*plan
}

type plan struct {
	ext     string
	next    time.Time // when the account is read next; zero: never (it has no reading)
	err     string    // why the last reading could not be made
	windows map[string]*windowState
}

type windowState struct {
	state    string
	next     time.Time // when the window is (or would be) started next
	failures int       // failures in a row
	retryAt  time.Time // no ignition before then: a retry's wait
	paused   time.Time // no ignition before then: failures paused it
	firedAt  time.Time // when a request that succeeded was sent, until a reading shows the window started
	model    string    // the model of the last request
	lastAt   time.Time // when the last request was sent
	lastErr  string    // why the last request, or the window, failed; empty after a success
	why      string    // what the window waits for
}

// NewPlanner returns a planner with no accounts.
func NewPlanner() *Planner { return &Planner{accounts: map[string]*plan{}} }

// Set makes cfg the settings and accounts (the accounts in service) the accounts ignition may
// run for: those cfg turns on are kept or added, the others dropped. An added account is read at
// now; when the settings changed, every account is read at now, so they apply at once.
func (p *Planner) Set(cfg *Config, accounts []Ref, now time.Time) {
	var c Config
	if cfg != nil {
		c = *cfg
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := !reflect.DeepEqual(c, p.cfg)
	p.cfg = c
	keep := map[string]bool{}
	for _, a := range accounts {
		if !c.AccountOn(a.ID, a.Extension) {
			continue
		}
		keep[a.ID] = true
		pl := p.accounts[a.ID]
		if pl == nil || pl.ext != a.Extension {
			p.accounts[a.ID] = &plan{ext: a.Extension, next: now, windows: map[string]*windowState{}}
			continue
		}
		if changed {
			pl.next = now
		}
	}
	for id := range p.accounts {
		if !keep[id] {
			delete(p.accounts, id)
		}
	}
}

// Due returns the accounts to read at now, in ID order.
func (p *Planner) Due(now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for id, pl := range p.accounts {
		if !pl.next.IsZero() && !pl.next.After(now) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Next returns when the planner next has an account to read; zero when it has none.
func (p *Planner) Next() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	var t time.Time
	for _, pl := range p.accounts {
		if !pl.next.IsZero() && (t.IsZero() || pl.next.Before(t)) {
			t = pl.next
		}
	}
	return t
}

// NoReading records that account id's provider has no usage reading (an API key, an extension
// without the quota call): it is not read again until the settings or the accounts change.
func (p *Planner) NoReading(id, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pl := p.accounts[id]; pl != nil {
		pl.next, pl.err = time.Time{}, why
	}
}

// ReadFailed records that account id's usage could not be read: it is read again a retry's wait
// later.
func (p *Planner) ReadFailed(id, why string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pl := p.accounts[id]; pl != nil {
		pl.next, pl.err = now.Add(p.cfg.retry()), why
	}
}

// Plan decides, from a reading q of account id's usage taken at now and the account's health,
// which of its windows to start now, and when to read it next. A window is started only when it
// is not running (it has reset, or never started), within the hours, when the account can serve,
// when no other window that covers the same requests is full, and when no retry's wait or pause
// holds it. A window still not running a hold after an ignition that succeeded counts as a
// failed ignition.
func (p *Planner) Plan(id string, q *protocol.Quota, h Health, now time.Time) []Fire {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.accounts[id]
	if pl == nil || q == nil {
		return nil
	}
	c := &p.cfg
	pl.err = ""
	var next time.Time
	wake := func(t time.Time) {
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	seen := map[string]bool{}
	var fire []Fire
	for _, w := range Windows(q.Limits, now) {
		if !c.windowOn(pl.ext, w.ID) {
			continue
		}
		seen[w.ID] = true
		ws := pl.windows[w.ID]
		if ws == nil {
			ws = &windowState{}
			pl.windows[w.ID] = ws
		}
		if w.Running {
			// Started, by an ignition or by a request: whatever failed before is over.
			ws.failures, ws.retryAt, ws.paused, ws.firedAt, ws.lastErr = 0, time.Time{}, time.Time{}, time.Time{}, ""
			ws.state, ws.why = StateRunning, ""
			t := w.Reset.Add(c.grace())
			ws.next = c.InHours(t)
			wake(t)
			continue
		}
		if !ws.firedAt.IsZero() {
			if at := ws.firedAt.Add(c.hold()); now.Before(at) {
				wake(at)
				continue
			}
			ws.firedAt = time.Time{}
			p.fail(ws, Failure{Message: "the window had not started " + c.hold().String() + " after the request"}, now)
		}
		t, why := now, ""
		later := func(u time.Time, reason string) {
			if u.After(t) {
				t, why = u, reason
			}
		}
		later(ws.retryAt, "a retry after a failure")
		later(ws.paused, "the next start of the hours, after failures paused it")
		if !w.BlockedUntil.IsZero() {
			later(w.BlockedUntil.Add(c.grace()), "the reset of a full usage window that covers the same requests")
		}
		if !h.OK {
			u := h.Until
			if u.IsZero() {
				u = now.Add(c.retry())
			} else {
				u = u.Add(c.grace())
			}
			later(u, "the account to serve again: "+h.Why)
		}
		if in := c.InHours(t); in.After(t) {
			t, why = in, "the start of the hours"
		}
		ws.next, ws.why = t, why
		ws.state = StateWaiting
		if !ws.paused.IsZero() && !ws.paused.Before(t) {
			ws.state = StatePaused
		}
		if t.After(now) {
			wake(t)
			continue
		}
		fire = append(fire, Fire{Window: w, Model: c.Model(id, pl.ext, w.ID)})
	}
	for wid := range pl.windows {
		if !seen[wid] {
			delete(pl.windows, wid)
		}
	}
	if next.IsZero() && len(fire) == 0 {
		// Nothing to wait for: no window to start.
		next = now.Add(readEvery)
	}
	// With every window started now, next stays zero until Done sets when to read again.
	pl.next = next
	return fire
}

// Done records the outcome of the ignition request of window w of account id, sent at now with
// model; f is nil when it succeeded. After a success the window is read again a hold later, to
// see that it started; after a failure, a retry's wait later, the wait growing by the multiplier
// with each failure in a row; the failure that reaches the limit, or a hard one, pauses the
// window until the next start of the hours.
func (p *Planner) Done(id, w, model string, f *Failure, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.accounts[id]
	if pl == nil {
		return
	}
	ws := pl.windows[w]
	if ws == nil {
		ws = &windowState{}
		pl.windows[w] = ws
	}
	ws.lastAt, ws.model = now, model
	var t time.Time
	if f == nil {
		ws.firedAt, ws.lastErr, ws.state, ws.why = now, "", StateStarting, ""
		t = now.Add(p.cfg.hold())
		ws.next = time.Time{}
	} else {
		t = p.fail(ws, *f, now)
	}
	if pl.next.IsZero() || t.Before(pl.next) {
		pl.next = t
	}
}

// fail counts a failed ignition of ws at now and returns when to read the window again.
func (p *Planner) fail(ws *windowState, f Failure, now time.Time) time.Time {
	c := &p.cfg
	ws.failures++
	ws.lastErr = f.Message
	if f.Hard || ws.failures >= c.maxFailures() {
		ws.failures, ws.retryAt = 0, time.Time{}
		ws.paused = c.nextStart(now)
		ws.state, ws.next, ws.why = StatePaused, ws.paused, "the next start of the hours, after failures paused it"
		return ws.paused
	}
	wait := time.Duration(float64(c.retry()) * math.Pow(c.backoffMultiplier(), float64(ws.failures-1)))
	ws.retryAt = now.Add(wait)
	ws.state, ws.next, ws.why = StateWaiting, c.InHours(ws.retryAt), "a retry after a failure"
	return ws.retryAt
}

// View is an account's ignition as the management API shows it.
type View struct {
	// Error is why the account's usage could not be read, or why it has no reading.
	Error   string       `json:"error,omitempty"`
	Windows []WindowView `json:"windows"`
}

// WindowView is one window's ignition.
type WindowView struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// Next is when core starts the window next (when it waits, or once it resets); absent while
	// a request it sent is being checked.
	Next *time.Time `json:"next,omitempty"`
	// Why says what the window waits for.
	Why string `json:"why,omitempty"`
	// Failures is the failed ignitions in a row.
	Failures int `json:"failures,omitempty"`
	// Last is when the last ignition request was sent, Model the model it named and Error why it
	// failed (or why the window did not start after it).
	Last  *time.Time `json:"last,omitempty"`
	Model string     `json:"model,omitempty"`
	Error string     `json:"error,omitempty"`
}

// View returns account id's ignition, or nil when ignition does not run for it.
func (p *Planner) View(id string) *View {
	p.mu.Lock()
	defer p.mu.Unlock()
	pl := p.accounts[id]
	if pl == nil {
		return nil
	}
	v := &View{Error: pl.err, Windows: []WindowView{}}
	for _, wid := range slices.Sorted(maps.Keys(pl.windows)) {
		ws := pl.windows[wid]
		wv := WindowView{ID: wid, State: ws.state, Why: ws.why, Failures: ws.failures, Model: ws.model, Error: ws.lastErr}
		if !ws.next.IsZero() {
			t := ws.next.UTC()
			wv.Next = &t
		}
		if !ws.lastAt.IsZero() {
			t := ws.lastAt.UTC()
			wv.Last = &t
		}
		v.Windows = append(v.Windows, wv)
	}
	return v
}
