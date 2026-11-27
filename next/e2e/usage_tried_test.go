package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"
)

// firstUsageEvent waits for the usage record to hold n events and returns them.
func (c *mcore) waitUsageEvents(t *testing.T, n int) ([]usageEvent, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p, raw := c.usageEvents(t, "")
		if len(p.Events) >= n || time.Now().After(deadline) {
			if len(p.Events) != n {
				t.Fatalf("usage events: %s", raw)
			}
			return p.Events, raw
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A request that fell back says how many attempts it took, what each hit, the last by "ok", and
// why the account that answered was chosen; a request that went to one account and succeeded
// carries no tried.
func TestUsageSaysWhatAFallbackTried(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-a" {
			rateLimited(w)
			return
		}
		okReply(w)
	}
	base := startExplainCore(t, u, map[string]any{"strategy": "fill-first"},
		explainAccount{id: "first", key: "sk-a", priority: 1}, explainAccount{id: "second", key: "sk-b"})
	ids, ext := publicIDs(t, base)
	c := &mcore{base: base}

	resp := post(t, context.Background(), base, clientKey, plainBody)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Pluxy-Attempts") != "2" {
		t.Fatalf("status %d, attempts %q", resp.StatusCode, resp.Header.Get("X-Pluxy-Attempts"))
	}
	events, raw := c.waitUsageEvents(t, 1)
	e := events[0]
	want := []usageTried{
		{ids["first"], ext, "gpt-test", "rate_limit"},
		{ids["second"], ext, "gpt-test", "ok"},
	}
	if e.Attempts != 2 || !slices.Equal(e.Tried, want) || e.Account != "second" || e.Tried[1].Account != resp.Header.Get("X-Pluxy-Account") {
		t.Fatalf("attempts %d, tried %+v, want 2 and %+v; account %q\n%s", e.Attempts, e.Tried, want, e.Account, raw)
	}
	// The second account is a backup: the first, ahead of it in the order, could not take it.
	if e.Why != "backup" {
		t.Fatalf("why %q, want backup\n%s", e.Why, raw)
	}

	// The first account is cooling now: the next request goes to one account and succeeds.
	resp = post(t, context.Background(), base, clientKey, plainBody)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-Pluxy-Attempts") != "1" {
		t.Fatalf("second request: attempts %q", resp.Header.Get("X-Pluxy-Attempts"))
	}
	events, raw = c.waitUsageEvents(t, 2)
	var rawEvents struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(raw), &rawEvents); err != nil {
		t.Fatal(err)
	}
	last := rawEvents.Events[1]
	if events[1].Attempts != 1 || string(last["attempts"]) != "1" || events[1].Why != "backup" {
		t.Fatalf("one successful attempt: %s", raw)
	}
	if _, has := last["tried"]; has {
		t.Fatalf("one successful attempt has a tried: %s", raw)
	}
}

// A request that failed on every account has the error body's tried in its event, exactly.
func TestUsageTriedEqualsTheErrorsTriedWhenEveryAccountFails(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { rateLimited(w) }
	base := startExplainCore(t, u, map[string]any{"strategy": "fill-first"},
		explainAccount{id: "first", key: "sk-a", priority: 1}, explainAccount{id: "second", key: "sk-b"})
	c := &mcore{base: base}

	resp := post(t, context.Background(), base, clientKey, plainBody)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var b explainBody
	if err := json.Unmarshal(raw, &b); err != nil || b.Error.Pluxy == nil || len(b.Error.Pluxy.Tried) != 2 {
		t.Fatalf("status %d, body %s (%v)", resp.StatusCode, raw, err)
	}
	events, evRaw := c.waitUsageEvents(t, 1)
	e := events[0]
	var want []usageTried
	for _, x := range b.Error.Pluxy.Tried {
		want = append(want, usageTried{x.Account, x.Provider, x.Model, x.Result})
	}
	if !e.Failed || e.Attempts != 2 || !slices.Equal(e.Tried, want) {
		t.Fatalf("event %s, want attempts 2 and tried %+v", evRaw, want)
	}
}

// A single attempt that fails has one entry.
func TestUsageOneFailedAttemptHasOneEntry(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { rateLimited(w) }
	base := startExplainCore(t, u, nil, explainAccount{id: "only", key: "sk-a"})
	ids, ext := publicIDs(t, base)
	c := &mcore{base: base}
	resp := post(t, context.Background(), base, clientKey, plainBody)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	events, raw := c.waitUsageEvents(t, 1)
	want := []usageTried{{ids["only"], ext, "gpt-test", "rate_limit"}}
	if events[0].Attempts != 1 || !slices.Equal(events[0].Tried, want) {
		t.Fatalf("event %s, want %+v", raw, want)
	}
}

// A stream that breaks after it began ends the list with the error's code.
func TestUsageStreamBrokenAfterItBeganEndsTriedWithTheErrorCode(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// The reply ends with no finish and no [DONE].
	}
	base := startExplainCore(t, u, nil, explainAccount{id: "only", key: "sk-a"})
	ids, ext := publicIDs(t, base)
	c := &mcore{base: base}
	code, _, _ := chatReply(t, base, clientKey, true)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	events, raw := c.waitUsageEvents(t, 1)
	e := events[0]
	if !e.Failed || e.ErrorCode == "" || e.Status != http.StatusOK || e.Attempts != 1 || len(e.Tried) != 1 ||
		e.Tried[0] != (usageTried{ids["only"], ext, "gpt-4o", e.ErrorCode}) {
		t.Fatalf("event %s", raw)
	}
}
