// Package ljfamily registers LiveJournal-code sites.
package ljfamily

import (
	"context"
	"net/url"
	"strings"

	"journal/lj"
	"journal/service"
)

// Register returns livejournal, dreamwidth, and rossia.
func Register() []service.Service {
	return []service.Service{
		def("livejournal", "LiveJournal", "livejournal.com", "/data/atom", true),
		def("dreamwidth", "Dreamwidth", "dreamwidth.org", "/data/atom", true),
		def("rossia", "Rossia", "lj.rossia.org", "/users/%s/data/rss", true),
	}
}

func def(id, name, domain, feed string, login bool) *site {
	return &site{id: id, name: name, domain: domain, feed: feed, login: login}
}

type site struct {
	id, name, domain, feed string
	login                  bool
}

func (s *site) ID() string   { return s.id }
func (s *site) Name() string { return s.name }
func (s *site) Caps() service.Caps {
	return service.Caps{Login: s.login, PublicFeed: true}
}
func (s *site) Login(context.Context, string, service.Secret) (service.Session, error) {
	return service.Session{}, &service.UnsupportedError{Method: "login"}
}
func (s *site) FriendList(context.Context, service.Session) ([]service.RemoteFriend, []service.RemoteGroup, error) {
	return nil, nil, &service.UnsupportedError{Method: "friends"}
}
func (s *site) FriendsPage(context.Context, service.Session, int) (service.FriendsPage, error) {
	return service.FriendsPage{}, &service.UnsupportedError{Method: "friendspage"}
}
func (s *site) PublicFeed(_ context.Context, journal string, _ service.Conditional) (service.FeedResult, error) {
	_ = s.feedURL(journal)
	return service.FeedResult{}, &service.UnsupportedError{Method: "feed"}
}

func (s *site) feedURL(journal string) string {
	if strings.Contains(s.feed, "%s") {
		return "https://" + s.domain + strings.ReplaceAll(s.feed, "%s", journal)
	}
	return "https://" + journal + "." + s.domain + s.feed
}
func (s *site) NormalizeHandle(input string) (string, error) {
	return lj.NormalizeUsername(input)
}
func (s *site) ParseURL(u *url.URL) (string, bool) {
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host == s.domain {
		name := strings.Trim(u.Path, "/")
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[:i]
		}
		if name == "" || name == "users" {
			return "", false
		}
		return name, true
	}
	suffix := "." + s.domain
	if strings.HasSuffix(host, suffix) {
		return strings.TrimSuffix(host, suffix), true
	}
	return "", false
}
func (s *site) JournalURL(journal string) string {
	return "https://" + journal + "." + s.domain + "/"
}
