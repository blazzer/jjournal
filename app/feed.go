package app

import (
	"context"
	"errors"

	"journal/store"
)

// Feed is one reading page plus the viewer's group links.
type Feed struct {
	Page   store.Page
	Groups []store.Group
	Note   string
}

// Reading returns the signed-in member's own friends page.
func Reading(ctx context.Context, st *store.Store, v Viewer, owner, filter string, skip int) (Feed, error) {
	if owner != v.Handle {
		return Feed{}, Forbidden{Msg: "forbidden"}
	}
	skip = store.NormalizeSkip(skip)
	mask, err := st.GroupMask(ctx, v.ID, filter)
	var note string
	if errors.Is(err, store.ErrUnknownGroup) {
		note = "No such group."
		mask = 0x80000000
		err = nil
	}
	if err != nil {
		return Feed{}, err
	}
	var page store.Page
	if note == "" {
		page, err = st.FriendsPage(ctx, v.ID, mask, skip, store.PageSize)
		if err != nil {
			return Feed{}, err
		}
	}
	groups, _ := st.ListGroups(ctx, v.ID)
	return Feed{Page: page, Groups: groups, Note: note}, nil
}

// Journal returns entries in one journal the viewer may see.
func Journal(ctx context.Context, st *store.Store, v Viewer, journal string, skip int) (store.Page, error) {
	skip = store.NormalizeSkip(skip)
	return st.JournalPage(ctx, v.ID, journal, skip, store.PageSize)
}
