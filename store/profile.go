package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"journal/vault"
)

// ErrHandle means the handle is not a valid profile name.
var ErrHandle = errors.New("store: handle")

// ErrFull means MAX_USERS has been reached.
var ErrFull = errors.New("store: full")

// ErrInvite means the invite is missing, used, or expired.
var ErrInvite = errors.New("store: invite")

// ErrNoVault means this profile has no passphrase yet.
var ErrNoVault = errors.New("store: no vault")

// NormalizeHandle checks a profile handle.
func NormalizeHandle(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) < 2 || len(s) > 30 {
		return "", ErrHandle
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return "", ErrHandle
		}
	}
	return s, nil
}

// HashToken is the SHA-256 hex of an invite or recovery token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreateInvite stores a single-use invite and returns the raw token once.
func (s *Store) CreateInvite(ctx context.Context, createdBy int64, admin bool, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	flag := 0
	if admin {
		flag = 1
	}
	var by any
	if createdBy != 0 {
		by = createdBy
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO invites(token_hash, admin, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`, HashToken(token), flag, by, FormatTime(now), FormatTime(now.Add(7*24*time.Hour)))
	if err != nil {
		return "", err
	}
	return token, nil
}

// InviteOpen reports whether the invite can still be used.
func (s *Store) InviteOpen(ctx context.Context, token string, now time.Time) error {
	var expires string
	var used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT expires_at, used_at FROM invites WHERE token_hash=?`, HashToken(token)).
		Scan(&expires, &used)
	if err != nil || used.Valid || parseTime(sql.NullString{String: expires, Valid: true}).Before(now) {
		return ErrInvite
	}
	return nil
}

// RedeemInvite creates a profile from a single-use invite.
func (s *Store) RedeemInvite(ctx context.Context, token, handle, display, passphrase string, maxUsers int, now time.Time) (User, error) {
	handle, err := NormalizeHandle(handle)
	if err != nil {
		return User{}, err
	}
	if len(passphrase) < vault.MinPassphrase {
		return User{}, vault.ErrPassphrase
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return User{}, err
	}
	if maxUsers > 0 && n >= maxUsers {
		return User{}, ErrFull
	}
	var inviteID int64
	var admin int
	var expires string
	var used sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, admin, expires_at, used_at FROM invites WHERE token_hash=?`, HashToken(token)).
		Scan(&inviteID, &admin, &expires, &used)
	if err != nil {
		return User{}, ErrInvite
	}
	if used.Valid || parseTime(sql.NullString{String: expires, Valid: true}).Before(now) {
		return User{}, ErrInvite
	}
	if strings.TrimSpace(display) == "" {
		display = handle
	}
	isAdmin := admin == 1 || n == 0
	adminFlag := 0
	if isAdmin {
		adminFlag = 1
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO users(handle, display_name, is_admin, ui, created_at) VALUES (?, ?, ?, 'modern', ?)`,
		handle, display, adminFlag, FormatTime(now))
	if err != nil {
		return User{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	dek, err := vault.NewDEK()
	if err != nil {
		return User{}, err
	}
	defer clear(dek)
	w, err := vault.SealPassphrase(ctx, passphrase, dek, vault.DEKAAD(id))
	if err != nil {
		return User{}, err
	}
	params, err := vault.MarshalParams(w.Params)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET kdf_salt=?, kdf_params=?, dek_wrapped=? WHERE id=?`,
		w.Salt, params, w.Wrapped, id); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET used_at=?, used_by=? WHERE id=? AND used_at IS NULL`,
		FormatTime(now), id, inviteID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

// OpenVault unwraps the profile data key.
func (s *Store) OpenVault(ctx context.Context, userID int64, passphrase string) ([]byte, error) {
	var salt []byte
	var params sql.NullString
	var wrapped []byte
	err := s.db.QueryRowContext(ctx, `SELECT kdf_salt, kdf_params, dek_wrapped FROM users WHERE id=?`, userID).
		Scan(&salt, &params, &wrapped)
	if err != nil {
		return nil, err
	}
	if len(salt) == 0 || !params.Valid || len(wrapped) == 0 {
		return nil, ErrNoVault
	}
	p, err := vault.UnmarshalParams(params.String)
	if err != nil {
		return nil, err
	}
	return vault.OpenPassphrase(ctx, passphrase, vault.Wrap{Salt: salt, Params: p, Wrapped: wrapped}, vault.DEKAAD(userID))
}

// ChangePassphrase re-wraps the same data key and ends every other session.
func (s *Store) ChangePassphrase(ctx context.Context, userID int64, oldPass, newPass, keepSession string) error {
	dek, err := s.OpenVault(ctx, userID, oldPass)
	if err != nil {
		return err
	}
	defer clear(dek)
	if len(newPass) < vault.MinPassphrase {
		return vault.ErrPassphrase
	}
	w, err := vault.SealPassphrase(ctx, newPass, dek, vault.DEKAAD(userID))
	if err != nil {
		return err
	}
	params, err := vault.MarshalParams(w.Params)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET kdf_salt=?, kdf_params=?, dek_wrapped=? WHERE id=?`,
		w.Salt, params, w.Wrapped, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=? AND id<>?`, userID, keepSession); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateRecovery stores a 24-hour recovery token and returns it once.
func (s *Store) CreateRecovery(ctx context.Context, userID int64, now time.Time) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO recovery_tokens(user_id, token_hash, created_at, expires_at)
		VALUES (?, ?, ?, ?)`, userID, HashToken(token), FormatTime(now), FormatTime(now.Add(24*time.Hour)))
	if err != nil {
		return "", err
	}
	return token, nil
}

// RecoveryUser returns the profile for a live recovery token.
func (s *Store) RecoveryUser(ctx context.Context, token string, now time.Time) (int64, error) {
	var userID int64
	var expires string
	var used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT user_id, expires_at, used_at FROM recovery_tokens WHERE token_hash=?`, HashToken(token)).
		Scan(&userID, &expires, &used)
	if err != nil || used.Valid || parseTime(sql.NullString{String: expires, Valid: true}).Before(now) {
		return 0, ErrInvite
	}
	return userID, nil
}

// ResetVault replaces the data key, drops service secrets, and ends every session.
func (s *Store) ResetVault(ctx context.Context, token, passphrase string, now time.Time) error {
	userID, err := s.RecoveryUser(ctx, token, now)
	if err != nil {
		return err
	}
	dek, err := vault.NewDEK()
	if err != nil {
		return err
	}
	defer clear(dek)
	w, err := vault.SealPassphrase(ctx, passphrase, dek, vault.DEKAAD(userID))
	if err != nil {
		return err
	}
	params, err := vault.MarshalParams(w.Params)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET kdf_salt=?, kdf_params=?, dek_wrapped=? WHERE id=?`,
		w.Salt, params, w.Wrapped, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET password_enc=NULL, session_enc=NULL, secret_state='needs_password' WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE recovery_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL`, FormatTime(now), HashToken(token)); err != nil {
		return err
	}
	return tx.Commit()
}

