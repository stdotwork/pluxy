package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// What a client reply says about the account that served it, and what an error says about the
// accounts it tried and left out. The account in a reply is an opaque ID, never the ID or
// label a person gave the account, which may be an email address.

var publicID = regexp.MustCompile("^acc_[0-9a-f]{8}$")

type explainAccount struct {
	id, key  string
	priority int
}

// startExplainCore runs core with one account per entry, all pointed at u, and the management
// API on.
func startExplainCore(t *testing.T, u *upstream, routing map[string]any, accounts ...explainAccount) string {
	t.Helper()
	var list []any
	for _, a := range accounts {
		list = append(list, map[string]any{
			"id":         a.id,
			"priority":   a.priority,
			"settings":   map[string]any{"base_url": u.URL + "/v1"},
			"credential": map[string]any{"kind": "api_key", "api_key": a.key},
		})
	}
	return runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing":        routing,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": list,
		},
	})
}

// publicIDs returns each account's public ID by its ID, and its extension, from GET /accounts.
func publicIDs(t *testing.T, base string) (ids map[string]string, extension string) {
	t.Helper()
	code, b := mcall(t, base, managementKey, http.MethodGet, "/accounts", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /accounts: %d %s", code, b)
	}
	var raw struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	ids = map[string]string{}
	for _, a := range raw.Accounts {
		pid, _ := a["public_id"].(string)
		ids[a["id"].(string)] = pid
		extension, _ = a["extension"].(string)
	}
	return ids, extension
}

func rateLimited(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "600")
	w.WriteHeader(http.StatusTooManyRequests)
	io.WriteString(w, `{"error":{"message":"UPSTREAM-SECRET-TEXT rate limit","type":"requests","code":"rate_limit_exceeded"}}`)
}

// A reply carries the trace ID, and the account that served it by an opaque ID, its provider,
// the model sent upstream and how many accounts the request tried. No header holds the ID the
// account is configured under, and none holds an email address.
func TestRepliesNameTheAccount(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-a" {
			rateLimited(w)
			return
		}
		okReply(w)
	}
	base := startExplainCore(t, u, map[string]any{"strategy": "fill-first"},
		explainAccount{id: "jane@example.com", key: "sk-a", priority: 1}, explainAccount{id: "work laptop", key: "sk-b"})
	ids, ext := publicIDs(t, base)
	for id, pid := range ids {
		if !publicID.MatchString(pid) || strings.Contains(pid, id) {
			t.Errorf("account %q has public ID %q, want acc_ and 8 hex digits", id, pid)
		}
	}
	if ids["jane@example.com"] == ids["work laptop"] {
		t.Fatalf("two accounts share the public ID %q", ids["work laptop"])
	}
	resp := post(t, context.Background(), base, clientKey, plainBody)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	want := map[string]string{
		"X-Pluxy-Account":  ids["work laptop"],
		"X-Pluxy-Provider": ext,
		"X-Pluxy-Model":    "gpt-test",
		"X-Pluxy-Attempts": "2", // sk-a refused first
	}
	for h, v := range want {
		if got := resp.Header.Get(h); got != v || v == "" {
			t.Errorf("%s = %q, want %q", h, got, v)
		}
	}
	if resp.Header.Get("X-CPA-TRACE-ID") == "" {
		t.Error("the reply has no X-CPA-TRACE-ID")
	}
	for name, vs := range resp.Header {
		for _, v := range vs {
			if strings.Contains(v, "@") || strings.Contains(v, "jane") || strings.Contains(v, "laptop") {
				t.Errorf("header %s = %q holds an account's ID", name, v)
			}
		}
	}
}

// The public IDs are the same after core restarts.
func TestPublicAccountIDsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	u := newUpstream(t)
	writeConfig(t, path, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "acct-one", "settings": map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-one"}}},
		},
	})
	bin := filepath.Join(binDir, "pluxy")
	var first string
	for i := range 2 {
		base, _, stop, err := startCoreStoppable(t, bin, "-config", path)
		if err != nil {
			t.Fatal(err)
		}
		ids, _ := publicIDs(t, base)
		stop()
		if i == 0 {
			first = ids["acct-one"]
			if !publicID.MatchString(first) {
				t.Fatalf("public ID %q", first)
			}
		} else if ids["acct-one"] != first {
			t.Errorf("public ID after a restart = %q, before %q", ids["acct-one"], first)
		}
	}
}

