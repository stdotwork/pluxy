package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real clients hit the four API families and legacy completions at the same time. Every
// request log must be a whole file of its own, its body and reply intact, including the failed
// request and the streamed reply. This covers the real-world classes where logging consumed or
// corrupted the request body, where streamed chunks went missing, and where concurrent writes
// left logs absent or incomplete.
func TestVerifyRequestLogLifecycleAcrossConcurrentPaths(t *testing.T) {
	u := answeringUpstream(t)
	reply := u.streamReply
	u.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if strings.Contains(string(body), "verify-path-failure") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"verify failure reply","type":"server_error"}}`)
			return
		}
		reply(w, r, body)
	}

	base, path, _ := runCoreArgs(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{"id": "upstream-1",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey}}},
		},
	})
	c := &mcore{base: base, path: path}
	c.expect(t, http.StatusOK, http.MethodPatch, "/settings", `{"observability": {"logs": {"request-log": true}}}`)
	cases := []struct {
		name, path, body string
		failed           bool
	}{
		{"chat stream", "/v1/chat/completions", `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"verify-path-chat"}]}`, false},
		{"legacy completions", "/v1/completions", `{"model":"gpt-4o","prompt":"verify-path-completions","max_tokens":16}`, false},
		{"responses", "/v1/responses", `{"model":"gpt-4o","input":"verify-path-responses"}`, false},
		{"messages", "/v1/messages", `{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"verify-path-messages"}]}`, false},
		{"gemini", "/v1beta/models/gpt-4o:generateContent", `{"contents":[{"role":"user","parts":[{"text":"verify-path-gemini"}]}]}`, false},
		{"upstream failure", "/v1/chat/completions", `{"model":"gpt-4o","messages":[{"role":"user","content":"verify-path-failure"}]}`, true},
	}
	type result struct {
		status int
		trace  string
		body   string
		err    error
	}
	results := make([]result, len(cases))
	var wg sync.WaitGroup
	for i := range cases {
		wg.Go(func() {
			resp := postPath(t, context.Background(), c.base, cases[i].path, clientKey, cases[i].body)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			results[i] = result{status: resp.StatusCode, trace: resp.Header.Get("X-CPA-TRACE-ID"), body: string(body), err: err}
		})
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("%s: %v", cases[i].name, r.err)
		}
		if r.trace == "" {
			t.Errorf("%s: no trace ID (status %d): %s", cases[i].name, r.status, r.body)
		}
		if cases[i].failed && r.status < 400 || !cases[i].failed && r.status != http.StatusOK {
			t.Errorf("%s: status %d: %s", cases[i].name, r.status, r.body)
		}
		if !cases[i].failed && !strings.Contains(r.body, "Hello") {
			t.Errorf("%s: the reply is missing its content: %s", cases[i].name, r.body)
		}
	}
	// The logging did not corrupt what any request sent upstream. Reading each body as JSON
	// catches the read-once and shared-body failures; the marker catches a body from another
	// request in its place.
	bodies, _ := u.received()
	for _, tc := range cases {
		marker := tc.body[strings.Index(tc.body, "verify-path-"):]
		if strings.Contains(marker, `"`) {
			marker = marker[:strings.IndexByte(marker, '"')]
		}
		found := false
		for _, body := range bodies {
			if !strings.Contains(string(body), marker) {
				continue
			}
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				t.Errorf("the upstream got malformed JSON for %s: %v: %s", tc.name, err, body)
				continue
			}
			found = true
		}
		if !found {
			t.Errorf("no intact upstream request held %s", marker)
		}
	}

	dir := c.logsDir()
	var logs []string
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		logs = logs[:0]
		var partial []string
		for _, e := range entries {
			switch {
			case strings.HasPrefix(e.Name(), ".writing-"):
				partial = append(partial, e.Name())
			case strings.HasSuffix(e.Name(), ".log") && !strings.HasPrefix(e.Name(), "error-"):
				logs = append(logs, e.Name())
			}
		}
		if len(logs) == len(cases) && len(partial) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d complete request logs and partial files %v, want %d logs: %v", len(logs), partial, len(cases), logs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	texts := make([]string, 0, len(logs))
	for _, name := range logs {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "=== REQUEST INFO ===") || !strings.Contains(string(b), "=== RESPONSE ===") {
			t.Errorf("request log %s is not whole:\n%s", name, b)
		}
		texts = append(texts, string(b))
	}
	for i, r := range results {
		var text string
		n := 0
		for _, candidate := range texts {
			if strings.Contains(candidate, "Trace ID: "+r.trace+"\n") {
				text, n = candidate, n+1
			}
		}
		if n != 1 {
			t.Errorf("%s: trace %s is in %d request logs:\n%s", cases[i].name, r.trace, n, strings.Join(logs, "\n"))
			continue
		}
		marker := cases[i].body[strings.Index(cases[i].body, "verify-path-"):]
		if strings.Contains(marker, `"`) {
			marker = marker[:strings.IndexByte(marker, '"')]
		}
		for _, want := range []string{"URL: " + cases[i].path, "Method: POST", marker, fmt.Sprintf("Status: %d", r.status)} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: request log lacks %q:\n%s", cases[i].name, want, text)
			}
		}
		replyText := "Hello"
		if cases[i].failed {
			replyText = "verify failure reply"
		}
		if !strings.Contains(text, replyText) {
			t.Errorf("%s: request log lacks the reply %q:\n%s", cases[i].name, replyText, text)
		}
	}
}
