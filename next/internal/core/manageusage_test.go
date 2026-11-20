package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/management"
)

func writeRawUsageDay(t testing.TB, usageDir string, day time.Time, events []usage.Event) {
	t.Helper()
	if err := os.MkdirAll(usageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(usageDir, day.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	type row struct {
		V int `json:"v"`
		usage.Event
	}
	for _, ev := range events {
		b, err := json.Marshal(row{V: 1, Event: ev})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func startTestCoreWithUsage(t testing.TB, setup func(usageDir string)) *Core {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-management-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(UsageDir(cfgPath))
	}
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

func callUsageMgmt(t testing.TB, c *Core, path string, out any) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, management.Prefix+path, nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer fake-management-key")
	rec := httptest.NewRecorder()
	c.Management().ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: unmarshal %s: %v", path, rec.Body.String(), err)
		}
	}
	return rec.Code, rec.Body.Bytes()
}

func TestManageUsageRangesByKeyAndBefore(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	fiveDaysAgo := now.Add(-5 * 24 * time.Hour)
	tenDaysAgo := now.Add(-10 * 24 * time.Hour)

	c := startTestCoreWithUsage(t, func(usageDir string) {
		in10 := int64(10)
		out5 := int64(5)
		tot15 := int64(15)
		writeRawUsageDay(t, usageDir, tenDaysAgo, []usage.Event{
			{
				Seq: 1, Timestamp: tenDaysAgo, Endpoint: "/v1/chat/completions", Model: "m-alpha",
				Provider: "p/a", Account: "acct-1", KeyID: "key-1", LatencyMS: 100,
				Tokens: usage.Tokens{Input: &in10, Output: &out5, Total: &tot15},
			},
		})
		writeRawUsageDay(t, usageDir, fiveDaysAgo, []usage.Event{
			{
				Seq: 2, Timestamp: fiveDaysAgo, Endpoint: "/v1/chat/completions", Model: "m-beta",
				Provider: "p/b", Account: "acct-2", KeyID: "key-2", Failed: true, LatencyMS: 200,
				Tokens: usage.TokensOf(&llm.Usage{InputTokens: &in10, OutputTokens: &out5}),
			},
		})
	})

	// Add 3 events today so we can test before paging too.
	for i := range 3 {
		c.usage.Add(usage.Event{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Model:     fmt.Sprintf("today-%d", i),
			Provider:  "p/a",
			Account:   "acct-1",
			KeyID:     "key-1",
		})
	}

	// since=30d, by=account, daily buckets (>48h).
	var reply map[string]any
	code, raw := callUsageMgmt(t, c, "/usage?since=30d&by=account", &reply)
	if code != http.StatusOK {
		t.Fatalf("GET /usage?since=30d&by=account: status %d: %s", code, raw)
	}
	if int(reply["total_requests"].(float64)) != 5 {
		t.Fatalf("total_requests = %v, want 5; body=%s", reply["total_requests"], raw)
	}
	if reply["by"] != "account" {
		t.Fatalf("by = %v, want account; body=%s", reply["by"], raw)
	}
	breakdown, _ := reply["breakdown"].([]any)
	if len(breakdown) != 2 {
		t.Fatalf("breakdown len = %d, want 2: %s", len(breakdown), raw)
	}
	if int(reply["bucket_seconds"].(float64)) != 86400 {
		t.Fatalf("bucket_seconds = %v, want 86400 for 30d range", reply["bucket_seconds"])
	}

	// key alias filter
	var keyFiltered management.UsageReply
	code, raw = callUsageMgmt(t, c, "/usage?since=30d&key=key-2", &keyFiltered)
	if code != http.StatusOK || keyFiltered.Requests != 1 || len(keyFiltered.Keys) != 1 || keyFiltered.Keys[0].Name != "key-2" {
		t.Fatalf("GET /usage?since=30d&key=key-2: code=%d body=%s", code, raw)
	}

	// <= 48h defaults to hourly buckets.
	var twoDay management.UsageReply
	code, raw = callUsageMgmt(t, c, "/usage?since=2d", &twoDay)
	if code != http.StatusOK || twoDay.BucketSeconds != 3600 {
		t.Fatalf("GET /usage?since=2d: code=%d bucket_seconds=%d body=%s", code, twoDay.BucketSeconds, raw)
	}

	// Invalid by and invalid since=0d.
	for _, bad := range []string{"/usage?by=bogus", "/usage?since=0d", "/usage?key=a&key_id=b", "/usage/events?after=1&before=5", "/usage/events?before=bad"} {
		code, _ := callUsageMgmt(t, c, bad, nil)
		if code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}

	// GET /usage/events?before=<seq>&limit=2 newest first.
	var page1 management.UsageEventsReply
	code, raw = callUsageMgmt(t, c, "/usage/events?before=0&limit=2", &page1)
	if code != http.StatusOK || len(page1.Events) != 2 || !page1.More ||
		page1.Events[0].Model != "today-2" || page1.Events[1].Model != "today-1" || page1.Next != page1.Events[1].Seq {
		t.Fatalf("GET /usage/events?before=0&limit=2: code=%d page=%+v body=%s", code, page1, raw)
	}
	var page2 management.UsageEventsReply
	code, raw = callUsageMgmt(t, c, fmt.Sprintf("/usage/events?before=%d&limit=2", page1.Next), &page2)
	if code != http.StatusOK || len(page2.Events) != 1 || page2.More || page2.Events[0].Model != "today-0" {
		t.Fatalf("GET /usage/events?before=%d&limit=2: code=%d page=%+v body=%s", page1.Next, code, page2, raw)
	}
}

func seedMonthUsage5kPerDay(t testing.TB) (*Core, string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Hour)
	models := []string{"claude-sonnet", "gpt-5", "gemini-pro", "codex-mini", "haiku"}
	providers := []string{"p/claude", "p/openai", "p/gemini", "p/codex", "p/claude"}
	accounts := []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"}
	keys := []string{"k-1", "k-2", "k-3", "k-4"}
	inTok, outTok, reasTok, cachedTok, cwTok, totTok := int64(120), int64(45), int64(10), int64(30), int64(15), int64(165)

	var cfgPath string
	c := startTestCoreWithUsage(t, func(usageDir string) {
		cfgPath = filepath.Join(filepath.Dir(usageDir), "config.json")
		var seq uint64
		for d := 29; d >= 0; d-- {
			dayStart := now.Truncate(24*time.Hour).AddDate(0, 0, -d)
			evs := make([]usage.Event, 0, 5000)
			for i := range 5000 {
				seq++
				hr := i % 24
				ts := dayStart.Add(time.Duration(hr)*time.Hour + time.Duration(i%60)*time.Second)
				if d == 0 && ts.After(now) {
					ts = dayStart.Add(time.Duration(i%60) * time.Second)
				}
				mIdx := i % len(models)
				evs = append(evs, usage.Event{
					Seq:       seq,
					Timestamp: ts,
					Endpoint:  "/v1/chat/completions",
					Model:     models[mIdx],
					Provider:  providers[mIdx],
					Account:   accounts[i%len(accounts)],
					KeyID:     keys[i%len(keys)],
					Failed:    i%20 == 0,
					LatencyMS: int64(50 + (i % 100)),
					Tokens: usage.Tokens{
						Input:      &inTok,
						Output:     &outTok,
						Reasoning:  &reasTok,
						Cached:     &cachedTok,
						CacheWrite: &cwTok,
						Total:      &totTok,
					},
				})
			}
			writeRawUsageDay(t, usageDir, dayStart, evs)
		}
	})
	return c, cfgPath
}

