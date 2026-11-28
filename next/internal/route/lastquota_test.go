package route

import (
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

func TestLastQuota(t *testing.T) {
	s := NewState()
	if _, ok := s.LastQuota("a"); ok {
		t.Fatal("a new state holds a quota reply")
	}
	s.NoteQuota("a", nil, time.Unix(1, 0))
	if _, ok := s.LastQuota("a"); ok {
		t.Fatal("a nil reply was kept")
	}
	q := &protocol.Quota{Plan: "one", Limits: []protocol.QuotaLimit{{ID: "x"}}}
	s.NoteQuota("a", q, time.Unix(10, 0))
	q.Limits[0].ID = "changed"
	if r, ok := s.LastQuota("a"); !ok || r.Quota.Plan != "one" || r.Quota.Limits[0].ID != "x" || !r.At.Equal(time.Unix(10, 0)) {
		t.Fatalf("last quota = %+v, %v", r, ok)
	}
	s.NoteQuota("a", &protocol.Quota{Plan: "two"}, time.Unix(20, 0))
	s.NoteQuota("b", &protocol.Quota{Plan: "b"}, time.Unix(20, 0))
	if r, _ := s.LastQuota("a"); r.Quota.Plan != "two" || !r.At.Equal(time.Unix(20, 0)) {
		t.Fatalf("a newer reply did not replace the older: %+v", r)
	}
	s.RetainQuotas(func(id string) bool { return id == "b" })
	if _, ok := s.LastQuota("a"); ok {
		t.Fatal("RetainQuotas kept an account that no longer exists")
	}
	if _, ok := s.LastQuota("b"); !ok {
		t.Fatal("RetainQuotas dropped an account that exists")
	}
	s.ForgetQuota("b")
	if _, ok := s.LastQuota("b"); ok {
		t.Fatal("ForgetQuota kept the reply")
	}
}
