package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"journal/lj"

	_ "modernc.org/sqlite"
)

const (
	PageSize = 20

	StatusOK         = "ok"
	StatusAuthFailed = "auth_failed"
	StatusBlocked    = "blocked"
	StatusError      = "error"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is the SQLite database.
type Store struct {
	db      *sql.DB
	key     []byte
	path    string
	dataDir string
}

// User is a local account. The first user (id 1) is the admin.
type User struct {
	ID              int64
	Username        string
	DisplayName     string
	MigratedAt      time.Time
	SyncStatus      string
	SyncError       string
	FailCount       int
	LastSyncedAt    time.Time
	FriendsSyncedAt time.Time
	BlockedUntil    time.Time
	NextSyncAt      time.Time
	CreatedAt       time.Time
	BackfillSkip    int
}

// Session is a browser login.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt time.Time
}

// Open opens or creates the database and applies migrations.
func Open(path string, key []byte) (*Store, error) {
	return OpenWithDataDir(path, key, "")
}

// OpenWithDataDir is Open, writing pre-migration backups under dataDir/backups.
func OpenWithDataDir(path string, key []byte, dataDir string) (*Store, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("store: secret key must be 32 bytes")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, key: key, path: path, dataDir: dataDir}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func sqlitePath(path string) string {
	p := filepath.ToSlash(path)
	p = strings.ReplaceAll(p, "?", "%3F")
	p = strings.ReplaceAll(p, "#", "%23")
	p = strings.ReplaceAll(p, " ", "%20")
	return p
}

func sqliteDSN(path string) string {
	return "file:" + sqlitePath(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
}

func sqliteReadOnlyDSN(path string) string {
	return "file:" + sqlitePath(path) + "?mode=ro"
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ready reports whether the database answers and every migration is applied.
func (s *Store) Ready(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		return err
	}
	if n != len(names) {
		return fmt.Errorf("store: migrations %d of %d", n, len(names))
	}
	return nil
}

// NormalizeSkip clamps a paging offset.
func NormalizeSkip(skip int) int {
	if skip < 0 {
		return 0
	}
	if skip > 100000 {
		return 100000
	}
	return skip
}

// IsAdmin reports whether user is the first account.
func IsAdmin(u User) bool { return u.ID == 1 }

// UpsertLogin creates or refreshes a user after a successful LJ login.
func (s *Store) UpsertLogin(ctx context.Context, username, display, pwMD5, cookie string) (User, error) {
	username, err := lj.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	pwEnc, err := Seal(s.key, []byte(pwMD5))
	if err != nil {
		return User{}, err
	}
	var cookieEnc []byte
	if cookie != "" {
		cookieEnc, err = Seal(s.key, []byte(cookie))
		if err != nil {
			return User{}, err
		}
	}
	now := time.Now().UTC()
	existing, err := s.UserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		display = strings.TrimSpace(display)
		if display == "" {
			display = username
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO users(
			lj_username, display_name, lj_pw_md5_enc, lj_session_enc, sync_status, sync_error,
			next_sync_at, created_at) VALUES (?, ?, ?, ?, 'ok', '', ?, ?)`,
			username, display, pwEnc, nullBytes(cookieEnc), FormatTime(now), FormatTime(now))
		if err != nil {
			return User{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return User{}, err
		}
		if err := s.ensureDefaultGroup(ctx, id); err != nil {
			return User{}, err
		}
		return s.UserByID(ctx, id)
	}
	if err != nil {
		return User{}, err
	}
	display = strings.TrimSpace(display)
	if display == "" {
		display = existing.DisplayName
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET display_name=?, lj_pw_md5_enc=?, lj_session_enc=?,
		sync_status='ok', sync_error='', sync_fail_count=0, blocked_until=NULL, next_sync_at=? WHERE id=?`,
		display, pwEnc, nullBytes(cookieEnc), FormatTime(now), existing.ID)
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, existing.ID)
}

func (s *Store) ensureDefaultGroup(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO friend_groups(user_id, name, bit, origin)
		SELECT ?, 'Friends', 1, 'native'
		WHERE NOT EXISTS (SELECT 1 FROM friend_groups WHERE user_id=? AND bit=1)`, userID, userID)
	return err
}

// UserByUsername loads a user.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	username, err := lj.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE lj_username=?`, username))
}

// UserByID loads a user.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE id=?`, id))
}

const userSelect = `SELECT id, lj_username, display_name, migrated_at, sync_status, sync_error, sync_fail_count,
	last_synced_at, friends_synced_at, blocked_until, next_sync_at, created_at, friends_backfill_skip FROM users`

func (s *Store) scanUser(row *sql.Row) (User, error) {
	var u User
	var migrated, last, friends, blocked, next sql.NullString
	var created string
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &migrated, &u.SyncStatus, &u.SyncError, &u.FailCount,
		&last, &friends, &blocked, &next, &created, &u.BackfillSkip)
	if err != nil {
		return User{}, err
	}
	u.MigratedAt = parseTime(migrated)
	u.LastSyncedAt = parseTime(last)
	u.FriendsSyncedAt = parseTime(friends)
	u.BlockedUntil = parseTime(blocked)
	u.NextSyncAt = parseTime(next)
	u.CreatedAt = parseTime(sql.NullString{String: created, Valid: created != ""})
	return u, nil
}

