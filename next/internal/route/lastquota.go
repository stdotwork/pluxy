package route

import (
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

// QuotaReading is the last quota reply core read for an account, and when.
type QuotaReading struct {
	At    time.Time
	Quota *protocol.Quota
}

// NoteQuota keeps q, a quota reply read for account id at at, as the account's last one; it
// replaces the one before. Core calls it for every reply it reads, whoever asked: a management
// call, routing's own readings or ignition.
func (s *State) NoteQuota(id string, q *protocol.Quota, at time.Time) {
	if q == nil {
		return
	}
	kept := *q
	kept.Limits = slices.Clone(q.Limits)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotas[id] = QuotaReading{At: at, Quota: &kept}
}

// LastQuota returns the last quota reply kept for account id; false when none is.
func (s *State) LastQuota(id string) (QuotaReading, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.quotas[id]
	return r, ok
}

// ForgetQuota drops the quota reply kept for account id, which was removed.
func (s *State) ForgetQuota(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.quotas, id)
}

// RetainQuotas drops the quota replies of accounts that known reports no longer exist. Accounts
// that merely left routing (turned off, waiting for a sign-in) keep theirs.
func (s *State) RetainQuotas(known func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.quotas {
		if !known(id) {
			delete(s.quotas, id)
		}
	}
}
