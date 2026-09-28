package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "j.db")
	s, err := Open(path, testKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertLogin(ctx, "ada", "Ada", strings.Repeat("a", 32), "cookie"); err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(dir, "backups")
	saved, err := s.Backup(ctx, bdir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertLogin(ctx, "bob", "Bob", strings.Repeat("b", 32), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(saved, path); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(path + ".pre-restore-*")
	if err != nil || len(matches) != 1 {
		t.Fatal(matches, err)
	}
	s, err = Open(path, testKey())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	users, err := s.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].Username != "ada" {
		t.Fatalf("%+v %v", users, err)
	}
	pw, cookie, err := s.Secrets(ctx, users[0].ID)
	if err != nil || pw != strings.Repeat("a", 32) || cookie != "cookie" {
		t.Fatal(pw, cookie, err)
	}
}

func TestPruneBackupsDailyAndWeekly(t *testing.T) {
	dir := t.TempDir()
	keep := []string{}
	drop := []string{}
	write := func(at time.Time, retained bool) {
		name := backupPrefix + at.UTC().Format(backupLayout) + ".db"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if retained {
			keep = append(keep, name)
		} else {
			drop = append(drop, name)
		}
	}
	write(time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), false)
	write(time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC), false)
	write(time.Date(2024, 1, 8, 12, 0, 0, 0, time.UTC), false)
	write(time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC), true)
	write(time.Date(2024, 1, 22, 12, 0, 0, 0, time.UTC), true)
	write(time.Date(2024, 1, 29, 12, 0, 0, 0, time.UTC), true)
	for d := 5; d <= 11; d++ {
		write(time.Date(2024, 2, d, 8, 0, 0, 0, time.UTC), true)
	}
	if err := pruneBackups(dir, 7, 4); err != nil {
		t.Fatal(err)
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("missing", name, err)
		}
	}
	for _, name := range drop {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("kept", name, err)
		}
	}
}

func TestV1SurvivesMigrations(t *testing.T) {
	s := buildV1(t)
	if err := foreignKeyOK(s.db); err != nil {
		t.Fatal(err)
	}
	if err := integrityOK(s.db); err != nil {
		t.Fatal(err)
	}
	var event, display string
	var parentID int
	if err := s.db.QueryRow(`SELECT event_time FROM entries WHERE remote_id='1000'`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if event != "2024-03-01T00:00:00.000Z" {
		t.Fatal(event)
	}
	if err := s.db.QueryRow(`SELECT display_name FROM users WHERE id=1`).Scan(&display); err != nil || display != "Ada Lovelace" {
		t.Fatal(display, err)
	}
	if err := s.db.QueryRow(`SELECT parent_id FROM comments WHERE id=2`).Scan(&parentID); err != nil || parentID != 1 {
		t.Fatal(parentID, err)
	}
	pw, cookie, err := s.Secrets(context.Background(), 1)
	if err != nil || pw != "11111111111111111111111111111111" || cookie != "sess-ada" {
		t.Fatal(pw, cookie, err)
	}
}

func TestEmptyDatabaseMigrations(t *testing.T) {
	s := openTest(t)
	if err := foreignKeyOK(s.db); err != nil {
		t.Fatal(err)
	}
	if err := integrityOK(s.db); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 5 {
		t.Fatal(n, err)
	}
}
