// Package ignite starts an account's next usage window right after its last one resets (window
// ignition): it watches the reset times the account's provider reports (the extension's quota
// call) and, once a window has reset, sends the account one small request, so the next window
// starts at once instead of at the first real request. It runs only between set hours of the
// machine's local time.
//
// The package holds the settings (routing.ignition), which windows of a reading it starts
// (Targets), the plan of what to do next for each account (Planner) and the choice of a model
// (ChooseModel). Core reads the usage and sends the requests (internal/core, ignition.go).
package ignite

import (
	"errors"
	"fmt"
	"time"
)

// Config is routing.ignition. The zero value, like no section at all, is off.
type Config struct {
	// Enabled turns ignition on. Off by default.
	Enabled bool `json:"enabled,omitempty"`
	// StartHour and EndHour bound the hours ignition runs in, in the machine's local time
	// (0-23): from StartHour:00 to EndHour:00 plus EndGraceMS. Defaults 7 and 22.
	StartHour *int `json:"start_hour,omitempty"`
	EndHour   *int `json:"end_hour,omitempty"`
	// EndGraceMS extends the hours past EndHour:00. Default 1800000 (30 minutes).
	EndGraceMS *int64 `json:"end_grace_ms,omitempty"`
	// GraceMS is how long after a window's reset core reads it and starts the next. Default
	// 3000.
	GraceMS *int64 `json:"grace_ms,omitempty"`
	// RetryMS is how long core waits after a failed ignition before it tries again, multiplied
	// by BackoffMultiplier for each further failure in a row. Defaults 300000 and 3.
	RetryMS           *int64   `json:"retry_ms,omitempty"`
	BackoffMultiplier *float64 `json:"backoff_multiplier,omitempty"`
	// MaxFailures is how many failures in a row pause a window's ignition until the next
	// StartHour. Default 3.
	MaxFailures *int `json:"max_failures,omitempty"`
	// HoldMS is how long after an ignition that succeeded core reads the window again to see
	// that it started, and so the least time between two ignitions of a window. Default 60000;
	// at least 15000, so a window the request started is told from one that is not running
	// (see running).
	HoldMS *int64 `json:"hold_ms,omitempty"`
	// Extensions turns ignition on for the accounts of each extension listed, by extension ID.
	Extensions map[string]Extension `json:"extensions,omitempty"`
	// Accounts turns it on or off for one account, by account ID, over its extension's choice.
	Accounts map[string]Account `json:"accounts,omitempty"`
}

// Extension is the ignition of one extension's accounts.
type Extension struct {
	// Enabled false leaves the extension's accounts out; listed without it, they take part.
	Enabled *bool `json:"enabled,omitempty"`
	// Model is the model the request names; empty: a small one the account serves
	// (ChooseModel).
	Model string `json:"model,omitempty"`
	// Windows, when it lists any, are the only usage windows started, by window ID (as the
	// account's quota names them), each with the model to start it with ("" for Model or the
	// default).
	Windows map[string]string `json:"windows,omitempty"`
}

// Account is the ignition of one account.
type Account struct {
	// Enabled true or false turns the account's ignition on or off whatever its extension says;
	// unset, its extension decides.
	Enabled *bool `json:"enabled,omitempty"`
	// Model, when set, names the model for the account's windows that Extension.Windows gives
	// none.
	Model string `json:"model,omitempty"`
}

// Defaults, as the Lamplighter plugin for CLIProxyAPI has them.
const (
	DefaultStartHour         = 7
	DefaultEndHour           = 22
	DefaultEndGrace          = 30 * time.Minute
	DefaultGrace             = 3 * time.Second
	DefaultRetry             = 5 * time.Minute
	DefaultBackoffMultiplier = 3.0
	DefaultMaxFailures       = 3
	DefaultHold              = time.Minute
)

// day bounds the durations of the settings, and minHold is the shortest hold.
const (
	day     = 24 * time.Hour
	minHold = 15 * time.Second
)

// Validate checks the settings' ranges.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	for _, h := range []struct {
		name string
		v    *int
	}{{"start_hour", c.StartHour}, {"end_hour", c.EndHour}} {
		if h.v != nil && (*h.v < 0 || *h.v > 23) {
			return fmt.Errorf("routing.ignition.%s must be between 0 and 23", h.name)
		}
	}
	for _, d := range []struct {
		name string
		v    *int64
		min  int64
	}{{"end_grace_ms", c.EndGraceMS, 0}, {"grace_ms", c.GraceMS, 0}, {"retry_ms", c.RetryMS, 1000}, {"hold_ms", c.HoldMS, minHold.Milliseconds()}} {
		if d.v != nil && (*d.v < d.min || *d.v > day.Milliseconds()) {
			return fmt.Errorf("routing.ignition.%s must be between %d and %d (a day)", d.name, d.min, day.Milliseconds())
		}
	}
	if m := c.BackoffMultiplier; m != nil && (!(*m >= 1) || *m > 10) {
		return errors.New("routing.ignition.backoff_multiplier must be between 1 and 10")
	}
	if n := c.MaxFailures; n != nil && (*n < 1 || *n > 10) {
		return errors.New("routing.ignition.max_failures must be between 1 and 10")
	}
	for id, e := range c.Extensions {
		if id == "" {
			return errors.New("routing.ignition.extensions: an extension ID must not be empty")
		}
		for w := range e.Windows {
			if w == "" {
				return fmt.Errorf("routing.ignition.extensions.%s.windows: a window ID must not be empty", id)
			}
		}
	}
	for id := range c.Accounts {
		if id == "" {
			return errors.New("routing.ignition.accounts: an account ID must not be empty")
		}
	}
	return nil
}