// The client formats an error can come in, with a request each.
var explainFormats = []struct{ name, path, body string }{
	{"chat", "/v1/chat/completions", plainBody},
	{"messages", "/v1/messages", `{"model":"gpt-test","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`},
	{"gemini", "/v1beta/models/gpt-test:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
}

type explainBody struct {
	Error struct {
		Pluxy *struct {
			Trace string `json:"trace"`
			Tried []struct {
				Account, Provider, Model, Result string
			} `json:"tried"`
			LeftOut []struct {
				Account, State, Until string
			} `json:"left_out"`
		} `json:"pluxy"`
	} `json:"error"`
}

// An error says which accounts the request tried and with what result, and which it left out and
// why, in each client format: under the error, in the format's own error object. It holds codes
// and times, never the upstream's words.
func TestErrorSaysWhatWasTried(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { rateLimited(w) }
	base := startExplainCore(t, u, nil, explainAccount{id: "acct-a", key: "sk-a"}, explainAccount{id: "acct-b", key: "sk-b"})
	ids, ext := publicIDs(t, base)
	for i, f := range explainFormats {
		resp := postPath(t, context.Background(), base, f.path, clientKey, f.body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var b explainBody
		if err := json.Unmarshal(raw, &b); err != nil || b.Error.Pluxy == nil {
			t.Fatalf("%s: status %d, no error.pluxy in %s (%v)", f.name, resp.StatusCode, raw, err)
		}
		p := b.Error.Pluxy
		if strings.Contains(mustJSON(p), "UPSTREAM-SECRET-TEXT") {
			t.Errorf("%s: pluxy holds the upstream's words: %s", f.name, mustJSON(p))
		}
		if p.Trace == "" || p.Trace != resp.Header.Get("X-CPA-TRACE-ID") {
			t.Errorf("%s: pluxy.trace = %q, header %q", f.name, p.Trace, resp.Header.Get("X-CPA-TRACE-ID"))
		}
		var tried, left []string
		if i == 0 {
			// The first request tries both accounts; each answered rate_limit.
			for _, x := range p.Tried {
				if x.Provider != ext || x.Model != "gpt-test" || x.Result != "rate_limit" {
					t.Errorf("%s: tried %+v", f.name, x)
				}
				tried = append(tried, x.Account)
			}
			if len(p.LeftOut) != 0 {
				t.Errorf("%s: left_out = %+v, want none", f.name, p.LeftOut)
			}
			want := []string{ids["acct-a"], ids["acct-b"]}
			slices.Sort(tried)
			slices.Sort(want)
			if !slices.Equal(tried, want) {
				t.Errorf("%s: tried %v, want %v", f.name, tried, want)
			}
			continue
		}
		// Later requests find both cooling: nothing tried, both left out with the end of the block.
		if len(p.Tried) != 0 {
			t.Errorf("%s: tried = %+v, want none", f.name, p.Tried)
		}
		for _, x := range p.LeftOut {
			until, err := time.Parse(time.RFC3339, x.Until)
			if x.State != "cooling" || err != nil || !until.After(time.Now()) {
				t.Errorf("%s: left_out %+v, want cooling with a time ahead", f.name, x)
			}
			left = append(left, x.Account)
		}
		slices.Sort(left)
		want := []string{ids["acct-a"], ids["acct-b"]}
		slices.Sort(want)
		if !slices.Equal(left, want) {
			t.Errorf("%s: left_out %v, want %v", f.name, left, want)
		}
	}
}

// A request that finds its only account banned says so, where it said "no account can serve
// model" and no reason.
func TestBannedAccountIsNamedInTheError(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" && r.Method == http.MethodPost {
			calls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"type":"error","error":{"type":"permission_error","message":"Your account has been suspended."}}`)
			return
		}
		io.WriteString(w, `{"data":[{"type":"model","id":"claude-sonnet-4-5-20250929"}],"has_more":false}`)
	}))
	t.Cleanup(up.Close)
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "claude", "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "me@example.com", "settings": map[string]any{"base_url": up.URL},
				"credential": map[string]any{"kind": "api_key", "api_key": "sk-ant-api-a"}}},
		},
	})
	ids, _ := publicIDs(t, base)
	const body = `{"model":"claude-sonnet-4-5-20250929","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
	var last explainBody
	var status int
	for range 2 {
		resp := postPath(t, context.Background(), base, "/v1/messages", clientKey, body)
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		status = resp.StatusCode
		last = explainBody{}
		_ = json.Unmarshal(raw, &last)
		if last.Error.Pluxy == nil {
			t.Fatalf("status %d, no error.pluxy in %s", status, raw)
		}
	}
	p := last.Error.Pluxy
	if calls.Load() != 1 || len(p.Tried) != 0 || len(p.LeftOut) != 1 || p.LeftOut[0].State != "banned" || p.LeftOut[0].Account != ids["me@example.com"] {
		t.Errorf("after the ban: upstream calls %d, status %d, pluxy %s", calls.Load(), status, mustJSON(p))
	}
}

// routing.explain false leaves out the headers but the trace ID, and the pluxy member.
func TestExplainFalseLeavesOutHeadersAndMember(t *testing.T) {
	for _, explain := range []bool{true, false} {
		for _, f := range explainFormats {
			for _, failing := range []bool{false, true} {
				// A fresh core each time: a refused account is blocked afterwards.
				u := newUpstream(t)
				u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
					if failing {
						rateLimited(w)
						return
					}
					okReply(w)
				}
				base := startExplainCore(t, u, map[string]any{"explain": explain}, explainAccount{id: "acct-a", key: "sk-a"})
				resp := postPath(t, context.Background(), base, f.path, clientKey, f.body)
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.Header.Get("X-CPA-TRACE-ID") == "" {
					t.Errorf("explain %v, %s, failing %v: no trace ID", explain, f.name, failing)
				}
				for _, h := range []string{"X-Pluxy-Account", "X-Pluxy-Provider", "X-Pluxy-Model", "X-Pluxy-Attempts"} {
					if (resp.Header.Get(h) != "") != explain {
						t.Errorf("explain %v, %s, failing %v: %s = %q", explain, f.name, failing, h, resp.Header.Get(h))
					}
				}
				if failing && strings.Contains(string(raw), "pluxy") != explain {
					t.Errorf("explain %v, %s: error body %s", explain, f.name, raw)
				}
			}
		}
	}
}

type routeReply struct {
	Model string `json:"model"`
	Order []struct {
		Account, Provider, Model string
	} `json:"order"`
	LeftOut []struct {
		Account, State, Until string
	} `json:"left_out"`
}

// GET /route says the order a request would try its accounts in, and why each account it would
// not try is left out, and sends nothing upstream.
func TestRouteSaysWhatARequestWouldDo(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-z" {
			rateLimited(w)
			return
		}
		okReply(w)
	}
	base := startExplainCore(t, u, map[string]any{"strategy": "fill-first"},
		explainAccount{id: "acct-x", key: "sk-x"}, explainAccount{id: "acct-y", key: "sk-y", priority: 5}, explainAccount{id: "acct-z", key: "sk-z", priority: 9})
	ids, ext := publicIDs(t, base)
	get := func() routeReply {
		t.Helper()
		code, b := mcall(t, base, managementKey, http.MethodGet, "/route?model=gpt-test", nil)
		var r routeReply
		if code != http.StatusOK || json.Unmarshal(b, &r) != nil {
			t.Fatalf("GET /route: %d %s", code, b)
		}
		return r
	}
	order := func(r routeReply) []string {
		var out []string
		for _, o := range r.Order {
			if o.Provider != ext || o.Model != "gpt-test" {
				t.Errorf("order entry %+v", o)
			}
			out = append(out, o.Account)
		}
		return out
	}
	// Before anything blocks: highest priority first, then configured order.
	if got, want := order(get()), []string{ids["acct-z"], ids["acct-y"], ids["acct-x"]}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
	if n, _ := u.received(); len(n) != 0 {
		t.Fatalf("GET /route sent %d requests upstream", len(n))
	}
	// A request: acct-z (first) refuses, so it is blocked and acct-y serves.
	postOK(t, base, plainBody)
	before, _ := u.received()
	r := get()
	if got, want := order(r), []string{ids["acct-y"], ids["acct-x"]}; !slices.Equal(got, want) {
		t.Errorf("order after a block = %v, want %v", got, want)
	}
	if len(r.LeftOut) != 1 || r.LeftOut[0].Account != ids["acct-z"] || r.LeftOut[0].State != "cooling" || r.LeftOut[0].Until == "" {
		t.Errorf("left_out = %+v", r.LeftOut)
	}
	if after, _ := u.received(); len(after) != len(before) {
		t.Errorf("GET /route sent a request upstream")
	}
	// The command prints the same.
	res := runCLI(t, nil, "route", "gpt-test", "--url", base, "--key", managementKey)
	if res.code != 0 || !strings.Contains(res.stdout, ids["acct-y"]) || !strings.Contains(res.stdout, ids["acct-z"]) || !strings.Contains(res.stdout, "cooling") {
		t.Errorf("pluxy route gpt-test: %s", res)
	}
}

// The account header a reply names can be sent back to pin the next request to it.
func TestPinAcceptsPublicID(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	base := startExplainCore(t, u, map[string]any{"strategy": "fill-first"},
		explainAccount{id: "acct-a", key: "sk-a"}, explainAccount{id: "acct-b", key: "sk-b"})
	ids, _ := publicIDs(t, base)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(plainBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("X-Pluxy-Account", ids["acct-b"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if keys := usedKeys(u); resp.StatusCode != http.StatusOK || !slices.Equal(keys, []string{"sk-b"}) {
		t.Errorf("status %d, upstream keys %v, want 200 and sk-b", resp.StatusCode, keys)
	}
}

// The handshake of the Responses WebSocket carries a trace ID, as an HTTP reply does; the account
// of a turn is chosen after the upgrade, so no account header can come with it.
func TestResponsesWebSocketHandshakeCarriesTraceID(t *testing.T) {
	u := newWSUpstream(t)
	base := startWSCore(t, u, true, "sk-a")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses",
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + clientKey}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if id := resp.Header.Get("X-CPA-TRACE-ID"); !regexp.MustCompile(`^\d{14}-[0-9a-f]{16}$`).MatchString(id) {
		t.Errorf("handshake trace ID = %q", id)
	}
}
