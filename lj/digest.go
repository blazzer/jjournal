package lj

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Digest reads one journal's RSS feed with HTTP digest authentication (qop=auth).
// The app passes the hex MD5 as the digest password because plaintext is never stored.
// cmd/ljprobe still has the password in memory and passes that to Fetch.
type Digest struct {
	Client *http.Client
	URLFor func(user string) (string, error)
}

// NewDigest returns a digest source. A nil URLFor uses the live LiveJournal URL.
func NewDigest(client *http.Client) *Digest {
	return &Digest{Client: client, URLFor: DefaultDigestURL}
}

func (d *Digest) feedURL(user string) (string, error) {
	if d.URLFor != nil {
		return d.URLFor(user)
	}
	return DefaultDigestURL(user)
}

// FriendList is not available on a single-journal feed.
func (d *Digest) FriendList(context.Context, Session) ([]LJFriend, error) {
	return nil, &UnsupportedError{Source: "digest", Method: "FriendList"}
}

// Login confirms the digest secret by fetching the feed.
func (d *Digest) Login(ctx context.Context, user, pwMD5 string) (Session, error) {
	user, err := NormalizeUsername(user)
	if err != nil {
		return Session{}, err
	}
	if len(pwMD5) != 32 {
		return Session{}, &AuthError{Reason: "invalid password hash"}
	}
	if _, _, err := d.Fetch(ctx, user, pwMD5, true); err != nil {
		return Session{}, err
	}
	return NewSession(user, pwMD5, "", ""), nil
}

// FriendsPage returns this user's own journal, not an aggregated friends page.
func (d *Digest) FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error) {
	body, _, err := d.Fetch(ctx, s.Username, s.PasswordMD5, true)
	if err != nil {
		return nil, err
	}
	entries, err := ParseRSS(body, s.Username)
	if err != nil {
		return nil, err
	}
	return FilterSince(entries, since), nil
}

// Page returns this user's feed. Older skips are not available over digest.
func (d *Digest) Page(ctx context.Context, s Session, skip int) ([]LJEntry, bool, error) {
	if skip > 0 {
		return nil, true, &UnsupportedError{Source: "digest", Method: "Page"}
	}
	entries, err := d.FriendsPage(ctx, s, time.Time{})
	if err != nil {
		return nil, false, err
	}
	return entries, true, nil
}

