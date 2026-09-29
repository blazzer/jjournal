package ljfamily

import (
	"context"
	"net/http"
	"strconv"

	"journal/lj"
	"journal/service"
)

// Open builds the three LiveJournal-code services on one outbound client.
// LiveJournal keeps the XML-RPC, digest, and scrape fallback. The others use XML-RPC only.
func Open(client *http.Client) (*service.Registry, error) {
	live, err := lj.NewSource("xmlrpc", client)
	if err != nil {
		return nil, err
	}
	return service.NewRegistry(
		wrap(def("livejournal", "LiveJournal", "livejournal.com", "/data/atom", true), live),
		wrap(def("dreamwidth", "Dreamwidth", "dreamwidth.org", "/data/atom", true), lj.NewXMLRPC(client, "https://www.dreamwidth.org/interface/xmlrpc")),
		wrap(def("rossia", "Rossia", "lj.rossia.org", "/users/%s/data/rss", true), lj.NewXMLRPC(client, "https://lj.rossia.org/interface/xmlrpc")),
	), nil
}

// Adapt exposes an existing LiveJournal source as a service. Tests use this with a fake.
func Adapt(id, name, domain string, src lj.LJSource) service.Service {
	return wrap(def(id, name, domain, "/data/atom", true), src)
}

type spoke struct {
	*site
	src lj.LJSource
}

func wrap(s *site, src lj.LJSource) *spoke { return &spoke{site: s, src: src} }

func (s *spoke) Login(ctx context.Context, user string, secret service.Secret) (service.Session, error) {
	sess, err := s.src.Login(ctx, user, secret.PasswordMD5)
	if err != nil {
		return service.Session{}, mapErr(err)
	}
	return service.Session{Username: sess.Username, Token: sess.Cookie, FullName: sess.FullName}, nil
}

func (s *spoke) FriendList(ctx context.Context, sess service.Session) ([]service.RemoteFriend, []service.RemoteGroup, error) {
	friends, err := s.src.FriendList(ctx, lj.NewSession(sess.Username, "", sess.Token, sess.FullName))
	if err != nil {
		return nil, nil, mapErr(err)
	}
	out := make([]service.RemoteFriend, len(friends))
	for i, f := range friends {
		out[i] = service.RemoteFriend{Username: f.Username, FullName: f.FullName, GroupMask: f.GroupMask}
	}
	return out, nil, nil
}

func (s *spoke) FriendsPage(ctx context.Context, sess service.Session, skip int) (service.FriendsPage, error) {
	entries, end, err := s.src.Page(ctx, lj.NewSession(sess.Username, "", sess.Token, sess.FullName), skip)
	if err != nil {
		return service.FriendsPage{}, mapErr(err)
	}
	out := make([]service.Entry, len(entries))
	for i, e := range entries {
		out[i] = service.Entry{
			RemoteID: strconv.FormatInt(e.ItemID, 10),
			Journal:  e.Journal,
			Author:   e.Author,
			HTML:     e.EventHTML,
			When:     e.EventTime,
			Security: e.Security,
			URL:      e.URL,
		}
	}
	return service.FriendsPage{Entries: out, End: end}, nil
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case lj.IsAuth(err):
		return &service.AuthError{Reason: lj.SafeMessage(err)}
	case lj.IsBlocked(err):
		return &service.BlockedError{Reason: lj.SafeMessage(err)}
	case lj.IsUnsupported(err):
		return &service.UnsupportedError{Method: "lj"}
	default:
		return &service.FaultError{Reason: lj.SafeMessage(err)}
	}
}
