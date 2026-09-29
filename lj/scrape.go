package lj

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Scrape reads the HTML friends page with an ljsession cookie.
type Scrape struct {
	Client *http.Client
	URLFor func(user string, skip int) (string, error)
	XML    *XMLRPC
}

// NewScrape returns an HTML friends-page source. Login uses XML-RPC sessiongenerate.
func NewScrape(client *http.Client, xmlEndpoint string) *Scrape {
	return &Scrape{
		Client: client,
		URLFor: DefaultScrapeURL,
		XML:    NewXMLRPC(client, xmlEndpoint),
	}
}

func (sc *Scrape) pageURL(user string, skip int) (string, error) {
	if sc.URLFor != nil {
		return sc.URLFor(user, skip)
	}
	return DefaultScrapeURL(user, skip)
}

func (sc *Scrape) xml() *XMLRPC {
	if sc.XML != nil {
		return sc.XML
	}
	return NewXMLRPC(sc.Client, XMLRPCEndpoint)
}

// Login obtains an ljsession cookie via XML-RPC.
func (sc *Scrape) Login(ctx context.Context, user, pwMD5 string) (Session, error) {
	return sc.xml().Login(ctx, user, pwMD5)
}

// FriendList is not available from the HTML friends page.
func (sc *Scrape) FriendList(context.Context, Session) ([]LJFriend, error) {
	return nil, &UnsupportedError{Source: "scrape", Method: "FriendList"}
}

// FriendsPage walks ?skip=N until entries are older than since or the page cap is hit.
func (sc *Scrape) FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error) {
	if s.Cookie == "" {
		return nil, &AuthError{Reason: "missing session"}
	}
	var all []LJEntry
	for page := 0; page < MaxScrapePages; page++ {
		skip := page * 20
		body, err := sc.fetch(ctx, s, skip)
		if err != nil {
			return nil, err
		}
		entries, err := ParseFriendsHTML(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			break
		}
		all = append(all, entries...)
		oldest := oldestTime(entries)
		if !since.IsZero() && !oldest.IsZero() && oldest.Before(since) {
			break
		}
		if len(entries) < 20 {
			break
		}
	}
	return FilterSince(all, since), nil
}

// Page loads one HTML friends page.
func (sc *Scrape) Page(ctx context.Context, s Session, skip int) ([]LJEntry, bool, error) {
	if s.Cookie == "" {
		return nil, false, &AuthError{Reason: "missing session"}
	}
	body, err := sc.fetch(ctx, s, skip)
	if err != nil {
		return nil, false, err
	}
	entries, err := ParseFriendsHTML(bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	return entries, len(entries) < 20, nil
}

func oldestTime(entries []LJEntry) time.Time {
	var oldest time.Time
	for _, e := range entries {
		if e.EventTime.IsZero() {
			continue
		}
		if oldest.IsZero() || e.EventTime.Before(oldest) {
			oldest = e.EventTime
		}
	}
	return oldest
}

func (sc *Scrape) fetch(ctx context.Context, s Session, skip int) ([]byte, error) {
	rawURL, err := sc.pageURL(s.Username, skip)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "ljsession", Value: s.Cookie})
	client, err := useClient(sc.Client)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
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
	if LooksLikeLoginPage(data) {
		return nil, &AuthError{Reason: "session expired"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lj: scrape http %d", resp.StatusCode)
	}
	return data, nil
}

// ParseFriendsHTML extracts entries from a friends page.
func ParseFriendsHTML(r io.Reader) ([]LJEntry, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("lj: scrape parse: %w", err)
	}
	var entries []LJEntry
	var walk func(*html.Node, bool)
	walk = func(n *html.Node, inside bool) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && isEntryContainer(n) && !inside {
			if e, ok := entryFromNode(n); ok {
				entries = append(entries, e)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c, true)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inside)
		}
	}
	walk(doc, false)
	return entries, nil
}

func isEntryContainer(n *html.Node) bool {
	for _, c := range classTokens(n) {
		switch c {
		case "entry", "entryunit", "b-singlepost":
			return true
		}
	}
	return false
}

func classTokens(n *html.Node) []string {
	for _, a := range n.Attr {
		if a.Key == "class" {
			return strings.Fields(a.Val)
		}
	}
	return nil
}

func hasClass(n *html.Node, want string) bool {
	if n == nil || want == "" {
		return false
	}
	for _, c := range classTokens(n) {
		if c == want {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func entryFromNode(n *html.Node) (LJEntry, bool) {
	e := LJEntry{
		ItemID:      asInt(attr(n, "data-itemid")),
		Journal:     strings.ToLower(attr(n, "data-journal")),
		Author:      strings.ToLower(attr(n, "data-poster")),
		JournalType: strings.ToUpper(attr(n, "data-journaltype")),
		URL:         attr(n, "data-url"),
	}
	if sec := attr(n, "data-security"); sec != "" {
		mask := uint32(asInt(attr(n, "data-allowmask")))
		e.Security, e.AllowMask = NormalizeSecurity(sec, mask)
	} else {
		e.Security, e.AllowMask = "public", 0
	}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch {
			case hasClass(node, "i-ljuser-username") && e.Author == "":
				e.Author = strings.ToLower(textContent(node))
			case (hasClass(node, "entry-title") || hasClass(node, "entryunit__title")) && e.Subject == "":
				e.Subject = textContent(node)
				if href := firstHref(node); href != "" && e.URL == "" {
					e.URL = href
				}
			case node.Data == "time" && e.EventTime.IsZero():
				e.EventTime = ParseLJTime(attr(node, "datetime"))
			case (hasClass(node, "entry-text") || hasClass(node, "entryunit__text")) && e.EventHTML == "":
				e.EventHTML = innerHTML(node)
			case hasClass(node, "mood") && e.Mood == "":
				e.Mood = textContent(node)
			case hasClass(node, "music") && e.Music == "":
				e.Music = textContent(node)
			case hasClass(node, "comments") && e.CommentCount == 0:
				e.CommentCount = commentCount(textContent(node))
			case node.Data == "img" && (hasClass(node, "userpic") || hasClass(node.Parent, "entryunit__userpic")) && e.UserpicURL == "":
				e.UserpicURL = attr(node, "src")
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	if e.URL != "" && e.ItemID == 0 {
		e.ItemID = ItemIDFromURL(e.URL)
	}
	if e.Journal == "" {
		e.Journal = userFromLJURL(e.URL)
	}
	if e.Author == "" {
		e.Author = e.Journal
	}
	if e.Journal == "" {
		e.Journal = e.Author
	}
	if e.JournalType == "" {
		if e.Journal != "" && e.Author != "" && e.Journal != e.Author {
			e.JournalType = "C"
		} else {
			e.JournalType = "P"
		}
	}
	if e.ItemID == 0 {
		return LJEntry{}, false
	}
	return e, true
}

func firstHref(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "a" {
		if href := attr(n, "href"); href != "" {
			return href
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if href := firstHref(c); href != "" {
			return href
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

func innerHTML(n *html.Node) string {
	var buf bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&buf, c)
	}
	return buf.String()
}

var commentRe = regexp.MustCompile(`(\d+)`)

func commentCount(s string) int {
	m := commentRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
