package e2e

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// startCoreAccounts runs core with one account per key, all pointed at u, and the given
// routing section. Account i has ID "acct-<key>".
func startCoreAccounts(t *testing.T, u *upstream, routing map[string]any, keys ...string) string {
	t.Helper()
	var accounts []any
	for _, k := range keys {
		accounts = append(accounts, map[string]any{
			"id":         "acct-" + k,
			"settings":   map[string]any{"base_url": u.URL + "/v1"},
			"credential": map[string]any{"kind": "api_key", "api_key": k},
		})
	}
	return runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"routing":     routing,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": accounts,
		},
	})
}

// keyOf returns the upstream key a request carried.
func keyOf(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func okReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"chatcmpl-r","object":"chat.completion","created":1700000002,"model":"gpt-test",`+
		`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
}

func usedKeys(u *upstream) []string {
	_, auth := u.received()
	keys := make([]string, len(auth))
	for i, a := range auth {
		keys[i] = strings.TrimPrefix(a, "Bearer ")
	}
	return keys
}

func postOK(t *testing.T, base, body string) {
	t.Helper()
	resp := post(t, context.Background(), base, clientKey, body)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", resp.StatusCode, raw)
	}
}

const plainBody = `{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`

// Requests take turns over the accounts in configured order.
func TestRoundRobinRotatesAccounts(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	base := startCoreAccounts(t, u, nil, "sk-a", "sk-b", "sk-c")
	for range 6 {
		postOK(t, base, plainBody)
	}
	want := []string{"sk-a", "sk-b", "sk-c", "sk-a", "sk-b", "sk-c"}
	if got := usedKeys(u); !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// An account the upstream rate-limits is skipped: the same request moves on to the next
// account, and later requests leave the limited account alone until its Retry-After passes.
func TestRateLimitedAccountRotatesAndCoolsDown(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-a" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`)
			return
		}
		okReply(w)
	}
	base := startCoreAccounts(t, u, nil, "sk-a", "sk-b")
	for range 3 {
		postOK(t, base, plainBody)
	}
	// First request: sk-a refuses, sk-b serves. Then sk-a is cooling down for 60s, so even
	// though it is sk-a's turn the next two requests go straight to sk-b.
	want := []string{"sk-a", "sk-b", "sk-b", "sk-b"}
	if got := usedKeys(u); !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// Once a streamed reply has started, a failure ends it in the client's stream; core does not
// try another account and splice a second reply in.
func TestNoRetryAfterFirstByte(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) != "sk-a" {
			okReply(w)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		const head = `"id":"chatcmpl-a","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test"`
		io.WriteString(w, `data: {`+head+`,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]}`+"\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // the upstream connection breaks mid-reply
	}
	base := startCoreAccounts(t, u, nil, "sk-a", "sk-b")
	resp := post(t, context.Background(), base, clientKey, `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var data []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			data = append(data, d)
		}
	}
	hello := slices.IndexFunc(data, func(d string) bool { return strings.Contains(d, `"Hello"`) })
	failed := slices.IndexFunc(data, func(d string) bool { return strings.Contains(d, `"error"`) })
	if hello < 0 || failed < hello {
		t.Errorf("client stream = %q, want the Hello chunk and then an error", data)
	}
	if got := usedKeys(u); !slices.Equal(got, []string{"sk-a"}) {
		t.Errorf("upstream keys = %v, want only [sk-a]: no retry after the reply started", got)
	}
}

// An account whose upstream speaks another format refuses a request it cannot translate, but
// that refusal is about the account, not the request: core moves the request on to an account
// that serves the client's format instead of failing it.
func TestAccountRefusingTheFormatRotates(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) { okReply(w) }
	base := runCore(t, map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{clientKey},
		"routing":     map[string]any{"strategy": "fill-first"},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{
				map[string]any{
					"id":         "acct-gemini",
					"settings":   map[string]any{"base_url": u.URL + "/v1beta", "wire_api": "gemini"},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-gemini"},
				},
				map[string]any{
					"id":         "acct-chat",
					"settings":   map[string]any{"base_url": u.URL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": "sk-chat"},
				},
			},
		},
	})
	postOK(t, base, plainBody)
	if got, want := usedKeys(u), []string{"sk-chat"}; !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// An account whose upstream says it does not have the model passes the request on, within the
// same request, to an account that serves the model.
func TestModelNotFoundRotates(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-a" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"The model `+"`gpt-test`"+` does not exist or you do not have access to it.","type":"invalid_request_error","code":"model_not_found"}}`)
			return
		}
		okReply(w)
	}
	base := startCoreAccounts(t, u, map[string]any{"strategy": "fill-first"}, "sk-a", "sk-b")
	postOK(t, base, plainBody)
	if got, want := usedKeys(u), []string{"sk-a", "sk-b"}; !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}

// A 404 that is not about the model (here, a path the upstream does not serve) is not taken
// as the account lacking the model: it goes to the client, and no other account is tried.
func TestOtherNotFoundDoesNotRotate(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if keyOf(r) == "sk-a" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<html>404 page not found</html>`)
			return
		}
		okReply(w)
	}
	base := startCoreAccounts(t, u, map[string]any{"strategy": "fill-first"}, "sk-a", "sk-b")
	resp := post(t, context.Background(), base, clientKey, plainBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
	if got, want := usedKeys(u), []string{"sk-a"}; !slices.Equal(got, want) {
		t.Errorf("upstream keys = %v, want %v", got, want)
	}
}
