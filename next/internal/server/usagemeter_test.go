package server

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
)

func meterFor(t *testing.T) (*meter, *usage.Store) {
	t.Helper()
	store, err := usage.Open(usage.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return &meter{store: store, start: time.Now()}, store
}

func attemptOn(m *meter, n int) {
	m.attempt(route.Attempt{Account: fmt.Sprint("a", n), Public: fmt.Sprintf("acc_%d", n), Extension: "ext", Model: "up"}, "trace")
}

func onlyEvent(t *testing.T, s *usage.Store) usage.Event {
	t.Helper()
	ev := s.Events(usage.Filter{}, 0, 10).Events
	if len(ev) != 1 {
		t.Fatalf("events: %+v", ev)
	}
	return ev[0]
}

// A failed request records the accounts its error says routing left out, by public ID, state and
// until, at most 8 of them and never by config ID, whether or not the client's error shows them;
// with no attempt, it names no account.
func TestMeterRecordsLeftOut(t *testing.T) {
	m, s := meterFor(t)
	until := time.Unix(1_800_000_000, 0).UTC()
	var left []llm.LeftOut
	for i := range 10 {
		left = append(left, llm.LeftOut{ID: fmt.Sprint("config-", i), Account: fmt.Sprintf("acc_%d", i), State: "cooling", Until: &until})
	}
	m.fail(&llm.Error{Code: llm.CodeRateLimit, Routed: &llm.Explain{Tried: []llm.Tried{}, LeftOut: left}}, 429)
	e := onlyEvent(t, s)
	b, _ := json.Marshal(e)
	if e.Account != "" || e.Attempts != 0 || len(e.LeftOut) != 8 || strings.Contains(string(b), "config-") {
		t.Fatalf("event %s", b)
	}
	for i, l := range e.LeftOut {
		if l.Account != fmt.Sprintf("acc_%d", i) || l.State != "cooling" || l.Until == nil || !l.Until.Equal(until) || l.ID != "" {
			t.Errorf("left_out[%d] = %+v", i, l)
		}
	}
	if !strings.Contains(string(b), `"left_out":[{"account":"acc_0","state":"cooling","until":"2027-01-15T08:00:00Z"}`) {
		t.Errorf("event %s, want left_out with account, state and until", b)
	}

	// A request that left none out records no list.
	m, s = meterFor(t)
	attemptOn(m, 1)
	m.fail(&llm.Error{Code: llm.CodeRateLimit, Routed: &llm.Explain{Tried: []llm.Tried{}, LeftOut: []llm.LeftOut{}}}, 429)
	if b, _ := json.Marshal(onlyEvent(t, s)); strings.Contains(string(b), "left_out") {
		t.Errorf("event %s, want no left_out", b)
	}
}

func TestMeterOneSuccessfulAttemptStoresNoTried(t *testing.T) {
	m, s := meterFor(t)
	attemptOn(m, 1)
	m.finish(llm.Event{})
	e := onlyEvent(t, s)
	b, _ := json.Marshal(e)
	if e.Attempts != 1 || e.Tried != nil || strings.Contains(string(b), `"tried"`) {
		t.Fatalf("event %s", b)
	}
}

func TestMeterOneFailedAttemptStoresOneEntry(t *testing.T) {
	m, s := meterFor(t)
	attemptOn(m, 1)
	m.attemptFailed(llm.Tried{Account: "acc_1", Provider: "ext", Model: "up", Result: "rate_limit"})
	m.fail(&llm.Error{Code: llm.CodeRateLimit}, 429)
	e := onlyEvent(t, s)
	want := []llm.Tried{{Account: "acc_1", Provider: "ext", Model: "up", Result: "rate_limit"}}
	if e.Attempts != 1 || !slices.Equal(e.Tried, want) {
		t.Fatalf("event %+v", e)
	}
}

func TestMeterFallbackListsEveryAttemptEndingOk(t *testing.T) {
	m, s := meterFor(t)
	attemptOn(m, 1)
	m.attemptFailed(llm.Tried{Account: "acc_1", Provider: "ext", Model: "up", Result: "rate_limit"})
	attemptOn(m, 2)
	m.finish(llm.Event{})
	e := onlyEvent(t, s)
	want := []llm.Tried{{Account: "acc_1", Provider: "ext", Model: "up", Result: "rate_limit"},
		{Account: "acc_2", Provider: "ext", Model: "up", Result: "ok"}}
	if e.Attempts != 2 || !slices.Equal(e.Tried, want) {
		t.Fatalf("event %+v", e)
	}
}

func TestMeterStreamBrokenAfterItBeganEndsWithTheErrorCode(t *testing.T) {
	m, s := meterFor(t)
	attemptOn(m, 1)
	m.fail(&llm.Error{Code: llm.CodeUpstream}, 200)
	e := onlyEvent(t, s)
	want := []llm.Tried{{Account: "acc_1", Provider: "ext", Model: "up", Result: llm.CodeUpstream}}
	if e.Attempts != 1 || !slices.Equal(e.Tried, want) {
		t.Fatalf("event %+v", e)
	}
}

// An error's code can be one an extension chose, so an attempt's result takes it only when it is
// one of core's own, as the error body's tried does.
func TestMeterResultIsNeverAnExtensionsText(t *testing.T) {
	m, s := meterFor(t)
	attemptOn(m, 1)
	m.fail(&llm.Error{Code: "text-an-extension-chose"}, 200)
	e := onlyEvent(t, s)
	if len(e.Tried) != 1 || e.Tried[0].Result != "error" {
		t.Fatalf("tried %+v, want one entry with the result error", e.Tried)
	}
}

func TestMeterBoundsTriedToFirst15AndLast(t *testing.T) {
	m, s := meterFor(t)
	for i := 1; i <= 20; i++ {
		attemptOn(m, i)
		if i < 20 {
			m.attemptFailed(llm.Tried{Account: fmt.Sprintf("acc_%d", i), Provider: "ext", Model: "up", Result: "rate_limit"})
		}
	}
	m.finish(llm.Event{})
	e := onlyEvent(t, s)
	if e.Attempts != 20 || len(e.Tried) != 16 {
		t.Fatalf("attempts %d, %d entries", e.Attempts, len(e.Tried))
	}
	for i := 0; i < 15; i++ {
		if e.Tried[i].Account != fmt.Sprintf("acc_%d", i+1) {
			t.Fatalf("entry %d = %+v", i, e.Tried[i])
		}
	}
	if last := e.Tried[15]; last.Account != "acc_20" || last.Result != "ok" {
		t.Fatalf("last entry %+v", last)
	}
}
