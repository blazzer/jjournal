package app

import (
	"context"
	"time"

	"journal/render"
	"journal/store"
)

// CommentDraft is a new comment or a delete.
type CommentDraft struct {
	Delete    bool
	CommentID int64
	Body      string
	ParentID  int64
}

// Comment posts or deletes on a native entry the caller already opened.
func Comment(ctx context.Context, st *store.Store, v Viewer, e store.Entry, in CommentDraft, now time.Time) (int64, error) {
	if in.Delete {
		if err := st.DeleteComment(ctx, v.ID, in.CommentID); err != nil {
			return 0, Forbidden{Msg: "Could not delete that comment."}
		}
		return 0, nil
	}
	if e.Source != "native" {
		return 0, Invalid{Field: "body", Msg: "Comments on mirrored entries stay on the original site."}
	}
	if len(in.Body) > 20000 {
		return 0, Invalid{Field: "body", Msg: "Comment is too long."}
	}
	clean := render.Sanitize(in.Body)
	if !render.HasText(clean) {
		return 0, Invalid{Field: "body", Msg: "Write a comment first."}
	}
	id, err := st.AddComment(ctx, e.ID, in.ParentID, v.ID, clean, now)
	if err != nil {
		return 0, Invalid{Field: "body", Msg: "Could not save the comment."}
	}
	return id, nil
}
