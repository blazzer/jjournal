package classic

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"strconv"
	"strings"
	"time"

	"journal/lj"
	"journal/render"
	"journal/store"
	"journal/web"
)

type baseView struct {
	SiteName   string
	Title      string
	CSRF       string
	LoggedIn   bool
	Username   string
	Display    string
	Userpic    string
	IsAdmin    bool
	Banner     string
	FriendsURL string
	JournalURL string
	ProfileURL string
}

type entryView struct {
	Author       string
	AuthorURL    string
	Community    string
	CommunityURL string
	Subject      string
	When         string
	Security     string
	SecIcon      string
	Mood         string
	Music        string
	Body         template.HTML
	Userpic      string
	ViaLJ        bool
	CommentLabel string
	CommentURL   string
	LeaveURL     string
}

type listView struct {
	baseView
	Intro      string
	FilterNote string
	AllURL     string
	Groups     []linkView
	Entries    []entryView
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
}

type linkView struct {
	Name string
	URL  string
}

type commentView struct {
	ID        int64
	Author    string
	When      string
	Body      template.HTML
	Collapse  bool
	CanReply  bool
	CanDelete bool
	CSRF      string
	Action    string
	Children  []*commentView
}

func base(s *web.Server, u store.User, title, csrf string) baseView {
	pic := pictureURL(s, u.Username)
	return baseView{
		SiteName:   s.Config.SiteName,
		Title:      title,
		CSRF:       csrf,
		LoggedIn:   true,
		Username:   u.Username,
		Display:    u.DisplayName,
		Userpic:    pic,
		IsAdmin:    store.IsAdmin(u),
		Banner:     banner(u),
		FriendsURL: "/~" + u.Username + "/friends",
		JournalURL: "/~" + u.Username + "/",
		ProfileURL: "/~" + u.Username + "/profile",
	}
}

func banner(u store.User) string {
	switch u.SyncStatus {
	case store.StatusAuthFailed:
		if strings.Contains(strings.ToLower(u.SyncError), "digest authentication") {
			return "LiveJournal digest access is off. Enable it at " + lj.DigestManageURL + " and log in again."
		}
		return "LiveJournal rejected the saved login. Log in again to resume syncing."
	case store.StatusBlocked:
		return "Syncing is paused for several hours because LiveJournal returned a block or captcha."
	case store.StatusError:
		if u.SyncError != "" {
			return "The last sync failed: " + u.SyncError
		}
		return "The last sync failed."
	default:
		return ""
	}
}

func pictureURL(s *web.Server, username string) string {
	raw, err := s.Store.LatestUserpic(context.Background(), username)
	if err != nil || raw == "" {
		return "/static/userhead.svg"
	}
	return picture(s, raw)
}

func picture(s *web.Server, raw string) string {
	if raw == "" {
		return "/static/userhead.svg"
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	if s.Pics != nil {
		if local, ok := s.Pics.CachedURL(raw); ok {
			return local
		}
	}
	if s.Images != nil {
		if signed, err := s.Images.Sign(raw); err == nil {
			return signed
		}
	}
	return "/static/userhead.svg"
}

func localUsers(s *web.Server) map[string]string {
	users, err := s.Store.ListUsers(context.Background())
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(users))
	for _, u := range users {
		out[u.Username] = "/~" + u.Username + "/profile"
	}
	return out
}

func entries(s *web.Server, list []store.Entry, full bool) []entryView {
	locals := localUsers(s)
	out := make([]entryView, 0, len(list))
	for _, e := range list {
		out = append(out, oneEntry(s, e, full, locals))
	}
	return out
}

