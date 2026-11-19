package route

import (
	"context"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type Health struct {
	State     string     `json:"state"`
	Until     *time.Time `json:"until,omitempty"`
	Why       string     `json:"why,omitempty"`
	Source    string     `json:"source,omitempty"`
	NextCheck *time.Time `json:"next_check,omitempty"`
}

const (
	HealthOK         = "ok"
	HealthCooling    = "cooling"
	HealthExhausted  = "exhausted"
	HealthNeedsLogin = "needs_login"
	HealthBanned     = "banned"
	HealthDisabled   = "disabled"
	SourceProvider   = "provider"
	SourceBackoff    = "backoff"
	SourceUnverified = "unverified"
)

type Hold struct {
	Disabled, NeedsLogin bool
	Why                  string
}

func (s *State) Health(a Account, now time.Time, h Hold) Health { return Health{State: HealthOK} }

type Readings interface {
	Reading(ctx context.Context, a Account) (*protocol.Quota, *llm.Error)
}

func (r *Router) UseReadings(Readings)  {}
func (r *Router) Check(context.Context) {}

type SavedBlock struct {
	Account string    `json:"account"`
	Scope   string    `json:"scope"`
	Model   string    `json:"model,omitempty"`
	Until   time.Time `json:"until"`
	Source  string    `json:"source"`
}

func (s *State) Saved(time.Time) []SavedBlock                              { return nil }
func (s *State) Load([]SavedBlock, time.Time, func(string) bool)           {}
func (s *State) Changes() <-chan struct{}                                  { return make(chan struct{}) }
