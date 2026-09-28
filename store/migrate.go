package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MigrateDeps is passed to a Go data migration.
type MigrateDeps struct {
	Key []byte
}

// DataMigrate runs inside the migration transaction after the SQL file of the
// same version. It must be idempotent.
type DataMigrate func(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error

// dataMigrations is keyed by the SQL filename, for example "003_timestamps.sql".
var dataMigrations = map[string]DataMigrate{}

func migrationNames() ([]string, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	var applied int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		return err
	}
	appliedSet := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		appliedSet[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	var pending []string
	for _, name := range names {
		if !appliedSet[name] {
			pending = append(pending, name)
		}
	}
	var backupPath string
	if applied > 0 && len(pending) > 0 {
		backupPath, err = s.preMigrateBackup(ctx, pending[0])
		if err != nil {
			return err
		}
	}
	for _, name := range pending {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			if backupPath != "" {
				return fmt.Errorf("store: migration %s failed; backup %s: %w", name, backupPath, err)
			}
			return fmt.Errorf("store: migration %s: %w", name, err)
		}
	}
	if err := integrityOK(s.db); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}

func (s *Store) preMigrateBackup(ctx context.Context, version string) (string, error) {
	dir := s.backupDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	safe := strings.TrimSuffix(version, ".sql")
	stamp := time.Now().UTC().Format(backupLayout)
	path := filepath.Join(dir, "pre-"+safe+"-"+stamp+".db")
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("store: backup %s: %w", path, err)
	}
	return path, nil
}

func (s *Store) backupDir() string {
	if s.dataDir != "" {
		return filepath.Join(s.dataDir, "backups")
	}
	return filepath.Join(filepath.Dir(s.path), "backups")
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	if strings.HasPrefix(strings.TrimSpace(body), "-- journal: foreign_keys=off") {
		return s.applyFKOff(ctx, name, body)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if err := runDataMigration(ctx, tx, name, s.key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, FormatTime(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) applyFKOff(ctx context.Context, name, body string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	var bad bool
	if rows.Next() {
		bad = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if bad {
		return fmt.Errorf("store: foreign_key_check failed")
	}
	if err := runDataMigration(ctx, tx, name, s.key); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, FormatTime(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func runDataMigration(ctx context.Context, tx *sql.Tx, name string, key []byte) error {
	fn := dataMigrations[name]
	if fn == nil {
		return nil
	}
	return fn(ctx, tx, MigrateDeps{Key: key})
}

func integrityOK(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var msgs []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(msgs) != 1 || msgs[0] != "ok" {
		return fmt.Errorf("integrity_check: %s", strings.Join(msgs, "; "))
	}
	return nil
}

func foreignKeyOK(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check failed")
	}
	return rows.Err()
}
