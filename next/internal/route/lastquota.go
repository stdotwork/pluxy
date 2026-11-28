package route

import (
	"slices"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

type QuotaReading struct {
	At    time.Time
	Quota *protocol.Quota
}

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

func (s *State) LastQuota(id string) (QuotaReading, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.quotas[id]
	return r, ok
}

func (s *State) ForgetQuota(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.quotas, id)
}

func (s *State) RetainQuotas(known func(id string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.quotas {
		if !known(id) {
			delete(s.quotas, id)
		}
	}
}
