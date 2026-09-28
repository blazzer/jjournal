package outbound

import (
	"context"
	"sync"
	"time"
)

type lane struct {
	mu       sync.Mutex
	limit    int
	gap      time.Duration
	inflight int
	next     map[string]time.Time
}

func newLane(limit int, gap time.Duration) *lane {
	return &lane{limit: limit, gap: gap, next: map[string]time.Time{}}
}

func (l *lane) acquire(ctx context.Context, host string, paused func() error) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := paused(); err != nil {
			return nil, err
		}
		l.mu.Lock()
		if l.inflight >= l.limit {
			l.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Millisecond):
			}
			continue
		}
		delay := time.Until(l.next[host])
		if delay > 0 {
			l.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		l.inflight++
		l.next[host] = time.Now().Add(l.gap)
		l.mu.Unlock()
		return func() {
			l.mu.Lock()
			l.inflight--
			l.mu.Unlock()
		}, nil
	}
}
