// Package service is the hub's view of a remote journal site.
package service

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// Caps says which methods a service implements.
type Caps struct {
	Login      bool
	PublicFeed bool
}

// Secret is a service credential. For LiveJournal-code sites it is the hex MD5.
type Secret struct {
	PasswordMD5 string
}

// Session is a reusable login.
type Session struct {
	Username string
	Token    string
	FullName string
}

// RemoteFriend is one friend-of relation.
type RemoteFriend struct {
	Username  string
	FullName  string
	GroupMask uint32
}

// RemoteGroup is a friend group.
type RemoteGroup struct {
	ID   int
	Name string
}

// Entry is one remote post.
type Entry struct {
	RemoteID string
	Journal  string
	Author   string
	HTML     string
	When     time.Time
	Security string
	URL      string
}

// FriendsPage is one window of the friends page.
type FriendsPage struct {
	Entries []Entry
	End     bool
}

// Conditional is a cache validator for a public feed.
type Conditional struct {
	ETag         string
	LastModified string
}

// FeedResult is a fetched public feed.
type FeedResult struct {
	NotModified bool
	Entries     []Entry
	ETag        string
	Modified    string
}

// Service is one remote site. Spokes never see passphrases.
type Service interface {
	ID() string
	Name() string
	Caps() Caps
	Login(ctx context.Context, user string, secret Secret) (Session, error)
	FriendList(ctx context.Context, s Session) ([]RemoteFriend, []RemoteGroup, error)
	FriendsPage(ctx context.Context, s Session, skip int) (FriendsPage, error)
	PublicFeed(ctx context.Context, journal string, cond Conditional) (FeedResult, error)
	NormalizeHandle(input string) (string, error)
	ParseURL(u *url.URL) (string, bool)
	JournalURL(journal string) string
}

// AuthError means the service rejected the credential.
type AuthError struct{ Reason string }

func (e *AuthError) Error() string { return "service: auth: " + e.Reason }

// BlockedError means the service asked us to wait.
type BlockedError struct {
	Reason     string
	RetryAfter time.Duration
}

func (e *BlockedError) Error() string { return "service: blocked: " + e.Reason }

// UnsupportedError means this service cannot do the method.
type UnsupportedError struct{ Method string }

func (e *UnsupportedError) Error() string { return "service: unsupported: " + e.Method }

// FaultError is any other remote failure.
type FaultError struct{ Reason string }

func (e *FaultError) Error() string { return "service: fault: " + e.Reason }

// Registry holds services by id.
type Registry struct {
	byID map[string]Service
}

// NewRegistry indexes services by ID.
func NewRegistry(list ...Service) *Registry {
	r := &Registry{byID: map[string]Service{}}
	for _, s := range list {
		r.byID[s.ID()] = s
	}
	return r
}

// Get returns a service by id.
func (r *Registry) Get(id string) (Service, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// IDs returns service ids in registration order is not stable; tests use Get.
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}

// ErrUnknown is returned when a service id is not registered.
var ErrUnknown = errors.New("service: unknown")
