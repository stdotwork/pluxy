package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// listedAccount is an account of GET /accounts, as far as the demo test reads it.
type listedAccount struct {
	ID        string `json:"id"`
	Extension string `json:"extension"`
	Source    string `json:"source"`
	Health    struct {
		State string `json:"state"`
	} `json:"health"`
}

// panelcore -demo fills a real core with accounts in every state, usage over a week, three client
// keys, a chain and request logs, as its package comment says; the test reads it all back through
// the management API. Without -demo the one line it prints has the same four members.
func TestPanelcoreDemo(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "panelcore")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./panelcore")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./panelcore: %v\n%s", err, out)
	}
	// -listen picks the port: a free one, found the way the other tests' cores find theirs.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := ln.Addr().String()
	ln.Close()

	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, "-demo", "-listen", listen)
	cmd.Env = append(os.Environ(), testHomeEnv(home)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 15 * time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close() // panelcore stops core when its stdin ends
		_ = cmd.Wait()
		cancel()
		if t.Failed() {
			t.Logf("panelcore's stderr:\n%s", stderr.String())
		}
	})

	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	var line string
	select {
	case line = <-lines:
	case <-time.After(3 * time.Minute):
		t.Fatal("panelcore -demo printed no line within 3 minutes")
	}
	var ready map[string]string
	if err := json.Unmarshal([]byte(line), &ready); err != nil {
		t.Fatalf("panelcore's first line is not JSON: %v: %s", err, line)
	}
	for _, k := range []string{"base", "key", "client_key", "config"} {
		if ready[k] == "" {
			t.Errorf("panelcore's line has no %q: %s", k, line)
		}
	}
	if len(ready) != 4 {
		t.Errorf("panelcore's line has %d members, want the 4 it prints without -demo: %s", len(ready), line)
	}
	if want := "http://" + listen; ready["base"] != want {
		t.Errorf("base = %q, want %q (-listen)", ready["base"], want)
	}
	base, key := ready["base"], ready["key"]
	get := func(path string, out any) {
		t.Helper()
		code, raw := managedJSON(t, base, key, http.MethodGet, path, nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d: %s", path, code, raw)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, raw)
		}
	}

	// Accounts: 8 to 12, of at least three providers, in every state the product has but a
	// broken credential file, with a very long name among them.
	var accounts struct {
		Accounts []listedAccount `json:"accounts"`
	}
	get("/accounts", &accounts)
	if n := len(accounts.Accounts); n < 8 || n > 12 {
		t.Errorf("%d accounts, want 8 to 12", n)
	}
	states, providers, longest := map[string][]string{}, map[string]bool{}, ""
	for _, a := range accounts.Accounts {
		states[a.Health.State] = append(states[a.Health.State], a.ID)
		providers[a.Extension] = true
		if len(a.ID) > len(longest) {
			longest = a.ID
		}
	}
	for _, state := range []string{"ok", "cooling", "exhausted", "needs_login", "banned", "disabled"} {
		if len(states[state]) == 0 {
			t.Errorf("no account is %s; states: %v", state, states)
		}
	}
	if len(providers) < 3 {
		t.Errorf("accounts of %d providers, want at least 3: %v", len(providers), providers)
	}
	if len(longest) < 60 {
		t.Errorf("the longest account ID is %q, want one of 60 characters or more", longest)
	}
	sources := map[string]bool{}
	for _, a := range accounts.Accounts {
		sources[a.Source] = true
	}
	if !sources["config"] || !sources["login"] {
		t.Errorf("account sources %v, want config and login", sources)
	}

	// Quota: windows near their limit on one account, none on another.
	var quota struct {
		Quota struct {
			Limits []struct {
				ID          string  `json:"id"`
				UsedPercent float64 `json:"used_percent"`
			} `json:"limits"`
		} `json:"quota"`
	}
	get("/accounts/aurora-pro/quota", &quota)
	if l := quota.Quota.Limits; len(l) != 2 || l[0].UsedPercent < 90 {
		t.Errorf("aurora-pro quota %+v, want two windows with the five-hour one near its limit", l)
	}
	get("/accounts/borealis-main/quota", &quota)
	if len(quota.Quota.Limits) != 0 {
		t.Errorf("borealis-main quota %+v, want no windows", quota.Quota.Limits)
	}

	// Client keys and the chain with its fallback.
	var keys struct {
		Keys []struct{ ID string } `json:"keys"`
	}
	get("/client-keys", &keys)
	if len(keys.Keys) != 3 {
		t.Errorf("%d client keys, want 3", len(keys.Keys))
	}
	var chains struct {
		Chains []struct {
			Name  string            `json:"name"`
			Steps []json.RawMessage `json:"steps"`
		} `json:"chains"`
	}
	get("/chains", &chains)
	if len(chains.Chains) != 1 || len(chains.Chains[0].Steps) < 2 {
		t.Errorf("chains %+v, want one with a fallback step", chains.Chains)
	}

	// Usage over the last week: several models with tokens, three keys, failures, and days.
	var sum usageSummary
	get("/usage?since=30d&bucket=24h", &sum)
	if sum.Requests < 300 || sum.Failure == 0 || sum.Tokens.Total == 0 {
		t.Errorf("usage: %d requests, %d failed, %d tokens; want 300 or more requests, some failed, tokens", sum.Requests, sum.Failure, sum.Tokens.Total)
	}
	withTokens, long := 0, false
	for _, m := range sum.Models {
		if m.Tokens.Total > 0 {
			withTokens++
		}
		long = long || len(m.Name) > 60
	}
	if withTokens < 5 || !long {
		t.Errorf("%d models with tokens, a long model name: %v; want at least 5 and true (%+v)", withTokens, long, sum.Models)
	}
	if len(sum.Keys) != 3 || len(sum.Providers) < 3 {
		t.Errorf("usage by key %d, by provider %d; want 3 and at least 3", len(sum.Keys), len(sum.Providers))
	}
	days := 0
	for _, b := range sum.Series {
		if b.Requests > 0 {
			days++
		}
	}
	if days < 7 {
		t.Errorf("requests on %d days, want at least 7", days)
	}
	// The accounts of the usage are the accounts listed: no events under a name that is not one.
	for _, g := range sum.Accounts {
		if !slices.ContainsFunc(accounts.Accounts, func(a listedAccount) bool { return a.ID == g.Name }) {
			t.Errorf("usage names the account %q, which is not listed", g.Name)
		}
	}

	// Events: requests that fell back to another account, and failed ones with their request logs.
	var events struct {
		Events []struct {
			ID       string `json:"id"`
			Failed   bool   `json:"failed"`
			Attempts int    `json:"attempts"`
		} `json:"events"`
	}
	get("/usage/events?before=0&limit=1000", &events)
	var fellBack, failedWithLog int
	for _, e := range events.Events {
		if e.Attempts > 1 && !e.Failed {
			fellBack++
		}
		if e.Failed && e.ID != "" {
			code, raw := managedJSON(t, base, key, http.MethodGet, "/request-log-by-id/"+e.ID, nil)
			if code != http.StatusOK || !strings.Contains(string(raw), e.ID) {
				t.Errorf("request log of failed request %s: %d: %.200s", e.ID, code, raw)
				break
			}
			failedWithLog++
		}
	}
	if fellBack == 0 || failedWithLog == 0 {
		t.Errorf("%d requests fell back, %d failed requests have a log; want some of both (of %d events)", fellBack, failedWithLog, len(events.Events))
	}
}
