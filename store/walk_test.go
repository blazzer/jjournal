package store

import (
	"testing"
	"time"
)

func TestFeedDoesNotReplaceCredentialedCopy(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	ada, err := s.UpsertLogin(ctx, "ada", "Ada", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "c")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	id, err := s.UpsertLJEntry(ctx, ada.ID, LJEntryIn{
		ItemID: 9, Journal: "bob", Author: "bob", BodyHTML: "<p>full</p>", Security: "friends", EventTime: when,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, replaced, err := s.UpsertFeedEntry(ctx, "livejournal", "9", LJEntryIn{
		Journal: "bob", Author: "bob", BodyHTML: "<p>short</p>", EventTime: when,
	})
	if err != nil || replaced || got != id {
		t.Fatal(got, replaced, err)
	}
	var body, security string
	if err := s.db.QueryRow(`SELECT body_html, security FROM entries WHERE id=?`, id).Scan(&body, &security); err != nil {
		t.Fatal(err)
	}
	if body != "<p>full</p>" || security != "friends" {
		t.Fatal(body, security)
	}
	feedID, replaced, err := s.UpsertFeedEntry(ctx, "livejournal", "10", LJEntryIn{
		Journal: "bob", Author: "bob", BodyHTML: "<p>public</p>", EventTime: when,
	})
	if err != nil || !replaced || feedID == 0 {
		t.Fatal(feedID, replaced, err)
	}
	account, err := s.AccountID(ctx, ada.ID, "livejournal")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.NoteSeen(ctx, id, ada.ID, account, 1, when); err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertLJEntry(ctx, ada.ID, LJEntryIn{
		ItemID: 11, Journal: "bob", Author: "bob", BodyHTML: "<p>gone</p>", Security: "friends", EventTime: when,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.NoteSeen(ctx, other, ada.ID, account, 1, when); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteSeen(ctx, id, ada.ID, account, 2, when); err != nil {
		t.Fatal(err)
	}
	n, err := s.RevokeUnseen(ctx, account, 2, when.Add(-time.Hour))
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entry_visibility WHERE entry_id=?`, other).Scan(&left); err != nil || left != 0 {
		t.Fatal(left, err)
	}
	old := when.AddDate(0, 0, -40)
	if _, err := s.db.Exec(`UPDATE entries SET last_seen_at=? WHERE id=?`, FormatTime(old), feedID); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireRemote(ctx, when, 30, 90); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE id=?`, feedID).Scan(&left); err != nil || left != 1 {
		t.Fatal("public feed inside 90 days", left, err)
	}
	if _, err := s.db.Exec(`UPDATE entries SET last_seen_at=? WHERE id=?`, FormatTime(when.AddDate(0, 0, -100)), feedID); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireRemote(ctx, when, 30, 90); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE id=?`, feedID).Scan(&left); err != nil || left != 0 {
		t.Fatal("expired feed remains", left, err)
	}
}
