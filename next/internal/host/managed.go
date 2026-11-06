package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// States of a managed extension generation.
const (
	// StateStarting: the program is starting and has not finished its handshake.
	StateStarting = "starting"
	// StateActive: the program runs and takes calls.
	StateActive = "active"
	// StateIdle: the program was not needed for a while and was stopped; the next call starts it.
	StateIdle = "idle"
	// StateCrashed: the program exited on its own or failed to start; it is restarted after a
	// backoff.
	StateCrashed = "crashed"
	// StateQuarantined: the program crashed too often in a row and is not restarted until a
	// config reload prepares a new generation.
	StateQuarantined = "quarantined"
	// StateDraining: a newer generation took over; this one finishes the requests that started
	// on it, until its drain deadline.
	StateDraining = "draining"
	// StateStopped: the generation is finished and takes no calls.
	StateStopped = "stopped"
)

// Policy says how a managed extension is run. Zero durations mean "never" where noted.
type Policy struct {
	// IdleStop stops the program after it has had no request for this long; the next request
	// starts it again. 0 keeps it running.
	IdleStop time.Duration
	// DrainTimeout bounds how long a replaced generation waits for its requests to finish
	// before its program is stopped.
	DrainTimeout time.Duration
	// RestartBackoff is the wait before the first restart after a crash; it doubles with each
	// crash in a row, up to RestartBackoffMax.
	RestartBackoff    time.Duration
	RestartBackoffMax time.Duration
	// QuarantineAfter is how many crashes in a row quarantine the extension.
	QuarantineAfter int
	// StableAfter: a program that ran this long before it crashed starts a new count of
	// crashes in a row.
	StableAfter time.Duration
}

// DefaultPolicy is the policy for fields the config leaves out.
//
// Restarts: 1s doubling to 1 minute, quarantine after 5 crashes in a row, a run of 10 minutes
// resets the count. Draining waits up to 10 minutes, long enough for a long model reply.
// Idle stop after 10 minutes without requests.
func DefaultPolicy() Policy {
	return Policy{
		IdleStop:          10 * time.Minute,
		DrainTimeout:      10 * time.Minute,
		RestartBackoff:    time.Second,
		RestartBackoffMax: time.Minute,
		QuarantineAfter:   5,
		StableAfter:       10 * time.Minute,
	}
}

// Fingerprint identifies an extension generation's code: a digest of its manifest and of the
// program it runs on this platform. Two configs naming the same manifest with the same
// fingerprint can share one running generation.
func Fingerprint(manifestPath string) (string, error) {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return "", err
	}
	prog, err := m.Program()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, p := range []string{manifestPath, prog} {
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%d:", len(b))
		h.Write(b)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Managed is one generation of an extension: one manifest and program, run while it is needed.
// It starts the program on demand, stops it when idle, restarts it with backoff when it
// crashes, quarantines it when it keeps crashing, and drains it when a newer generation takes
// over.
//
// Callers hold a lease (Acquire, Release) for the whole of each request that may call the
// extension; Generate, Login and Refresh must only be called while holding one. A lease keeps a
// replaced generation from finishing its drain; only running calls keep the program from its
// idle stop, so a request may hold leases on several extensions and use one.
type Managed struct {
	path        string
	fingerprint string
	manifest    *Manifest
	info        protocol.Info // from the first handshake
	generation  uint64
	opts        Options
	logf        func(format string, args ...any)

	mu        sync.Mutex
	policy    Policy
	ext       *Extension
	state     string
	starting  chan struct{} // closed when the start in progress ends
	leases    int           // held by requests (Acquire)
	calls     int           // calls running on the program (Generate until its stream closes, Login, Refresh)
	idleSeq   uint64
	idle      *time.Timer
	restart   *time.Timer
	crashes   int // in a row
	restarts  int
	lastErr   string
	retryAt   time.Time
	startedAt time.Time
	draining  bool
	drained   chan struct{} // closed when a draining generation has no leases left
	retired   bool
	stopping  map[*Extension]bool // programs asked to stop that may not have exited yet
}

// Prepare starts a new generation of the extension at manifestPath and waits for its handshake,
// so a config that names a broken extension is refused before it is published. generation
// labels it in status and logs.
func Prepare(manifestPath string, generation uint64, policy Policy, opts Options) (*Managed, error) {
	fp, err := Fingerprint(manifestPath)
	if err != nil {
		return nil, err
	}
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	g := &Managed{path: manifestPath, fingerprint: fp, manifest: m, generation: generation, opts: opts,
		logf: log.Printf, policy: policy, state: StateStarting, stopping: map[*Extension]bool{}}
	ext, err := Start(manifestPath, opts)
	if err != nil {
		return nil, err
	}
	g.info = ext.Info()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running(ext, false)
	return g, nil
}

// Path returns the manifest path the generation was prepared from.
func (g *Managed) Path() string { return g.path }

// Fingerprint returns the generation's fingerprint (see Fingerprint).
func (g *Managed) Fingerprint() string { return g.fingerprint }

// Manifest returns the generation's manifest.
func (g *Managed) Manifest() *Manifest { return g.manifest }

// Info returns what the program said about itself in its first handshake.
func (g *Managed) Info() protocol.Info { return g.info }

// State returns the generation's state.
func (g *Managed) State() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}

