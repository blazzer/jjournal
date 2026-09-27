package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LJEntryIn is a sanitized LiveJournal entry to store.
type LJEntryIn struct {
	ItemID       int64
	Journal      string
	Author       string
	URL          string
	Subject      string
	BodyHTML     string
	Security     string
	AllowMask    uint32
	EventTime    time.Time
	UserpicURL   string
	Mood         string
	Music        string
	CommentCount int
	JournalType  string
}

// Entry is a stored post.
type Entry struct {
	ID            int64
	Source        string
	Author        string
	Journal       string
	ItemID        int64
	URL           string
	Subject       string
	BodyHTML      string
	Security      string
	AllowMask     uint32
	EventTime     time.Time
	UserpicURL    string
	Mood          string
	Music         string
	CommentCount  int
	JournalType   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Comment is a native comment.
type Comment struct {
	ID        int64
	EntryID   int64
	ParentID  int64
	AuthorID  int64
	Author    string
	BodyHTML  string
	CreatedAt time.Time
	Deleted   bool
}

// Page is one slice of a journal or friends view.
type Page struct {
	Entries []Entry
	Skip    int
	HasNext bool
	HasPrev bool
}

// UpsertLJEntry inserts or updates an LJ entry and records visibility.
func (s *Store) UpsertLJEntry(ctx context.Context, viewerID int64, in LJEntryIn) (int64, error) {
	if in.ItemID <= 0 || in.Journal == "" || in.Author == "" {
		return 0, fmt.Errorf("store: incomplete lj entry")
	}
	if in.EventTime.IsZero() {
		in.EventTime = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM entries WHERE source='lj' AND journal_lj_username=? AND lj_itemid=?`, in.Journal, in.ItemID).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := tx.ExecContext(ctx, `INSERT INTO entries(
			source, author_lj_username, journal_lj_username, lj_itemid, lj_url, subject, body_html,
			security, allowmask, event_time, userpic_url, mood, music, lj_comment_count, journal_type,
			created_at, updated_at) VALUES ('lj', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Author, in.Journal, in.ItemID, in.URL, in.Subject, in.BodyHTML, in.Security, in.AllowMask,
			formatTime(in.EventTime), in.UserpicURL, in.Mood, in.Music, in.CommentCount, in.JournalType,
			formatTime(now), formatTime(now))
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE entries SET author_lj_username=?, lj_url=?, subject=?, body_html=?,
			security=?, allowmask=?, event_time=?, userpic_url=?, mood=?, music=?, lj_comment_count=?,
			journal_type=?, updated_at=? WHERE id=?`,
			in.Author, in.URL, in.Subject, in.BodyHTML, in.Security, in.AllowMask, formatTime(in.EventTime),
			in.UserpicURL, in.Mood, in.Music, in.CommentCount, in.JournalType, formatTime(now), id)
		if err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO entry_visibility(entry_id, viewer_user_id) VALUES (?, ?)`, id, viewerID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// CreateNativeEntry stores a post and sets migrated_at on the author's first post.
func (s *Store) CreateNativeEntry(ctx context.Context, authorID int64, in LJEntryIn) (int64, error) {
	if in.Security == "" {
		in.Security = "public"
	}
	switch in.Security {
	case "public", "friends", "private", "custom":
	default:
		return 0, fmt.Errorf("store: bad security")
	}
	author, err := s.UserByID(ctx, authorID)
	if err != nil {
		return 0, err
	}
	if in.EventTime.IsZero() {
		in.EventTime = time.Now().UTC()
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO entries(
		source, author_lj_username, journal_lj_username, lj_url, subject, body_html, security, allowmask,
		event_time, userpic_url, mood, music, lj_comment_count, journal_type, created_at, updated_at)
		VALUES ('native', ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, 0, 'P', ?, ?)`,
		author.Username, author.Username, in.Subject, in.BodyHTML, in.Security, in.AllowMask,
		formatTime(in.EventTime), in.UserpicURL, in.Mood, in.Music, formatTime(now), formatTime(now))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET migrated_at=? WHERE id=? AND migrated_at IS NULL`, formatTime(in.EventTime), authorID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// AddComment stores a native comment.
func (s *Store) AddComment(ctx context.Context, entryID, parentID, authorID int64, body string, at time.Time) (int64, error) {
	var source string
	err := s.db.QueryRowContext(ctx, `SELECT source FROM entries WHERE id=?`, entryID).Scan(&source)
	if err != nil {
		return 0, err
	}
	if source != "native" {
		return 0, fmt.Errorf("store: comments are native only")
	}
	if parentID != 0 {
		var parentEntry int64
		err := s.db.QueryRowContext(ctx, `SELECT entry_id FROM comments WHERE id=?`, parentID).Scan(&parentEntry)
		if err != nil {
			return 0, err
		}
		if parentEntry != entryID {
			return 0, fmt.Errorf("store: parent is on another entry")
		}
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var parent any
	if parentID != 0 {
		parent = parentID
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO comments(entry_id, parent_id, author_user_id, body_html, created_at, deleted)
		VALUES (?, ?, ?, ?, ?, 0)`, entryID, parent, authorID, body, formatTime(at))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListComments returns comments on an entry in id order.
func (s *Store) ListComments(ctx context.Context, entryID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.entry_id, c.parent_id, c.author_user_id, u.lj_username, c.body_html, c.created_at, c.deleted
		FROM comments c JOIN users u ON u.id = c.author_user_id
		WHERE c.entry_id=? ORDER BY c.id`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		var parent sql.NullInt64
		var created string
		var deleted int
		if err := rows.Scan(&c.ID, &c.EntryID, &parent, &c.AuthorID, &c.Author, &c.BodyHTML, &created, &deleted); err != nil {
			return nil, err
		}
		if parent.Valid {
			c.ParentID = parent.Int64
		}
		c.CreatedAt = parseTime(sql.NullString{String: created, Valid: true})
		c.Deleted = deleted != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteComment soft-deletes a comment for its author or the entry author.
func (s *Store) DeleteComment(ctx context.Context, viewerID, commentID int64) error {
	var authorID, entryAuthor int64
	err := s.db.QueryRowContext(ctx, `SELECT c.author_user_id, eu.id
		FROM comments c
		JOIN entries e ON e.id = c.entry_id
		JOIN users eu ON eu.lj_username = e.author_lj_username
		WHERE c.id=?`, commentID).Scan(&authorID, &entryAuthor)
	if err != nil {
		return err
	}
	if viewerID != authorID && viewerID != entryAuthor {
		return fmt.Errorf("store: cannot delete comment")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE comments SET deleted=1, body_html='' WHERE id=?`, commentID)
	return err
}

// LatestUserpic returns the newest userpic URL for a username.
func (s *Store) LatestUserpic(ctx context.Context, username string) (string, error) {
	var url string
	err := s.db.QueryRowContext(ctx, `SELECT userpic_url FROM entries WHERE author_lj_username=? AND userpic_url != '' ORDER BY event_time DESC, id DESC LIMIT 1`, username).Scan(&url)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return url, err
}
