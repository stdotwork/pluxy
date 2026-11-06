package core

import (
	"context"
	"os"
	"time"
)

// Watch reloads the config file when it changes, until ctx ends. It looks at the file every
// interval and reloads once a change has settled: the file looked the same on two looks in a
// row, so a save written in pieces is read whole. A reload of contents core has already seen,
// or of an empty file, does nothing (see reload).
//
// shortcut: it polls the file's size and modification time instead of using OS file events,
// which keeps core free of dependencies; a change takes up to two intervals to apply, and a
// rewrite that keeps both size and modification time is missed. Upgrade to OS events if
// faster pickup is needed.
func (c *Core) Watch(ctx context.Context, interval time.Duration) {
	type stamp struct {
		size int64
		mod  time.Time
	}
	look := func() (stamp, bool) {
		fi, err := os.Stat(c.opts.ConfigPath)
		if err != nil {
			return stamp{}, false
		}
		return stamp{fi.Size(), fi.ModTime()}, true
	}
	// The file may have changed since core loaded it: check once when it has settled. The
	// content hash makes that a no-op when it has not.
	last, _ := look()
	pending := true
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now, ok := look()
		if !ok {
			continue
		}
		if now != last {
			last, pending = now, true
			continue
		}
		if pending {
			pending = false
			_ = c.reload(false, "config file changed") // a failure is logged and shown in status
		}
	}
}
