package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/usage"
)

// TestPanelcoreDemoUsageDays checks the day files the demo writes for the clock it starts at, with
// the start time given in zones ahead of and behind UTC. The files are UTC days, as core reads
// them, so the demo must name them by the UTC date whatever the host's zone is.
func TestPanelcoreDemoUsageDays(t *testing.T) {
	sydney := time.FixedZone("AEDT", 11*60*60)
	losAngeles := time.FixedZone("PST", -8*60*60)
	cases := []struct {
		name  string
		now   time.Time
		today string // the UTC day of now
	}{
		{"UTC evening", time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC), "2026-11-26"},
		// 21:00 UTC on the 26th is 08:00 on the 27th in Sydney: the local date is ahead.
		{"Sydney morning, local date ahead", time.Date(2026, 11, 26, 21, 0, 0, 0, time.UTC).In(sydney), "2026-11-26"},
		// 02:30 UTC on the 27th is 13:30 on the 27th in Sydney, and 18:30 on the 26th in Los Angeles,
		// where the local date is behind.
		{"Los Angeles evening, local date behind", time.Date(2026, 11, 27, 2, 30, 0, 0, time.UTC).In(losAngeles), "2026-11-27"},
		{"UTC after midnight", time.Date(2026, 11, 27, 2, 30, 0, 0, time.UTC), "2026-11-27"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			setup := &demoSetup{now: tc.now, keys: []string{"key-a", "key-b", "key-c"}}
			if err := writeDemoUsage(dir, setup); err != nil {
				t.Fatalf("writeDemoUsage: %v", err)
			}
			today, err := time.Parse("2006-01-02", tc.today)
			if err != nil {
				t.Fatal(err)
			}
			// Eight UTC days: today and the seven before it, each in its own file.
			entries, err := os.ReadDir(filepath.Join(dir, "usage"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 8 {
				t.Errorf("%d usage files, want 8", len(entries))
			}
			for back := 7; back >= 0; back-- {
				day := today.AddDate(0, 0, -back)
				name := day.Format("2006-01-02") + ".jsonl"
				raw, err := os.ReadFile(filepath.Join(dir, "usage", name))
				if err != nil {
					t.Errorf("usage file for UTC day %s: %v", day.Format("2006-01-02"), err)
					continue
				}
				text := strings.TrimSpace(string(raw))
				if text == "" {
					// Today's events are the ones before the demo's start less five minutes, so an
					// early hour can leave none; a past day always has its events.
					if back > 0 {
						t.Errorf("%s has no events", name)
					}
					continue
				}
				for _, line := range strings.Split(text, "\n") {
					var e struct {
						usage.Event
					}
					if err := json.Unmarshal([]byte(line), &e); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if e.Timestamp.Before(day) || !e.Timestamp.Before(day.AddDate(0, 0, 1)) {
						t.Errorf("%s has an event at %s, outside its UTC day", name, e.Timestamp.UTC())
						break
					}
					if back == 0 && !e.Timestamp.Before(tc.now.Add(-5*time.Minute)) {
						t.Errorf("today's file has an event at %s, at or after the demo's start less five minutes (%s)", e.Timestamp.UTC(), tc.now.UTC().Add(-5*time.Minute))
						break
					}
				}
			}
		})
	}
}