func oneEntry(s *web.Server, e store.Entry, full bool, locals map[string]string) entryView {
	readMore := e.URL
	if e.Source == "native" {
		readMore = "/~" + e.Journal + "/" + strconv.FormatInt(e.ID, 10) + ".html"
	}
	body := render.RenderBody(e.BodyHTML, render.Options{
		Full:        full,
		ReadMoreURL: readMore,
		LocalUsers:  locals,
		JournalURL:  lj.ProfileURL,
		SignImage: func(src string) string {
			if s.Images == nil {
				return ""
			}
			signed, err := s.Images.Sign(src)
			if err != nil {
				return ""
			}
			return signed
		},
	})
	v := entryView{
		Author:    e.Author,
		AuthorURL: profileURL(e.Author, locals),
		Subject:   e.Subject,
		When:      e.EventTime.UTC().Format("02 Jan 2006 15:04 UTC"),
		Security:  e.Security,
		SecIcon:   "/static/sec-" + e.Security + ".svg",
		Mood:      e.Mood,
		Music:     e.Music,
		Body:      template.HTML(body),
		Userpic:   picture(s, e.UserpicURL),
	}
	if e.Subject == "" {
		v.Subject = "(no subject)"
	}
	if e.JournalType == "C" || (e.Journal != "" && e.Journal != e.Author) {
		v.Community = e.Journal
		v.CommunityURL = profileURL(e.Journal, locals)
	}
	if e.Source == "remote" {
		v.ViaLJ = true
		v.CommentLabel = fmt.Sprintf("%d comments on LJ", e.CommentCount)
		v.CommentURL = e.URL
	} else {
		v.CommentLabel = fmt.Sprintf("%d comments", e.CommentCount)
		v.CommentURL = readMore + "#comments"
		v.LeaveURL = readMore + "#reply"
	}
	return v
}

func profileURL(name string, locals map[string]string) string {
	if href, ok := locals[name]; ok {
		return href
	}
	return lj.ProfileURL(name)
}

func pagerURLs(path, filter string, skip int, hasPrev, hasNext bool) (prev, next string) {
	q := url.Values{}
	if filter != "" {
		q.Set("filter", filter)
	}
	if hasPrev {
		q.Set("skip", strconv.Itoa(skip-store.PageSize))
		if skip-store.PageSize <= 0 {
			q.Del("skip")
		}
		prev = path
		if enc := q.Encode(); enc != "" {
			prev += "?" + enc
		}
		q = url.Values{}
		if filter != "" {
			q.Set("filter", filter)
		}
	}
	if hasNext {
		q.Set("skip", strconv.Itoa(skip+store.PageSize))
		next = path
		if enc := q.Encode(); enc != "" {
			next += "?" + enc
		}
	}
	return prev, next
}

func thread(s *web.Server, comments []store.Comment, csrf, action string, viewer store.User, entryAuthorID int64) []*commentView {
	flat := make([]render.Comment, len(comments))
	byID := map[int64]store.Comment{}
	for i, c := range comments {
		flat[i] = render.Comment{ID: c.ID, ParentID: c.ParentID, Author: c.Author, BodyHTML: c.BodyHTML, Deleted: c.Deleted}
		byID[c.ID] = c
	}
	var conv func([]*render.CommentNode) []*commentView
	conv = func(nodes []*render.CommentNode) []*commentView {
		out := make([]*commentView, 0, len(nodes))
		for _, n := range nodes {
			src := byID[n.ID]
			body := "[deleted]"
			if !n.Deleted {
				body = render.RenderBody(n.BodyHTML, render.Options{Full: true, LocalUsers: localUsers(s), JournalURL: lj.ProfileURL, SignImage: func(src string) string {
					if s.Images == nil {
						return ""
					}
					signed, err := s.Images.Sign(src)
					if err != nil {
						return ""
					}
					return signed
				}})
			}
			out = append(out, &commentView{
				ID:        n.ID,
				Author:    n.Author,
				When:      src.CreatedAt.UTC().Format("02 Jan 2006 15:04 UTC"),
				Body:      template.HTML(body),
				Collapse:  render.ShouldCollapse(n.Depth),
				CanReply:  !n.Deleted,
				CanDelete: !n.Deleted && (viewer.ID == src.AuthorID || viewer.ID == entryAuthorID),
				CSRF:      csrf,
				Action:    action,
				Children:  conv(n.Children),
			})
		}
		return out
	}
	return conv(render.Thread(flat))
}

func formatWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("02 Jan 2006 15:04 UTC")
}
