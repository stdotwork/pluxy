package sdk

import "time"

const (
	// MaxLoginWait is the longest a device code is believed to live.
	MaxLoginWait = 24 * time.Hour
	// MaxTokenLife is the longest an access token is believed to live.
	MaxTokenLife    = 365 * 24 * time.Hour
	MaxPollInterval = 5 * time.Minute
)

// BoundSeconds turns a provider's number of seconds into a duration of at most max. Zero or
// less returns 0; a huge number does not overflow.
func BoundSeconds(secs int64, max time.Duration) time.Duration {
	if secs <= 0 {
		return 0
	}
	if secs >= int64(max/time.Second) {
		return max
	}
	return time.Duration(secs) * time.Second
}

// ExpiryUnix returns the Unix time a credential that lives expiresIn seconds from now expires, or 0
// when the provider named no (or a negative) lifetime. The lifetime is cut to MaxTokenLife.
func ExpiryUnix(now time.Time, expiresIn int64) int64 {
	d := BoundSeconds(expiresIn, MaxTokenLife)
	if d == 0 {
		return 0
	}
	return now.Unix() + int64(d/time.Second)
}
