package store

import "testing"

func TestSuppressionHidesAuthor(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	ada, err := s.UpsertLogin(ctx, "ada", "Ada", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "c")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertLJEntry(ctx, ada.ID, LJEntryIn{
		ItemID: 1, Journal: "bob", Author: "bob", BodyHTML: "<p>x</p>", Security: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(len(page.Entries), err)
	}
	if err := s.Suppress(ctx, ada.ID, "livejournal", "bob"); err != nil {
		t.Fatal(err)
	}
	page, err = s.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) != 0 {
		t.Fatal(len(page.Entries), err)
	}
	if err := s.Unsuppress(ctx, ada.ID, "livejournal", "bob"); err != nil {
		t.Fatal(err)
	}
	page, err = s.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].ID != id {
		t.Fatal(page.Entries, err)
	}
	if err := s.Subscribe(ctx, ada.ID, "livejournal", "carol", "https://carol.livejournal.com/data/atom"); err != nil {
		t.Fatal(err)
	}
}