// SetPolicy changes the policy of a running generation.
func (g *Managed) SetPolicy(p Policy) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.policy = p
	g.armIdle()
}

// Acquire takes a lease for one request. It returns false once a newer generation has taken
// over (the generation drains or has stopped); the caller then uses the newer one.
func (g *Managed) Acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired || g.draining {
		return false
	}
	g.leases++
	return true
}

// Release returns a lease taken by Acquire.
func (g *Managed) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.leases--
	if g.leases == 0 && g.draining && g.drained != nil {
		close(g.drained)
		g.drained = nil
	}
}

// beginCall counts a call from before its program is started, so an idle stop cannot take the
// program away under it; the returned func ends the call once.
func (g *Managed) beginCall() func() {
	g.mu.Lock()
	g.calls++
	g.idleSeq++ // cancels a pending idle stop
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.calls--
			g.armIdle()
		})
	}
}

// Generate starts a generate call, starting the program first if it is idle. The call runs
// until its stream is closed.
func (g *Managed) Generate(ctx context.Context, account protocol.Account, req llm.Request) *Stream {
	end := g.beginCall()
	ext, e := g.ensure(ctx)
	if e != nil {
		end()
		s := &Stream{}
		s.fail(e)
		return s
	}
	s := ext.Generate(ctx, account, req)
	s.onClose = end
	return s
}

// Login starts a login call, starting the program first if it is idle. The caller holds a
// lease until the call is closed.
func (g *Managed) Login(ctx context.Context, account protocol.Account, login protocol.Login) *LoginCall {
	end := g.beginCall()
	ext, e := g.ensure(ctx)
	if e != nil {
		end()
		l := &LoginCall{ctx: ctx}
		l.fail(e)
		return l
	}
	l := ext.Login(ctx, account, login)
	l.onClose = end
	return l
}

// Refresh runs a refresh call, starting the program first if it is idle. The caller holds a
// lease for the call.
func (g *Managed) Refresh(ctx context.Context, account protocol.Account) (*protocol.Credential, *llm.Error) {
	defer g.beginCall()()
	ext, e := g.ensure(ctx)
	if e != nil {
		return nil, e
	}
	return ext.Refresh(ctx, account)
}

func (g *Managed) unavailable(msg string, args ...any) *llm.Error {
	return &llm.Error{Code: llm.CodeExtensionUnavailable,
		Message: fmt.Sprintf("extension %s %s: ", g.manifest.ID, g.manifest.Version) + fmt.Sprintf(msg, args...)}
}

// ensure returns the running program, starting it when it is idle or its restart is due.
func (g *Managed) ensure(ctx context.Context) (*Extension, *llm.Error) {
	for {
		g.mu.Lock()
		switch {
		case g.ext != nil:
			ext := g.ext
			g.mu.Unlock()
			return ext, nil
		case g.retired || g.state == StateStopped:
			g.mu.Unlock()
			return nil, g.unavailable("this generation has stopped")
		case g.state == StateQuarantined:
			e := g.unavailable("quarantined after %d crashes in a row (last: %s); fix it and reload the config", g.crashes, g.lastErr)
			g.mu.Unlock()
			return nil, e
		case g.starting != nil:
			ch := g.starting
			g.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, &llm.Error{Code: llm.CodeCancelled, Message: "the request was cancelled"}
			}
		case g.state == StateCrashed && time.Now().Before(g.retryAt):
			wait := time.Until(g.retryAt)
			e := g.unavailable("crashed (%s); restarting in %s", g.lastErr, wait.Round(time.Millisecond))
			e.RetryAfterMS = wait.Milliseconds() + 1
			g.mu.Unlock()
			return nil, e
		}
		// Idle, or crashed with its restart due: start it here.
		restart := g.state == StateCrashed
		g.mu.Unlock()
		if !g.start(restart) {
			g.mu.Lock()
			e := g.unavailable("could not start: %s", g.lastErr)
			g.mu.Unlock()
			return nil, e
		}
	}
}

