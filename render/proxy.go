package render

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"journal/keys"
	"strings"
	"sync"
	"time"
)

const maxImageBytes = 5 << 20

// Proxy fetches and caches signed remote images.
type Proxy struct {
	Key          []byte
	Dir          string
	Client       *http.Client
	AllowPrivate bool

	mu   sync.Mutex
	root *os.Root
}

// Sign returns /img?u= for a remote URL, or an error when the URL is not allowed.
func (p *Proxy) Sign(raw string) (string, error) {
	if p == nil {
		return "", errors.New("render: no proxy")
	}
	if err := validateURL(raw, p.AllowPrivate); err != nil {
		return "", err
	}
	root, err := keys.NewRoot(p.Key)
	if err != nil {
		return "", err
	}
	sig, err := keys.SignImage(root, raw)
	if err != nil {
		return "", err
	}
	token := hex.EncodeToString(root.ID[:]) + "." + base64.RawURLEncoding.EncodeToString([]byte(raw)) + "." + sig
	return "/img?u=" + token, nil
}

// Open verifies a signed token and returns the raw URL.
func (p *Proxy) Open(token string) (string, error) {
	parts := strings.Split(token, ".")
	var urlBytes []byte
	var err error
	switch len(parts) {
	case 3:
		root, err := keys.NewRoot(p.Key)
		if err != nil {
			return "", err
		}
		if hex.EncodeToString(root.ID[:]) != parts[0] {
			return "", errors.New("render: bad image signature")
		}
		urlBytes, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", err
		}
		want, err := keys.SignImage(root, string(urlBytes))
		if err != nil || !hmac.Equal([]byte(want), []byte(parts[2])) {
			return "", errors.New("render: bad image signature")
		}
	case 2:
		urlBytes, err = base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return "", err
		}
		got, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", err
		}
		mac := hmac.New(sha256.New, p.Key)
		mac.Write(urlBytes)
		if !hmac.Equal(got, mac.Sum(nil)) {
			return "", errors.New("render: bad image signature")
		}
	default:
		return "", errors.New("render: bad image token")
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
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return false
	}
	// Unique local fc00::/7.
	if ip16[0]&0xfe == 0xfc {
		return false
	}
	// Documentation prefix 2001:db8::/32.
	if ip16[0] == 0x20 && ip16[1] == 0x01 && ip16[2] == 0x0d && ip16[3] == 0xb8 {
		return false
	}
	// NAT64 64:ff9b::/96. The embedded IPv4 must itself be public.
	if ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b &&
		ip16[4]|ip16[5]|ip16[6]|ip16[7]|ip16[8]|ip16[9]|ip16[10]|ip16[11] == 0 {
		return PublicIP(net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]))
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
	f, mod, err := p.openCached(r.Context(), raw)
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, "img", mod, f)
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

// Close releases the cache directory.
func (p *Proxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root == nil {
		return nil
	}
	err := p.root.Close()
	p.root = nil
	return err
}

func (p *Proxy) files() (*os.Root, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.root != nil {
		return p.root, nil
	}
	if p.Dir == "" {
		return nil, errors.New("render: no cache dir")
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		return nil, err
	}
	p.root = root
	return root, nil
}

func (p *Proxy) openCached(ctx context.Context, raw string) (*os.File, time.Time, error) {
	name, err := p.ensure(ctx, raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	root, err := p.files()
	if err != nil {
		return nil, time.Time{}, err
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, time.Time{}, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, time.Time{}, err
	}
	return f, st.ModTime(), nil
}

func (p *Proxy) cache(ctx context.Context, raw string) (string, error) {
	name, err := p.ensure(ctx, raw)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Dir, name), nil
}

func (p *Proxy) ensure(ctx context.Context, raw string) (string, error) {
	root, err := p.files()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(raw))
	name := fmt.Sprintf("%x", sum[:])
	if st, err := root.Stat(name); err == nil && st.Size() > 0 {
		now := time.Now()
		_ = root.Chtimes(name, now, now)
		return name, nil
	}
	if p.Client == nil {
		return "", errors.New("render: http client is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("render: image http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
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
	in, err := os.Open(tmpName)
	if err != nil {
		os.Remove(tmpName)
		return "", err
	}
	out, err := root.Create(name)
	if err != nil {
		in.Close()
		os.Remove(tmpName)
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		in.Close()
		os.Remove(tmpName)
		root.Remove(name)
		return "", err
	}
	out.Close()
	in.Close()
	os.Remove(tmpName)
	return name, nil
}

// CachedURL returns the local userpic path when raw is already on disk.
func (p *Proxy) CachedURL(raw string) (string, bool) {
	if p == nil || raw == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(raw))
	name := fmt.Sprintf("%x", sum[:])
	root, err := p.files()
	if err != nil {
		return "", false
	}
	st, err := root.Stat(name)
	if err != nil || st.Size() == 0 {
		return "", false
	}
	return "/userpics/" + name, true
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
