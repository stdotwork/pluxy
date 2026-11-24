package main

// Demo mode (-demo): a core that looks lived in. The accounts, the chain and the requests go
// through core itself: the accounts are in the config or made through the management API, and
// the requests are real ones sent to core, which routes them to the test extension, so the usage
// record, the request logs and the routing state (a cooldown, a ban, an exhausted window) are what
// core makes of them. Everything is seeded: the same accounts, the same requests in the same
// order, every run.
//
// The one thing a request cannot make is a past: core stamps an event with the time it arrives,
// and nothing in core moves its clock. So the week before the demo's requests is written into
// core's usage folder as core's own day files (writeDemoUsage), before core starts, the way
// e2e/usage_test.go writes a closed day; core reads yesterday's and today's into its record when
// it starts and adds up the older days when the usage is read. The client key IDs in those events
// come from core's own key (configdoc.LoadKeyID), written by that same code.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
)

// The demo providers: the test extension under other names.
var demoPackages = []pkg{
	{id: "demo/aurora", version: "0.3.0", file: "aurora.tar.gz", title: "Aurora", summary: "Fake provider with a usage window", kind: "provider", tier: "verified"},
	{id: "demo/borealis", version: "1.2.0", file: "borealis.tar.gz", title: "Borealis", summary: "Fake provider with no usage windows", kind: "provider", tier: "verified"},
}

// The demo's models, as clients name them.
const (
	modelLarge  = "gpt-demo-large"
	modelMini   = "gpt-demo-mini"
	modelSonnet = "claude-demo-sonnet"
	modelHaiku  = "claude-demo-haiku"
	modelPro    = "gemini-demo-pro"
	// modelFlash always fails: its only account answers it with a server error.
	modelFlash = "gemini-demo-flash"
	// modelLong is a model name as long as a real one can get, for the panel's layout.
	modelLong = "gemini-demo-pro-preview-with-an-unusually-long-model-name-for-layout-2026-11-23-extended-context"
	// chainName is the fallback chain: Aurora's exhausted account first, so the chain falls to Claude.
	chainName = "demo-best"
	// longAccount is an account ID as long as a real one can get.
	longAccount = "alpha-shared-research-workspace-account-with-a-deliberately-very-long-name-0123456789"
)

// demoAccount is a config account of the demo: its extension and the account entry of the config.
type demoAccount struct {
	extension string
	entry     map[string]any
}

// demoSetup is what the demo needs to know before core starts: the time it starts at and its
// client keys (the config's, then two more).
type demoSetup struct {
	now  time.Time
	keys []string
}