// start starts the program unless a start is already running or none is needed. It reports
// whether the program runs afterwards. A draining generation may still start, for a request
// that began on it while its program was idle.
func (g *Managed) start(restart bool) bool {
	g.mu.Lock()
	if g.ext != nil {
		g.mu.Unlock()
		return true
	}
	if ch := g.starting; ch != nil {
		// Another caller is starting it: wait for that start's result.
		g.mu.Unlock()
		<-ch
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.ext != nil
	}
	if g.retired || g.state == StateQuarantined || g.state == StateStopped {
		g.mu.Unlock()
		return false
	}
	if g.restart != nil {
		g.restart.Stop()
		g.restart = nil
	}
	ch := make(chan struct{})
	g.starting, g.state = ch, StateStarting
	g.mu.Unlock()

	// The generation runs the code it was prepared with, or nothing: files changed in place
	// belong to the next reload's generation.
	var ext *Extension
	fp, err := Fingerprint(g.path)
	if err == nil && fp != g.fingerprint {
		err = fmt.Errorf("extension %s: its files changed since generation %d was prepared; reload the config to use them", g.manifest.ID, g.generation)
	}
	if err == nil {
		ext, err = Start(g.path, g.opts)
	}

	g.mu.Lock()
	if err == nil && g.retired {
		// Stopped for good while starting: Stop waits on ch until this program has exited.
		g.mu.Unlock()
		ext.Stop()
		g.mu.Lock()
	}
	g.starting = nil
	close(ch)
	defer g.mu.Unlock()
	switch {
	case err != nil:
		g.crashed(err.Error(), 0)
		return false
	case g.retired:
		return false
	}
	g.running(ext, restart)
	return true
}

// running records a started program. g.mu is held.
func (g *Managed) running(ext *Extension, restart bool) {
	g.ext, g.state, g.startedAt = ext, StateActive, time.Now()
	if g.draining {
		g.state = StateDraining
	}
	if restart {
		g.restarts++
	}
	go func() {
		<-ext.Exited()
		g.exited(ext)
	}()
	g.armIdle()
}

// exited handles the program's exit. An exit core asked for (idle stop, drain, shutdown) has
// already cleared g.ext; any other exit is a crash.
func (g *Managed) exited(ext *Extension) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ext != ext {
		return
	}
	g.ext = nil
	reason := "exited"
	if err := ext.ExitError(); err != nil {
		reason = "exited: " + err.Error()
	}
	g.crashed(reason, time.Since(g.startedAt))
}

// crashed records a crash or failed start, then schedules a restart or quarantines the
// generation. ran is how long the program ran. g.mu is held.
func (g *Managed) crashed(reason string, ran time.Duration) {
	if ran >= g.policy.StableAfter {
		g.crashes = 0
	}
	g.crashes++
	g.lastErr = reason
	if g.retired || g.draining {
		g.state = StateStopped
		g.logf("extension %s %s (generation %d) %s while draining", g.manifest.ID, g.manifest.Version, g.generation, reason)
		return
	}
	if g.crashes >= max(g.policy.QuarantineAfter, 1) {
		g.state = StateQuarantined
		g.logf("extension %s %s quarantined after %d crashes in a row (last: %s); reload the config to try again",
			g.manifest.ID, g.manifest.Version, g.crashes, reason)
		return
	}
	backoff := g.policy.RestartBackoff
	for i := 1; i < g.crashes && backoff < g.policy.RestartBackoffMax; i++ {
		backoff *= 2
	}
	backoff = min(backoff, g.policy.RestartBackoffMax)
	g.state, g.retryAt = StateCrashed, time.Now().Add(backoff)
	g.logf("extension %s %s %s; restarting in %s", g.manifest.ID, g.manifest.Version, reason, backoff)
	g.restart = time.AfterFunc(backoff, func() { g.start(true) })
}

