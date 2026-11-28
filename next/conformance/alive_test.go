package conformance

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/protocol"
)

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

func TestAliveMinorForQuietTarget(t *testing.T) {
	old := protocol.Info{ProtocolMinor: host.AliveMinor - 1}
	if err := aliveMinorError(old, true); err == nil || !strings.Contains(err.Error(), "protocol 0.") {
		t.Fatalf("old quiet target: error = %v, want a failure naming its protocol minor", err)
	}
	if err := aliveMinorError(old, false); err != nil {
		t.Fatalf("old non-quiet target: error = %v, want a skip path", err)
	}
	current := protocol.Info{ProtocolMinor: host.AliveMinor}
	if err := aliveMinorError(current, true); err != nil {
		t.Fatalf("current quiet target: error = %v", err)
	}
}

func TestAccountClosedMinor(t *testing.T) {
	old := protocol.Info{ProtocolMinor: host.AccountClosedMinor - 1}
	if err := accountClosedMinorError(old); err == nil || !strings.Contains(err.Error(), "protocol 0.") {
		t.Fatalf("old extension: error = %v, want a failure naming its protocol minor", err)
	}
	current := protocol.Info{ProtocolMinor: host.AccountClosedMinor}
	if err := accountClosedMinorError(current); err != nil {
		t.Fatalf("current extension: error = %v", err)
	}
}
