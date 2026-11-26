package e2e

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// With soonest-reset, two accounts whose weekly windows end on different days: once core has read
// their usage, new requests go to the one whose week ends first, and pluxy route shows the reset
// each account was ranked by.
func TestSoonestResetRoutesToTheWeekThatEndsFirst(t *testing.T) {
	// The readings' loop ticks every 100 ms; the first refresh is due at its first tick.
	t.Setenv("PLUXY_TEST_CHECK_INTERVAL_MS", "1500")
	pkg := testPackageID(t, "test/soonest", "1.0.0")
	logPath := filepath.Join(t.TempDir(), "generate.log")
	now := time.Now()
	soon, late := now.Add(2*24*time.Hour).Unix(), now.Add(6*24*time.Hour).Unix()
	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"routing":        map[string]any{"strategy": "soonest-reset"},
		"extension": map[string]any{
			"manifest": filepath.Join(pkg, "pluxy-extension.json"),
			"accounts": []any{
				// The later week comes first in the config, so the order is not the config's.
				map[string]any{"id": "late", "settings": map[string]any{"weekly_resets_at": late, "generate_log": logPath}},
				map[string]any{"id": "soon", "settings": map[string]any{"weekly_resets_at": soon, "generate_log": logPath}},
			},
		},
	})

	plan := func() mgmt.RouteReply {
		t.Helper()
		code, b := mcall(t, base, managementKey, http.MethodGet, "/route?model="+url.QueryEscape("plain"), nil)
		if code != http.StatusOK {
			t.Fatalf("GET /route: %d %s", code, b)
		}
		var r mgmt.RouteReply
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	read := func(r mgmt.RouteReply) bool {
		for _, s := range r.Order {
			if s.Rank == nil || s.Rank.Reset == nil {
				return false
			}
		}
		return len(r.Order) == 2
	}
	deadline := time.Now().Add(30 * time.Second)
	r := plan()
	for !read(r) {
		if time.Now().After(deadline) {
			t.Fatalf("no usage readings after 30 s: %+v", r)
		}
		time.Sleep(50 * time.Millisecond)
		r = plan()
	}
	hour := func(unix int64) time.Time { return time.Unix(unix, 0).UTC().Round(time.Hour) }
	if r.Order[0].ID != "soon" || r.Order[1].ID != "late" ||
		!r.Order[0].Rank.Reset.Equal(hour(soon)) || !r.Order[1].Rank.Reset.Equal(hour(late)) ||
		r.Order[0].Rank.WindowSeconds != 7*24*3600 {
		t.Fatalf("order %+v %+v, want soon (week ends %v) then late (%v)", r.Order[0], r.Order[1], hour(soon), hour(late))
	}

	const body = `{"model":"plain","messages":[{"role":"user","content":"hi"}]}`
	for range 3 {
		postOK(t, base, body)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); !slices.Equal(got, []string{"soon", "soon", "soon"}) {
		t.Errorf("requests went to %v, want soon each time", got)
	}

	res := runCLI(t, nil, "route", "plain", "--url", base, "--key", managementKey)
	lines := strings.Split(res.stdout, "\n")
	at := func(unix int64) string { return hour(unix).Local().Format("2006-01-02 15:04") + " (7d window)" }
	if res.code != 0 || len(lines) < 3 || !strings.Contains(lines[0], "RESET") ||
		!strings.Contains(lines[1], "soon") || !strings.Contains(lines[1], at(soon)) ||
		!strings.Contains(lines[2], "late") || !strings.Contains(lines[2], at(late)) {
		t.Errorf("pluxy route plain: %s\nwant soon's reset %q, then late's %q", res, at(soon), at(late))
	}
}