// demoSettings returns the account settings every demo account gets: usage reported, and the
// two windows' resets in the future. Each of extra's fields is added.
func demoSettings(now time.Time, extra map[string]any) map[string]any {
	s := map[string]any{
		"report_usage":        true,
		"five_hour_resets_at": now.Add(2 * time.Hour).Unix(),
		"weekly_resets_at":    now.Add(3 * 24 * time.Hour).Unix(),
	}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

// aliased is a model entry that answers to the name clients use while the extension is asked
// for upstream, one of the test extension's models that fail on purpose.
func aliased(upstream, alias string) map[string]string {
	return map[string]string{"name": upstream, "alias": alias}
}

// demoConfigAccounts are the demo's accounts that the config holds, besides the three of the
// default config. The test extension answers a model by its name, so an account that lists
// "rate-limited" under the alias gpt-demo-large fails that model with a rate limit, and one that
// lists "account-closed" has its account closed: both are the ways a provider really answers.
func demoConfigAccounts(now time.Time) []demoAccount {
	account := func(id string, priority int, models []any, settings map[string]any) map[string]any {
		return map[string]any{
			"id": id, "priority": priority, "models": models, "settings": demoSettings(now, settings),
			// Opaque, so core's redaction of the secret's parts never cuts a word out of a message.
			"credential": map[string]any{"kind": "api_key", "api_key": fmt.Sprintf("demo-%08x", crc32.ChecksumIEEE([]byte(id)))},
		}
	}
	return []demoAccount{
		// Aurora: the exhausted account is tried first and the rate-limited model second, so a
		// request for gpt-demo-large falls through both to aurora-pro, which is near both limits.
		{"demo/aurora", account("aurora-exhausted", 30, []any{modelLarge},
			map[string]any{"exhausted_until": now.Add(3 * time.Hour).Unix()})},
		{"demo/aurora", account("aurora-team", 20, []any{aliased("rate-limited", modelLarge), modelMini},
			map[string]any{"five_hour_percent": 35, "weekly_percent": 18})},
		{"demo/aurora", account("aurora-pro", 10, []any{modelLarge, modelMini},
			map[string]any{"five_hour_percent": 93, "weekly_percent": 71})},
		// Borealis: the closed account is tried first, then the one that answers.
		{"demo/borealis", account("borealis-closed", 20, []any{aliased("account-closed", modelSonnet)}, nil)},
		{"demo/borealis", account("borealis-main", 10, []any{modelSonnet, modelHaiku},
			map[string]any{"no_limits": true})},
		// Alpha: the account with the long name serves the long model, and the model that fails.
		{"test/alpha", account(longAccount, 10, []any{modelPro, modelLong, aliased("server-error", modelFlash)},
			map[string]any{"five_hour_percent": 64, "weekly_percent": 97})},
	}
}

// demoConfig adds the demo's parts to the default config: its client keys, request logs for every
// request, and the demo accounts of the config.
func demoConfig(cfg map[string]any, setup *demoSetup) {
	now := setup.now
	cfg["client_keys"] = setup.keys
	cfg["logs"] = map[string]any{"to_file": true, "request_log": true}
	exts := cfg["extensions"].(map[string]any)
	for _, a := range demoConfigAccounts(now) {
		ext, _ := exts[a.extension].(map[string]any)
		if ext == nil {
			ext = map[string]any{}
			exts[a.extension] = ext
		}
		ext["accounts"] = append(accountsOf(ext), a.entry)
	}
}

func accountsOf(ext map[string]any) []any {
	l, _ := ext["accounts"].([]any)
	return l
}

// demo drives one running core.
type demo struct {
	base, key string
	http      *http.Client
	rng       *rand.Rand
	now       time.Time
}

// seedDemo fills the running core at base with the demo's accounts of a sign-in, its chain and its
// requests, and returns when it is done.
func seedDemo(base, key string, setup *demoSetup) error {
	d := &demo{base: base, key: key, http: &http.Client{Timeout: startTimeout}, rng: rand.New(rand.NewPCG(20261123, 7)), now: setup.now}
	if err := d.loginAccounts(); err != nil {
		return err
	}
	if err := d.call(http.MethodPut, "/chains/"+chainName, map[string]any{"steps": []any{
		map[string]any{"model": modelLarge, "provider": "demo/aurora", "accounts": []string{"aurora-exhausted"}},
		map[string]any{"model": modelHaiku, "provider": "demo/borealis"},
	}}, nil); err != nil {
		return fmt.Errorf("demo chain: %w", err)
	}
	return d.requests(setup.keys)
}

// call sends a management request with body (when not nil) and reads the reply into out (when
// not nil).
func (d *demo) call(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, d.base+"/v0/management"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// loginAccounts adds the two accounts only a sign-in makes: one signed in at a provider that then
// revoked the sign-in (core learns of it when it renews the credential, and the account needs
// a new login), and one a person turned off.
func (d *demo) loginAccounts() error {
	add := func(id, extension, method string, input map[string]string, fields map[string]any) (string, error) {
		body := map[string]any{"id": id, "extension": extension, "method": method, "input": input, "settings": demoSettings(d.now, nil)}
		for k, v := range fields {
			body[k] = v
		}
		var reply struct {
			Login struct{ ID string } `json:"login"`
		}
		if err := d.call(http.MethodPost, "/accounts", body, &reply); err != nil {
			return "", fmt.Errorf("demo account %s: %w", id, err)
		}
		return reply.Login.ID, nil
	}
	// The browser sign-in waits for the address the browser ends on.
	login, err := add("aurora-old-login", "demo/aurora", "browser", nil, map[string]any{"priority": 10, "models": []any{modelMini}})
	if err != nil {
		return err
	}
	if err := d.waitLogin(login, "pending"); err != nil {
		return err
	}
	if err := d.call(http.MethodPost, "/logins/"+login+"/input", map[string]any{"input": map[string]string{"redirect_url": "https://login.example.test/callback?code=revoked"}}, nil); err != nil {
		return fmt.Errorf("demo account aurora-old-login: %w", err)
	}
	if err := d.waitLogin(login, "done"); err != nil {
		return err
	}
	// The provider has revoked the sign-in: renewing the credential is refused, for good.
	_ = d.call(http.MethodPost, "/accounts/aurora-old-login/refresh", nil, nil)

	login, err = add("borealis-paused", "demo/borealis", "api_key", map[string]string{"api_key": "demo-6b1d3f09", "label": "Paused account"},
		map[string]any{"priority": 10, "models": []any{modelHaiku}})
	if err != nil {
		return err
	}
	if err := d.waitLogin(login, "done"); err != nil {
		return err
	}
	return d.call(http.MethodPatch, "/accounts/borealis-paused", map[string]any{"disabled": true}, nil)
}

// waitLogin waits until the sign-in login is in state.
func (d *demo) waitLogin(login, state string) error {
	deadline := time.Now().Add(startTimeout)
	var last error
	for time.Now().Before(deadline) {
		var reply struct {
			Login struct{ State string } `json:"login"`
		}
		last = d.call(http.MethodGet, "/logins/"+login, nil, &reply)
		if last == nil {
			if reply.Login.State == state {
				return nil
			}
			last = fmt.Errorf("state %s", reply.Login.State)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("demo sign-in %s did not reach %s within %s (last seen: %v)", login, state, startTimeout, last)
}

// A client request: the API path it goes to, and its body.
type clientRequest struct {
	path string
	body map[string]any
}

var words = strings.Fields("the quick brown fox jumps over lazy dog while server routes request through account window limit " +
	"model reply stream token usage cache chain fallback cooldown quota reset panel proxy upstream client key log retry")

func (d *demo) prompt(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(words[d.rng.IntN(len(words))])
	}
	return b.String()
}

// build returns the request that asks model through the API the model's family speaks.
func (d *demo) build(model string, stream bool) clientRequest {
	text := d.prompt(4 + d.rng.IntN(120))
	maxTokens := []int{64, 128, 256, 512, 1024, 2048}[d.rng.IntN(6)]
	switch {
	case strings.HasPrefix(model, "claude"):
		return clientRequest{"/v1/messages", map[string]any{"model": model, "max_tokens": maxTokens, "stream": stream,
			"messages": []any{map[string]any{"role": "user", "content": text}}}}
	case strings.HasPrefix(model, "gemini"):
		method := ":generateContent"
		if stream {
			method = ":streamGenerateContent?alt=sse"
		}
		return clientRequest{"/v1beta/models/" + url.PathEscape(model) + method, map[string]any{
			"contents":         []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}},
			"generationConfig": map[string]any{"maxOutputTokens": maxTokens}}}
	case strings.HasPrefix(model, "gpt") && d.rng.IntN(3) == 0:
		return clientRequest{"/v1/responses", map[string]any{"model": model, "input": text, "max_output_tokens": maxTokens, "stream": stream}}
	}
	return clientRequest{"/v1/chat/completions", map[string]any{"model": model, "max_tokens": maxTokens, "stream": stream,
		"messages": []any{map[string]any{"role": "user", "content": text}}}}
}

// ask sends one client request with key and reads the whole reply.
func (d *demo) ask(key string, r clientRequest) (int, error) {
	b, err := json.Marshal(r.body)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodPost, d.base+r.path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// requests sends the demo's requests to core. The first of each kind makes core's routing state:
// the exhausted account, the rate-limited model and the closed account. After them the weighted
// mix goes through the three keys; a failure the demo expects (the failing model, a model no
// account serves) is not an error here, any other one is.
func (d *demo) requests(keys []string) error {
	send := func(model string, key string, stream, expectFail bool) error {
		code, err := d.ask(key, d.build(model, stream))
		if err != nil {
			return fmt.Errorf("demo request for %s: %w", model, err)
		}
		if code/100 != 2 && !expectFail {
			return fmt.Errorf("demo request for %s: status %d", model, code)
		}
		return nil
	}
	// The three first requests: each tries an account that fails, then one that answers.
	for _, model := range []string{chainName, modelLarge, modelSonnet} {
		if err := send(model, keys[0], false, false); err != nil {
			return err
		}
	}
	mix := []struct {
		model  string
		weight int
		fail   bool
	}{
		{modelLarge, 14, false}, {modelMini, 18, false}, {modelSonnet, 14, false}, {modelHaiku, 12, false},
		{modelPro, 9, false}, {modelLong, 4, false}, {chainName, 6, false}, {"fake-model", 3, false}, {"alpha-model", 4, false},
		{modelFlash, 7, true},
		{"gpt-demo-ultra", 2, true}, // no account serves it
	}
	var total int
	for _, m := range mix {
		total += m.weight
	}
	const n = 110
	for i := 0; i < n; i++ {
		pick := d.rng.IntN(total)
		m := mix[0]
		for _, c := range mix {
			if pick < c.weight {
				m = c
				break
			}
			pick -= c.weight
		}
		key := keys[d.rng.IntN(len(keys))]
		if err := send(m.model, key, d.rng.IntN(4) == 0, m.fail); err != nil {
			return err
		}
		if i == n/2 {
			// A person lets the rate-limited account try again: its next request for gpt-demo-large falls back again.
			if err := d.call(http.MethodPost, "/accounts/aurora-team/cooldown/reset", nil, nil); err != nil {
				return fmt.Errorf("demo cooldown reset: %w", err)
			}
			if err := send(modelLarge, key, false, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// dayWeight says how busy each hour of the day is (UTC): quiet nights, a working day.
var dayWeight = [24]int{1, 1, 1, 1, 1, 2, 3, 5, 8, 10, 11, 10, 8, 9, 11, 11, 10, 8, 6, 5, 4, 3, 2, 1}

// pastModel is a model the demo's accounts served before the demo's own requests, and which.
type pastModel struct {
	name, provider, endpoint string
	accounts                 []string
	weight                   int
}

var pastModels = []pastModel{
	{modelLarge, "demo/aurora", "/v1/chat/completions", []string{"aurora-pro", "aurora-pro", "aurora-team", "aurora-exhausted"}, 18},
	{modelMini, "demo/aurora", "/v1/chat/completions", []string{"aurora-pro", "aurora-team", "aurora-old-login"}, 24},
	{modelSonnet, "demo/borealis", "/v1/messages", []string{"borealis-main", "borealis-closed"}, 18},
	{modelHaiku, "demo/borealis", "/v1/messages", []string{"borealis-main", "borealis-paused"}, 14},
	{modelPro, "test/alpha", "/v1beta/models/" + modelPro + ":generateContent", []string{longAccount}, 10},
	{modelLong, "test/alpha", "/v1beta/models/" + modelLong + ":generateContent", []string{longAccount}, 4},
	{modelFlash, "test/alpha", "/v1beta/models/" + modelFlash + ":generateContent", []string{longAccount}, 5},
	{"alpha-model", "test/alpha", "/v1/chat/completions", []string{"alpha-1", "alpha-2"}, 5},
	{"fake-model", "test/local", "/v1/chat/completions", []string{"local-1"}, 2},
}

// writeDemoUsage writes the usage of the seven days before the demo's requests, and of today up to
// then, into core's usage folder, as core's own day files, with the accounts, models and client
// keys the demo has. Call it before core starts. dir is the folder of core's config.
func writeDemoUsage(dir string, setup *demoSetup) error {
	ids, err := configdoc.LoadKeyID(filepath.Join(dir, "state", "key-id.key"))
	if err != nil {
		return err
	}
	var keyIDs []string
	for _, k := range setup.keys {
		keyIDs = append(keyIDs, ids.ID(k))
	}
	usageDir := filepath.Join(dir, "usage")
	if err := os.MkdirAll(usageDir, 0o700); err != nil {
		return err
	}
	rng := rand.New(rand.NewPCG(20261122, 3))
	var total int
	for _, m := range pastModels {
		total += m.weight
	}
	// Today's events end a few minutes before core starts, so the demo's own requests come last.
	end := setup.now.Add(-5 * time.Minute)
	today := time.Date(setup.now.Year(), setup.now.Month(), setup.now.Day(), 0, 0, 0, 0, time.UTC)
	var seq uint64
	for back := 7; back >= 0; back-- {
		day := today.AddDate(0, 0, -back)
		// A busier week towards today, and a quiet weekend.
		count := 50 + (7-back)*6
		if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
			count /= 3
		}
		if back == 0 {
			count = 100 * int(end.Sub(today)/time.Minute) / (24 * 60)
		}
		events := make([]usage.Event, 0, count)
		for range count {
			e := pastEvent(rng, day, total, keyIDs[rng.IntN(len(keyIDs))])
			if e.Timestamp.Before(end) {
				events = append(events, e)
			}
		}
		slices.SortFunc(events, func(a, b usage.Event) int { return a.Timestamp.Compare(b.Timestamp) })
		var buf bytes.Buffer
		for _, e := range events {
			seq++
			e.Seq = seq
			b, err := json.Marshal(struct {
				V int `json:"v"`
				usage.Event
			}{1, e})
			if err != nil {
				return err
			}
			buf.Write(append(b, '\n'))
		}
		if err := os.WriteFile(filepath.Join(usageDir, day.Format("2006-01-02")+".jsonl"), buf.Bytes(), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pastEvent makes one event of day: at an hour by how busy the hours are, a model by weight, one
// of its accounts, and now and then a failure.
func pastEvent(rng *rand.Rand, day time.Time, total int, keyID string) usage.Event {
	hour := 0
	for pick, sum := rng.IntN(100), 0; hour < 23; hour++ {
		if sum += dayWeight[hour]; pick < sum {
			break
		}
	}
	ts := day.Add(time.Duration(hour)*time.Hour + time.Duration(rng.IntN(3600))*time.Second)
	pick := rng.IntN(total)
	m := pastModels[0]
	for _, c := range pastModels {
		if pick < c.weight {
			m = c
			break
		}
		pick -= c.weight
	}
	in, out := int64(20+rng.IntN(1800)), int64([]int{64, 128, 256, 512, 1024, 2048}[rng.IntN(6)])
	e := usage.Event{
		Timestamp: ts, Endpoint: m.endpoint, Stream: rng.IntN(4) == 0, Model: m.name, Provider: m.provider,
		Account: m.accounts[rng.IntN(len(m.accounts))], Attempts: 1, KeyID: keyID, ClientIP: "127.0.0.1",
		Status: http.StatusOK, LatencyMS: int64(180 + rng.IntN(5200)),
		Tokens: usage.TokensOf(&llm.Usage{InputTokens: &in, OutputTokens: &out}),
	}
	switch {
	case m.name == modelFlash:
		e.Failed, e.Status, e.ErrorCode, e.Tokens = true, http.StatusInternalServerError, llm.CodeUpstream, usage.Tokens{}
	case rng.IntN(40) == 0:
		e.Failed, e.Status, e.ErrorCode, e.Tokens = true, http.StatusTooManyRequests, llm.CodeRateLimit, usage.Tokens{}
	}
	return e
}
