package corpus

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The old core's ports come from below the system's ephemeral range, so no listener that binds
// port 0 during the run can take one before the old core binds it, and no port is handed out
// twice.
func TestFreePortIsOutsideTheEphemeralRange(t *testing.T) {
	ephemeralMin := 32768
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		if f := strings.Fields(string(b)); len(f) == 2 {
			ephemeralMin, _ = strconv.Atoi(f[0])
		}
	}
	seen := map[int]bool{}
	for range 200 {
		port, err := freePort()
		if err != nil {
			t.Fatal(err)
		}
		if port < oldCorePortMin || port > oldCorePortMax || port >= ephemeralMin {
			t.Fatalf("port %d is outside %d-%d or inside the ephemeral range from %d", port, oldCorePortMin, oldCorePortMax, ephemeralMin)
		}
		if seen[port] {
			t.Fatalf("port %d handed out twice", port)
		}
		seen[port] = true
	}
}
