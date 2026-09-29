package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	db       *sql.DB
	key      []byte
	previous []byte
	path     string
	dataDir  string
}

// User is a local account. The first user (id 1) is the admin.
type User struct {
	ID              int64
	Username        string
	DisplayName     string
	IsAdmin         bool
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
	HasVault        bool
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

// ContentHash is a stable digest of every application table. Tests use it to prove a request did not write.
func (s *Store) ContentHash(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sum := sha256.New()
	for _, table := range tables {
		if _, err := sum.Write([]byte(table)); err != nil {
			return "", err
		}
		q, err := s.db.QueryContext(ctx, `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`" ORDER BY rowid`)
		if err != nil {
			return "", err
		}
		cols, err := q.Columns()
		if err != nil {
			q.Close()
			return "", err
		}
		for q.Next() {
			dest := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range dest {
				ptrs[i] = &dest[i]
			}
			if err := q.Scan(ptrs...); err != nil {
				q.Close()
				return "", err
			}
			for _, v := range dest {
				fmt.Fprintf(sum, "%v\x1f", v)
			}
			sum.Write([]byte{'\n'})
		}
		if err := q.Err(); err != nil {
			q.Close()
			return "", err
		}
		q.Close()
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

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
func IsAdmin(u User) bool { return u.IsAdmin }

// UpsertLogin creates or refreshes a user after a successful LJ login.
func (s *Store) UpsertLogin(ctx context.Context, username, display, pwMD5, cookie string) (User, error) {
	username, err := lj.NormalizeUsername(username)
	if err != nil {
		return User{}, err
	}
	pwEnc, err := s.sealSecret(pwMD5, "journal/v1/legacy-secret")
	if err != nil {
		return User{}, err
	}
	cookieEnc, err := s.sealSecret(cookie, "journal/v1/token")
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	existing, err := s.UserByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		display = strings.TrimSpace(display)
		if display == "" {
			display = username
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO users(handle, display_name, is_admin, ui, created_at)
			VALUES (?, ?, CASE WHEN (SELECT COUNT(*) FROM users) = 0 THEN 1 ELSE 0 END, 'classic', ?)`,
			username, display, FormatTime(now))
		if err != nil {
			return User{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return User{}, err
		}
		root, err := s.roots()
		if err != nil {
			return User{}, err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts(
			user_id, service, username, secret_scheme, key_id, password_enc, session_enc, sync_status, next_sync_at)
			VALUES (?, 'livejournal', ?, 'envelope', ?, ?, ?, 'ok', ?)`,
			id, username, root[0].ID[:], pwEnc, nullBytes(cookieEnc), FormatTime(now)); err != nil {
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
	if _, err = s.db.ExecContext(ctx, `UPDATE users SET display_name=? WHERE id=?`, display, existing.ID); err != nil {
		return User{}, err
	}
	root, err := s.roots()
	if err != nil {
		return User{}, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET password_enc=?, session_enc=?, secret_scheme='envelope', key_id=?,
		sync_status='ok', sync_error='', sync_fail_count=0, blocked_until=NULL, next_sync_at=?
		WHERE user_id=? AND service='livejournal'`,
		pwEnc, nullBytes(cookieEnc), root[0].ID[:], FormatTime(now), existing.ID)
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
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE u.handle=?`, username))
}

// UserByID loads a user.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE u.id=?`, id))
}

const userSelect = `SELECT u.id, u.handle, u.display_name, u.is_admin, u.migrated_at,
	COALESCE(a.sync_status,'ok'), COALESCE(a.sync_error,''), COALESCE(a.sync_fail_count,0),
	a.last_synced_at, a.friends_synced_at, a.blocked_until, a.next_sync_at, u.created_at,
	COALESCE(a.walk_skip,0), (u.dek_wrapped IS NOT NULL)
	FROM users u LEFT JOIN accounts a ON a.user_id = u.id AND a.service = 'livejournal'`

func (s *Store) scanUser(row *sql.Row) (User, error) {
	var u User
	var migrated, last, friends, blocked, next sql.NullString
	var created string
	var admin, hasVault int
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &migrated, &u.SyncStatus, &u.SyncError, &u.FailCount,
		&last, &friends, &blocked, &next, &created, &u.BackfillSkip, &hasVault)
	u.IsAdmin = admin != 0
	u.HasVault = hasVault != 0
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
	rows, err := s.db.QueryContext(ctx, userSelect+` ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		var migrated, last, friends, blocked, next sql.NullString
		var created string
		var admin, hasVault int
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &admin, &migrated, &u.SyncStatus, &u.SyncError, &u.FailCount,
			&last, &friends, &blocked, &next, &created, &u.BackfillSkip, &hasVault); err != nil {
			return nil, err
		}
		u.MigratedAt = parseTime(migrated)
		u.LastSyncedAt = parseTime(last)
		u.FriendsSyncedAt = parseTime(friends)
		u.BlockedUntil = parseTime(blocked)
		u.NextSyncAt = parseTime(next)
		u.IsAdmin = admin != 0
		u.HasVault = hasVault != 0
		u.CreatedAt = parseTime(sql.NullString{String: created, Valid: created != ""})
		out = append(out, u)
	}
	return out, rows.Err()
}

// Secrets returns the decrypted password hash and session cookie.
func (s *Store) Secrets(ctx context.Context, userID int64) (pwMD5, cookie string, err error) {
	var pw, sess []byte
	err = s.db.QueryRowContext(ctx, `SELECT password_enc, session_enc FROM accounts WHERE user_id=? AND service='livejournal'`, userID).Scan(&pw, &sess)
	if err != nil {
		return "", "", err
	}
	pwMD5, err = s.openSecret(pw, "journal/v1/legacy-secret")
	if err != nil {
		return "", "", err
	}
	cookie, err = s.openSecret(sess, "journal/v1/token")
	if err != nil {
		return "", "", err
	}
	return pwMD5, cookie, nil
}

// SaveSecrets replaces the encrypted LJ secret material.
func (s *Store) SaveSecrets(ctx context.Context, userID int64, pwMD5, cookie string) error {
	pwEnc, err := s.sealSecret(pwMD5, "journal/v1/legacy-secret")
	if err != nil {
		return err
	}
	cookieEnc, err := s.sealSecret(cookie, "journal/v1/token")
	if err != nil {
		return err
	}
	root, err := s.roots()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET password_enc=?, session_enc=?, secret_scheme='envelope', key_id=? WHERE user_id=? AND service='livejournal'`, pwEnc, nullBytes(cookieEnc), root[0].ID[:], userID)
	return err
}

// MarkSyncOK records a successful sync.
func (s *Store) MarkSyncOK(ctx context.Context, userID int64, at, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET sync_status='ok', sync_error='', sync_fail_count=0,
		blocked_until=NULL, last_synced_at=?, next_sync_at=? WHERE user_id=? AND service='livejournal'`, FormatTime(at), FormatTime(next), userID)
	return err
}

// SetBackfillSkip remembers how far into the friends-page window the next sync starts.
func (s *Store) SetBackfillSkip(ctx context.Context, userID int64, skip int) error {
	if skip < 0 {
		skip = 0
	}
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET walk_skip=? WHERE user_id=? AND service='livejournal'`, skip, userID)
	return err
}

// MarkSyncError records a retryable failure and the next attempt.
func (s *Store) MarkSyncError(ctx context.Context, userID int64, msg string, failCount int, next time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET sync_status='error', sync_error=?, sync_fail_count=?, next_sync_at=? WHERE user_id=? AND service='livejournal'`,
		clamp(msg, 240), failCount, FormatTime(next), userID)
	return err
}

// MarkAuthFailed stops sync until the user logs in again.
func (s *Store) MarkAuthFailed(ctx context.Context, userID int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET sync_status='auth_failed', sync_error=?,
		secret_state=CASE WHEN remember_password=1 AND password_enc IS NOT NULL THEN 'needs_unlock' ELSE 'needs_password' END
		WHERE user_id=? AND service='livejournal'`, clamp(msg, 240), userID)
	return err
}

// MarkBlocked pauses sync until until.
func (s *Store) MarkBlocked(ctx context.Context, userID int64, msg string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET sync_status='blocked', sync_error=?, blocked_until=?, next_sync_at=? WHERE user_id=? AND service='livejournal'`,
		clamp(msg, 240), FormatTime(until), FormatTime(until), userID)
	return err
}

// TouchFriends records a friend-list refresh.
func (s *Store) TouchFriends(ctx context.Context, userID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET friends_synced_at=? WHERE user_id=? AND service='livejournal'`, FormatTime(at), userID)
	return err
}

// UsersDue returns accounts the worker should sync now.
func (s *Store) UsersDue(ctx context.Context, now time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM accounts
		WHERE service='livejournal' AND sync_status != 'auth_failed'
		  AND (blocked_until IS NULL OR blocked_until <= ?)
		  AND (next_sync_at IS NULL OR next_sync_at <= ?)
		ORDER BY user_id`, FormatTime(now), FormatTime(now))
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
