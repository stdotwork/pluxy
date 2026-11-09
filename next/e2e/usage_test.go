package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The usage record: requests through the real core and a fake upstream, read back with the
// management API's usage calls.

type usageTokens struct {
	Input     *int64 `json:"input_tokens"`
	Output    *int64 `json:"output_tokens"`
	Reasoning *int64 `json:"reasoning_tokens"`
	Cached    *int64 `json:"cached_tokens"`
	Total     *int64 `json:"total_tokens"`
}

type usageEvent struct {
	Seq           uint64      `json:"seq"`
	ID            string      `json:"id"`
	Timestamp     time.Time   `json:"timestamp"`
	Endpoint      string      `json:"endpoint"`
	Stream        bool        `json:"stream"`
	Model         string      `json:"model"`
	UpstreamModel string      `json:"upstream_model"`
	Provider      string      `json:"provider"`
	Account       string      `json:"account"`
	KeyID         string      `json:"key_id"`
	ClientIP      string      `json:"client_ip"`
	Failed        bool        `json:"failed"`
	Status        int         `json:"status"`
	ErrorCode     string      `json:"error_code"`
	LatencyMS     int64       `json:"latency_ms"`
	Tokens        usageTokens `json:"tokens"`
}

type usagePage struct {
	Enabled bool         `json:"enabled"`
	Events  []usageEvent `json:"events"`
	Next    uint64       `json:"next"`
	More    bool         `json:"more"`
	Lost    bool         `json:"lost"`
}

type usageTotals struct {
	Requests    int64   `json:"total_requests"`
	Success     int64   `json:"success_count"`
	Failure     int64   `json:"failure_count"`
	SuccessRate float64 `json:"success_rate"`
	Tokens      struct {
		Input  int64 `json:"input_tokens"`
		Output int64 `json:"output_tokens"`
		Total  int64 `json:"total_tokens"`
	} `json:"tokens"`
}

type usageGroup struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	usageTotals
}

type usageSummary struct {
	Enabled bool `json:"enabled"`
	usageTotals
	Models        []usageGroup `json:"models"`
	Providers     []usageGroup `json:"providers"`
	Accounts      []usageGroup `json:"accounts"`
	Keys          []usageGroup `json:"keys"`
	BucketSeconds int64        `json:"bucket_seconds"`
	Series        []struct {
		Start    time.Time `json:"start"`
		Requests int64     `json:"requests"`
		Failure  int64     `json:"failure_count"`
		Tokens   int64     `json:"total_tokens"`
	} `json:"series"`
}

func (c *mcore) usageEvents(t *testing.T, query string) (usagePage, string) {
	t.Helper()
	b := c.expect(t, http.StatusOK, http.MethodGet, "/usage/events"+query, nil)
	var p usagePage
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p, string(b)
}

func (c *mcore) usageSummary(t *testing.T, query string) (usageSummary, string) {
	t.Helper()
	b := c.expect(t, http.StatusOK, http.MethodGet, "/usage"+query, nil)
	var s usageSummary
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s, string(b)
}

