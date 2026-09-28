package app

import (
	"context"

	"journal/store"
)

// Users lists members for the admin page.
func Users(ctx context.Context, st *store.Store, v Viewer) ([]store.User, error) {
	if !v.IsAdmin {
		return nil, Forbidden{Msg: "forbidden"}
	}
	return st.ListUsers(ctx)
}
