package server

import (
	"strings"
	"testing"
	"time"
)

// inexact marks settings with a number a browser would write back changed, and only those,
// however long the number's text.
func TestInexactSettingsNumbers(t *testing.T) {
	zeros := strings.Repeat("0", 20000)
	for tok, want := range map[string]bool{
		"0.10000000000000001":               true,
		"1e-400":                            true,
		"1e400":                             true,
		"-1e400":                            true,
		"9007199254740993":                  true,
		"1e99999999999999999999999":         true,
		"1" + zeros + "1e-20001":            true, // 1.000…01, which a browser reads as 1
		"0.1":                               false,
		"1.0":                               false,
		"-0":                                false,
		"-0.0e-5":                           false,
		"0e99999999999999999999":            false,
		"2e3":                               false,
		"9007199254740994":                  false,
		"1e300":                             false,
		"5e-324":                            false,
		"1" + zeros + "e-20000":             false, // 1
		"1." + strings.Repeat("0", 1000001): false, // 1
		"0.00" + zeros + "123e" + "20003":   false, // 1.23
		"-12345678901234567890e-10":         true,  // a browser writes -1234567890.1234567
	} {
		if got := inexact([]byte(`{"n":` + tok + `}`)); got != want {
			t.Errorf("inexact(%.40s… [%d bytes]) = %v, want %v", tok, len(tok), got, want)
		}
	}
	if !inexact([]byte(`{"a":[1,{"b":"x","c":0.10000000000000001}]}`)) {
		t.Error("a number deep in the settings is not looked at")
	}
}

// A very long number costs time in proportion to its length, not more: a 1 MB one well under
// 50 ms, or ten times that with the race detector.
func TestInexactSettingsNumbersIsFast(t *testing.T) {
	bound := 50 * time.Millisecond
	if raceDetector {
		bound *= 10
	}
	for _, tok := range []string{
		"1." + strings.Repeat("0", 1000000),
		"1" + strings.Repeat("0", 1000000) + "e-1000000",
		"0." + strings.Repeat("3", 1000000),
	} {
		b := []byte(`{"n":` + tok + `}`)
		best := time.Hour
		for range 3 {
			start := time.Now()
			inexact(b)
			best = min(best, time.Since(start))
		}
		if best > bound {
			t.Errorf("inexact of a %d-byte number took %v", len(tok), best)
		}
	}
}
