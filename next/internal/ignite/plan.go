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

const readEvery = time.Hour

const (
	StateRunning  = "running"
	StateWaiting  = "waiting"
	StateStarting = "starting"
	StatePaused   = "paused"
)

type Ref struct {
	ID, Extension string
}

type Health struct {
	OK    bool
	Until time.Time
	Why   string
}

type Fire struct {
	Window Window
	Model  string
}

type Failure struct {
	Message string
	Hard    bool
}

type Planner struct {
	mu       sync.Mutex
	cfg      Config
	accounts map[string]*plan
}

type plan struct {
	ext     string
	next    time.Time
	err     string
	windows map[string]*windowState
}

type windowState struct {
	state    string
	next     time.Time
	failures int
	retryAt  time.Time
	paused   time.Time
	firedAt  time.Time
	model    string
	lastAt   time.Time
	lastErr  string
	why      string
}

func NewPlanner() *Planner { return &Planner{accounts: map[string]*plan{}} }

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
			for _, ws := range pl.windows {
				ws.failures, ws.retryAt, ws.paused = 0, time.Time{}, time.Time{}
			}
		}
	}
	for id := range p.accounts {
		if !keep[id] {
			delete(p.accounts, id)
		}
	}
}

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

func (p *Planner) NoReading(id, why string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pl := p.accounts[id]; pl != nil {
		pl.next, pl.err = time.Time{}, why
	}
}

func (p *Planner) ReadFailed(id, why string, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pl := p.accounts[id]; pl != nil {
		pl.next, pl.err = now.Add(p.cfg.retry()), why
	}
}

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
		ws.state, ws.next, ws.why = StateStarting, time.Time{}, ""
		fire = append(fire, Fire{Window: w, Model: c.Model(id, pl.ext, w.ID)})
	}
	for wid := range pl.windows {
		if !seen[wid] {
			delete(pl.windows, wid)
		}
	}
	if next.IsZero() && len(fire) == 0 {
		next = now.Add(readEvery)
	}
	pl.next = next
	return fire
}

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

type View struct {
	Error   string       `json:"error,omitempty"`
	Windows []WindowView `json:"windows"`
}

type WindowView struct {
	ID       string     `json:"id"`
	State    string     `json:"state"`
	Next     *time.Time `json:"next,omitempty"`
	Why      string     `json:"why,omitempty"`
	Failures int        `json:"failures,omitempty"`
	Last     *time.Time `json:"last,omitempty"`
	Model    string     `json:"model,omitempty"`
	Error    string     `json:"error,omitempty"`
}

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
