package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	LaneAPI   = "api"
	LaneImage = "image"

	maxRedirects   = 3
	dialTimeout    = 10 * time.Second
	requestTimeout = 30 * time.Second
	imageLimit     = 5 << 20
	bodyLimit      = 10 << 20
	pauseFloor     = 6 * time.Hour
)

// Resolver looks up a host. Tests inject a fake.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a connection. Tests inject a recorder.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// PauseStore persists host pauses. Implementations must not import this package's caller cycle.
type PauseStore interface {
	List(ctx context.Context) (map[string]time.Time, error)
	Put(ctx context.Context, host string, until time.Time, reason string) error
}

// Config builds a Client.
type Config struct {
	Contact      string
	AllowPrivate bool
	Resolver     Resolver
	Dial         DialFunc
	Pauses       PauseStore
}

// Client is the only outbound HTTP client.
type Client struct {
	contact      string
	allowPrivate bool
	resolver     Resolver
	dial         DialFunc
	pauses       PauseStore

	mu    sync.Mutex
	until map[string]time.Time

	api   *lane
	image *lane
	base  *http.Transport
	apiC  *http.Client
	imgC  *http.Client
}

// New returns a client that paces requests and refuses non-public addresses.
func New(cfg Config) *Client {
	c := &Client{
		contact:      cfg.Contact,
		allowPrivate: cfg.AllowPrivate,
		resolver:     cfg.Resolver,
		pauses:       cfg.Pauses,
		until:        map[string]time.Time{},
		api:          newLane(2, time.Second),
		image:        newLane(4, 250*time.Millisecond),
	}
	if c.resolver == nil {
		c.resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{Timeout: dialTimeout, ControlContext: c.control}
	if cfg.Dial != nil {
		c.dial = cfg.Dial
	} else {
		c.dial = dialer.DialContext
	}
	c.base = &http.Transport{
		Proxy:                 nil,
		DialContext:           c.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: requestTimeout,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          8,
	}
	c.apiC = c.httpClient(LaneAPI)
	c.imgC = c.httpClient(LaneImage)
	return c
}

func (c *Client) httpClient(lane string) *http.Client {
	return &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: c.redirect,
		Transport:     &laneTrip{c: c, lane: lane, base: c.base},
	}
}

// HTTP returns the client for a lane. lane is LaneAPI or LaneImage.
func (c *Client) HTTP(lane string) *http.Client {
	if lane == LaneImage {
		return c.imgC
	}
	return c.apiC
}

// Load reads persisted pauses.
func (c *Client) Load(ctx context.Context) error {
	if c.pauses == nil {
		return nil
	}
	m, err := c.pauses.List(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, until := range m {
		if prev, ok := c.until[host]; !ok || until.After(prev) {
			c.until[host] = until
		}
	}
	return nil
}

// PausedUntil reports an active pause.
func (c *Client) PausedUntil(host string) (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.until[host]
	if !ok || !until.After(time.Now()) {
		return time.Time{}, false
	}
	return until, true
}

// PauseHost pauses host for at least d, and for at least 6 hours.
func (c *Client) PauseHost(host string, d time.Duration, reason string) {
	if d < pauseFloor {
		d = pauseFloor
	}
	until := time.Now().Add(d)
	c.mu.Lock()
	if prev, ok := c.until[host]; ok && prev.After(until) {
		until = prev
	}
	c.until[host] = until
	c.mu.Unlock()
	if c.pauses != nil {
		_ = c.pauses.Put(context.Background(), host, until, reason)
	}
}

// Acquire waits for lane capacity and per-host spacing.
// A paused host returns ErrHostPaused without sleeping.
func (c *Client) Acquire(ctx context.Context, lane, host string) (func(), error) {
	if until, ok := c.PausedUntil(host); ok {
		return nil, ErrHostPaused{Until: until}
	}
	l := c.api
	if lane == LaneImage {
		l = c.image
	}
	return l.acquire(ctx, host, func() error {
		if until, ok := c.PausedUntil(host); ok {
			return ErrHostPaused{Until: until}
		}
		return nil
	})
}

// ErrHostPaused means the host is not to be contacted yet.
type ErrHostPaused struct {
	Until time.Time
}

func (e ErrHostPaused) Error() string {
	return "outbound: host paused until " + e.Until.UTC().Format(time.RFC3339)
}

func (c *Client) allow(addr netip.Addr) bool {
	if c.allowPrivate {
		return addr.IsValid()
	}
	return PublicAddr(addr)
}

// DialContext resolves host, rejects non-public answers, and dials a vetted address.
func (c *Client) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("outbound: bad port")
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip.Unmap()}
	} else {
		addrs, err = c.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	var vetted []netip.AddrPort
	for _, ip := range addrs {
		ip = ip.Unmap()
		if !c.allow(ip) {
			continue
		}
		vetted = append(vetted, netip.AddrPortFrom(ip, uint16(portNum)))
	}
	if len(vetted) == 0 {
		return nil, fmt.Errorf("outbound: no public address for %s", host)
	}
	var last error
	for _, ap := range vetted {
		conn, err := c.dial(ctx, "tcp", ap.String())
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (c *Client) control(ctx context.Context, network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !c.allow(ip.Unmap()) {
		return fmt.Errorf("outbound: refused %s", ip)
	}
	return nil
}

func (c *Client) redirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errors.New("outbound: too many redirects")
	}
	host := req.URL.Hostname()
	if ip, err := netip.ParseAddr(host); err == nil {
		if !c.allow(ip.Unmap()) {
			return fmt.Errorf("outbound: refused redirect to %s", host)
		}
		return nil
	}
	addrs, err := c.resolver.LookupNetIP(req.Context(), "ip", host)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("outbound: redirect host %s did not resolve", host)
	}
	for _, ip := range addrs {
		if !c.allow(ip.Unmap()) {
			return fmt.Errorf("outbound: refused redirect to %s", host)
		}
	}
	return nil
}

type laneTrip struct {
	c    *Client
	lane string
	base http.RoundTripper
}

func (t *laneTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	release, err := t.c.Acquire(req.Context(), t.lane, host)
	if err != nil {
		return nil, err
	}
	defer release()
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", UserAgent(t.c.contact))
	resp, err := t.base.RoundTrip(req)
	if resp != nil {
		t.c.observe(host, resp)
		limit := int64(bodyLimit)
		if t.lane == LaneImage {
			limit = imageLimit
		}
		resp.Body = &limitedBody{r: io.LimitReader(resp.Body, limit+1), c: resp.Body, limit: limit}
	}
	return resp, err
}

func (c *Client) observe(host string, resp *http.Response) {
	d, ok := ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		c.PauseHost(host, d, "429")
	case http.StatusServiceUnavailable:
		if ok {
			c.PauseHost(host, d, "503")
		}
	}
}

type limitedBody struct {
	r     io.Reader
	c     io.Closer
	limit int64
	read  int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, fmt.Errorf("outbound: response exceeds %d bytes", b.limit)
	}
	return n, err
}

func (b *limitedBody) Close() error { return b.c.Close() }
