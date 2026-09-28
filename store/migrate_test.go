package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestForeignKeysOffCheck(t *testing.T) {
	s := openTest(t)
	body := `-- journal: foreign_keys=off
CREATE TABLE p(id INTEGER PRIMARY KEY);
CREATE TABLE c(id INTEGER PRIMARY KEY, p_id INTEGER REFERENCES p(id));
INSERT INTO c(id, p_id) VALUES (1, 99);
`
	err := s.applyMigration(context.Background(), "fk_bad.sql", body)
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='c'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("orphan insert was committed")
	}

	okBody := `-- journal: foreign_keys=off
CREATE TABLE p(id INTEGER PRIMARY KEY);
CREATE TABLE c(id INTEGER PRIMARY KEY, p_id INTEGER REFERENCES p(id));
INSERT INTO p(id) VALUES (1);
INSERT INTO c(id, p_id) VALUES (1, 1);
`
	if err := s.applyMigration(context.Background(), "fk_ok.sql", okBody); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM c`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestDataMigrationSameTransaction(t *testing.T) {
	s := openTest(t)
	dataMigrations["probe.sql"] = func(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error {
		if len(deps.Key) != 32 {
			return errors.New("missing key")
		}
		_, err := tx.ExecContext(ctx, `CREATE TABLE marker(n INTEGER)`)
		return err
	}
	t.Cleanup(func() { delete(dataMigrations, "probe.sql") })
	if err := s.applyMigration(context.Background(), "probe.sql", `CREATE TABLE probe_parent(id INTEGER PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM marker`).Scan(&n); err != nil {
		t.Fatal(err)
	}

	dataMigrations["bad.sql"] = func(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error {
		return errors.New("nope")
	}
	t.Cleanup(func() { delete(dataMigrations, "bad.sql") })
	if err := s.applyMigration(context.Background(), "bad.sql", `CREATE TABLE should_go(id INTEGER);`); err == nil {
		t.Fatal("expected data migration error")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='should_go'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("sql committed despite data migration error")
	}
}

func TestMigrationFailureNamesBackup(t *testing.T) {
	path := seedV1Path(t)
	dataMigrations["003_timestamps.sql"] = func(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error {
		return errors.New("boom")
	}
	t.Cleanup(func() { delete(dataMigrations, "003_timestamps.sql") })
	_, err := Open(path, testKey())
	if err == nil || !strings.Contains(err.Error(), "pre-003_timestamps") || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
}

func TestDataMigrationIdempotent(t *testing.T) {
	s := openTest(t)
	calls := 0
	fn := func(ctx context.Context, tx *sql.Tx, deps MigrateDeps) error {
		calls++
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS once(n INTEGER);
			INSERT INTO once(n) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM once)`)
		return err
	}
	for i := 0; i < 2; i++ {
		tx, err := s.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := fn(context.Background(), tx, MigrateDeps{Key: testKey()}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM once`).Scan(&n); err != nil || n != 1 || calls != 2 {
		t.Fatalf("n=%d calls=%d err=%v", n, calls, err)
	}
}
