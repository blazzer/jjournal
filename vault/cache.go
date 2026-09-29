package vault

import (
	"sync"
	"time"
)

const cacheTTL = 15 * time.Minute

type cached struct {
	dek   []byte
	until time.Time
}

// Cache holds unwrapped data keys for a short time after sign-in.
type Cache struct {
	mu    sync.Mutex
	items map[string]cached
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{items: map[string]cached{}}
}

// Put stores a copy of dek until now+15m.
func (c *Cache) Put(sessionID string, dek []byte, now time.Time) {
	if c == nil || sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[sessionID] = cached{dek: append([]byte(nil), dek...), until: now.Add(cacheTTL)}
}

// Get returns the data key when it has not expired.
func (c *Cache) Get(sessionID string, now time.Time) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.items[sessionID]
	if !ok || !now.Before(item.until) {
		delete(c.items, sessionID)
		return nil, false
	}
	return append([]byte(nil), item.dek...), true
}

// Drop forgets one session.
func (c *Cache) Drop(sessionID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, sessionID)
}
