package store

import (
	"context"
	"database/sql"
	"strconv"
)

const entryCols = `e.id, e.source, e.author_username, e.journal_username, e.remote_id, e.url,
	e.subject, e.body_html, e.security, e.allowmask, e.event_time, e.userpic_url, e.mood, e.music,
	e.comment_count, e.journal_type, e.created_at, e.updated_at,
	CASE WHEN e.source = 'native'
		THEN (SELECT COUNT(*) FROM comments c WHERE c.entry_id = e.id AND c.deleted = 0)
		ELSE e.comment_count END`

func scanEntry(row interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var item sql.NullString
	var event, created, updated string
	var count int
	err := row.Scan(&e.ID, &e.Source, &e.Author, &e.Journal, &item, &e.URL, &e.Subject, &e.BodyHTML,
		&e.Security, &e.AllowMask, &event, &e.UserpicURL, &e.Mood, &e.Music, &e.CommentCount, &e.JournalType,
		&created, &updated, &count)
	if err != nil {
		return Entry{}, err
	}
	if item.Valid {
		if n, err := strconv.ParseInt(item.String, 10, 64); err == nil {
			e.ItemID = n
		}
	}
	e.EventTime = parseTime(sql.NullString{String: event, Valid: true})
	e.CreatedAt = parseTime(sql.NullString{String: created, Valid: true})
	e.UpdatedAt = parseTime(sql.NullString{String: updated, Valid: true})
	e.CommentCount = count
	return e, nil
}

// FriendsPage lists the merged reading page for viewer.
func (s *Store) FriendsPage(ctx context.Context, viewerID int64, groupMask uint32, skip, limit int) (Page, error) {
	skip = NormalizeSkip(skip)
	if limit <= 0 {
		limit = PageSize
	}
	q, args := friendsQuery(viewerID, groupMask, skip, limit)
	return s.queryPage(ctx, q, args, skip, limit)
}

// JournalPage lists entries in a journal the viewer may see.
func (s *Store) JournalPage(ctx context.Context, viewerID int64, journal string, skip, limit int) (Page, error) {
	skip = NormalizeSkip(skip)
	if limit <= 0 {
		limit = PageSize
	}
	q, args := journalQuery(viewerID, journal, skip, limit)
	return s.queryPage(ctx, q, args, skip, limit)
}

// nativeVisibleSQL expects the viewer id as the next placeholder and refers to entries alias e.
const nativeVisibleSQL = `EXISTS (
	SELECT 1 FROM users author, users viewer
	WHERE author.handle = e.author_username AND viewer.id = ?
	  AND (
		author.id = viewer.id
		OR e.security = 'public'
		OR (e.security = 'friends' AND (
			EXISTS (SELECT 1 FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = author.id AND f.username = viewer.handle)
			OR EXISTS (SELECT 1 FROM native_friends n WHERE n.user_id = author.id AND n.friend_user_id = viewer.id)
		))
		OR (e.security = 'custom' AND (
			(COALESCE((SELECT f.groupmask FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = author.id AND f.username = viewer.handle), 0)
			 | COALESCE((SELECT n.groupmask FROM native_friends n WHERE n.user_id = author.id AND n.friend_user_id = viewer.id), 0)
			) & e.allowmask) != 0)
	  )
)`

