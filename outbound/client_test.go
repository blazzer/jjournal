package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type memPauses struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (p *memPauses) List(ctx context.Context) (map[string]time.Time, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]time.Time{}
	for k, v := range p.m {
		out[k] = v
	}
	return out, nil
}

func (p *memPauses) Put(ctx context.Context, host string, until time.Time, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]time.Time{}
	}
	p.m[host] = until
	return nil
}

type fakeResolver struct {
	addrs []netip.Addr
}

func (f fakeResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f.addrs, nil
}

type dialRec struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dialRec) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, address)
	d.mu.Unlock()
	return nil, errors.New("not dialed")
}

func TestRebindingNeverDialsPrivate(t *testing.T) {
	rec := &dialRec{}
	c := New(Config{
		Contact: "ops@example.com",
		Resolver: fakeResolver{addrs: []netip.Addr{
			netip.MustParseAddr("1.1.1.1"),
			netip.MustParseAddr("10.0.0.1"),
		}},
		Dial: rec.Dial,
	})
	_, err := c.DialContext(context.Background(), "tcp", "example.com:443")
	if err == nil {
		t.Fatal("expected dial error")
	}
	if len(rec.addrs) != 1 || rec.addrs[0] != "1.1.1.1:443" {
		t.Fatalf("dialed %v", rec.addrs)
	}
}

func TestPausedHostDoesNotSleep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(Config{Contact: "ops@example.com"})
		c.PauseHost("blocked.example", time.Hour, "test")
		start := time.Now()
		_, err := c.Acquire(t.Context(), LaneAPI, "blocked.example")
		var paused ErrHostPaused
		if !errors.As(err, &paused) {
			t.Fatal(err)
		}
		if time.Since(start) > time.Second {
			t.Fatal("slept through a pause", time.Since(start))
		}
	})
}

func TestAPISpacing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := New(Config{Contact: "ops@example.com"})
		ctx := t.Context()
		rel, err := c.Acquire(ctx, LaneAPI, "www.livejournal.com")
		if err != nil {
			t.Fatal(err)
		}
		rel()
		start := time.Now()
		rel, err = c.Acquire(ctx, LaneAPI, "www.livejournal.com")
		if err != nil {
			t.Fatal(err)
		}
		rel()
		if time.Since(start) < time.Second {
			t.Fatalf("spacing %s", time.Since(start))
		}
	})
}

func TestRetryAfterAndPausePersists(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &memPauses{}
		c := New(Config{Contact: "ops@example.com", Pauses: store})
		d, ok := ParseRetryAfter("120", time.Now())
		if !ok || d != 2*time.Minute {
			t.Fatal(d, ok)
		}
		when := time.Now().Add(3 * time.Hour).UTC()
		d, ok = ParseRetryAfter(when.Format(http.TimeFormat), time.Now())
		if !ok || d < 2*time.Hour {
			t.Fatal(d, ok)
		}
		c.PauseHost("www.livejournal.com", 2*time.Minute, "429")
		until, ok := c.PausedUntil("www.livejournal.com")
		if !ok || time.Until(until) < 6*time.Hour-time.Second {
			t.Fatal(until, ok)
		}
		c2 := New(Config{Contact: "ops@example.com", Pauses: store})
		if err := c2.Load(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, ok := c2.PausedUntil("www.livejournal.com"); !ok {
			t.Fatal("pause did not survive reload")
		}
	})
}

func TestUserAgent(t *testing.T) {
	Version = "test"
	t.Cleanup(func() { Version = "" })
	if got := UserAgent("ops@example.com"); got != "Journal/test (private reader; +ops@example.com)" {
		t.Fatal(got)
	}
}

func TestResponsePauseOn429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" || r.Header.Get("User-Agent")[:7] != "Journal" {
			t.Errorf("ua %q", r.Header.Get("User-Agent"))
		}
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := New(Config{Contact: "ops@example.com", AllowPrivate: true})
	resp, err := c.HTTP(LaneAPI).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if _, ok := c.PausedUntil(resp.Request.URL.Hostname()); !ok {
		t.Fatal("429 did not pause")
	}
}

func TestPublicAddrRanges(t *testing.T) {
	ok := []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"}
	bad := []string{
		"127.0.0.1", "10.1.2.3", "192.168.0.1", "172.16.0.1",
		"100.64.0.1", "169.254.1.1", "198.18.0.1", "192.0.2.1",
		"::1", "fc00::1", "fd00::1", "fe80::1", "2001:db8::1",
		"::ffff:10.0.0.1", "64:ff9b::a00:1", "224.0.0.1",
	}
	for _, s := range ok {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Fatal("want public", s)
		}
	}
	for _, s := range bad {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Fatal("want rejected", s)
		}
	}
}
