package lj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProbeConfig controls cmd/ljprobe. Password is used only in memory.
type ProbeConfig struct {
	User           string
	Password       string
	OutDir         string
	Client         *http.Client
	XMLRPCEndpoint string
	DigestURL      func(user string) (string, error)
	ScrapeURL      func(user string, skip int) (string, error)
}

func (cfg ProbeConfig) httpClient() (*http.Client, error) {
	if cfg.Client == nil {
		return nil, errors.New("lj: http client is required")
	}
	return cfg.Client, nil
}

// RunProbe writes probe-out/report.txt. It returns process exit codes:
// 0 when any method returns entries or the live probe is skipped,
// 2 when every method fails, 1 on usage or filesystem errors.
func RunProbe(ctx context.Context, cfg ProbeConfig) (int, error) {
	if cfg.OutDir == "" {
		cfg.OutDir = "probe-out"
	}
	if err := os.MkdirAll(cfg.OutDir, 0o700); err != nil {
		return 1, err
	}
	if strings.TrimSpace(cfg.User) == "" || cfg.Password == "" {
		const msg = "The live probe was skipped because LJ_USER and LJ_PASSWORD are not set.\n"
		if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.txt"), []byte(msg), 0o600); err != nil {
			return 1, err
		}
		return 0, nil
	}
	user, err := NormalizeUsername(cfg.User)
	if err != nil {
		return 1, err
	}
	password := cfg.Password
	pwMD5 := PasswordMD5(password)
	redact := []string{password, pwMD5}

	client, err := cfg.httpClient()
	if err != nil {
		return 1, err
	}
	xmlrpc := NewXMLRPC(client, cfg.XMLRPCEndpoint)
	digest := NewDigest(client)
	if cfg.DigestURL != nil {
		digest.URLFor = cfg.DigestURL
	}
	scrape := NewScrape(client, cfg.XMLRPCEndpoint)
	if cfg.ScrapeURL != nil {
		scrape.URLFor = cfg.ScrapeURL
	}

	var b strings.Builder
	var anyEntries bool
	var cookie string

	x := runXMLRPCProbe(ctx, xmlrpc, user, pwMD5)
	b.WriteString(x.report)
	anyEntries = anyEntries || x.entries > 0
	cookie = x.cookie

	// Drop the plaintext after the digest channel, which is the only caller that needs it.
	d := runDigestProbe(ctx, digest, user, password)
	password = ""
	b.WriteString(d.report)
	anyEntries = anyEntries || d.authEntries > 0

	s := runScrapeProbe(ctx, scrape, user, pwMD5, cookie, x.loginBlocked)
	b.WriteString(s.report)
	anyEntries = anyEntries || s.entries > 0
	if cookie == "" {
		cookie = s.cookie
	}

	if cookie != "" {
		redact = append(redact, cookie)
		if err := os.WriteFile(filepath.Join(cfg.OutDir, "ljsession.txt"), []byte(cookie), 0o600); err != nil {
			return 1, err
		}
	}
	report := b.String()
	for _, secret := range redact {
		if secret != "" {
			report = strings.ReplaceAll(report, secret, "[redacted]")
		}
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.txt"), []byte(report), 0o600); err != nil {
		return 1, err
	}
	if anyEntries {
		return 0, nil
	}
	return 2, nil
}

type xmlResult struct {
	report       string
	entries      int
	cookie       string
	loginBlocked bool
}

func runXMLRPCProbe(ctx context.Context, x *XMLRPC, user, pwMD5 string) xmlResult {
	var r xmlResult
	sess, err := x.Login(ctx, user, pwMD5)
	if err != nil {
		r.loginBlocked = IsBlocked(err)
		r.report = fmt.Sprintf("source=xmlrpc status=%s entries=0 itemshow=%d friends_locked=unknown blocked=%t error=%s\n",
			statusOf(err), ItemShow, IsBlocked(err), SafeMessage(err, pwMD5))
		return r
	}
	r.cookie = sess.Cookie
	friends, ferr := x.FriendList(ctx, sess)
	if IsBlocked(ferr) {
		r.report = fmt.Sprintf("source=xmlrpc status=blocked entries=0 itemshow=%d friends_locked=unknown blocked=true friends=%d error=%s\n",
			ItemShow, len(friends), SafeMessage(ferr, pwMD5, sess.Cookie))
		return r
	}
	entries, err := x.FriendsPage(ctx, sess, time.Time{})
	if err != nil {
		r.report = fmt.Sprintf("source=xmlrpc status=%s entries=0 itemshow=%d friends_locked=unknown blocked=%t friends=%d error=%s\n",
			statusOf(err), ItemShow, IsBlocked(err), len(friends), SafeMessage(err, pwMD5, sess.Cookie))
		return r
	}
	r.entries = len(entries)
	locked := "no"
	for _, e := range entries {
		if e.Security != "public" {
			locked = "yes"
			break
		}
	}
	r.report = fmt.Sprintf("source=xmlrpc status=ok entries=%d itemshow=%d friends_locked=%s blocked=false friends=%d\n",
		len(entries), ItemShow, locked, len(friends))
	return r
}

