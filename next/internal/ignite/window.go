package ignite

import (
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// FiveHours is the length of the usage window ignition starts.
const FiveHours = 5 * 60 * 60

// placeholderSlack is how close to "now plus the window's length" a reset must be named for an
// unused window to count as not running (see running).
const placeholderSlack = 10 * time.Second

// Window is one usage window ignition starts, from a reading of the account's usage.
type Window struct {
	// ID is the window's ID in the reading, such as five_hour or gemini_models_five_hour.
	ID string
	// Models are the models the window counts, when not all of the account's.
	Models []string
	// Running is true when the window has started and has not reset; Reset is then when it
	// resets.
	Running bool
	Reset   time.Time
	// BlockedUntil, when set, is when another window that covers the same requests, full now,
	// resets: a request before then starts nothing.
	BlockedUntil time.Time
}

// window reports whether l is a usage window: it counts the account's requests (all of them,
// or those to its Models) and has a length or a reset.
func window(l *protocol.QuotaLimit) bool {
	return !l.Feature && !l.Unlimited && (l.WindowSeconds > 0 || l.ResetsAt > 0)
}

// scope names the requests a window counts: "" for all of the account's, else its models.
func scope(l *protocol.QuotaLimit) string {
	m := slices.Clone(l.Models)
	slices.Sort(m)
	return strings.Join(m, "\x00")
}

// running reports whether window l is running at now: it names a reset still to come, and is
// not an unused window whose reset moves with the clock. Some providers name an unused window's
// reset as now plus its length, so the reset of a window no request started keeps moving; such a
// window, with nothing used and a reset within placeholderSlack of now plus its length, is not
// running. A window a request started HoldMS (at least 15 seconds) before the reading resets that
// much sooner, so it counts as running.
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

// full reports whether window l is used up until a reset still to come.
func full(l *protocol.QuotaLimit, now time.Time) bool {
	return l.ResetsAt > now.Unix() && (l.UsedPercent != nil && *l.UsedPercent >= 100 || l.Remaining != nil && *l.Remaining <= 0)
}

// Windows returns the windows of a reading that ignition starts, at now, with times in now's
// location: each usage window of five hours, whether it counts all of the account's requests or
// those of a group of models (Antigravity has one per group). A reading with one usage window
// only, of any length, has that one. No other window (a weekly window, one model family's) is
// started: a request starts it anyway once a five-hour window is used. But when one of them that
// covers the same requests (the account's, or the same group's) is full, a request would start
// nothing, and BlockedUntil says until when.
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
			// A full window of the whole account blocks every window; one of a group of models,
			// the windows of that group.
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
