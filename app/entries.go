package app

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

	"journal/render"
	"journal/store"
)

// Post is a native entry draft.
type Post struct {
	Subject  string
	Body     string
	Security string
	Groups   []int64
	Userpic  string
	Mood     string
	Music    string
}

// OpenEntry returns one entry the viewer may see.
func OpenEntry(ctx context.Context, st *store.Store, v Viewer, journal string, id int64) (store.Entry, error) {
	e, err := st.VisibleEntry(ctx, v.ID, journal, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Entry{}, NotFound{Msg: "not found"}
	}
	if err != nil {
		return store.Entry{}, err
	}
	return e, nil
}

// CreatePost stores a native entry after validation.
func CreatePost(ctx context.Context, st *store.Store, v Viewer, in Post, now time.Time) (int64, error) {
	groups, err := st.ListGroups(ctx, v.ID)
	if err != nil {
		return 0, err
	}
	subject := strings.TrimSpace(in.Subject)
	if len(subject) > 200 || len(in.Body) > 100000 {
		return 0, Invalid{Field: "body", Msg: "That post is too long."}
	}
	clean := render.Sanitize(in.Body)
	if !render.HasText(clean) {
		return 0, Invalid{Field: "body", Msg: "Write something first."}
	}
	sec := in.Security
	switch sec {
	case "public", "friends", "private", "custom":
	default:
		sec = "public"
	}
	var mask uint32
	if sec == "friends" {
		mask = 1
	}
	if sec == "custom" {
		for _, id := range in.Groups {
			for _, g := range groups {
				if g.ID == id {
					mask |= store.Mask(g.Bit)
				}
			}
		}
		if mask == 0 {
			return 0, Invalid{Field: "group", Msg: "Choose at least one group."}
		}
	}
	pic := strings.TrimSpace(in.Userpic)
	if pic != "" {
		u, err := url.Parse(pic)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return 0, Invalid{Field: "userpic", Msg: "Userpic must be an http or https URL."}
		}
	}
	return st.CreateNativeEntry(ctx, v.ID, store.LJEntryIn{
		Subject: subject, BodyHTML: clean, Security: sec, AllowMask: mask,
		EventTime: now.UTC(), UserpicURL: pic, Mood: trimMax(in.Mood, 80), Music: trimMax(in.Music, 80),
	})
}

func trimMax(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
