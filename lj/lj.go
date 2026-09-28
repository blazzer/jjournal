// Package lj reads LiveJournal. It never posts or comments.
package lj

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	XMLRPCEndpoint = "https://www.livejournal.com/interface/xmlrpc"
	ClientVersion  = "Journal/1.0"
	ItemShow       = 50
	MaxScrapePages = 5
)

func useClient(c *http.Client) (*http.Client, error) {
	if c == nil {
		return nil, errors.New("lj: http client is required")
	}
	return c, nil
}

// LJSource is the read-only LiveJournal bridge.
type LJSource interface {
	Login(ctx context.Context, user string, pwMD5 string) (Session, error)
	FriendList(ctx context.Context, s Session) ([]LJFriend, error)
	FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error)
}

// Session is an in-memory LJ login. String redacts secrets.
type Session struct {
	Username    string
	PasswordMD5 string
	Cookie      string
	FullName    string
	extra       *sessionExtra
}

type sessionExtra struct {
	mu     sync.Mutex
	groups []LJGroup
	have   bool
}

// LJFriend is one friend-of relation.
type LJFriend struct {
	Username   string
	FullName   string
	GroupMask  uint32
	UserpicURL string
}

// LJGroup is a friend group. ID is the 1-based bit index LiveJournal uses.
type LJGroup struct {
	ID        int
	Name      string
	SortOrder int
	Public    bool
}

// LJEntry is one friends-page or journal item.
type LJEntry struct {
	ItemID       int64
	Journal      string
	JournalType  string
	Author       string
	Subject      string
	EventHTML    string
	EventTime    time.Time
	Security     string
	AllowMask    uint32
	UserpicURL   string
	Mood         string
	Music        string
	CommentCount int
	URL          string
}

// AuthError means the username or password was rejected.
type AuthError struct{ Reason string }

func (e *AuthError) Error() string {
	if e == nil || e.Reason == "" {
		return "lj: authentication failed"
	}
	return "lj: authentication failed: " + e.Reason
}

// BlockedError means LiveJournal returned a block, rate limit, or captcha.
type BlockedError struct {
	StatusCode int
	Captcha    bool
	Reason     string
}

func (e *BlockedError) Error() string {
	if e == nil {
		return "lj: blocked"
	}
	if e.Captcha {
		if e.Reason != "" && e.Reason != "captcha" {
			return "lj: blocked: " + e.Reason
		}
		return "lj: blocked: captcha"
	}
	if e.Reason != "" {
		return "lj: blocked: " + e.Reason
	}
	return fmt.Sprintf("lj: blocked: http %d", e.StatusCode)
}

// UnsupportedError means this backend cannot perform the method.
type UnsupportedError struct {
	Source string
	Method string
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return "lj: unsupported"
	}
	return e.Source + " does not support " + e.Method
}

// FaultError is an XML-RPC fault that is not an auth failure.
type FaultError struct {
	Code    int
	Message string
}

func (e *FaultError) Error() string {
	if e == nil {
		return "lj: xmlrpc fault"
	}
	return fmt.Sprintf("lj: xmlrpc fault %d: %s", e.Code, e.Message)
}

// IsAuth reports whether err is an authentication failure.
func IsAuth(err error) bool {
	var a *AuthError
	return errors.As(err, &a)
}

// IsBlocked reports whether err is a block or captcha.
func IsBlocked(err error) bool {
	var b *BlockedError
	return errors.As(err, &b)
}

// IsUnsupported reports whether err is a typed unsupported error.
func IsUnsupported(err error) bool {
	var u *UnsupportedError
	return errors.As(err, &u)
}

// NewSession builds a session that can carry friend groups back to the caller.
func NewSession(user, pwMD5, cookie, fullName string) Session {
	return Session{
		Username:    user,
		PasswordMD5: pwMD5,
		Cookie:      cookie,
		FullName:    fullName,
		extra:       &sessionExtra{},
	}
}

func (s Session) String() string {
	return "lj.Session{user:" + s.Username + "}"
}

func (s Session) noteGroups(groups []LJGroup) {
	if s.extra == nil {
		return
	}
	s.extra.mu.Lock()
	defer s.extra.mu.Unlock()
	s.extra.groups = append([]LJGroup(nil), groups...)
	s.extra.have = true
}

// Groups returns friend groups recorded by the last FriendList call on this session.
func (s Session) Groups() ([]LJGroup, bool) {
	if s.extra == nil {
		return nil, false
	}
	s.extra.mu.Lock()
	defer s.extra.mu.Unlock()
	if !s.extra.have {
		return nil, false
	}
	return append([]LJGroup(nil), s.extra.groups...), true
}

