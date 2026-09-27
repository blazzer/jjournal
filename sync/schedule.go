package sync

import "time"

const (
	DefaultInterval    = 20 * time.Minute
	DefaultJitter      = 5 * time.Minute
	DefaultBlockFor    = 6 * time.Hour
	DefaultFriendEvery = 24 * time.Hour
	DefaultConcurrent  = 2
	// DefaultMaxPages is how many friends-page requests one sync may make.
	// Each page is 50 entries. Three pages plus the one-second gate stays
	// under a burst while still moving through the 1,000-entry window.
	DefaultMaxPages = 3
)

// JitteredInterval returns interval plus a fraction of the jitter window.
// unit 0 is interval-jitter and unit 1 is interval+jitter.
func JitteredInterval(interval, jitter time.Duration, unit float64) time.Duration {
	if unit < 0 {
		unit = 0
	}
	if unit > 1 {
		unit = 1
	}
	return interval - jitter + time.Duration(float64(2*jitter)*unit)
}

// Backoff is the delay after failCount consecutive errors, capped at 20 minutes.
func Backoff(failCount int) time.Duration {
	if failCount < 1 {
		failCount = 1
	}
	if failCount > 10 {
		return 20 * time.Minute
	}
	d := 30 * time.Second * time.Duration(uint(1)<<uint(failCount-1))
	if d > 20*time.Minute || d <= 0 {
		return 20 * time.Minute
	}
	return d
}
