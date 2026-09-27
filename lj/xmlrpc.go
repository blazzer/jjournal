package lj

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// XMLRPC talks to LJ.XMLRPC.
type XMLRPC struct {
	Client   *http.Client
	Endpoint string
	Gate     *Gate
}

// NewXMLRPC returns a client for endpoint. An empty endpoint uses the live URL.
func NewXMLRPC(client *http.Client, endpoint string) *XMLRPC {
	if endpoint == "" {
		endpoint = XMLRPCEndpoint
	}
	return &XMLRPC{Client: client, Endpoint: endpoint}
}

func (x *XMLRPC) client() *http.Client {
	if x.Client != nil {
		return x.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (x *XMLRPC) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	body, err := encodeCall(method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("User-Agent", UserAgent)
	if err := x.Gate.Wait(ctx); err != nil {
		return nil, err
	}
	resp, err := x.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, &BlockedError{StatusCode: resp.StatusCode, Reason: "http " + strconv.Itoa(resp.StatusCode)}
	}
	if LooksLikeCaptcha(data) {
		return nil, &BlockedError{StatusCode: resp.StatusCode, Captcha: true, Reason: "captcha"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lj: xmlrpc http %d", resp.StatusCode)
	}
	val, err := decodeResponse(data)
	if err != nil {
		var fault *FaultError
		if errorsAsFault(err, &fault) {
			return nil, classifyFault(fault)
		}
		return nil, err
	}
	m, ok := val.(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return m, nil
}

func errorsAsFault(err error, target **FaultError) bool {
	if err == nil {
		return false
	}
	f, ok := err.(*FaultError)
	if !ok {
		return false
	}
	*target = f
	return true
}

func (x *XMLRPC) challenge(ctx context.Context) (string, error) {
	m, err := x.call(ctx, "LJ.XMLRPC.getchallenge", map[string]any{})
	if err != nil {
		return "", err
	}
	c := asString(m["challenge"])
	if c == "" {
		return "", fmt.Errorf("lj: empty challenge")
	}
	return c, nil
}

func (x *XMLRPC) authParams(ctx context.Context, s Session) (map[string]any, error) {
	if s.Username == "" || s.PasswordMD5 == "" {
		return nil, &AuthError{Reason: "missing credentials"}
	}
	ch, err := x.challenge(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"username":       s.Username,
		"auth_method":    "challenge",
		"auth_challenge": ch,
		"auth_response":  AuthResponse(ch, s.PasswordMD5),
		"ver":            1,
	}, nil
}

// Login checks the password hash and fetches an ljsession cookie.
func (x *XMLRPC) Login(ctx context.Context, user, pwMD5 string) (Session, error) {
	user, err := NormalizeUsername(user)
	if err != nil {
		return Session{}, err
	}
	if len(pwMD5) != 32 {
		return Session{}, &AuthError{Reason: "invalid password hash"}
	}
	sess := NewSession(user, pwMD5, "", "")
	params, err := x.authParams(ctx, sess)
	if err != nil {
		return Session{}, err
	}
	params["clientversion"] = ClientVersion
	m, err := x.call(ctx, "LJ.XMLRPC.login", params)
	if err != nil {
		return Session{}, err
	}
	full := asString(m["fullname"])
	params2, err := x.authParams(ctx, sess)
	if err != nil {
		return Session{}, err
	}
	params2["expiration"] = "long"
	sm, err := x.call(ctx, "LJ.XMLRPC.sessiongenerate", params2)
	if err != nil {
		return Session{}, err
	}
	cookie := asString(sm["ljsession"])
	if cookie == "" {
		return Session{}, fmt.Errorf("lj: empty session")
	}
	return NewSession(user, pwMD5, cookie, full), nil
}

// FriendList loads friends and records groups on the session.
func (x *XMLRPC) FriendList(ctx context.Context, s Session) ([]LJFriend, error) {
	params, err := x.authParams(ctx, s)
	if err != nil {
		return nil, err
	}
	params["includegroups"] = 1
	m, err := x.call(ctx, "LJ.XMLRPC.getfriends", params)
	if err != nil {
		return nil, err
	}
	friends, groups, haveGroups := parseFriends(m)
	if haveGroups {
		s.noteGroups(groups)
	}
	return friends, nil
}

// FriendsPage loads the newest friends-page entries. A non-zero since sends lastsync.
func (x *XMLRPC) FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error) {
	if since.IsZero() {
		return x.FriendsPageSkip(ctx, s, 0)
	}
	params, err := x.authParams(ctx, s)
	if err != nil {
		return nil, err
	}
	params["itemshow"] = ItemShow
	params["lastsync"] = since.UTC().Format("2006-01-02 15:04:05")
	m, err := x.call(ctx, "LJ.XMLRPC.getfriendspage", params)
	if err != nil {
		return nil, err
	}
	return FilterSince(parseEntries(asSlice(m["entries"]), s.Username), since), nil
}

// FriendsPageSkip loads one page. skip is the number of newer entries to pass over.
// LiveJournal returns at most ItemShow (50) entries. skip above MaxFriendsSkip is refused
// locally. A server fault for the skip parameter comes back as LimitError.
func (x *XMLRPC) FriendsPageSkip(ctx context.Context, s Session, skip int) ([]LJEntry, error) {
	if skip < 0 || skip > MaxFriendsSkip {
		return nil, &LimitError{Param: "skip"}
	}
	params, err := x.authParams(ctx, s)
	if err != nil {
		return nil, err
	}
	params["itemshow"] = ItemShow
	if skip > 0 {
		params["skip"] = skip
	}
	m, err := x.call(ctx, "LJ.XMLRPC.getfriendspage", params)
	if err != nil {
		return nil, err
	}
	return parseEntries(asSlice(m["entries"]), s.Username), nil
}

func parseFriends(m map[string]any) ([]LJFriend, []LJGroup, bool) {
	var friends []LJFriend
	for _, raw := range asSlice(m["friends"]) {
		fm := asMap(raw)
		name := asString(fm["username"])
		if name == "" {
			continue
		}
		name, err := NormalizeUsername(name)
		if err != nil {
			continue
		}
		mask := uint32(1)
		if _, ok := fm["groupmask"]; ok {
			mask = uint32(asInt(fm["groupmask"]))
		}
		friends = append(friends, LJFriend{
			Username:   name,
			FullName:   asString(fm["fullname"]),
			GroupMask:  mask,
			UserpicURL: firstNonEmpty(asString(fm["userpic"]), asString(fm["userpic_url"])),
		})
	}
	var groups []LJGroup
	rawGroups, ok := m["friendgroups"]
	if !ok {
		return friends, nil, false
	}
	for _, raw := range asSlice(rawGroups) {
		gm := asMap(raw)
		id := int(asInt(gm["id"]))
		if id < 1 || id > 30 {
			continue
		}
		name := strings.TrimSpace(asString(gm["name"]))
		if name == "" {
			name = fmt.Sprintf("Group %d", id)
		}
		pub := asInt(gm["public"]) == 1 || asString(gm["public"]) == "1"
		groups = append(groups, LJGroup{
			ID:        id,
			Name:      name,
			SortOrder: int(asInt(gm["sortorder"])),
			Public:    pub,
		})
	}
	return friends, groups, true
}

func parseEntries(items []any, fallbackUser string) []LJEntry {
	out := make([]LJEntry, 0, len(items))
	for _, raw := range items {
		m := asMap(raw)
		itemID := asInt(m["ditemid"])
		if itemID == 0 {
			itemID = asInt(m["itemid"])
		}
		journal := strings.ToLower(asString(m["journalname"]))
		author := strings.ToLower(firstNonEmpty(asString(m["postername"]), asString(m["poster"]), journal, fallbackUser))
		if journal == "" {
			journal = author
		}
		props := asMap(m["props"])
		sec, mask := NormalizeSecurity(asString(m["security"]), uint32(asInt(m["allowmask"])))
		when := entryTime(m)
		url := asString(m["url"])
		if url == "" {
			base := strings.TrimRight(asString(m["journalurl"]), "/")
			if base != "" && itemID != 0 {
				url = base + "/" + strconv.FormatInt(itemID, 10) + ".html"
			} else if journal != "" && itemID != 0 {
				url = "https://" + journal + ".livejournal.com/" + strconv.FormatInt(itemID, 10) + ".html"
			}
		}
		if itemID == 0 && url != "" {
			itemID = ItemIDFromURL(url)
		}
		if itemID == 0 {
			continue
		}
		out = append(out, LJEntry{
			ItemID:       itemID,
			Journal:      journal,
			JournalType:  strings.ToUpper(asString(m["journaltype"])),
			Author:       author,
			Subject:      firstNonEmpty(m["subject"], m["subject_raw"]),
			EventHTML:    firstNonEmpty(m["event"], m["event_raw"]),
			EventTime:    when,
			Security:     sec,
			AllowMask:    mask,
			UserpicURL:   firstNonEmpty(asString(m["poster_userpic_url"]), asString(m["userpic_url"])),
			Mood:         firstNonEmpty(asString(props["current_mood"]), asString(m["current_mood"])),
			Music:        firstNonEmpty(asString(props["current_music"]), asString(m["current_music"])),
			CommentCount: int(asInt(firstNonEmpty(m["reply_count"], m["replycount"]))),
			URL:          url,
		})
	}
	return out
}

func firstNonEmpty(vals ...any) string {
	for _, v := range vals {
		s := strings.TrimSpace(asString(v))
		if s != "" {
			return s
		}
	}
	return ""
}

// entryTime reads the post time. Current getfriendspage responses omit eventtime
// and send logtime as a Unix timestamp. Older responses send eventtime as text.
func entryTime(m map[string]any) time.Time {
	if t := ParseLJTime(asString(m["eventtime"])); !t.IsZero() {
		return t
	}
	if t := unixOrLJTime(m["logtime"]); !t.IsZero() {
		return t
	}
	return unixOrLJTime(m["event_timestamp"])
}

func unixOrLJTime(v any) time.Time {
	if s, ok := v.(string); ok {
		if t := ParseLJTime(s); !t.IsZero() {
			return t
		}
	}
	n := asInt(v)
	if n < 1_000_000_000 || n > 4_000_000_000 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// ParseLJTime parses LiveJournal and RSS timestamps. Zone-less values are UTC.
func ParseLJTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		time.RFC1123Z,
		time.RFC1123,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ItemIDFromURL reads the numeric id from a LiveJournal permalink.
func ItemIDFromURL(raw string) int64 {
	raw = strings.TrimSpace(raw)
	i := strings.LastIndex(raw, "/")
	if i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.TrimSuffix(raw, ".html")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
