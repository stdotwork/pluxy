package ignite

import (
	"errors"
	"fmt"
	"time"
)

type Config struct {
	Enabled           bool                 `json:"enabled,omitempty"`
	StartHour         *int                 `json:"start_hour,omitempty"`
	EndHour           *int                 `json:"end_hour,omitempty"`
	EndGraceMS        *int64               `json:"end_grace_ms,omitempty"`
	GraceMS           *int64               `json:"grace_ms,omitempty"`
	RetryMS           *int64               `json:"retry_ms,omitempty"`
	BackoffMultiplier *float64             `json:"backoff_multiplier,omitempty"`
	MaxFailures       *int                 `json:"max_failures,omitempty"`
	HoldMS            *int64               `json:"hold_ms,omitempty"`
	Extensions        map[string]Extension `json:"extensions,omitempty"`
	Accounts          map[string]Account   `json:"accounts,omitempty"`
}

type Extension struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Model   string            `json:"model,omitempty"`
	Windows map[string]string `json:"windows,omitempty"`
}

type Account struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Model   string `json:"model,omitempty"`
}

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

const (
	day     = 24 * time.Hour
	minHold = 15 * time.Second
)

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

func (c *Config) windowOn(ext, w string) bool {
	e := c.Extensions[ext]
	if len(e.Windows) == 0 {
		return true
	}
	_, ok := e.Windows[w]
	return ok
}

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

func (c *Config) span(t time.Time) (start, end time.Time, all bool) {
	loc := t.Location()
	y, m, d := t.Date()
	sh, eh, g := c.startHour(), c.endHour(), c.endGrace()
	hours := eh - sh
	if hours <= 0 {
		hours += 24
	}
	if time.Duration(hours)*time.Hour+g >= day {
		return t, t, true
	}
	for dd := -1; dd <= 1; dd++ {
		s := clock(y, m, d+dd, sh, loc)
		over := 0
		if eh <= sh {
			over = 1
		}
		e := clock(y, m, d+dd+over, eh, loc).Add(g)
		if !t.Before(s) && t.Before(e) || t.Before(s) {
			return s, e, false
		}
	}
	return t, t, true
}

func clock(y int, m time.Month, d, hour int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, hour, 0, 0, 0, loc)
	if t.Hour() != hour {
		return time.Date(y, m, d, hour+1, 0, 0, 0, loc)
	}
	if u := t.Add(-time.Hour); u.Hour() == hour && u.Minute() == 0 {
		return u
	}
	return t
}

func (c *Config) InHours(t time.Time) time.Time {
	s, _, all := c.span(t)
	if all || !t.Before(s) {
		return t
	}
	return s
}

func (c *Config) nextStart(t time.Time) time.Time {
	y, m, d := t.Date()
	s := clock(y, m, d, c.startHour(), t.Location())
	if !s.After(t) {
		s = clock(y, m, d+1, c.startHour(), t.Location())
	}
	return s
}