// ListUsers returns every account, oldest first.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var migrated, last, friends, blocked, next sql.NullString
		var created string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &migrated, &u.SyncStatus, &u.SyncError, &u.FailCount,
			&last, &friends, &blocked, &next, &created, &u.BackfillSkip); err != nil {
			return nil, err
		}
		u.MigratedAt = parseTime(migrated)
		u.LastSyncedAt = parseTime(last)
		u.FriendsSyncedAt = parseTime(friends)
		u.BlockedUntil = parseTime(blocked)
		u.NextSyncAt = parseTime(next)
		u.CreatedAt = parseTime(sql.NullString{String: created, Valid: created != ""})
		out = append(out, u)
	}
	return out, rows.Err()
}

// Secrets returns the decrypted password hash and session cookie.
func (s *Store) Secrets(ctx context.Context, userID int64) (pwMD5, cookie string, err error) {
	var pw, sess []byte
	err = s.db.QueryRowContext(ctx, `SELECT lj_pw_md5_enc, lj_session_enc FROM users WHERE id=?`, userID).Scan(&pw, &sess)
	if err != nil {
		return "", "", err
	}
	if len(pw) > 0 {
		b, err := Unseal(s.key, pw)
		if err != nil {
			return "", "", err
		}
		pwMD5 = string(b)
	}
	if len(sess) > 0 {
		b, err := Unseal(s.key, sess)
		if err != nil {
			return "", "", err
		}
		cookie = string(b)
	}
	return pwMD5, cookie, nil
}

// SaveSecrets replaces the encrypted LJ secret material.
func (s *Store) SaveSecrets(ctx context.Context, userID int64, pwMD5, cookie string) error {
	pwEnc, err := Seal(s.key, []byte(pwMD5))
	if err != nil {
		return err
	}
	var cookieEnc []byte
	if cookie != "" {
		cookieEnc, err = Seal(s.key, []byte(cookie))
		if err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET lj_pw_md5_enc=?, lj_session_enc=? WHERE id=?`, pwEnc, nullBytes(cookieEnc), userID)
	return err
}

// MarkSyncOK records a successful sync.
func (s *Store) MarkSyncOK(ctx context.Context, userID int64, at, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET sync_status='ok', sync_error='', sync_fail_count=0,
		blocked_until=NULL, last_synced_at=?, next_sync_at=? WHERE id=?`, FormatTime(at), FormatTime(next), userID)
	return err
}

// SetBackfillSkip remembers how far into the friends-page window the next sync starts.
func (s *Store) SetBackfillSkip(ctx context.Context, userID int64, skip int) error {
	if skip < 0 {
		skip = 0
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET friends_backfill_skip=? WHERE id=?`, skip, userID)
	return err
}

// MarkSyncError records a retryable failure and the next attempt.
func (s *Store) MarkSyncError(ctx context.Context, userID int64, msg string, failCount int, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET sync_status='error', sync_error=?, sync_fail_count=?, next_sync_at=? WHERE id=?`,
		clamp(msg, 240), failCount, FormatTime(next), userID)
	return err
}

// MarkAuthFailed stops sync until the user logs in again.
func (s *Store) MarkAuthFailed(ctx context.Context, userID int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET sync_status='auth_failed', sync_error=? WHERE id=?`, clamp(msg, 240), userID)
	return err
}

// MarkBlocked pauses sync until until.
func (s *Store) MarkBlocked(ctx context.Context, userID int64, msg string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET sync_status='blocked', sync_error=?, blocked_until=?, next_sync_at=? WHERE id=?`,
		clamp(msg, 240), FormatTime(until), FormatTime(until), userID)
	return err
}

// TouchFriends records a friend-list refresh.
func (s *Store) TouchFriends(ctx context.Context, userID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET friends_synced_at=? WHERE id=?`, FormatTime(at), userID)
	return err
}

// UsersDue returns accounts the worker should sync now.
func (s *Store) UsersDue(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users
		WHERE sync_status != 'auth_failed'
		  AND (blocked_until IS NULL OR blocked_until <= ?)
		  AND (next_sync_at IS NULL OR next_sync_at <= ?)
		ORDER BY id`, FormatTime(now), FormatTime(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateSession starts a browser session.
func (s *Store) CreateSession(ctx context.Context, userID int64, expires time.Time) (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf[:])
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id, user_id, expires_at) VALUES (?, ?, ?)`, id, userID, FormatTime(expires))
	return id, err
}

// LookupSession returns a live session.
func (s *Store) LookupSession(ctx context.Context, id string) (Session, error) {
	var sess Session
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, expires_at FROM sessions WHERE id=?`, id).Scan(&sess.ID, &sess.UserID, &exp)
	if err != nil {
		return Session{}, err
	}
	sess.ExpiresAt = parseTime(sql.NullString{String: exp, Valid: true})
	if !sess.ExpiresAt.After(time.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
		return Session{}, sql.ErrNoRows
	}
	return sess, nil
}

// DeleteSession ends a browser session.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
	return err
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
