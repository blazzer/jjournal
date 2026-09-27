package lj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Fallback tries sources in order. Authentication failures do not fall through.
type Fallback struct {
	sources []LJSource
	names   []string
}

// NewSource builds the configured backend with the fallback order.
func NewSource(kind string, client *http.Client) (*Fallback, error) {
	order := FallbackOrder(kind)
	switch order[0] {
	case "xmlrpc", "digest", "scrape":
	default:
		return nil, fmt.Errorf("lj: unknown LJ_SOURCE %q", kind)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	gate := NewGate(RequestGap)
	xml := NewXMLRPC(client, XMLRPCEndpoint)
	xml.Gate = gate
	digest := NewDigest(client)
	digest.Gate = gate
	scrape := NewScrape(client, XMLRPCEndpoint)
	scrape.Gate = gate
	scrape.XML.Gate = gate
	f := &Fallback{names: order}
	for _, name := range order {
		switch name {
		case "xmlrpc":
			f.sources = append(f.sources, xml)
		case "digest":
			f.sources = append(f.sources, digest)
		case "scrape":
			f.sources = append(f.sources, scrape)
		}
	}
	return f, nil
}

// NewFallback wraps already constructed sources. names aligns with sources.
func NewFallback(names []string, sources []LJSource) *Fallback {
	return &Fallback{names: append([]string(nil), names...), sources: sources}
}

func (f *Fallback) Login(ctx context.Context, user, pwMD5 string) (Session, error) {
	var errs []error
	for _, src := range f.sources {
		sess, err := src.Login(ctx, user, pwMD5)
		if err == nil {
			return sess, nil
		}
		if IsAuth(err) {
			return Session{}, err
		}
		errs = append(errs, err)
	}
	return Session{}, preferErr(errs)
}

func (f *Fallback) FriendList(ctx context.Context, s Session) ([]LJFriend, error) {
	var errs []error
	for _, src := range f.sources {
		friends, err := src.FriendList(ctx, s)
		if err == nil {
			return friends, nil
		}
		if IsAuth(err) {
			return nil, err
		}
		errs = append(errs, err)
		if !IsUnsupported(err) && !IsBlocked(err) {
			continue
		}
	}
	return nil, preferErr(errs)
}

// FriendsPageSkip pages the first source that can. A skip the server rejects
// is returned as-is so the caller stops instead of trying another backend.
func (f *Fallback) FriendsPageSkip(ctx context.Context, s Session, skip int) ([]LJEntry, error) {
	var errs []error
	for _, src := range f.sources {
		p, ok := src.(interface {
			FriendsPageSkip(context.Context, Session, int) ([]LJEntry, error)
		})
		if !ok {
			continue
		}
		entries, err := p.FriendsPageSkip(ctx, s, skip)
		if err == nil || IsAuth(err) || IsBlocked(err) || IsLimit(err) {
			return entries, err
		}
		errs = append(errs, err)
	}
	if skip > 0 {
		return nil, &LimitError{Param: "skip"}
	}
	if len(errs) == 0 {
		return f.FriendsPage(ctx, s, time.Time{})
	}
	return nil, preferErr(errs)
}

func (f *Fallback) FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error) {
	var errs []error
	for _, src := range f.sources {
		entries, err := src.FriendsPage(ctx, s, since)
		if err == nil {
			return entries, nil
		}
		if IsAuth(err) {
			return nil, err
		}
		errs = append(errs, err)
	}
	return nil, preferErr(errs)
}

func preferErr(errs []error) error {
	if len(errs) == 0 {
		return fmt.Errorf("lj: no source")
	}
	var blocked *BlockedError
	allBlockedOrUnsupported := true
	for _, err := range errs {
		var b *BlockedError
		if errors.As(err, &b) {
			blocked = b
			continue
		}
		if !IsUnsupported(err) {
			allBlockedOrUnsupported = false
		}
	}
	if blocked != nil && allBlockedOrUnsupported {
		return blocked
	}
	return errors.Join(errs...)
}