func TestManageUsage30Days5kPerDayTiming(t *testing.T) {
	c, _ := seedMonthUsage5kPerDay(t)

	// Build summaries on disk first (simulating warmed summaries before user query).
	var warm management.UsageReply
	code, raw := callUsageMgmt(t, c, "/usage?since=30d&by=account", &warm)
	if code != http.StatusOK || warm.Requests != 150000 || len(warm.Breakdown) != 8 {
		t.Fatalf("warmup GET /usage?since=30d&by=account: code=%d reqs=%d body=%s", code, warm.Requests, raw)
	}

	// Measure with summaries already built on disk and cached in memory.
	start := time.Now()
	var reply management.UsageReply
	code, raw = callUsageMgmt(t, c, "/usage?since=30d&by=account", &reply)
	elapsed := time.Since(start)
	if code != http.StatusOK || reply.Requests != 150000 || reply.By != "account" || len(reply.Breakdown) != 8 {
		t.Fatalf("GET /usage?since=30d&by=account: code=%d body=%s", code, raw)
	}
	if elapsed >= time.Second {
		t.Fatalf("GET /usage?since=30d&by=account took %v, want well under 1s", elapsed)
	}
}

func BenchmarkManageUsage30Days5kPerDay(b *testing.B) {
	c, cfgPath := seedMonthUsage5kPerDay(b)

	// Build summaries on disk once.
	var warm management.UsageReply
	code, _ := callUsageMgmt(b, c, "/usage?since=30d&by=account", &warm)
	if code != http.StatusOK || warm.Requests != 150000 {
		b.Fatalf("warmup failed: code=%d reqs=%d", code, warm.Requests)
	}

	b.Run("WarmMemorySummaries", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			req := httptest.NewRequest(http.MethodGet, management.Prefix+"/usage?since=30d&by=account", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer fake-management-key")
			rec := httptest.NewRecorder()
			c.Management().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		}
	})

	b.StopTimer()
	c.Stop()

	// Also measure a cold Core restart reading the 29 closed summary/*.json files + 5,000 today events from disk.
	b.Run("ColdDiskSummariesAfterRestart", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			c2, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
			if err != nil {
				b.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodGet, management.Prefix+"/usage?since=30d&by=account", nil)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer fake-management-key")
			rec := httptest.NewRecorder()
			b.StartTimer()
			c2.Management().ServeHTTP(rec, req)
			b.StopTimer()
			c2.Stop()
			if rec.Code != http.StatusOK {
				b.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		}
	})
}
