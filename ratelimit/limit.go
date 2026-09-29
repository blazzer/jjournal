// Package ratelimit slows sign-in and signup attempts. It never locks a handle out forever.
package ratelimit

import (
	"sync"
	"time"
)

const (
	window     = 15 * time.Minute
	pairLimit  = 5
	ipLimit    = 20
	pauseFor   = 15 * time.Minute
	signupWin  = time.Hour
	signupMax  = 10
	serviceWin = time.Hour
	serviceMax = 5
)

// Decision is what the caller should do before checking a passphrase.
type Decision struct {
	Paused bool
	Delay  time.Duration
	Until  time.Time
}

// Gate counts failures in memory. A restart clears it.
type Gate struct {
	mu      sync.Mutex
	pair    map[string][]time.Time
	ip      map[string][]time.Time
	handleN map[string]int
	paused  map[string]time.Time
	signup  map[string][]time.Time
	service map[string][]time.Time
}

// New returns an empty gate.
func New() *Gate {
	return &Gate{
		pair:    map[string][]time.Time{},
		ip:      map[string][]time.Time{},
		handleN: map[string]int{},
		paused:  map[string]time.Time{},
		signup:  map[string][]time.Time{},
		service: map[string][]time.Time{},
	}
}

func pairKey(handle, ip string) string { return handle + "\x00" + ip }

// Allow reports a pause or a delay earned by earlier failures.
func (g *Gate) Allow(now time.Time, handle, ip string) Decision {
	if g == nil {
		return Decision{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.paused[pairKey(handle, ip)]; ok && now.Before(until) {
		return Decision{Paused: true, Until: until}
	}
	if until, ok := g.paused["ip\x00"+ip]; ok && now.Before(until) {
		return Decision{Paused: true, Until: until}
	}
	n := g.handleN[handle]
	if n <= 0 {
		return Decision{}
	}
	delay := time.Second << (n - 1)
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return Decision{Delay: delay}
}

// Fail records one rejected sign-in.
func (g *Gate) Fail(now time.Time, handle, ip string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	pk := pairKey(handle, ip)
	g.pair[pk] = prune(append(g.pair[pk], now), now, window)
	g.ip[ip] = prune(append(g.ip[ip], now), now, window)
	if len(g.pair[pk]) >= pairLimit {
		g.paused[pk] = now.Add(pauseFor)
	}
	if len(g.ip[ip]) >= ipLimit {
		g.paused["ip\x00"+ip] = now.Add(pauseFor)
	}
	n := g.handleN[handle] + 1
	if n > 6 {
		n = 6
	}
	g.handleN[handle] = n
}

// Reset clears the per-handle delay after a successful sign-in.
func (g *Gate) Reset(handle string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.handleN, handle)
}

// AllowSignup is false after too many signup attempts from one IP.
func (g *Gate) AllowSignup(now time.Time, ip string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.signup[ip] = prune(g.signup[ip], now, signupWin)
	return len(g.signup[ip]) < signupMax
}

// Signup records one signup attempt.
func (g *Gate) Signup(now time.Time, ip string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.signup[ip] = append(prune(g.signup[ip], now, signupWin), now)
}

// AllowService is false after 5 service-password failures in an hour for this key.
func (g *Gate) AllowService(now time.Time, key string) bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.service[key] = prune(g.service[key], now, serviceWin)
	return len(g.service[key]) < serviceMax
}

// ServiceFail records one service-password failure.
func (g *Gate) ServiceFail(now time.Time, key string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.service[key] = append(prune(g.service[key], now, serviceWin), now)
}

func prune(in []time.Time, now time.Time, win time.Duration) []time.Time {
	out := in[:0]
	for _, t := range in {
		if now.Sub(t) < win {
			out = append(out, t)
		}
	}
	return out
}
