package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSQLiteMultiStatementTrigger(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "m.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	body := `
CREATE TABLE t(id INTEGER PRIMARY KEY, n INTEGER);
CREATE TRIGGER t_bi BEFORE INSERT ON t
BEGIN
  SELECT CASE WHEN NEW.n < 0 THEN RAISE(ABORT, 'neg') END;
END;
INSERT INTO t(n) VALUES (1);
`
	if _, err := db.Exec(body); err != nil {
		t.Fatalf("multi-statement exec: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT n FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := db.Exec(`INSERT INTO t(n) VALUES (-1)`); err == nil {
		t.Fatal("trigger did not abort")
	}
}

func TestSQLiteFractionalSecondsWithZ(t *testing.T) {
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(t.TempDir(), "t.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got string
	err = db.QueryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ', '2024-01-02T03:04:05.123456789Z')`).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2024-01-02T03:04:05.123Z" {
		t.Fatalf("strftime = %q", got)
	}
	var ordered int
	err = db.QueryRow(`SELECT '2024-01-02T03:04:05.123456789Z' < '2024-01-02T03:04:05.123456790Z'`).Scan(&ordered)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("lexical: %d", ordered)
}

func TestVacuumIntoBoundParameter(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, "src.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(n INTEGER); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.db")
	if _, err := db.ExecContext(context.Background(), `VACUUM INTO ?`, dest); err != nil {
		t.Fatalf("VACUUM INTO ?: %v", err)
	}
	db2, err := sql.Open("sqlite", sqliteDSN(dest))
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var n int
	if err := db2.QueryRow(`SELECT n FROM t`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}
