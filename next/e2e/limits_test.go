package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// An upstream that echoes the account's key in its error text must not get the key to the
// client or into core's log, whether the error comes before the reply or halfway through it.
func TestUpstreamErrorDoesNotLeakCredential(t *testing.T) {
	u := newUpstream(t)
	u.respond = func(w http.ResponseWriter, r *http.Request, body []byte) {
		auth := r.Header.Get("Authorization") // "Bearer <key>"
		if strings.Contains(string(body), `"model":"refused"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"Rejected %s","type":"invalid_request_error","code":"bad_key_%s"}}`, auth, auth[len("Bearer "):])
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprintf(w, `data: {"error":{"message":"Rejected %s mid-stream","code":"revoked"}}`+"\n\n", auth)
	}
	base, logs := startCoreLogged(t, u)

	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"before reply", `{"model":"refused","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized},
		{"before reply, unstreamed", `{"model":"refused","messages":[{"role":"user","content":"hi"}]}`, http.StatusUnauthorized},
		{"mid-stream", `{"model":"midway","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := post(t, context.Background(), base, clientKey, c.body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != c.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, c.status, raw)
			}
			if !strings.Contains(string(raw), "Rejected") {
				t.Errorf("the upstream's message did not reach the client: %s", raw)
			}
			if strings.Contains(string(raw), upstreamKey) {
				t.Errorf("client reply holds the account's key: %s", raw)
			}
		})
	}
	all := logs.waitFor(t, "mid-stream")
	if strings.Contains(all, upstreamKey) {
		t.Errorf("core's log holds the account's key:\n%s", all)
	}
}
