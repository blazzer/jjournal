package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// buildV1 applies migrations 001 and 002 plus testdata/v1_seed.sql, then any
// later migrations through Open. Phase 0 has no later migrations.
func buildV1(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.db")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_init.sql", "002_backfill.sql"} {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range splitSQL(string(body)) {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, name, "2024-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	seed, err := os.ReadFile(filepath.Join("testdata", "v1_seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(seed)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, testKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestV1SeedShape(t *testing.T) {
	s := buildV1(t)
	ctx := context.Background()
	var users, ljEntries, nativeEntries, comments, deleted, sessions, ljFriends, nativeFriends, groups, vis int
	row := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(*) FROM entries WHERE source='lj'),
		(SELECT COUNT(*) FROM entries WHERE source='native'),
		(SELECT COUNT(*) FROM comments),
		(SELECT COUNT(*) FROM comments WHERE deleted=1),
		(SELECT COUNT(*) FROM sessions),
		(SELECT COUNT(*) FROM lj_friends),
		(SELECT COUNT(*) FROM native_friends),
		(SELECT COUNT(*) FROM friend_groups),
		(SELECT COUNT(*) FROM entry_visibility)`)
	if err := row.Scan(&users, &ljEntries, &nativeEntries, &comments, &deleted, &sessions, &ljFriends, &nativeFriends, &groups, &vis); err != nil {
		t.Fatal(err)
	}
	if users != 4 || ljEntries != 40 || nativeEntries != 16 || comments != 4 || deleted != 1 || sessions != 3 || ljFriends != 9 || nativeFriends != 4 || groups != 7 || vis == 0 {
		t.Fatalf("counts users=%d lj=%d native=%d comments=%d deleted=%d sessions=%d ljFriends=%d nativeFriends=%d groups=%d vis=%d",
			users, ljEntries, nativeEntries, comments, deleted, sessions, ljFriends, nativeFriends, groups, vis)
	}
	pw, cookie, err := s.Secrets(ctx, 1)
	if err != nil || pw != "11111111111111111111111111111111" || cookie != "sess-ada" {
		t.Fatalf("secrets %q %q %v", pw, cookie, err)
	}
	var parent int
	if err := s.db.QueryRow(`SELECT parent_id FROM comments WHERE id=2`).Scan(&parent); err != nil || parent != 1 {
		t.Fatalf("thread parent %d %v", parent, err)
	}
}

// feedIdent is the stable identity recorded in the v1 golden file.
type feedIdent struct {
	Journal  string `json:"journal"`
	ItemID   int64  `json:"itemid,omitempty"`
	NativeID int64  `json:"native_id,omitempty"`
}

type goldenQuery struct {
	Viewer  string      `json:"viewer"`
	Journal string      `json:"journal,omitempty"`
	Filter  string      `json:"filter"`
	Skip    int         `json:"skip"`
	Entries []feedIdent `json:"entries"`
}

type goldenFile struct {
	Friends  []goldenQuery `json:"friends"`
	Journals []goldenQuery `json:"journals"`
}

func entryIdent(e Entry) feedIdent {
	id := feedIdent{Journal: e.Journal}
	if e.Source == "native" || e.Source == "local" {
		id.NativeID = e.ID
		return id
	}
	id.ItemID = e.ItemID
	return id
}

func collectGolden(t *testing.T, s *Store) goldenFile {
	t.Helper()
	ctx := context.Background()
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var g goldenFile
	journals := make([]string, 0, len(users))
	for _, u := range users {
		journals = append(journals, u.Username)
	}
	for _, u := range users {
		groups, err := s.ListGroups(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		filters := []struct {
			name string
			mask uint32
		}{{"", 0}}
		for _, gr := range groups {
			filters = append(filters, struct {
				name string
				mask uint32
			}{gr.Name, Mask(gr.Bit)})
		}
		for _, f := range filters {
			for _, skip := range []int{0, 20} {
				page, err := s.FriendsPage(ctx, u.ID, f.mask, skip, PageSize)
				if err != nil {
					t.Fatal(err)
				}
				q := goldenQuery{Viewer: u.Username, Filter: f.name, Skip: skip}
				for _, e := range page.Entries {
					q.Entries = append(q.Entries, entryIdent(e))
				}
				if q.Entries == nil {
					q.Entries = []feedIdent{}
				}
				g.Friends = append(g.Friends, q)
			}
		}
		for _, journal := range journals {
			for _, skip := range []int{0, 20} {
				page, err := s.JournalPage(ctx, u.ID, journal, skip, PageSize)
				if err != nil {
					t.Fatal(err)
				}
				q := goldenQuery{Viewer: u.Username, Journal: journal, Skip: skip}
				for _, e := range page.Entries {
					q.Entries = append(q.Entries, entryIdent(e))
				}
				if q.Entries == nil {
					q.Entries = []feedIdent{}
				}
				g.Journals = append(g.Journals, q)
			}
		}
	}
	return g
}

func TestV1FeedGolden(t *testing.T) {
	s := buildV1(t)
	got := collectGolden(t, s)
	path := filepath.Join("testdata", "v1_feed_golden.json")
	raw, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(raw) {
		t.Fatalf("golden mismatch (%d vs %d bytes); set UPDATE_GOLDEN=1 to rewrite", len(want), len(raw))
	}
}