// LinkedAccounts reports how many service accounts a profile has.
func (s *Store) LinkedAccounts(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE user_id=?`, userID).Scan(&n)
	return n, err
}

// DeleteProfile removes a member. Comments they left on other posts stay as "[deleted]".
func (s *Store) DeleteProfile(ctx context.Context, userID int64) error {
	u, err := s.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE comments SET body_html='[deleted]', deleted=1
		WHERE author_user_id=? AND entry_id NOT IN (
			SELECT id FROM entries WHERE source='native' AND (author_username=? OR journal_username=?))`,
		userID, u.Username, u.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE source='native' AND (author_username=? OR journal_username=?)`,
		u.Username, u.Username); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET created_by=NULL WHERE created_by=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invites SET used_by=NULL WHERE used_by=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id=?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// PutRememberedPassword stores a service password under the data key.
func (s *Store) PutRememberedPassword(ctx context.Context, userID, accountID int64, dek []byte, pwMD5 string, remember bool) error {
	rememberFlag := 0
	var blob []byte
	state := "needs_password"
	if remember {
		rememberFlag = 1
		state = ""
		var err error
		blob, err = vault.Seal(dek, vault.SecretAAD("password", userID, accountID), pwMD5)
		if err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET password_enc=?, remember_password=?, secret_state=?, secret_scheme='vault' WHERE id=? AND user_id=?`,
		blob, rememberFlag, state, accountID, userID)
	return err
}

// LegacySecrets counts service passwords still sealed under the server key.
func (s *Store) LegacySecrets(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE secret_scheme='legacy' AND password_enc IS NOT NULL`).Scan(&n)
	return n, err
}

// PurgeLegacySecrets deletes server-key password copies.
func (s *Store) PurgeLegacySecrets(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET password_enc=NULL, secret_state='needs_password' WHERE secret_scheme='legacy'`)
	return err
}
