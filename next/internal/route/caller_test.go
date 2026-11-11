package route

import (
	"context"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Each attempt carries a caller value for the request's tenant: the same for one tenant on one
// extension, different for another tenant or extension, absent without a tenant, and never the
// tenant itself.
func TestAttemptCarriesCaller(t *testing.T) {
	var got []string
	attempt := func(_ context.Context, ext string, _ protocol.Account, req llm.Request) Stream {
		got = append(got, req.Caller)
		return &fakeStream{events: ok()}
	}
	r, err := New(Config{}, []Account{{ID: "k", Extension: "pluxy/kimi", Models: []ModelEntry{{Name: "m"}}}, {ID: "x", Extension: "pluxy/xai", Models: []ModelEntry{{Name: "n"}}}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(model, tenant string) string {
		s := r.Generate(context.Background(), Request{LLM: llm.Request{Model: model}, Tenant: tenant})
		for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
		}
		s.Close()
		return got[len(got)-1]
	}
	a1, a2, b, other, none := run("m", "aaaa"), run("m", "aaaa"), run("m", "bbbb"), run("n", "aaaa"), run("m", "")
	switch {
	case a1 == "" || a1 != a2:
		t.Errorf("one tenant on one extension: %q then %q, want one non-empty value", a1, a2)
	case b == a1:
		t.Errorf("two tenants got the same caller %q", a1)
	case other == a1 || other == "":
		t.Errorf("another extension got %q for the same tenant (kimi got %q)", other, a1)
	case none != "":
		t.Errorf("a request without a tenant got caller %q", none)
	case strings.Contains(a1, "aaaa"):
		t.Errorf("caller %q contains the tenant", a1)
	}
}
