package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A handler panic must not lose the request log: the response written before the panic and the
// request that caused it are still what an operator needs after the client's connection drops.
func TestVerifyRequestLogWrittenWhenHandlerPanics(t *testing.T) {
	t.Skip("bug usage-request-log-lifecycle-bugs: logged() writes the request log after the handler returns, so a handler panic loses it")
	dir := t.TempDir()
	requestLog := &RequestLog{Dir: dir, All: true, Redact: protocol.NewRedactor()}
	rec := httptest.NewRecorder()
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"verify-panic"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("X-Verify-Header", "kept")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("logged swallowed the handler's panic")
			}
		}()
		logged(rec, req, &Generation{RequestLog: requestLog}, func(w http.ResponseWriter, r *http.Request) {
			keepBody(r, body)
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":"verify panic reply"}`)
			panic("verify handler panic")
		})
	}()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("request log files after the panic: %d, want 1", len(entries))
	}
	raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"URL: /v1/chat/completions", "Method: POST", "X-Verify-Header: kept",
		"verify-panic", "Status: 503", "verify panic reply"} {
		if !strings.Contains(text, want) {
			t.Errorf("the panic's request log lacks %q:\n%s", want, text)
		}
	}
}
