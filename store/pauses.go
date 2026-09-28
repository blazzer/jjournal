package store

import (
	"context"
	"database/sql"
	"time"
)

// ListHostPauses returns stored host pauses.
func (s *Store) ListHostPauses(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT host, paused_until FROM host_pauses`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var host, until string
		if err := rows.Scan(&host, &until); err != nil {
			return nil, err
		}
		t := parseTime(sql.NullString{String: until, Valid: until != ""})
		if !t.IsZero() {
			out[host] = t
		}
	}
	return out, rows.Err()
}

// PutHostPause records a host pause.
func (s *Store) PutHostPause(ctx context.Context, host string, until time.Time, reason string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_pauses(host, paused_until, reason) VALUES (?, ?, ?)
		ON CONFLICT(host) DO UPDATE SET paused_until=excluded.paused_until, reason=excluded.reason`,
		host, FormatTime(until), reason)
	return err
}

// SetNextSync sets the next time an account should sync.
func (s *Store) SetNextSync(ctx context.Context, userID int64, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET next_sync_at=? WHERE user_id=? AND service='livejournal'`, FormatTime(next), userID)
	return err
}
