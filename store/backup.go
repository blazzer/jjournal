package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupKeepDaily  = 7
	backupKeepWeekly = 4
	backupPrefix     = "backup-"
	backupLayout     = "20060102T150405.000Z"
)

// Backup writes a consistent snapshot into dir and applies retention.
func (s *Store) Backup(ctx context.Context, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := backupPrefix + time.Now().UTC().Format(backupLayout) + ".db"
	path := filepath.Join(dir, name)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return "", fmt.Errorf("store: backup %s: %w", path, err)
	}
	if err := pruneBackups(dir, backupKeepDaily, backupKeepWeekly); err != nil {
		return path, err
	}
	return path, nil
}

type namedBackup struct {
	name string
	at   time.Time
}

func parseBackupName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, backupPrefix) || !strings.HasSuffix(name, ".db") {
		return time.Time{}, false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(name, backupPrefix), ".db")
	t, err := time.Parse(backupLayout, mid)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// pruneBackups keeps the newest backup of each of the most recent keepDaily
// UTC dates, and the newest backup of each of the most recent keepWeekly ISO weeks.
func pruneBackups(dir string, keepDaily, keepWeekly int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []namedBackup
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		at, ok := parseBackupName(e.Name())
		if !ok {
			continue
		}
		files = append(files, namedBackup{name: e.Name(), at: at})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	keep := map[string]bool{}
	seenDay := map[string]bool{}
	days := 0
	for _, f := range files {
		day := f.at.Format("2006-01-02")
		if seenDay[day] {
			continue
		}
		if days >= keepDaily {
			continue
		}
		seenDay[day] = true
		days++
		keep[f.name] = true
	}
	seenWeek := map[string]bool{}
	weeks := 0
	for _, f := range files {
		y, w := f.at.ISOWeek()
		key := fmt.Sprintf("%04d-%02d", y, w)
		if seenWeek[key] {
			continue
		}
		if weeks >= keepWeekly {
			continue
		}
		seenWeek[key] = true
		weeks++
		keep[f.name] = true
	}
	for _, f := range files {
		if keep[f.name] {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Restore replaces dbPath with a verified backup. The previous database, WAL,
// and SHM files are renamed to *.pre-restore-<time>.
func Restore(src, dbPath string) error {
	if err := verifyBackup(src); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(backupLayout)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(p, p+".pre-restore-"+stamp); err != nil {
			return err
		}
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dbPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func verifyBackup(src string) error {
	db, err := sql.Open("sqlite", sqliteReadOnlyDSN(src))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := integrityOK(db); err != nil {
		return fmt.Errorf("store: restore: %w", err)
	}
	versions, err := appliedVersions(db)
	if err != nil {
		return fmt.Errorf("store: restore: %w", err)
	}
	known, err := migrationNames()
	if err != nil {
		return err
	}
	if !versionPrefix(versions, known) {
		return fmt.Errorf("store: restore: schema %v is not a prefix of %v", versions, known)
	}
	return nil
}

func appliedVersions(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func versionPrefix(got, known []string) bool {
	if len(got) > len(known) {
		return false
	}
	for i := range got {
		if got[i] != known[i] {
			return false
		}
	}
	return len(got) > 0
}