type digestResult struct {
	report      string
	authEntries int
}

func runDigestProbe(ctx context.Context, d *Digest, user, password string) digestResult {
	var r digestResult
	anonBody, _, aerr := d.Fetch(ctx, user, "", false)
	if IsBlocked(aerr) {
		r.report = fmt.Sprintf("source=digest status=blocked anonymous_entries=0 authenticated_entries=0 blocked=true error=%s\n", SafeMessage(aerr))
		return r
	}
	anonN := 0
	if aerr == nil {
		if entries, err := ParseRSS(anonBody, user); err == nil {
			anonN = len(entries)
		}
	}
	authBody, _, err := d.Fetch(ctx, user, password, true)
	if err != nil {
		r.report = fmt.Sprintf("source=digest status=%s anonymous_entries=%d authenticated_entries=0 blocked=%t error=%s\n",
			statusOf(err), anonN, IsBlocked(err), digestError(err, authBody, password))
		return r
	}
	if text, ok := PlainServerBody(authBody); ok {
		r.report = fmt.Sprintf("source=digest status=error anonymous_entries=%d authenticated_entries=0 blocked=false error=%s\n",
			anonN, digestError(fmt.Errorf("%s", text), authBody))
		return r
	}
	entries, perr := ParseRSS(authBody, user)
	if perr != nil {
		r.report = fmt.Sprintf("source=digest status=error anonymous_entries=%d authenticated_entries=0 blocked=false error=%s\n",
			anonN, digestError(perr, authBody))
		return r
	}
	r.authEntries = len(entries)
	locked := "no"
	for _, e := range entries {
		if e.Security != "" && e.Security != "public" {
			locked = "yes"
			break
		}
	}
	r.report = fmt.Sprintf("source=digest status=ok anonymous_entries=%d authenticated_entries=%d friends_locked=%s blocked=false\n", anonN, len(entries), locked)
	return r
}

// digestError keeps a short non-feed body, such as "Digest authentication <b>FAILED</b>!", verbatim.
func digestError(err error, body []byte, secrets ...string) string {
	msg := SafeMessage(err, secrets...)
	text, ok := PlainServerBody(body)
	if !ok || strings.Contains(msg, text) {
		return msg
	}
	if msg == "" {
		return text
	}
	return msg + ": " + text
}

type scrapeResult struct {
	report  string
	entries int
	cookie  string
}

func runScrapeProbe(ctx context.Context, sc *Scrape, user, pwMD5, cookie string, loginBlocked bool) scrapeResult {
	var r scrapeResult
	r.cookie = cookie
	if cookie == "" && !loginBlocked {
		sess, err := sc.Login(ctx, user, pwMD5)
		if err != nil {
			r.report = fmt.Sprintf("source=scrape status=%s entries=0 blocked=%t error=%s\n", statusOf(err), IsBlocked(err), SafeMessage(err, pwMD5))
			return r
		}
		r.cookie = sess.Cookie
		cookie = sess.Cookie
	}
	if cookie == "" {
		r.report = "source=scrape status=skipped entries=0 blocked=false error=no session cookie\n"
		return r
	}
	sess := NewSession(user, pwMD5, cookie, "")
	entries, err := sc.FriendsPage(ctx, sess, time.Time{})
	if err != nil {
		r.report = fmt.Sprintf("source=scrape status=%s entries=0 blocked=%t error=%s\n", statusOf(err), IsBlocked(err), SafeMessage(err, pwMD5, cookie))
		return r
	}
	r.entries = len(entries)
	r.report = fmt.Sprintf("source=scrape status=ok entries=%d blocked=false\n", len(entries))
	return r
}

func statusOf(err error) string {
	switch {
	case err == nil:
		return "ok"
	case IsBlocked(err):
		return "blocked"
	case IsAuth(err):
		return "auth_failed"
	case IsUnsupported(err):
		return "unsupported"
	default:
		return "error"
	}
}