// armIdle schedules an idle stop when the program runs with no lease. g.mu is held.
func (g *Managed) armIdle() {
	g.idleSeq++
	if g.idle != nil {
		g.idle.Stop()
		g.idle = nil
	}
	if g.policy.IdleStop <= 0 || g.ext == nil || g.calls > 0 || g.draining || g.retired {
		return
	}
	seq := g.idleSeq
	g.idle = time.AfterFunc(g.policy.IdleStop, func() { g.idleStop(seq) })
}

func (g *Managed) idleStop(seq uint64) {
	g.mu.Lock()
	if seq != g.idleSeq || g.calls > 0 || g.ext == nil || g.draining || g.retired {
		g.mu.Unlock()
		return
	}
	ext := g.ext
	g.ext, g.state = nil, StateIdle
	g.stopping[ext] = true
	idle := g.policy.IdleStop
	g.mu.Unlock()
	g.logf("extension %s %s idle for %s; stopped until a request needs it", g.manifest.ID, g.manifest.Version, idle)
	ext.Stop()
	g.mu.Lock()
	delete(g.stopping, ext)
	g.mu.Unlock()
}

// Drain takes the generation out of service. It returns at once, with the generation marked
// draining and taking no new leases; in the background it waits until the requests holding
// leases end, or until the policy's drain timeout, then stops the program and calls done.
func (g *Managed) Drain(done func()) {
	g.mu.Lock()
	if g.retired || g.draining {
		g.mu.Unlock()
		go done()
		return
	}
	g.draining = true
	g.idleSeq++
	if g.restart != nil {
		g.restart.Stop()
		g.restart = nil
	}
	if g.ext != nil || g.leases > 0 {
		g.state = StateDraining
	}
	var drained chan struct{}
	if g.leases > 0 {
		drained = make(chan struct{})
		g.drained = drained
	}
	timeout, leases := g.policy.DrainTimeout, g.leases
	g.mu.Unlock()

	go func() {
		defer done()
		if drained != nil {
			g.logf("extension %s %s (generation %d) draining %d requests", g.manifest.ID, g.manifest.Version, g.generation, leases)
			t := time.NewTimer(timeout)
			select {
			case <-drained:
				t.Stop()
			case <-t.C:
				g.mu.Lock()
				left := g.leases
				g.mu.Unlock()
				g.logf("extension %s %s (generation %d) still had %d requests at its %s drain deadline; stopping it",
					g.manifest.ID, g.manifest.Version, g.generation, left, timeout)
			}
		}
		g.Stop()
	}()
}

// Stop stops the generation for good, without waiting for its requests, and returns once its
// programs have exited, including one an idle stop or another Stop is still stopping.
func (g *Managed) Stop() {
	g.mu.Lock()
	g.retired, g.draining = true, true
	g.idleSeq++
	for _, t := range []**time.Timer{&g.restart, &g.idle} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
	if g.ext != nil {
		g.stopping[g.ext] = true
		g.ext = nil
	}
	g.state = StateStopped
	exts := make([]*Extension, 0, len(g.stopping))
	for e := range g.stopping {
		exts = append(exts, e)
	}
	starting := g.starting
	g.mu.Unlock()
	for _, e := range exts {
		e.Stop() // safe from several goroutines: each returns once the program has exited
	}
	if starting != nil {
		<-starting // start sees retired and stops what it started before it closes this
	}
}

// Status is what management shows about a generation.
type Status struct {
	ID             string `json:"id"`
	Version        string `json:"version"`
	Generation     uint64 `json:"generation"`
	State          string `json:"state"`
	PID            int    `json:"pid,omitempty"`
	Restarts       int    `json:"restarts"`
	Crashes        int    `json:"crashes_in_a_row"`
	LastError      string `json:"last_error,omitempty"`
	ActiveRequests int    `json:"active_requests"`
}

// Status returns the generation's status.
func (g *Managed) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := Status{ID: g.manifest.ID, Version: g.manifest.Version, Generation: g.generation, State: g.state,
		Restarts: g.restarts, Crashes: g.crashes, LastError: g.lastErr, ActiveRequests: g.calls}
	if g.ext != nil {
		s.PID = g.ext.PID()
	}
	return s
}
