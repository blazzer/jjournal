package lj

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RequestGap is the minimum space between LiveJournal HTTP calls in production.
// The friends-page API has no published quota. One request a second stays far
// under what gets a captcha, and it is slow enough that a full window walk
// cannot burst.
const RequestGap = time.Second

// FriendsWindow is how long an entry stays on a friends page.
// https://www.livejournal.com/support/faq/92.html
const FriendsWindow = 14 * 24 * time.Hour

// MaxFriendsSkip is the most entries the friends page will hold.
// https://www.livejournal.com/support/faq/219.html
const MaxFriendsSkip = 1000

// Gate spaces LiveJournal calls across every source that shares it.
type Gate struct {
	gap  time.Duration
	mu   sync.Mutex
	next time.Time
}

// NewGate returns a gate. A non-positive gap does not wait.
func NewGate(gap time.Duration) *Gate {
	return &Gate{gap: gap}
}

// Wait blocks until this call may start. It reserves the following slot
// before sleeping, so overlapping callers stay in order.
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil || g.gap <= 0 {
		return nil
	}
	g.mu.Lock()
	wait := time.Until(g.next)
	if wait < 0 {
		wait = 0
	}
	g.next = time.Now().Add(wait + g.gap)
	g.mu.Unlock()
	if wait == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// LimitError means LiveJournal rejected a paging parameter.
// The open-source server faults skip above 100 and itemshow above 100,
// and it never returns more than 50 friends-page entries per call.
type LimitError struct {
	Param string
}

func (e *LimitError) Error() string {
	if e == nil || e.Param == "" {
		return "lj: paging limit"
	}
	return "lj: paging limit: " + e.Param
}

// IsLimit reports whether err is a paging-parameter fault.
func IsLimit(err error) bool {
	var l *LimitError
	return errors.As(err, &l)
}