// On reports whether ignition runs at all.
func (c *Config) On() bool { return c != nil && c.Enabled }

func intOr(v *int, d int) int {
	if v == nil {
		return d
	}
	return *v
}

func msOr(v *int64, d time.Duration) time.Duration {
	if v == nil {
		return d
	}
	return time.Duration(*v) * time.Millisecond
}

func (c *Config) startHour() int          { return intOr(c.StartHour, DefaultStartHour) }
func (c *Config) endHour() int            { return intOr(c.EndHour, DefaultEndHour) }
func (c *Config) endGrace() time.Duration { return msOr(c.EndGraceMS, DefaultEndGrace) }
func (c *Config) grace() time.Duration    { return msOr(c.GraceMS, DefaultGrace) }
func (c *Config) retry() time.Duration    { return msOr(c.RetryMS, DefaultRetry) }
func (c *Config) hold() time.Duration     { return msOr(c.HoldMS, DefaultHold) }
func (c *Config) maxFailures() int        { return intOr(c.MaxFailures, DefaultMaxFailures) }
func (c *Config) backoffMultiplier() float64 {
	if c.BackoffMultiplier == nil {
		return DefaultBackoffMultiplier
	}
	return *c.BackoffMultiplier
}

// AccountOn reports whether ignition runs for account id of extension ext: the account's own
// switch, else whether its extension is listed and not turned off.
func (c *Config) AccountOn(id, ext string) bool {
	if !c.On() {
		return false
	}
	if a, ok := c.Accounts[id]; ok && a.Enabled != nil {
		return *a.Enabled
	}
	e, ok := c.Extensions[ext]
	return ok && (e.Enabled == nil || *e.Enabled)
}

// windowOn reports whether window w of an account of extension ext is started: every target
// window, unless the extension lists windows, and then only those.
func (c *Config) windowOn(ext, w string) bool {
	e := c.Extensions[ext]
	if len(e.Windows) == 0 {
		return true
	}
	_, ok := e.Windows[w]
	return ok
}

// Model returns the model set for window w of account id of extension ext: the extension's
// model for the window, else the account's, else the extension's; empty when none is set.
func (c *Config) Model(id, ext, w string) string {
	e := c.Extensions[ext]
	if m := e.Windows[w]; m != "" {
		return m
	}
	if m := c.Accounts[id].Model; m != "" {
		return m
	}
	return e.Model
}

// span returns the hours that hold or follow t: the start and end of the day's range that
// contains t, or of the next one when t is outside every range. all is true when the range is
// the whole day.
func (c *Config) span(t time.Time) (start, end time.Time, all bool) {
	loc := t.Location()
	y, m, d := t.Date()
	sh, eh, g := c.startHour(), c.endHour(), c.endGrace()
	// Whether the hours are the whole day is a matter of the clock's hours, not of a day's
	// length, which a change to or from daylight saving time makes 23 or 25 hours.
	hours := eh - sh
	if hours <= 0 {
		hours += 24
	}
	if time.Duration(hours)*time.Hour+g >= day {
		return t, t, true
	}
	for dd := -1; dd <= 1; dd++ {
		s := clock(y, m, d+dd, sh, loc)
		// An end hour not after the start hour is the next day's: 22 to 6 runs over midnight,
		// and 7 to 7 all day.
		over := 0
		if eh <= sh {
			over = 1
		}
		e := clock(y, m, d+dd+over, eh, loc).Add(g)
		if !t.Before(s) && t.Before(e) || t.Before(s) {
			return s, e, false
		}
	}
	// Not reached: the next day's range starts after t.
	return t, t, true
}

// clock returns when the clock in loc first shows hour:00 on the day: on a day the clocks go
// forward over that hour, the moment they jump to (02:00 becomes 03:00); on a day they go back
// over it, the first of its two times.
func clock(y int, m time.Month, d, hour int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, hour, 0, 0, 0, loc)
	if t.Hour() != hour {
		// The hour does not exist: time.Date gave a time an hour off it; the jump is at hour+1.
		return time.Date(y, m, d, hour+1, 0, 0, 0, loc)
	}
	if u := t.Add(-time.Hour); u.Hour() == hour && u.Minute() == 0 {
		return u
	}
	return t
}

// InHours returns t when it is within the hours, else when they next start.
func (c *Config) InHours(t time.Time) time.Time {
	s, _, all := c.span(t)
	if all || !t.Before(s) {
		return t
	}
	return s
}

// nextStart returns the first StartHour:00 after t (local time): where a paused window starts
// again.
func (c *Config) nextStart(t time.Time) time.Time {
	y, m, d := t.Date()
	s := clock(y, m, d, c.startHour(), t.Location())
	if !s.After(t) {
		s = clock(y, m, d+1, c.startHour(), t.Location())
	}
	return s
}
