package e2e

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// A streamed reply that the extension holds quiet for longer than 15 seconds gets ": keep-alive"
// comments on the Chat, Responses and Messages routes, and then finishes normally once the extension
// goes on. The Gemini route gets none: Google's API never sends comments, and its Python SDK
// fails on a line that is not JSON.
func TestKeepAliveOnQuietStreams(t *testing.T) {
	g := newGate(t)
	base := runCore(t, lifecycleConfig(testPackage(t, "0.1.0"), g, nil))
	routes := []struct {
		name, path, body string
		quiet            bool // no comment is wanted
	}{
		{"chat", "/v1/chat/completions", `{"model":"gated","stream":true,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"responses", "/v1/responses", `{"model":"gated","stream":true,"input":"hi"}`, false},
		{"messages", "/v1/messages", `{"model":"gated","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, false},
		{"gemini", "/v1beta/models/gated:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var heard sync.WaitGroup
	for _, rt := range routes {
		if !rt.quiet {
			heard.Add(1)
		}
	}
	type result struct {
		name, before, after string
		keepAlive           bool
		status              int
	}
	results := make(chan result, len(routes))
	start := time.Now()
	for _, rt := range routes {
		go func() {
			res := result{name: rt.name}
			defer func() { results <- res }()
			resp, err := streamRequest(ctx, base, rt.path, rt.body)
			if err != nil {
				res.before = err.Error()
				return
			}
			defer resp.Body.Close()
			res.status = resp.StatusCode
			r := bufio.NewReader(resp.Body)
			var before strings.Builder
			once := sync.OnceFunc(heard.Done)
			if rt.quiet {
				once = func() {}
			}
			defer once()
			if rt.quiet {
				// Read to the end: a comment anywhere in the quiet or after it is a failure.
				b, _ := io.ReadAll(r)
				res.keepAlive = strings.Contains(string(b), ": keep-alive")
				res.after = string(b)
				return
			}
			for {
				line, err := r.ReadString('\n')
				before.WriteString(line)
				if strings.TrimRight(line, "\r\n") == ": keep-alive" {
					res.keepAlive = true
					break
				}
				if err != nil {
					break
				}
			}
			res.before = before.String()
			once()
			b, _ := io.ReadAll(r)
			res.after = string(b)
		}()
	}
	heard.Wait()
	quiet := time.Since(start)
	// The routes without comments have been quiet as long as the others by now; let a comment
	// that would come late come.
	time.Sleep(time.Second)
	g.open()
	for range routes {
		res := <-results
		quietRoute := res.name == "gemini"
		switch {
		case res.status != http.StatusOK:
			t.Errorf("%s: status %d: %s", res.name, res.status, res.before)
		case quietRoute && res.keepAlive:
			t.Errorf("%s: a keep-alive comment on a route whose clients do not take comments: %q", res.name, res.after)
		case !quietRoute && !res.keepAlive:
			t.Errorf("%s: no keep-alive comment while the extension was quiet: %q", res.name, res.before)
		case !strings.Contains(res.after, "done") || strings.Contains(res.after, "event: error") || strings.Contains(res.after, `"error":{`):
			t.Errorf("%s: after the keep-alive the stream did not finish normally: %q", res.name, res.after)
		}
	}
	if quiet < 15*time.Second {
		t.Errorf("the keep-alive comments came %s into the quiet, before 15s", quiet)
	}
}

// An extension that stops sending anything is ended with extension_timeout within its limits
// plus a minute: before its reply starts as a failed request, after as a stream that ends with
// the error. Core logs a line that names it.
func TestWatchdogEndsSilentExtension(t *testing.T) {
	g := newGate(t)
	cfg := lifecycleConfig(testPackage(t, "0.1.0"), g, nil)
	cfg["routing"] = map[string]any{"timeouts": map[string]any{"header_ms": 1000, "idle_ms": 1000, "total_ms": 1000}}
	base, logs := runCoreLogged(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	type result struct {
		model, body string
		status      int
		took        time.Duration
	}
	results := make(chan result, 2)
	for _, model := range []string{"silent", "stall"} {
		go func() {
			start := time.Now()
			resp, err := streamRequest(ctx, base, "/v1/chat/completions", `{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if err != nil {
				results <- result{model: model, body: err.Error()}
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			results <- result{model, string(b), resp.StatusCode, time.Since(start)}
		}()
	}
	for range 2 {
		res := <-results
		if res.took < 61*time.Second || res.took > 75*time.Second {
			t.Errorf("%s: ended after %s, want just after 61s (1s limits plus a minute)", res.model, res.took)
		}
		if !strings.Contains(res.body, "extension_timeout") {
			t.Errorf("%s: reply does not name extension_timeout: %d %q", res.model, res.status, res.body)
		}
		switch res.model {
		case "silent":
			if res.status != http.StatusGatewayTimeout {
				t.Errorf("silent: status %d, want 504 before the reply started", res.status)
			}
		case "stall":
			if res.status != http.StatusOK || !strings.Contains(res.body, "pid=") {
				t.Errorf("stall: status %d, want 200 with the first text and then the error: %q", res.status, res.body)
			}
		}
	}
	logs.waitFor(t, "extension_timeout")
}

// streamRequest posts body to path with the client key; unlike postPath it may run off the
// test's goroutine.
func streamRequest(ctx context.Context, base, path, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+clientKey)
	return http.DefaultClient.Do(req)
}