// Fetch performs an anonymous or digest-authenticated GET.
// When authenticate is true, secret is the HTTP digest password.
func (d *Digest) Fetch(ctx context.Context, user, secret string, authenticate bool) ([]byte, int, error) {
	rawURL, err := d.feedURL(user)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	client, err := useClient(d.Client)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	data, status, err := readLimited(resp)
	if err != nil && !IsBlocked(err) {
		return data, status, err
	}
	if IsBlocked(err) {
		return data, status, err
	}
	if !authenticate {
		if status >= 400 {
			return data, status, withServerBody(fmt.Errorf("lj: digest http %d", status), data)
		}
		if text, ok := PlainServerBody(data); ok {
			return data, status, fmt.Errorf("%s", text)
		}
		return data, status, nil
	}
	if status == http.StatusOK && resp.Header.Get("WWW-Authenticate") == "" {
		if text, ok := PlainServerBody(data); ok {
			return data, status, fmt.Errorf("%s", text)
		}
		return data, status, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusOK {
		return data, status, fmt.Errorf("lj: digest http %d", status)
	}
	chal, err := parseDigestChallenge(resp.Header.Get("WWW-Authenticate"))
	if err != nil {
		if status == http.StatusOK {
			if text, ok := PlainServerBody(data); ok {
				return data, status, fmt.Errorf("%s", text)
			}
			return data, status, nil
		}
		return data, status, withServerBody(err, data)
	}
	if secret == "" {
		return nil, status, &AuthError{Reason: "missing digest secret"}
	}
	uri := requestURI(rawURL)
	nc := "00000001"
	cnonce := randomCNonce()
	qop := "auth"
	if !chal.allowsQOP(qop) {
		return nil, status, fmt.Errorf("lj: digest server did not offer qop=auth")
	}
	response := DigestResponse(user, secret, chal.Realm, http.MethodGet, uri, chal.Nonce, nc, cnonce, qop)
	auth := buildDigestHeader(digestHeader{
		Username: user,
		Realm:    chal.Realm,
		Nonce:    chal.Nonce,
		URI:      uri,
		Response: response,
		QOP:      qop,
		NC:       nc,
		CNonce:   cnonce,
		Opaque:   chal.Opaque,
	})
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req2.Header.Set("Authorization", auth)
	resp2, err := client.Do(req2)
	if err != nil {
		return nil, 0, err
	}
	data, status, err = readLimited(resp2)
	if err != nil {
		return data, status, err
	}
	if status == http.StatusUnauthorized {
		if text, ok := PlainServerBody(data); ok {
			if err := digestDisabled(text); err != nil {
				return data, status, err
			}
			return data, status, &AuthError{Reason: text}
		}
		return data, status, &AuthError{Reason: "rejected"}
	}
	if status != http.StatusOK {
		if text, ok := PlainServerBody(data); ok {
			if err := digestDisabled(text); err != nil {
				return data, status, err
			}
		}
		return data, status, withServerBody(fmt.Errorf("lj: digest http %d", status), data)
	}
	if text, ok := PlainServerBody(data); ok {
		if err := digestDisabled(text); err != nil {
			return data, status, err
		}
		return data, status, fmt.Errorf("%s", text)
	}
	return data, status, nil
}

// DigestManageURL is LiveJournal's page where the account turns on HTTP digest.
const DigestManageURL = "https://www.livejournal.com/manage/auth_digest"

// DigestDisabledError means the account has not enabled digest authentication.
// Error text is LiveJournal's own message, unchanged.
type DigestDisabledError struct {
	Message string
}

func (e *DigestDisabledError) Error() string {
	if e == nil || e.Message == "" {
		return "Digest authentication <b>FAILED</b>!"
	}
	return e.Message
}

func (e *DigestDisabledError) Unwrap() error {
	return &AuthError{Reason: "digest disabled"}
}

// IsDigestDisabled reports whether err is a turned-off digest setting.
func IsDigestDisabled(err error) bool {
	var d *DigestDisabledError
	return errors.As(err, &d)
}

func digestDisabled(text string) error {
	s := strings.ToLower(text)
	s = strings.ReplaceAll(s, "<b>", "")
	s = strings.ReplaceAll(s, "</b>", "")
	if strings.Contains(s, "digest authentication") && strings.Contains(s, "failed") {
		return &DigestDisabledError{Message: text}
	}
	return nil
}

func withServerBody(err error, data []byte) error {
	text, ok := PlainServerBody(data)
	if err == nil || !ok || strings.Contains(err.Error(), text) {
		return err
	}
	return fmt.Errorf("%w: %s", err, text)
}

func readLimited(resp *http.Response) ([]byte, int, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return data, resp.StatusCode, &BlockedError{StatusCode: resp.StatusCode, Reason: "http " + strconv.Itoa(resp.StatusCode)}
	}
	if LooksLikeCaptcha(data) {
		return data, resp.StatusCode, &BlockedError{StatusCode: resp.StatusCode, Captcha: true, Reason: "captcha"}
	}
	return data, resp.StatusCode, nil
}

func requestURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.RawQuery == "" {
		return u.EscapedPath()
	}
	return u.EscapedPath() + "?" + u.RawQuery
}

type digestChallenge struct {
	Realm  string
	Nonce  string
	Opaque string
	QOP    string
}

func (c digestChallenge) allowsQOP(want string) bool {
	if strings.TrimSpace(c.QOP) == "" {
		return false
	}
	for _, part := range strings.Split(c.QOP, ",") {
		if strings.Trim(strings.TrimSpace(part), `"`) == want {
			return true
		}
	}
	return false
}

