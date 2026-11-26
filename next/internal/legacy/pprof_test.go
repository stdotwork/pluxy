package legacy

import (
	"reflect"
	"strings"
	"testing"
)

// The profiling pages' switch and address are read from observability.pprof and kept as the
// JSON pprof section, with no notice: both are supported.
func TestPprofSettings(t *testing.T) {
	r := translate(t, "observability:\n  pprof:\n    enable: true\n    addr: \"127.0.0.1:9000\"\n")
	if got := asJSON(t, r.Config["pprof"]); !reflect.DeepEqual(got, map[string]any{"enable": true, "addr": "127.0.0.1:9000"}) {
		t.Errorf("pprof section = %v, want enable and addr", got)
	}
	for _, n := range r.Notices {
		if strings.Contains(n, "pprof") {
			t.Errorf("notice %q; the pprof settings are supported", n)
		}
	}

	off := translate(t, "observability:\n  pprof:\n    enable: false\n")
	if _, ok := off.Config["pprof"]; ok {
		t.Errorf("pprof section = %v with it off; want none", off.Config["pprof"])
	}
}