// VisibleEntry returns one entry when the viewer may see it.
func (s *Store) VisibleEntry(ctx context.Context, viewerID int64, journal string, entryID int64) (Entry, error) {
	q := `SELECT ` + entryCols + ` FROM entries e
WHERE e.id = ? AND e.journal_username = ?
AND (
	(e.source = 'remote'
	  AND EXISTS (SELECT 1 FROM entry_visibility v WHERE v.entry_id = e.id AND v.viewer_user_id = ?)
	  AND NOT EXISTS (
		SELECT 1 FROM users mu WHERE mu.handle = e.author_username
		  AND mu.migrated_at IS NOT NULL AND mu.migrated_at <= e.event_time))
	OR (e.source = 'native' AND ` + nativeVisibleSQL + `)
)`
	row := s.db.QueryRowContext(ctx, q, entryID, journal, viewerID, viewerID)
	e, err := scanEntry(row)
	if err == sql.ErrNoRows {
		return Entry{}, err
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

func (s *Store) queryPage(ctx context.Context, q string, args []any, skip, limit int) (Page, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return Page{}, err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	hasNext := len(entries) > limit
	if hasNext {
		entries = entries[:limit]
	}
	return Page{Entries: entries, Skip: skip, HasNext: hasNext, HasPrev: skip > 0}, nil
}

// friendsQuery builds the reading-page query. limit is the page size; the SQL asks for one extra row.
func friendsQuery(viewerID int64, groupMask uint32, skip, limit int) (string, []any) {
	q := `SELECT ` + entryCols + ` FROM entries e
WHERE e.id IN (
	SELECT lj.id FROM entries lj
	WHERE lj.source = 'remote'
	  AND (
		EXISTS (SELECT 1 FROM entry_visibility v WHERE v.entry_id = lj.id AND v.viewer_user_id = ?)
		OR (lj.security = 'public' AND EXISTS (
			SELECT 1 FROM subscriptions sub WHERE sub.user_id = ? AND sub.service = lj.service AND sub.journal = lj.journal_username))
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM users mu WHERE mu.handle = lj.author_username
		  AND mu.migrated_at IS NOT NULL AND mu.migrated_at <= lj.event_time)
	  AND NOT EXISTS (
		SELECT 1 FROM suppressions sup WHERE sup.user_id = ? AND sup.service = lj.service
		  AND (sup.username = lj.author_username OR sup.username = lj.journal_username))
	  AND (? = 0 OR (
		(COALESCE((SELECT f.groupmask FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = ? AND f.username = lj.author_username), 0)
		 | COALESCE((SELECT n.groupmask FROM native_friends n JOIN users fu ON fu.id = n.friend_user_id WHERE n.user_id = ? AND fu.handle = lj.author_username), 0)
		) & ?) != 0)
	UNION
	SELECT na.id FROM entries na
	JOIN users author2 ON author2.handle = na.author_username
	JOIN users viewer ON viewer.id = ?
	WHERE na.source = 'native'
	  AND (
		EXISTS (SELECT 1 FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = viewer.id AND f.username = author2.handle)
		OR EXISTS (SELECT 1 FROM native_friends n WHERE n.user_id = viewer.id AND n.friend_user_id = author2.id)
	  )
	  AND NOT EXISTS (
		SELECT 1 FROM suppressions sup WHERE sup.user_id = viewer.id AND sup.service = 'local' AND sup.username = author2.handle)
	  AND (
		na.security = 'public'
		OR (na.security = 'friends' AND (
			EXISTS (SELECT 1 FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = author2.id AND f.username = viewer.handle)
			OR EXISTS (SELECT 1 FROM native_friends n WHERE n.user_id = author2.id AND n.friend_user_id = viewer.id)
		))
		OR (na.security = 'custom' AND (
			(COALESCE((SELECT f.groupmask FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = author2.id AND f.username = viewer.handle), 0)
			 | COALESCE((SELECT n.groupmask FROM native_friends n WHERE n.user_id = author2.id AND n.friend_user_id = viewer.id), 0)
			) & na.allowmask) != 0)
	  )
	  AND (? = 0 OR (
		(COALESCE((SELECT f.groupmask FROM remote_friends f JOIN accounts fa ON fa.id = f.account_id WHERE fa.user_id = viewer.id AND f.username = author2.handle), 0)
		 | COALESCE((SELECT n.groupmask FROM native_friends n WHERE n.user_id = viewer.id AND n.friend_user_id = author2.id), 0)
		) & ?) != 0)
)
ORDER BY e.event_time DESC, e.id DESC
LIMIT ? OFFSET ?`
	args := []any{viewerID, viewerID, viewerID, groupMask, viewerID, viewerID, groupMask, viewerID, groupMask, groupMask, limit + 1, skip}
	return q, args
}

func journalQuery(viewerID int64, journal string, skip, limit int) (string, []any) {
	q := `SELECT ` + entryCols + ` FROM entries e
WHERE e.journal_username = ?
AND (
	(e.source = 'remote'
	  AND EXISTS (SELECT 1 FROM entry_visibility v WHERE v.entry_id = e.id AND v.viewer_user_id = ?)
	  AND NOT EXISTS (
		SELECT 1 FROM users mu WHERE mu.handle = e.author_username
		  AND mu.migrated_at IS NOT NULL AND mu.migrated_at <= e.event_time))
	OR (e.source = 'native' AND ` + nativeVisibleSQL + `)
)
ORDER BY e.event_time DESC, e.id DESC
LIMIT ? OFFSET ?`
	args := []any{journal, viewerID, viewerID, limit + 1, skip}
	return q, args
}
