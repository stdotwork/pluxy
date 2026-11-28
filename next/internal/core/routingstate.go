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

const routingFile = "routing.json"

type routingDoc struct {
	V      int                `json:"v"`
	Blocks []route.SavedBlock `json:"blocks"`
	Bans   []route.SavedBan   `json:"bans,omitempty"`
}

const routingVersion = 1

const checkIntervalEnv = "PLUXY_TEST_CHECK_INTERVAL_MS"

const defaultCheckEvery = 15 * time.Minute

type routing struct {
	every     time.Duration
	stop      context.CancelFunc
	done      chan struct{}
	refreshed chan struct{}
	ignited   chan struct{}
	path      string
	saved     []byte
	bad       bool
}

func (c *Core) initRouting() {
	r := &routing{every: defaultCheckEvery, done: make(chan struct{}), refreshed: make(chan struct{}), ignited: make(chan struct{}), path: filepath.Join(StateDir(c.opts.ConfigPath), routingFile)}
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
	go c.runIgnition(ctx, c.routing)
}

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
		log.Printf("routing: %s cannot be read, so accounts start with no blocks; it is left as it is until a block must be saved, and then moved to %s: %v",
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

func (c *Core) stopRouting() {
	if c.routing == nil {
		return
	}
	c.routing.stop()
	<-c.routing.done
	<-c.routing.refreshed
	<-c.routing.ignited
}

func (c *Core) checkRouting(ctx context.Context) {
	if s := c.cur.Load(); s != nil && s.Router != nil {
		s.Router.Check(ctx)
	}
}

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
			log.Printf("error: routing: the blocks are not saved: moving %s, which cannot be read, aside: %v", r.path, err)
			return
		}
		log.Printf("routing: moved %s, which cannot be read, to %s", r.path, aside)
		r.bad = false
	}
	if err := writePrivate(r.path, b); err != nil {
		log.Printf("error: routing: saving the blocks: %v", err)
		return
	}
	r.saved = b
}

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

const readingTimeout = 30 * time.Second

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
	reply, err := c.accountQuota(ctx, s, a.ID)
	if err == nil {
		return reply.Quota, nil
	}
	var le *llm.Error
	if errors.As(err, &le) {
		return nil, le
	}
	return nil, &llm.Error{Code: llm.CodeInternal, Message: err.Error()}
}
