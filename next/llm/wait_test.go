package llm

import (
	"testing"
	"time"
)

func TestWholeSeconds(t *testing.T) {
	for _, c := range []struct{ in, want time.Duration }{
		{-time.Second, 0},
		{0, 0},
		{time.Nanosecond, time.Second},
		{200 * time.Millisecond, time.Second},
		{499 * time.Millisecond, time.Second},
		{999 * time.Millisecond, time.Second},
		{time.Second, time.Second},
		{1200 * time.Millisecond, 2 * time.Second},
		{59900 * time.Millisecond, time.Minute},
		{time.Minute, time.Minute},
		{time.Minute + time.Millisecond, time.Minute + time.Second},
	} {
		if got := WholeSeconds(c.in); got != c.want {
			t.Errorf("WholeSeconds(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
