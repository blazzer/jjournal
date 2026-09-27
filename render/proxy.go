package render

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxImageBytes = 5 << 20

// Proxy fetches and caches signed remote images.
type Proxy struct {
	Key          []byte
	Dir          string
	Client       *http.Client
	AllowPrivate bool
}

// Sign returns /img?u= for a remote URL, or an error when the URL is not allowed.
func (p *Proxy) Sign(raw string) (string, error) {
	if p == nil {
		return "", errors.New("render: no proxy")
	}
	if err := validateURL(raw, p.AllowPrivate); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, p.Key)
	mac.Write([]byte(raw))
	token := base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return "/img?u=" + token, nil
}

// Open verifies a signed token and returns the raw URL.
func (p *Proxy) Open(token string) (string, error) {
	raw, sig, ok := strings.Cut(token, ".")
	if !ok || raw == "" || sig == "" {
		return "", errors.New("render: bad image token")
	}
	urlBytes, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, p.Key)
	mac.Write(urlBytes)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", errors.New("render: bad image signature")
	}
	u := string(urlBytes)
	if err := validateURL(u, p.AllowPrivate); err != nil {
		return "", err
	}
	return u, nil
}

func validateURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("render: image scheme")
	}
	if u.User != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("render: image host")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && !allowPrivate && !PublicIP(ip) {
		return errors.New("render: private image host")
	}
	return nil
}

// PublicIP reports whether ip is a routable unicast address.
func PublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip = ip.To16()
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0, v4[0] == 100 && v4[1]&0xc0 == 64, v4[0] == 192 && v4[1] == 0 && v4[2] == 0,
			v4[0] == 192 && v4[1] == 0 && v4[2] == 2, v4[0] == 198 && v4[1] == 51 && v4[2] == 100,
			v4[0] == 203 && v4[1] == 0 && v4[2] == 113, v4[0] == 198 && v4[1]&0xfe == 18,
			v4[0] >= 224:
			return false
		}
	}
	return true
}

// ServeHTTP implements GET /img?u=.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	raw, err := p.Open(r.URL.Query().Get("u"))
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	path, err := p.cache(r.Context(), raw)
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	http.ServeFile(w, r, path)
}

// Download caches raw and returns a site path under /userpics or /img is not used.
// Userpics are served from /userpics/<file>.
func (p *Proxy) Download(ctx context.Context, raw string) (string, error) {
	path, err := p.cache(ctx, raw)
	if err != nil {
		return "", err
	}
	return "/userpics/" + filepath.Base(path), nil
}

func (p *Proxy) cache(ctx context.Context, raw string) (string, error) {
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(raw))
	name := fmt.Sprintf("%x", sum[:])
	final := filepath.Join(p.Dir, name)
	if st, err := os.Stat(final); err == nil && st.Size() > 0 {
		return final, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Journal/1.0")
	resp, err := p.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("render: image http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > maxImageBytes || !imageMagic(data) {
		return "", errors.New("render: not an image")
	}
	tmp, err := os.CreateTemp(p.Dir, "img-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, final); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return final, nil
}

// CachedURL returns the local userpic path when raw is already on disk.
func (p *Proxy) CachedURL(raw string) (string, bool) {
	if p == nil || raw == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(raw))
	name := fmt.Sprintf("%x", sum[:])
	final := filepath.Join(p.Dir, name)
	st, err := os.Stat(final)
	if err != nil || st.Size() == 0 {
		return "", false
	}
	return "/userpics/" + name, true
}

func (p *Proxy) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{
		Timeout:       20 * time.Second,
		CheckRedirect: p.checkRedirect,
		Transport:     &http.Transport{DialContext: p.dial},
	}
}

func (p *Proxy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return errors.New("render: too many redirects")
	}
	return validateURL(req.URL.String(), p.AllowPrivate)
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !p.AllowPrivate && !PublicIP(ip) {
			return nil, errors.New("render: refused private address")
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if !p.AllowPrivate && !PublicIP(ip.IP) {
			return nil, errors.New("render: refused private address")
		}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(host, port))
}

func imageMagic(b []byte) bool {
	if len(b) >= 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff {
		return true
	}
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return true
	}
	if len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a") {
		return true
	}
	if len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP" {
		return true
	}
	return false
}

// LocalUserpicPath maps /userpics/<hex> onto a file in dir.
func LocalUserpicPath(dir, urlPath string) (string, error) {
	name := strings.TrimPrefix(urlPath, "/userpics/")
	if len(name) != 64 {
		return "", errors.New("render: bad userpic")
	}
	for _, r := range name {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return "", errors.New("render: bad userpic")
		}
	}
	return filepath.Join(dir, name), nil
}
