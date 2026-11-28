package core

import (
	"context"
	"os"
	"time"
)

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
			_ = c.reload(false, "config file changed")
		}
	}
}
