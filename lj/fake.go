package lj

import (
	"context"
	"sync"
	"time"
)

// Fake is an in-memory LJSource for tests.
type Fake struct {
	mu                 sync.Mutex
	Password           map[string]string
	Friends            map[string][]LJFriend
	Groups             map[string][]LJGroup
	Entries            map[string][]LJEntry
	LoginErr           error
	PageErr            error
	FriendErr          error
	PageErrs           []error
	Delay              time.Duration
	UnsupportedFriends bool
	LoginCalls         int
	FriendCalls        int
	PageCalls          int
	active             int
	MaxActive          int
}

func (f *Fake) Login(ctx context.Context, user, pwMD5 string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LoginCalls++
	if f.LoginErr != nil {
		return Session{}, f.LoginErr
	}
	user, err := NormalizeUsername(user)
	if err != nil {
		return Session{}, err
	}
	if f.Password != nil && f.Password[user] != pwMD5 {
		return Session{}, &AuthError{Reason: "rejected"}
	}
	return NewSession(user, pwMD5, "ljsession-test", user), nil
}

func (f *Fake) FriendList(ctx context.Context, s Session) ([]LJFriend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FriendCalls++
	if f.UnsupportedFriends {
		return nil, &UnsupportedError{Source: "fake", Method: "FriendList"}
	}
	if f.FriendErr != nil {
		return nil, f.FriendErr
	}
	if groups, ok := f.Groups[s.Username]; ok {
		s.noteGroups(groups)
	}
	return append([]LJFriend(nil), f.Friends[s.Username]...), nil
}

func (f *Fake) FriendsPage(ctx context.Context, s Session, since time.Time) ([]LJEntry, error) {
	f.mu.Lock()
	f.PageCalls++
	f.active++
	if f.active > f.MaxActive {
		f.MaxActive = f.active
	}
	var err error
	if len(f.PageErrs) > 0 {
		err = f.PageErrs[0]
		f.PageErrs = f.PageErrs[1:]
	} else {
		err = f.PageErr
	}
	entries := append([]LJEntry(nil), f.Entries[s.Username]...)
	delay := f.Delay
	f.mu.Unlock()

	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.mu.Lock()
			f.active--
			f.mu.Unlock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	f.mu.Lock()
	f.active--
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return FilterSince(entries, since), nil
}
