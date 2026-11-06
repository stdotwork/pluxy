package server

import (
	"strings"
	"testing"
)

// An extension timing too large for a time.Duration is refused, not wrapped into a negative
// duration.
func TestConfigRefusesHugeTimings(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","extension":{"manifest":"m.json","accounts":[{"id":"a"}],"restart":{"stable_ms":9223372036855}}}`
	if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); err == nil || !strings.Contains(err.Error(), "timings") {
		t.Errorf("ParseConfig = %v, want an error about the timings", err)
	}
}