func parseDigestChallenge(h string) (digestChallenge, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return digestChallenge{}, fmt.Errorf("lj: missing digest challenge")
	}
	if i := strings.Index(strings.ToLower(h), "digest"); i == 0 {
		h = strings.TrimSpace(h[len("digest"):])
	}
	vals := map[string]string{}
	for len(h) > 0 {
		h = strings.TrimLeft(h, " ,")
		if h == "" {
			break
		}
		eq := strings.IndexByte(h, '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(h[:eq]))
		rest := strings.TrimSpace(h[eq+1:])
		var val string
		if strings.HasPrefix(rest, `"`) {
			rest = rest[1:]
			end := strings.IndexByte(rest, '"')
			if end < 0 {
				return digestChallenge{}, fmt.Errorf("lj: bad digest challenge")
			}
			val = rest[:end]
			h = rest[end+1:]
		} else {
			end := strings.IndexByte(rest, ',')
			if end < 0 {
				val = rest
				h = ""
			} else {
				val = strings.TrimSpace(rest[:end])
				h = rest[end+1:]
			}
		}
		vals[key] = val
	}
	if vals["realm"] == "" || vals["nonce"] == "" {
		return digestChallenge{}, fmt.Errorf("lj: incomplete digest challenge")
	}
	return digestChallenge{Realm: vals["realm"], Nonce: vals["nonce"], Opaque: vals["opaque"], QOP: vals["qop"]}, nil
}

type digestHeader struct {
	Username string
	Realm    string
	Nonce    string
	URI      string
	Response string
	QOP      string
	NC       string
	CNonce   string
	Opaque   string
}

func buildDigestHeader(h digestHeader) string {
	b := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", algorithm=MD5, response="%s", qop=%s, nc=%s, cnonce="%s"`,
		h.Username, h.Realm, h.Nonce, h.URI, h.Response, h.QOP, h.NC, h.CNonce)
	if h.Opaque != "" {
		b += fmt.Sprintf(`, opaque="%s"`, h.Opaque)
	}
	return b
}

func randomCNonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// DigestResponse computes the RFC 2617 qop=auth response hash.
func DigestResponse(user, pass, realm, method, uri, nonce, nc, cnonce, qop string) string {
	ha1 := md5Hex(user + ":" + realm + ":" + pass)
	ha2 := md5Hex(method + ":" + uri)
	if qop == "" {
		return md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return md5Hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ParseRSS reads a LiveJournal-style RSS feed into entries.
func ParseRSS(data []byte, fallbackUser string) ([]LJEntry, error) {
	dec := xml.NewDecoder(bytesReader(data))
	var (
		inItem bool
		cur    string
		fields map[string]string
		items  []map[string]string
	)
	flush := func() {
		if fields != nil {
			items = append(items, fields)
		}
		fields = map[string]string{}
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("lj: rss: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
			if cur == "item" {
				inItem = true
				fields = map[string]string{}
			}
		case xml.EndElement:
			if t.Name.Local == "item" && inItem {
				flush()
				inItem = false
			}
			cur = ""
		case xml.CharData:
			if inItem && cur != "" && cur != "item" {
				fields[cur] += string(t)
			}
		}
	}
	out := make([]LJEntry, 0, len(items))
	for _, f := range items {
		link := strings.TrimSpace(f["link"])
		itemID := ItemIDFromURL(link)
		if itemID == 0 {
			continue
		}
		author := strings.ToLower(strings.TrimSpace(fallbackUser))
		journal := author
		if hostUser := userFromLJURL(link); hostUser != "" {
			journal = hostUser
		}
		sec, mask := NormalizeSecurity(strings.TrimSpace(f["security"]), uint32(asInt(strings.TrimSpace(f["allowmask"]))))
		reply, _ := strconv.Atoi(strings.TrimSpace(f["reply-count"]))
		out = append(out, LJEntry{
			ItemID:       itemID,
			Journal:      journal,
			JournalType:  "P",
			Author:       author,
			Subject:      strings.TrimSpace(f["title"]),
			EventHTML:    strings.TrimSpace(f["description"]),
			EventTime:    ParseLJTime(strings.TrimSpace(f["pubDate"])),
			Security:     sec,
			AllowMask:    mask,
			Mood:         strings.TrimSpace(f["mood"]),
			Music:        strings.TrimSpace(f["music"]),
			CommentCount: reply,
			URL:          link,
		})
	}
	return out, nil
}

func bytesReader(data []byte) io.Reader { return strings.NewReader(string(data)) }

func userFromLJURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".livejournal.com")
	if host == "" || strings.Contains(host, ".") {
		return ""
	}
	if n, err := NormalizeUsername(host); err == nil {
		return n
	}
	return ""
}