// chatReply sends a chat request and returns the status, the trace header and the body; with
// stream, the body is the whole event stream.
func chatReply(t *testing.T, base, key string, stream bool, header ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body := fmt.Sprintf(`{"model":"gpt-4o","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("X-CPA-TRACE-ID"), string(b)
}

func i64Is(p *int64, want int64) bool { return p != nil && *p == want }

// Every request that reaches routing is recorded with its account, model, client key ID,
// status, latency and tokens; reading the record changes nothing; the filters, the cursor and
// the totals agree with the requests sent; no key or credential is in any reply.
func TestUsageRecordsRequests(t *testing.T) {
	c := startManaged(t, false)
	start := time.Now().Add(-time.Second)

	code, trace, _ := chatReply(t, c.base, clientKey, false)
	if code != http.StatusOK || trace == "" {
		t.Fatalf("plain request: status %d, trace %q", code, trace)
	}
	// No trusted proxies: a forwarding header is not believed.
	if code, _, _ = chatReply(t, c.base, secondClientKey, true, "X-Forwarded-For", "198.51.100.7"); code != http.StatusOK {
		t.Fatalf("streamed request: status %d", code)
	}
	// Pinned to an account that does not exist: routing refuses it before any account.
	if code, _, _ = chatReply(t, c.base, clientKey, false, "X-Pluxy-Account", "nobody"); code != http.StatusNotFound {
		t.Fatalf("pinned to no account: status %d", code)
	}
	// Refused before routing (wrong client key): not a request core served.
	if code, _, _ = chatReply(t, c.base, "wrong-key", false); code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status %d", code)
	}

	var keys []keyView
	keys = c.keys(t)
	idOf := map[string]string{}
	for _, k := range keys {
		idOf[k.Masked] = k.ID
	}
	id1, id2 := idOf[mask(clientKey)], idOf[mask(secondClientKey)]
	if id1 == "" || id2 == "" {
		t.Fatalf("client keys %+v", keys)
	}

	p, raw := c.usageEvents(t, "")
	wantNoSecrets(t, "GET /usage/events", raw, clientKey, secondClientKey, upstreamKey, managementKey)
	if !p.Enabled || len(p.Events) != 3 || p.More || p.Lost {
		t.Fatalf("events: %s", raw)
	}
	plain, streamed, refused := p.Events[0], p.Events[1], p.Events[2]
	if plain.ID != trace || plain.Endpoint != "/v1/chat/completions" || plain.Stream || plain.Model != "gpt-4o" ||
		plain.UpstreamModel != "" || plain.Provider != "pluxy/openai-compatible" || plain.Account != "upstream-1" ||
		plain.KeyID != id1 || plain.ClientIP != "127.0.0.1" || plain.Failed || plain.Status != 200 || plain.ErrorCode != "" ||
		!i64Is(plain.Tokens.Input, 9) || !i64Is(plain.Tokens.Output, 3) || !i64Is(plain.Tokens.Total, 12) ||
		plain.Timestamp.Before(start) || plain.Timestamp.After(time.Now()) || plain.LatencyMS < 0 {
		t.Errorf("plain request's event: %+v", plain)
	}
	if !streamed.Stream || streamed.KeyID != id2 || streamed.ClientIP != "127.0.0.1" || streamed.Failed || streamed.Status != 200 || !i64Is(streamed.Tokens.Total, 12) ||
		streamed.Seq <= plain.Seq {
		t.Errorf("streamed request's event: %+v", streamed)
	}
	if !refused.Failed || refused.Status != 404 || refused.ErrorCode != "not_found" || refused.Account != "" || refused.ID != "" ||
		refused.Tokens.Total != nil {
		t.Errorf("refused request's event: %+v", refused)
	}

	// Reading removed nothing.
	if again, _ := c.usageEvents(t, ""); len(again.Events) != 3 {
		t.Fatalf("second read: %d events", len(again.Events))
	}
	if after, _ := c.usageEvents(t, fmt.Sprintf("?after=%d", plain.Seq)); len(after.Events) != 2 || after.Events[0].Seq != streamed.Seq {
		t.Errorf("after %d: %+v", plain.Seq, after)
	}
	if one, _ := c.usageEvents(t, "?limit=1"); len(one.Events) != 1 || !one.More || one.Next != plain.Seq {
		t.Errorf("limit 1: %+v", one)
	}
	if failed, _ := c.usageEvents(t, "?failed=true"); len(failed.Events) != 1 || failed.Events[0].Seq != refused.Seq {
		t.Errorf("failed=true: %+v", failed)
	}
	if byKey, _ := c.usageEvents(t, "?key_id="+id2); len(byKey.Events) != 1 || byKey.Events[0].Seq != streamed.Seq {
		t.Errorf("key_id: %+v", byKey)
	}
	if future, _ := c.usageEvents(t, "?since="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); len(future.Events) != 0 {
		t.Errorf("since an hour from now: %+v", future)
	}
	for _, bad := range []string{"?after=x", "?limit=0", "?limit=1001", "?failed=maybe", "?since=yesterday"} {
		if code, b := c.call(t, http.MethodGet, "/usage/events"+bad, nil); code != http.StatusBadRequest || errorCode(t, b) != "invalid_request" {
			t.Errorf("%s: status %d: %s", bad, code, b)
		}
	}

	s, raw := c.usageSummary(t, "")
	wantNoSecrets(t, "GET /usage", raw, clientKey, secondClientKey, upstreamKey, managementKey)
	if !s.Enabled || s.Requests != 3 || s.Success != 2 || s.Failure != 1 || s.SuccessRate < 0.66 || s.SuccessRate > 0.67 ||
		s.Tokens.Input != 18 || s.Tokens.Output != 6 || s.Tokens.Total != 24 {
		t.Errorf("totals: %s", raw)
	}
	if len(s.Models) != 1 || s.Models[0].Name != "gpt-4o" || s.Models[0].Requests != 3 {
		t.Errorf("models: %+v", s.Models)
	}
	if len(s.Accounts) != 1 || s.Accounts[0].Name != "upstream-1" || s.Accounts[0].Provider != "pluxy/openai-compatible" ||
		s.Accounts[0].Requests != 2 || s.Accounts[0].SuccessRate != 1 {
		t.Errorf("accounts: %+v", s.Accounts)
	}
	if len(s.Providers) != 1 || s.Providers[0].Name != "pluxy/openai-compatible" {
		t.Errorf("providers: %+v", s.Providers)
	}
	if len(s.Keys) != 2 || s.Keys[0].Name != id1 || s.Keys[0].Requests != 2 || s.Keys[1].Name != id2 {
		t.Errorf("keys: %+v", s.Keys)
	}
	var inSeries int64
	for _, b := range s.Series {
		inSeries += b.Requests
	}
	if s.BucketSeconds != 60 || len(s.Series) != 60 || inSeries != 3 {
		t.Errorf("series: %d buckets of %ds holding %d requests", len(s.Series), s.BucketSeconds, inSeries)
	}
	if acct, _ := c.usageSummary(t, "?account=upstream-1&bucket=10s&buckets=6"); acct.Requests != 2 || len(acct.Series) != 6 || acct.BucketSeconds != 10 {
		t.Errorf("one account: %+v", acct)
	}
	for _, bad := range []string{"?bucket=0s", "?bucket=48h", "?buckets=0", "?buckets=1441", "?until=soon"} {
		if code, b := c.call(t, http.MethodGet, "/usage"+bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s: status %d: %s", bad, code, b)
		}
	}
	// The usage calls need the management key, as every management call does.
	if code, _ := mcall(t, c.base, clientKey, http.MethodGet, "/usage", nil); code != http.StatusUnauthorized {
		t.Errorf("usage with a client key: status %d", code)
	}
}

// usage-statistics-enabled turns the record off and on through the settings call, in both
// config formats; while it is off, requests are not recorded and what was recorded stays.
func TestUsageStatisticsSwitch(t *testing.T) {
	for _, f := range formats() {
		t.Run(f.name, func(t *testing.T) {
			c := startManaged(t, f.yaml)
			if !c.usageOn(t) {
				t.Fatal("usage statistics are off by default")
			}
			chatStatus(t, c.base, clientKey)
			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"usage": {"usage-statistics-enabled": false}}}`)
			if c.usageOn(t) {
				t.Fatal("still on after the change")
			}
			want := `"enabled": false`
			if f.yaml {
				want = "usage-statistics-enabled: false"
			}
			if file := c.file(t); !strings.Contains(file, want) {
				t.Fatalf("config file has no %q:\n%s", want, file)
			}
			chatStatus(t, c.base, clientKey)
			if p, raw := c.usageEvents(t, ""); p.Enabled || len(p.Events) != 1 {
				t.Fatalf("while off: %s", raw)
			}
			c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"usage": {"usage-statistics-enabled": true}}}`)
			chatStatus(t, c.base, clientKey)
			if p, raw := c.usageEvents(t, ""); !p.Enabled || len(p.Events) != 2 {
				t.Fatalf("on again: %s", raw)
			}
			if code, b := c.call(t, http.MethodPatch, "/settings", `{"observability": {"usage": {"usage-statistics-enabled": "yes"}}}`); code != http.StatusBadRequest {
				t.Fatalf("a non-boolean: status %d: %s", code, b)
			}
		})
	}
}

func (c *mcore) usageOn(t *testing.T) bool {
	t.Helper()
	var s struct {
		Observability struct {
			Usage struct {
				Enabled *bool `json:"usage-statistics-enabled"`
			} `json:"usage"`
		} `json:"observability"`
	}
	if err := json.Unmarshal(c.expect(t, http.StatusOK, http.MethodGet, "/settings", nil), &s); err != nil || s.Observability.Usage.Enabled == nil {
		t.Fatalf("settings: %v", err)
	}
	return *s.Observability.Usage.Enabled
}

// The record keeps the account's own name for an aliased model, and a client that gives up on
// a stream is recorded as cancelled with the 200 it had received.
func TestUsageUpstreamModelAndCancel(t *testing.T) {
	u := newUpstream(t)
	u.respond = u.streamReply
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"trusted_proxies": []string{"127.0.0.1"},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":          "aliased",
				"model_alias": []any{map[string]any{"name": "gpt-test-upstream", "alias": "fast"}},
				"settings":    map[string]any{"base_url": u.URL + "/v1"},
				"credential":  map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})
	c := &mcore{base: base}

	ctx, cancel := context.WithCancel(context.Background())
	resp := post(t, ctx, base, clientKey, `{"model":"fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	readData(t, bufio.NewReader(resp.Body)) // the first chunk: the reply has begun
	cancel()
	resp.Body.Close()
	select {
	case <-u.cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}

	var p usagePage
	deadline := time.Now().Add(10 * time.Second)
	for {
		p, _ = c.usageEvents(t, "")
		if len(p.Events) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(p.Events) != 1 {
		t.Fatalf("events: %+v", p)
	}
	e := p.Events[0]
	if e.Model != "fast" || e.UpstreamModel != "gpt-test-upstream" || e.Account != "aliased" || !e.Failed ||
		e.ErrorCode != "cancelled" || e.Status != 200 || !e.Stream || e.KeyID == "" || e.ClientIP != "127.0.0.1" {
		t.Fatalf("cancelled stream's event: %+v", e)
	}

	// From a trusted proxy, the client is the rightmost address in X-Forwarded-For that is not
	// a trusted proxy.
	if code, _, _ := chatReply(t, base, clientKey, false, "X-Pluxy-Account", "nobody",
		"X-Forwarded-For", "203.0.113.50, 198.51.100.7, 127.0.0.1"); code != http.StatusNotFound {
		t.Fatalf("pinned to no account: status %d", code)
	}
	if p, raw := c.usageEvents(t, "?after="+strconv.FormatUint(e.Seq, 10)); len(p.Events) != 1 || p.Events[0].ClientIP != "198.51.100.7" {
		t.Fatalf("forwarded request's event: %s", raw)
	}
}

// A Responses WebSocket turn is recorded like a request (the fake upstream reports no usage).
func TestUsageRecordsWebSocketTurns(t *testing.T) {
	u := newWSUpstream(t)
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "codex", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "acct-ws",
				"models":     []string{"gpt-test"},
				"settings":   map[string]any{"base_url": u.URL + "/v1", "websockets": true},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ws"},
			}},
		},
	})
	conn := dialResponses(t, base)
	if m := wsTurn(t, conn, wsCreate("hello", "")); m["type"] != "response.completed" {
		t.Fatalf("turn: %v", m)
	}
	c := &mcore{base: base}
	p, raw := c.usageEvents(t, "")
	wantNoSecrets(t, "GET /usage/events", raw, clientKey, "sk-ws")
	if len(p.Events) != 1 {
		t.Fatalf("events: %s", raw)
	}
	e := p.Events[0]
	if e.Endpoint != "/v1/responses" || !e.Stream || e.Model != "gpt-test" || e.Account != "acct-ws" || e.Provider != "pluxy/codex" ||
		e.Failed || e.Status != 200 || e.Tokens.Total != nil {
		t.Fatalf("turn's event: %+v", e)
	}
}
