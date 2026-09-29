package store

import (
	"context"
	"database/sql"
	"time"
)

// UpsertFeedEntry stores a public-feed copy. A credentialed row keeps its body and security.
func (s *Store) UpsertFeedEntry(ctx context.Context, serviceName, remoteID string, in LJEntryIn) (int64, bool, error) {
	if serviceName == "" || remoteID == "" || in.Journal == "" {
		return 0, false, sql.ErrNoRows
	}
	if in.EventTime.IsZero() {
		in.EventTime = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var id int64
	var origin string
	err = tx.QueryRowContext(ctx, `SELECT id, origin FROM entries WHERE source='remote' AND service=? AND journal_username=? AND remote_id=?`,
		serviceName, in.Journal, remoteID).Scan(&id, &origin)
	now := time.Now().UTC()
	if err == sql.ErrNoRows {
		res, err := tx.ExecContext(ctx, `INSERT INTO entries(
			service, origin, source, author_username, journal_username, remote_id, url, subject, body_html,
			security, allowmask, event_time, last_seen_at, created_at, updated_at)
			VALUES (?, 'feed', 'remote', ?, ?, ?, ?, ?, ?, 'public', 0, ?, ?, ?, ?)`,
			serviceName, in.Author, in.Journal, remoteID, in.URL, in.Subject, in.BodyHTML,
			FormatTime(in.EventTime), FormatTime(now), FormatTime(now), FormatTime(now))
		if err != nil {
			return 0, false, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if origin == "friendspage" {
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE entries SET body_html=?, subject=?, url=?, last_seen_at=?, updated_at=? WHERE id=?`,
		in.BodyHTML, in.Subject, in.URL, FormatTime(now), FormatTime(now), id)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// NoteSeen records that a credentialed walk returned this entry.
func (s *Store) NoteSeen(ctx context.Context, entryID, viewerID, accountID int64, generation int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE entry_visibility SET account_id=?, walk_generation=?, last_seen_at=?
		WHERE entry_id=? AND viewer_user_id=?`, accountID, generation, FormatTime(at), entryID, viewerID)
	return err
}

// RevokeUnseen drops grants inside the walked window that this generation did not return.
func (s *Store) RevokeUnseen(ctx context.Context, accountID int64, generation int, oldest time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM entry_visibility
		WHERE account_id=? AND walk_generation < ?
		AND entry_id IN (SELECT id FROM entries WHERE event_time >= ?)`,
		accountID, generation, FormatTime(oldest))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ExpireRemote removes grants and remote entries past the retention window.
func (s *Store) ExpireRemote(ctx context.Context, now time.Time, lockedDays, publicDays int) error {
	locked := now.AddDate(0, 0, -lockedDays)
	public := now.AddDate(0, 0, -publicDays)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM entry_visibility WHERE entry_id IN (
		SELECT e.id FROM entries e WHERE e.source='remote' AND e.security IN ('friends','custom','private') AND e.last_seen_at < ?)`,
		FormatTime(locked)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE source='remote' AND security='public' AND origin='feed' AND last_seen_at < ?
		AND id NOT IN (SELECT entry_id FROM entry_visibility)`, FormatTime(public)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE source='remote' AND id NOT IN (SELECT entry_id FROM entry_visibility)
		AND NOT (security='public' AND origin='feed')`)
	return err
}
func (s *Store) AccountID(ctx context.Context, userID int64, serviceName string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE user_id=? AND service=?`, userID, serviceName).Scan(&id)
	return id, err
}
