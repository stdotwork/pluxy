package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/protocol"
)

// The kit's alive check passes for an extension that streams through sdk.Upstream from an
// upstream that keeps sending SSE comments after its first text.
func TestAliveCheck(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: Hold on\n\n")
		w.(http.Flusher).Flush()
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				io.WriteString(w, ": ping\n\n")
				w.(http.Flusher).Flush()
			}
		}
	}))
	t.Cleanup(up.Close)
	manifest := BuildExtension(t, "testdata/quiet/pluxy-extension.json", "./testdata/quiet")
	m, err := host.LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]string{"url": up.URL})
	k := &kit{manifest: m, target: Target{
		Manifest: manifest,
		Quiet:    true,
		Prepare: func(t *testing.T, s Scenario) Setup {
			if s != ScenarioQuiet {
				t.Fatalf("scenario %s, want %s", s, ScenarioQuiet)
			}
			return Setup{Account: protocol.Account{ID: "quiet", Settings: settings}}
		},
	}}
	k.alive(t)
}
