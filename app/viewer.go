package app

import "journal/store"

// Viewer is the signed-in member. Front-ends fill it from the session.
type Viewer struct {
	ID      int64
	Handle  string
	IsAdmin bool
}

// ViewerFrom copies the fields a use case needs.
func ViewerFrom(u store.User) Viewer {
	return Viewer{ID: u.ID, Handle: u.Username, IsAdmin: store.IsAdmin(u)}
}
