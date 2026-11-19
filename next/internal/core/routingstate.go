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

// StateDir returns the state folder of the config file at configPath: state beside it, where core
// keeps what it must remember across a restart.
func StateDir(configPath string) string {
	dir := filepath.Dir(configPath)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, "state")
}

// routingFile is the file in the state folder that holds routing's quota blocks.
const routingFile = "routing.json"

// routingDoc is routing.json: the quota blocks of route.State.Saved.
type routingDoc struct {
	Blocks []route.SavedBlock `json:"blocks"`
}

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
	saved []byte // what routing.json holds now
}

// startRouting reads routing.json back into the routing state, then checks blocks on a timer
// (the blocks read back at once) and writes the file again whenever its blocks change.
func (c *Core) startRouting() {
	r := &routing{every: defaultCheckEvery, done: make(chan struct{})}
	if v := os.Getenv(checkIntervalEnv); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			r.every = time.Duration(ms) * time.Millisecond
			c.state.SetCheckInterval(r.every)
		}
	}
	c.loadRouting(r)
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	c.routing = r
	go c.runRouting(ctx, r)
}

// loadRouting puts back the blocks routing.json holds, for the accounts core has. A file that
// cannot be read is logged and left alone: core starts without its blocks.
func (c *Core) loadRouting(r *routing) {
	path := filepath.Join(StateDir(c.opts.ConfigPath), routingFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	var doc routingDoc
	if err == nil {
		err = json.Unmarshal(b, &doc)
	}
	if err != nil {
		log.Printf("routing: %s cannot be read, so accounts start with no blocks: %v", path, err)
		return
	}
	r.saved = b
	s := c.cur.Load()
	c.state.Load(doc.Blocks, time.Now(), func(id string) bool {
		_, ok := c.account(s, id)
		return ok
	})
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
}

func (c *Core) checkRouting(ctx context.Context) {
	if s := c.cur.Load(); s != nil && s.Router != nil {
		s.Router.Check(ctx)
	}
}

// saveRouting writes routing's quota blocks to routing.json when they changed: whole, to a
// temporary file renamed into place, readable by its owner only.
func (c *Core) saveRouting(r *routing) {
	b, err := json.MarshalIndent(routingDoc{Blocks: c.state.Saved(time.Now())}, "", "  ")
	if err != nil || bytes.Equal(b, r.saved) {
		return
	}
	if err := writePrivate(filepath.Join(StateDir(c.opts.ConfigPath), routingFile), b); err != nil {
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
