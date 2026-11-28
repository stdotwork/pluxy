package llm

import "time"

// WholeSeconds is the wait d as a client is told it: rounded up to whole seconds, so it never
// says less than is left, and at least one second while any is left. A wait that is over (0 or
// less) stays 0.
func WholeSeconds(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return (d + time.Second - 1) / time.Second * time.Second
}
