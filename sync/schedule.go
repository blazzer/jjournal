package sync

import (
	"math/rand/v2"
	"time"
)

const (
	DefaultInterval    = 20 * time.Minute
	DefaultJitter      = 5 * time.Minute
	DefaultBlockFor    = 6 * time.Hour
	DefaultFriendEvery = 24 * time.Hour
	DefaultConcurrent  = 2
	FeedInterval       = 60 * time.Minute
	FeedJitter         = 10 * time.Minute
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

const maxBackoff = 6 * time.Hour

// BackoffBase is the delay before jitter: min(interval * 2^(n-1), 6h).
// n of 1 equals interval.
func BackoffBase(interval time.Duration, n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := interval
	for i := 1; i < n; i++ {
		if d >= maxBackoff/2 {
			return maxBackoff
		}
		d *= 2
		if d > maxBackoff || d <= 0 {
			return maxBackoff
		}
	}
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// Backoff applies ±20% jitter. unit 0 is -20% and unit 1 is +20%.
func Backoff(interval time.Duration, n int, unit float64) time.Duration {
	if unit < 0 {
		unit = 0
	}
	if unit > 1 {
		unit = 1
	}
	base := BackoffBase(interval, n)
	scale := 0.8 + 0.4*unit
	return time.Duration(float64(base) * scale)
}

// Unit returns a production jitter sample in [0, 1].
func Unit() float64 { return rand.Float64() }
