package store

import (
	"context"
	"time"
)

// Suppress hides a person or journal for one member.
func (s *Store) Suppress(ctx context.Context, userID int64, serviceName, username string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO suppressions(user_id, service, username, created_at) VALUES (?, ?, ?, ?)`,
		userID, serviceName, username, FormatTime(time.Now().UTC()))
	return err
}

// Unsuppress removes one suppression.
func (s *Store) Unsuppress(ctx context.Context, userID int64, serviceName, username string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM suppressions WHERE user_id=? AND service=? AND username=?`, userID, serviceName, username)
	return err
}

// Subscribe follows a public journal.
func (s *Store) Subscribe(ctx context.Context, userID int64, serviceName, journal, feedURL string) error {
	now := FormatTime(time.Now().UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO subscriptions(user_id, service, journal, created_at) VALUES (?, ?, ?, ?)`,
		userID, serviceName, journal, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO public_feeds(service, journal, feed_url) VALUES (?, ?, ?)`,
		serviceName, journal, feedURL); err != nil {
		return err
	}
	return tx.Commit()
}
