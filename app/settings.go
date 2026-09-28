package app

import (
	"context"

	"journal/store"
)

// Profile is the public member card.
type Profile struct {
	User        store.User
	LJCount     int
	NativeCount int
}

// LoadProfile reads one member by handle.
func LoadProfile(ctx context.Context, st *store.Store, handle string) (Profile, error) {
	u, err := st.UserByUsername(ctx, handle)
	if err != nil {
		return Profile{}, NotFound{Msg: "not found"}
	}
	ljFriends, _ := st.ListLJFriends(ctx, u.ID)
	native, _ := st.ListNativeFriends(ctx, u.ID)
	return Profile{User: u, LJCount: len(ljFriends), NativeCount: len(native)}, nil
}

// EditSettings refuses changes to someone else's profile.
func EditSettings(v Viewer, owner string) error {
	if owner != v.Handle && !v.IsAdmin {
		return Forbidden{Msg: "forbidden"}
	}
	return nil
}
