package ignite

import (
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

const FiveHours = 5 * 60 * 60

const placeholderSlack = 10 * time.Second

type Window struct {
	ID           string
	Models       []string
	Running      bool
	Reset        time.Time
	BlockedUntil time.Time
}

func window(l *protocol.QuotaLimit) bool {
	return !l.Feature && !l.Unlimited && (l.WindowSeconds > 0 || l.ResetsAt > 0)
}

func scope(l *protocol.QuotaLimit) string {
	m := slices.Clone(l.Models)
	slices.Sort(m)
	return strings.Join(m, "\x00")
}

func running(l *protocol.QuotaLimit, now time.Time) bool {
	if l.ResetsAt <= now.Unix() {
		return false
	}
	unused := l.UsedPercent == nil || *l.UsedPercent == 0
	if unused && l.WindowSeconds > 0 {
		d := time.Unix(l.ResetsAt, 0).Sub(now.Add(time.Duration(l.WindowSeconds) * time.Second))
		if d.Abs() <= placeholderSlack {
			return false
		}
	}
	return true
}

func full(l *protocol.QuotaLimit, now time.Time) bool {
	return l.ResetsAt > now.Unix() && (l.UsedPercent != nil && *l.UsedPercent >= 100 || l.Remaining != nil && *l.Remaining <= 0)
}

func Windows(limits []protocol.QuotaLimit, now time.Time) []Window {
	var ws []*protocol.QuotaLimit
	for i := range limits {
		if window(&limits[i]) {
			ws = append(ws, &limits[i])
		}
	}
	var out []Window
	for _, l := range ws {
		if l.WindowSeconds != FiveHours && len(ws) > 1 {
			continue
		}
		w := Window{ID: l.ID, Models: slices.Clone(l.Models), Running: running(l, now)}
		if w.Running {
			w.Reset = time.Unix(l.ResetsAt, 0).In(now.Location())
		}
		for _, o := range ws {
			if o == l || !full(o, now) || len(o.Models) > 0 && scope(o) != scope(l) {
				continue
			}
			if t := time.Unix(o.ResetsAt, 0).In(now.Location()); t.After(w.BlockedUntil) {
				w.BlockedUntil = t
			}
		}
		out = append(out, w)
	}
	return out
}
