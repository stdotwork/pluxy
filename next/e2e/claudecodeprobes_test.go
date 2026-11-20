package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Claude Code sends unauthenticated probes before its Messages requests. Core answers them
// itself, and the event batch is accepted and dropped instead of reaching a provider.
func TestClaudeCodeProbeRoutes(t *testing.T) {
	u := newUpstream(t)
	base := startCore(t, u)

	for _, tc := range []struct {
		name, method, path, body, wantBody string
	}{
		{"hello GET", http.MethodGet, "/api/hello", "", `{"status":"ok"}`},
		{"hello HEAD", http.MethodHead, "/api/hello", "", ""},
		{"event logging", http.MethodPost, "/api/event_logging/batch", `{"events":[{"event":"test"}]}`, `{"status":"ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, base+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != tc.wantBody {
				t.Errorf("%s %s: %d %q, want 200 %q", tc.method, tc.path, resp.StatusCode, body, tc.wantBody)
			}
		})
	}

	if bodies, _ := u.received(); len(bodies) != 0 {
		t.Errorf("upstream requests = %d, want 0", len(bodies))
	}
}
