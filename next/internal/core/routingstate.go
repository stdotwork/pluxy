package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// routingFile is the file in the state folder that holds routing's quota blocks.
const routingFile = "routing.json"

// routingDoc is routing.json: the quota blocks of route.State.Saved, under the version of the
// document's form (routingVersion).
type routingDoc struct {
	V      int                `json:"v"`
	Blocks []route.SavedBlock `json:"blocks"`
	Bans   []route.SavedBan   `json:"bans,omitempty"`
}

// routingVersion is the version of routing.json's form that this core reads and writes.
const routingVersion = 1

// checkIntervalEnv, for tests only, sets how often routing checks long blocks, in milliseconds,
// in place of 15 minutes.
const checkIntervalEnv = "PLUXY_TEST_CHECK_INTERVAL_MS"

// defaultCheckEvery is how often routing checks long blocks (route.State.SetCheckInterval).
const defaultCheckEvery = 15 * time.Minute

// routing runs routing's checks and keeps routing.json, from start until Stop.
type routing struct {
	every time.Duration // the check interval
	stop  context.CancelFunc
	done  chan struct{}
	// refreshed is closed when the usage readings' loop (runQuotaRefresh) has ended.
	refreshed chan struct{}
	path      string // routing.json
	// saved is what routing.json holds now, when core read or wrote it; nil when there is no
	// file or core could not read it.
	saved []byte
	// bad is true while path holds a file core could not read or that another version wrote:
	// it is kept until a block must be saved.
	bad bool
}

// startRouting reads routing.json back into the routing state, then checks blocks on a timer
// (the blocks read back at once) and writes the file again whenever its blocks change.
func (c *Core) initRouting() {
	r := &routing{every: defaultCheckEvery, done: make(chan struct{}), refreshed: make(chan struct{}), path: filepath.Join(StateDir(c.opts.ConfigPath), routingFile)}
	if v := os.Getenv(checkIntervalEnv); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			r.every = time.Duration(ms) * time.Millisecond
			c.state.SetCheckInterval(r.every)
		}
	}
	c.routing = r
}

func (c *Core) startRouting() {
	if c.routing == nil {
		c.initRouting()
		c.loadRouting(c.routing)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.routing.stop = cancel
	go c.runRouting(ctx, c.routing)
	go c.runQuotaRefresh(ctx, c.routing)
}

// runQuotaRefresh reads the accounts' usage for soonest-reset (route.Router.Refresh), on a timer
// of its own so a slow reading never holds up the checks or a request. The router decides on each
// tick whether a refresh is due (routing.quota_refresh_ms); the first tick comes a tick after
// start, once core is serving. In safe mode, where no extension runs, it reads nothing.
func (c *Core) runQuotaRefresh(ctx context.Context, r *routing) {
	defer close(r.refreshed)
	if c.opts.SafeMode {
		return
	}
	tick := time.NewTicker(max(r.every/15, 10*time.Millisecond))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if s := c.cur.Load(); s != nil && s.Router != nil {
				s.Router.Refresh(ctx)
			}
		}
	}
}

// loadRouting puts back the blocks routing.json holds, for the accounts core has. A file that
// cannot be read, or that another version of core wrote, is logged and left in place: core starts
// without its blocks, and saveRouting moves it aside only when a block must be saved.
func (c *Core) loadRouting(r *routing) {
	b, err := os.ReadFile(r.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var doc routingDoc
	if err == nil {
		err = json.Unmarshal(b, &doc)
	}
	if err == nil && doc.V != routingVersion {
		err = fmt.Errorf("it is version %d of the file, and this core reads version %d", doc.V, routingVersion)
	}
	if err != nil {
		r.bad = true
		log.Printf("routing: %s cannot be read, so accounts start with no blocks; it is left as it is until a quota block must be saved, and then moved to %s: %v",
			r.path, filepath.Base(r.path)+".bad", err)
		return
	}
	r.saved = b
	s := c.cur.Load()
	known := func(id string) bool {
		_, ok := c.account(s, id)
		return ok
	}
	c.state.Load(doc.Blocks, time.Now(), known)
	c.state.LoadBans(doc.Bans, known)
}

func (c *Core) runRouting(ctx context.Context, r *routing) {
	defer close(r.done)
	tick := time.NewTicker(max(r.every/15, 10*time.Millisecond))
	defer tick.Stop()
	c.checkRouting(ctx)
	for {
		select {
		case <-ctx.Done():
			c.saveRouting(r)
			return
		case <-c.state.Changes():
			c.saveRouting(r)
		case <-tick.C:
			c.checkRouting(ctx)
		}
	}
}

// stopRouting ends the checks and writes routing.json a last time.
func (c *Core) stopRouting() {
	if c.routing == nil {
		return
	}
	c.routing.stop()
	<-c.routing.done
	<-c.routing.refreshed
}

func (c *Core) checkRouting(ctx context.Context) {
	if s := c.cur.Load(); s != nil && s.Router != nil {
		s.Router.Check(ctx)
	}
}

// saveRouting writes routing's quota blocks to routing.json when they changed: whole, to a
// temporary file renamed into place, readable by its owner only. With no block to save it writes
// nothing unless the file holds blocks core saved or read before. A file loadRouting could not
// read is left in place until a block must be saved, and then moved aside to routing.json.bad.
func (c *Core) saveRouting(r *routing) {
	blocks := c.state.Saved(time.Now())
	bans := c.state.SavedBans()
	b, err := json.MarshalIndent(routingDoc{V: routingVersion, Blocks: blocks, Bans: bans}, "", "  ")
	if err != nil || bytes.Equal(b, r.saved) || len(blocks) == 0 && len(bans) == 0 && r.saved == nil {
		return
	}
	if r.bad {
		aside := r.path + ".bad"
		if err := os.Rename(r.path, aside); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("routing: the quota blocks are not saved: moving %s, which cannot be read, aside: %v", r.path, err)
			return
		}
		log.Printf("routing: moved %s, which cannot be read, to %s", r.path, aside)
		r.bad = false
	}
	if err := writePrivate(r.path, b); err != nil {
		log.Printf("routing: saving the quota blocks: %v", err)
		return
	}
	r.saved = b
}

// writePrivate writes b to path whole, through a temporary file in its folder renamed into place,
// with mode 0600; the folder is made, 0700, when missing.
func writePrivate(path string, b []byte) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readingTimeout bounds one usage reading a check makes.
const readingTimeout = 30 * time.Second

// readings is routing's source of usage readings (route.Readings): the quota call of the
// account's extension.
type readings struct{ c *Core }

func (rd readings) Reading(ctx context.Context, a route.Account) (*protocol.Quota, *llm.Error) {
	c := rd.c
	s, release := c.acquire()
	defer release()
	g := s.exts[a.Extension]
	if g == nil {
		return nil, &llm.Error{Code: llm.CodeExtensionUnavailable, Message: fmt.Sprintf("extension %s does not run", a.Extension)}
	}
	if !slices.Contains(g.Info().Operations, protocol.OpQuota) {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("extension %s has no usage reading", a.Extension)}
	}
	ctx, cancel := context.WithTimeout(ctx, readingTimeout)
	defer cancel()
	q, err := c.accountQuota(ctx, s, a.ID)
	if err == nil {
		return q, nil
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return nil, le
	}
	return nil, &llm.Error{Code: llm.CodeInternal, Message: err.Error()}
}
