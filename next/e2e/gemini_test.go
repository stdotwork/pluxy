package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A Gemini request whose model turn holds a thought, which Chat Completions has no place for, is
// translated with the thought dropped under the default drop policy, and refused with Google's
// error envelope before any upstream call under drop_policy "reject". The thought signature on the
// call is carried over to the Chat upstream.
func TestGeminiDropPolicy(t *testing.T) {
	const body = `{"contents":[
		{"role":"user","parts":[{"text":"Hi"}]},
		{"role":"model","parts":[{"text":"secret thought","thought":true},{"functionCall":{"id":"c1","name":"f","args":{}},"thoughtSignature":"sig"}]},
		{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"f","response":{"content":{"ok":true}}}}]},
		{"role":"user","parts":[{"text":"Again."}]}]}`
	reply := func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"chatcmpl-up1","object":"chat.completion","created":1700000000,"model":"gpt-test-upstream",
			"choices":[{"index":0,"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}]}`)
	}
	const path = "/v1beta/models/gpt-test:generateContent"

	t.Run("report", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u)
		resp := postPath(t, context.Background(), base, path, clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"text":"Done."`) || !strings.Contains(string(raw), `"finishReason":"STOP"`) {
			t.Fatalf("status %d: %s", resp.StatusCode, raw)
		}
		bodies, _ := u.received()
		if len(bodies) != 1 {
			t.Fatalf("upstream got %d requests, want 1", len(bodies))
		}
		up := string(bodies[0])
		if strings.Contains(up, "secret thought") || !strings.Contains(up, `"thought_signature":"sig"`) || !strings.Contains(up, "Again.") {
			t.Errorf("upstream got %s, want one request without the thought, with the thought signature and the turns", up)
		}
	})

	t.Run("reject", func(t *testing.T) {
		u := newUpstream(t)
		u.respond = reply
		base := startCore(t, u, func(cfg map[string]any) { cfg["drop_policy"] = "reject" })
		resp := postPath(t, context.Background(), base, path, clientKey, body)
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), `"status":"INVALID_ARGUMENT"`) ||
			!strings.Contains(string(raw), "contents[1].parts[0]") {
			t.Errorf("status %d: %s; want 400 with a Google error naming contents[1].parts[0]", resp.StatusCode, raw)
		}
		if bodies, _ := u.received(); len(bodies) != 0 {
			t.Errorf("upstream got %d requests, want none", len(bodies))
		}
	})
}

// An upstream that redirects to another host does not get the account's key sent there: Go drops
// Authorization on such a redirect but not x-goog-api-key or x-api-key. The client gets a 502.
func TestUpstreamRedirectToAnotherHostIsNotFollowed(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	_, port, _ := net.SplitHostPort(other.Listener.Addr().String())
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	base := startCore(t, &upstream{Server: redirect}, func(cfg map[string]any) {
		ext := cfg["extension"].(map[string]any)
		acct := ext["accounts"].([]any)[0].(map[string]any)
		acct["settings"] = map[string]any{"base_url": redirect.URL + "/v1beta", "wire_api": "gemini"}
	})
	resp := postPath(t, context.Background(), base, "/v1beta/models/gemini-test:generateContent", clientKey,
		`{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if n := leaked.Load(); n != 0 {
		t.Errorf("the other host got %d requests carrying the account's key", n)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %d: %s; want 502", resp.StatusCode, raw)
	}
}

// Errors a Gemini upstream sends reach a Gemini client as the upstream sent them, so they must
// lose the account's credential on the way like every other extension error. The upstream echoes
// its key in an error chunk mid-stream and in an unstreamed error body.
func TestGeminiUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := keyForms(r.Header.Get("X-Goog-Api-Key"))
		if strings.HasSuffix(r.URL.Path, "/models/error-chunk:streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hi\"}]},\"index\":0}]}\n\n")
			fmt.Fprintf(w, "data: {\"error\":{\"code\":403,\"message\":\"Rejected %s in error-chunk\",\"status\":\"PERMISSION_DENIED\"}}\n\n", key)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/models/error-body:generateContent") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":{"code":403,"message":"Rejected %s in error-body","status":"PERMISSION_DENIED"}}`, key)
			return
		}
		http.NotFound(w, r)
	})

	// Each case gets its own core: an upstream error blocks the only account for later requests.
	for _, c := range []struct{ model, path string }{
		{"error-chunk", "/v1beta/models/error-chunk:streamGenerateContent?alt=sse"},
		{"error-body", "/v1beta/models/error-body:generateContent"},
	} {
		t.Run(c.model, func(t *testing.T) {
			base, logs := startCoreLogged(t, u, func(cfg map[string]any) {
				acct := cfg["extension"].(map[string]any)["accounts"].([]any)[0].(map[string]any)
				acct["settings"].(map[string]any)["wire_api"] = "gemini"
				acct["settings"].(map[string]any)["base_url"] = u.URL + "/v1beta"
			})
			resp := postPath(t, context.Background(), base, c.path, clientKey, `{"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !strings.Contains(string(raw), "Rejected") {
				t.Errorf("the upstream's message did not reach the client (status %d): %s", resp.StatusCode, raw)
			}
			if holdsKey(string(raw)) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
			all := logs.waitFor(t, "in "+c.model)
			if holdsKey(all) {
				t.Errorf("core's log holds the account's key:\n%s", all)
			}
		})
	}
}