// PasswordMD5 is the lowercase hex MD5 of the password.
func PasswordMD5(password string) string {
	sum := md5.Sum([]byte(password))
	return hex.EncodeToString(sum[:])
}

// AuthResponse is md5(challenge + pwMD5), hex encoded.
func AuthResponse(challenge, pwMD5 string) string {
	sum := md5.Sum([]byte(challenge + pwMD5))
	return hex.EncodeToString(sum[:])
}

// NormalizeUsername lowercases a LiveJournal username.
func NormalizeUsername(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 1 || len(s) > 25 {
		return "", fmt.Errorf("lj: invalid username")
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return "", fmt.Errorf("lj: invalid username")
		}
	}
	return s, nil
}

// NormalizeSecurity maps a LiveJournal security value onto the local set.
func NormalizeSecurity(security string, allowMask uint32) (string, uint32) {
	switch strings.ToLower(strings.TrimSpace(security)) {
	case "", "public":
		return "public", 0
	case "private":
		return "private", 0
	case "friends":
		return "friends", 1
	case "usemask":
		if allowMask == 0 || allowMask == 1 {
			if allowMask == 0 {
				return "private", 0
			}
			return "friends", 1
		}
		return "custom", allowMask
	case "custom":
		if allowMask == 0 {
			return "private", 0
		}
		if allowMask == 1 {
			return "friends", 1
		}
		return "custom", allowMask
	default:
		return "public", 0
	}
}

// FilterSince drops entries strictly older than since. A zero since keeps everything.
func FilterSince(entries []LJEntry, since time.Time) []LJEntry {
	if since.IsZero() {
		return entries
	}
	out := make([]LJEntry, 0, len(entries))
	for _, e := range entries {
		if e.EventTime.IsZero() || !e.EventTime.Before(since) {
			out = append(out, e)
		}
	}
	return out
}

// SafeMessage redacts secrets and clamps length for logs and the database.
func SafeMessage(err error, secrets ...string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, s := range secrets {
		if len(s) < 8 {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	msg = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, msg)
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return msg
}

// PlainServerBody returns a short non-feed response body unchanged.
// LiveJournal's digest failure is the HTML fragment "Digest authentication <b>FAILED</b>!".
func PlainServerBody(data []byte) (string, bool) {
	s := strings.TrimSpace(string(data))
	if s == "" || len(s) > 500 {
		return "", false
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "<rss") || strings.Contains(low, "<channel") || strings.Contains(low, "<item") || strings.Contains(low, "<feed") {
		return "", false
	}
	return s, true
}

// LooksLikeCaptcha reports whether an HTML body is an anti-bot challenge.
func LooksLikeCaptcha(body []byte) bool {
	s := strings.ToLower(string(body))
	if strings.Contains(s, "g-recaptcha") || strings.Contains(s, "hcaptcha") || strings.Contains(s, "cf-captcha") {
		return true
	}
	if strings.Contains(s, "captcha") && strings.Contains(s, "<form") {
		return true
	}
	return false
}

// LooksLikeLoginPage reports whether HTML is a login form rather than a journal.
func LooksLikeLoginPage(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "<form") && strings.Contains(s, `type="password"`)
}

// DefaultDigestURL is one user's authenticated RSS feed.
func DefaultDigestURL(user string) (string, error) {
	u, err := NormalizeUsername(user)
	if err != nil {
		return "", err
	}
	return "https://" + u + ".livejournal.com/data/rss?auth=digest", nil
}

// DefaultScrapeURL is the friends page, with skip when non-zero.
func DefaultScrapeURL(user string, skip int) (string, error) {
	u, err := NormalizeUsername(user)
	if err != nil {
		return "", err
	}
	raw := "https://" + u + ".livejournal.com/friends"
	if skip > 0 {
		raw += "?skip=" + fmt.Sprintf("%d", skip)
	}
	return raw, nil
}

// FallbackOrder tries primary first, then xmlrpc, digest, and scrape.
func FallbackOrder(primary string) []string {
	primary = strings.ToLower(strings.TrimSpace(primary))
	if primary == "" {
		primary = "xmlrpc"
	}
	base := []string{"xmlrpc", "digest", "scrape"}
	out := []string{primary}
	for _, name := range base {
		if name != primary {
			out = append(out, name)
		}
	}
	return out
}