// A Gemini client's non-streamed reply keeps every function call's thought signature, so the
// signatures count toward core's 64 MiB limit on a non-streamed reply like any other content,
// whether a Chat upstream (Gemini's OpenAI-compatible API) sends a call's signature when the call
// opens or replaces it later. This is the reviewer's case: 65 calls with 1 MiB signatures came
// back as a 200 of 68,162,298 bytes.
func TestGeminiNonStreamingReplyCountsThoughtSignatures(t *testing.T) {
	sig := strings.Repeat("A", 1<<20)
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late=%t", late), func(t *testing.T) {
			u := newUpstream(t)
			u.respond = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				w.Header().Set("Content-Type", "text/event-stream")
				const head = `"id":"c1","object":"chat.completion.chunk","created":1,"model":"m"`
				call := func(i int, args, s string) string {
					return fmt.Sprintf(`{"index":%d,"id":"call_%d","type":"function","function":{"name":"f","arguments":%q},"extra_content":{"google":{"thought_signature":%q}}}`, i, i, args, s)
				}
				write := func(format string, a ...any) bool {
					_, err := fmt.Fprintf(w, format, a...)
					w.(http.Flusher).Flush()
					return err == nil && r.Context().Err() == nil
				}
				for i := range 65 {
					first := sig
					if late {
						first = "small"
					}
					if !write("data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[%s]}}]}\n\n", head, call(i, "{}", first)) {
						close(u.cancelled)
						return
					}
					if late && !write("data: {%s,\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[%s]}}]}\n\n", head, call(i, "", sig)) {
						close(u.cancelled)
						return
					}
				}
				if !write("data: {%s,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n", head) {
					close(u.cancelled)
					return
				}
				// Hold the stream open: core must cancel it, not read it to the end.
				select {
				case <-r.Context().Done():
					close(u.cancelled)
				case <-time.After(10 * time.Second):
					write("data: [DONE]\n\n")
				}
			}
			base := startCore(t, u)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			resp := postPath(t, ctx, base, "/v1beta/models/gpt-test:generateContent", clientKey, `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "64 MiB") {
				t.Fatalf("status %d, want 502 naming the 64 MiB limit; body starts %.200s", resp.StatusCode, raw)
			}
			select {
			case <-u.cancelled:
			case <-time.After(10 * time.Second):
				t.Fatal("the upstream request was not cancelled after the limit was reached")
			}
		})
	}
}
