package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"journal/lj"
)

func testKey() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return b
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir()+"/t.db", testKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenTwiceAndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir+"/t.db", testKey())
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir+"/t.db", testKey())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.db.Exec(`INSERT INTO comments(entry_id, author_user_id, body_html, created_at) VALUES (99, 99, 'x', '2020-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatal("foreign key not enforced")
	}
}

func TestCryptoRoundTrip(t *testing.T) {
	key := testKey()
	a, err := Seal(key, []byte("pw-hash"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(key, []byte("pw-hash"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) {
		t.Fatal("nonce reused")
	}
	out, err := Unseal(key, a)
	if err != nil || string(out) != "pw-hash" {
		t.Fatal(out, err)
	}
	a[len(a)-1] ^= 0xff
	if _, err := Unseal(key, a); err == nil {
		t.Fatal("tamper accepted")
	}
	other := testKey()
	other[0] ^= 0xff
	sealed, _ := Seal(key, []byte("x"))
	if _, err := Unseal(other, sealed); err == nil {
		t.Fatal("wrong key accepted")
	}
	if !Verify(key, "msg", Sign(key, "msg")) || Verify(key, "msg", Sign(key, "other")) {
		t.Fatal("hmac")
	}
}

func TestNormalizeSkip(t *testing.T) {
	if NormalizeSkip(-5) != 0 || NormalizeSkip(3) != 3 || NormalizeSkip(1000000) != 100000 {
		t.Fatal(NormalizeSkip(-5), NormalizeSkip(1000000))
	}
}

func TestLoginSecretsAndAdmin(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.UpsertLogin(ctx, "Ada", "Ada Lovelace", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cookie-value")
	if err != nil {
		t.Fatal(err)
	}
	if !IsAdmin(u) || u.DisplayName != "Ada Lovelace" || u.SyncStatus != StatusOK {
		t.Fatalf("%+v", u)
	}
	pw, cookie, err := s.Secrets(ctx, u.ID)
	if err != nil || pw != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || cookie != "cookie-value" {
		t.Fatal(pw, cookie, err)
	}
	u2, err := s.UpsertLogin(ctx, "bob", "", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "")
	if err != nil || IsAdmin(u2) {
		t.Fatal(u2, err)
	}
	groups, err := s.ListGroups(ctx, u.ID)
	if err != nil || len(groups) != 1 || groups[0].Name != "Friends" || groups[0].Bit != 1 {
		t.Fatalf("%+v %v", groups, err)
	}
	id, err := s.CreateSession(ctx, u.ID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.LookupSession(ctx, id)
	if err != nil || sess.UserID != u.ID {
		t.Fatal(err)
	}
	old, err := s.CreateSession(ctx, u.ID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, old); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

func TestDedupeVisibilityAndPaging(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ada, _ := s.UpsertLogin(ctx, "ada", "Ada", strings.Repeat("a", 32), "c")
	bob, _ := s.UpsertLogin(ctx, "bob", "Bob", strings.Repeat("b", 32), "c")
	base := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	in := LJEntryIn{
		ItemID: 1001, Journal: "carol", Author: "carol", URL: "https://carol.livejournal.com/1001.html",
		Subject: "One", BodyHTML: "<p>first</p>", Security: "public", EventTime: base,
	}
	id1, err := s.UpsertLJEntry(ctx, ada.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	in.BodyHTML = "<p>second</p>"
	in.Subject = "Updated"
	id2, err := s.UpsertLJEntry(ctx, ada.ID, in)
	if err != nil || id1 != id2 {
		t.Fatal(id1, id2, err)
	}
	if _, err := s.UpsertLJEntry(ctx, bob.ID, in); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM entry_visibility`).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	var body string
	s.db.QueryRow(`SELECT body_html FROM entries WHERE id=?`, id1).Scan(&body)
	if body != "<p>second</p>" {
		t.Fatal(body)
	}
	for i := 0; i < 24; i++ {
		_, err := s.UpsertLJEntry(ctx, ada.ID, LJEntryIn{
			ItemID: int64(2000 + i), Journal: "carol", Author: "carol",
			Subject: "e", BodyHTML: "<p>x</p>", Security: "public",
			EventTime: base.Add(time.Duration(i) * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.FriendsPage(ctx, ada.ID, 0, 0, PageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != PageSize || !page.HasNext || page.HasPrev {
		t.Fatalf("%d next %v prev %v", len(page.Entries), page.HasNext, page.HasPrev)
	}
	if !page.Entries[0].EventTime.After(page.Entries[1].EventTime) {
		t.Fatal("order")
	}
	page2, err := s.FriendsPage(ctx, ada.ID, 0, 20, PageSize)
	if err != nil || len(page2.Entries) != 5 || page2.HasNext || !page2.HasPrev {
		t.Fatalf("%d %+v %v", len(page2.Entries), page2, err)
	}
	hidden, err := s.FriendsPage(ctx, bob.ID, 0, 0, PageSize)
	if err != nil || len(hidden.Entries) != 1 {
		t.Fatalf("bob should see only the shared entry, got %d %v", len(hidden.Entries), err)
	}
}

func TestMigratedExclusionAndNativeSecurity(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ada, _ := s.UpsertLogin(ctx, "ada", "Ada", strings.Repeat("a", 32), "")
	bob, _ := s.UpsertLogin(ctx, "bob", "Bob", strings.Repeat("b", 32), "")
	cara, _ := s.UpsertLogin(ctx, "cara", "Cara", strings.Repeat("c", 32), "")
	when := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(`UPDATE users SET migrated_at=? WHERE id=?`, formatTime(when), bob.ID); err != nil {
		t.Fatal(err)
	}
	before := LJEntryIn{ItemID: 1, Journal: "bob", Author: "bob", Security: "public", BodyHTML: "old", EventTime: when.Add(-time.Hour)}
	after := LJEntryIn{ItemID: 2, Journal: "bob", Author: "bob", Security: "public", BodyHTML: "new", EventTime: when.Add(time.Hour)}
	if _, err := s.UpsertLJEntry(ctx, ada.ID, before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertLJEntry(ctx, ada.ID, after); err != nil {
		t.Fatal(err)
	}
	page, err := s.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].BodyHTML != "old" {
		t.Fatalf("%+v %v", page.Entries, err)
	}

	if err := s.AddNativeFriend(ctx, ada.ID, bob.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNativeFriend(ctx, ada.ID, ada.ID, 1); err == nil {
		t.Fatal("self friend")
	}
	pub, err := s.CreateNativeEntry(ctx, bob.ID, LJEntryIn{Subject: "pub", BodyHTML: "p", Security: "public", EventTime: when.Add(2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNativeEntry(ctx, bob.ID, LJEntryIn{Subject: "priv", BodyHTML: "secret", Security: "private", EventTime: when.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNativeEntry(ctx, bob.ID, LJEntryIn{Subject: "fr", BodyHTML: "only", Security: "friends", EventTime: when.Add(4 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateNativeGroup(ctx, bob.ID, "Book")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNativeFriend(ctx, bob.ID, ada.ID, Mask(g.Bit)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNativeEntry(ctx, bob.ID, LJEntryIn{Subject: "custom", BodyHTML: "club", Security: "custom", AllowMask: Mask(g.Bit), EventTime: when.Add(5 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNativeEntry(ctx, bob.ID, LJEntryIn{Subject: "nope", BodyHTML: "hidden", Security: "custom", AllowMask: Mask(3), EventTime: when.Add(6 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	page, err = s.FriendsPage(ctx, ada.ID, 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range page.Entries {
		got[e.Subject] = true
	}
	for _, want := range []string{"pub", "fr", "custom"} {
		if !got[want] {
			t.Fatalf("missing %s in %#v", want, got)
		}
	}
	if got["priv"] || got["nope"] || got["new"] {
		t.Fatalf("leaked %#v", got)
	}
	_ = pub
	j, err := s.JournalPage(ctx, cara.ID, "bob", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	jgot := map[string]bool{}
	for _, e := range j.Entries {
		jgot[e.Subject] = true
	}
	if !jgot["pub"] || jgot["priv"] || jgot["fr"] {
		t.Fatalf("journal %#v", jgot)
	}
	own, err := s.JournalPage(ctx, bob.ID, "bob", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(own.Entries) < 5 {
		t.Fatalf("owner should see private posts, got %d", len(own.Entries))
	}
	if _, err := s.VisibleEntry(ctx, cara.ID, "bob", pub); err != nil {
		t.Fatal(err)
	}
	closeG, err := s.CreateNativeGroup(ctx, ada.ID, "Close")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNativeFriend(ctx, ada.ID, bob.ID, Mask(closeG.Bit)); err != nil {
		t.Fatal(err)
	}
	filtered, err := s.FriendsPage(ctx, ada.ID, Mask(closeG.Bit), 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	fgot := map[string]bool{}
	for _, e := range filtered.Entries {
		fgot[e.Subject] = true
	}
	for _, want := range []string{"pub", "fr", "custom"} {
		if !fgot[want] {
			t.Fatalf("filter missing %s in %#v", want, fgot)
		}
	}
	if fgot["priv"] || fgot["nope"] || fgot["old"] {
		t.Fatalf("filter leaked %#v", fgot)
	}
	var migrated string
	s.db.QueryRow(`SELECT migrated_at FROM users WHERE id=?`, bob.ID).Scan(&migrated)
	if migrated != formatTime(when) {
		t.Fatal("first post should not move migrated_at", migrated)
	}
}

func TestGroupMoveAndComments(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	ada, _ := s.UpsertLogin(ctx, "ada", "Ada", strings.Repeat("a", 32), "")
	bob, _ := s.UpsertLogin(ctx, "bob", "Bob", strings.Repeat("b", 32), "")
	g, err := s.CreateNativeGroup(ctx, ada.ID, "Book")
	if err != nil {
		t.Fatal(err)
	}
	if g.Bit == 1 {
		t.Fatal("default friends occupies bit 1")
	}
	if err := s.AddNativeFriend(ctx, ada.ID, bob.ID, Mask(1)|Mask(g.Bit)); err != nil {
		t.Fatal(err)
	}
	// Force the native group onto bit 2 if it isn't, then claim bit 2 from LJ.
	if _, err := s.db.Exec(`UPDATE friend_groups SET bit=2 WHERE id=?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE native_friends SET groupmask=? WHERE user_id=?`, Mask(2), ada.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceLJGroups(ctx, ada.ID, []lj.LJGroup{{ID: 2, Name: "Work"}}); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ListGroups(ctx, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	var bookBit int
	var sawWork bool
	for _, gr := range groups {
		if gr.Name == "Book" {
			bookBit = gr.Bit
			if gr.Origin != "native" {
				t.Fatal(gr)
			}
		}
		if gr.Name == "Work" && gr.Bit == 2 && gr.Origin == "lj" {
			sawWork = true
		}
	}
	if !sawWork || bookBit == 2 || bookBit == 0 {
		t.Fatalf("groups %+v", groups)
	}
	friends, err := s.ListNativeFriends(ctx, ada.ID)
	if err != nil || len(friends) != 1 || friends[0].GroupMask&Mask(bookBit) == 0 || friends[0].GroupMask&Mask(2) != 0 {
		t.Fatalf("%+v %v", friends, err)
	}
	if err := s.DeleteNativeGroup(ctx, ada.ID, groups[0].ID); err != nil && groups[0].Origin == "lj" {
		// delete the native one specifically
	}
	for _, gr := range groups {
		if gr.Origin == "lj" {
			if err := s.DeleteNativeGroup(ctx, ada.ID, gr.ID); err == nil {
				t.Fatal("deleted lj group")
			}
		}
		if gr.Name == "Book" {
			if err := s.DeleteNativeGroup(ctx, ada.ID, gr.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	id, err := s.CreateNativeEntry(ctx, ada.ID, LJEntryIn{Subject: "n", BodyHTML: "b", Security: "public", EventTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	cid, err := s.AddComment(ctx, id, 0, bob.ID, "<p>hi</p>", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(ctx, id, cid, ada.ID, "<p>re</p>", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteComment(ctx, bob.ID, cid); err != nil {
		t.Fatal(err)
	}
	comments, err := s.ListComments(ctx, id)
	if err != nil || len(comments) != 2 || !comments[0].Deleted || comments[0].BodyHTML != "" {
		t.Fatalf("%+v %v", comments, err)
	}
	ljID, err := s.UpsertLJEntry(ctx, ada.ID, LJEntryIn{ItemID: 9, Journal: "ada", Author: "ada", Security: "public", BodyHTML: "z", EventTime: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(ctx, ljID, 0, ada.ID, "no", time.Now()); err == nil {
		t.Fatal("comment on lj entry")
	}
}

func TestSyncStatusQueries(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.UpsertLogin(ctx, "ada", "Ada", strings.Repeat("a", 32), "")
	now := time.Now().UTC()
	due, err := s.UsersDue(ctx, now.Add(time.Minute))
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
	if err := s.MarkAuthFailed(ctx, u.ID, "authentication failed"); err != nil {
		t.Fatal(err)
	}
	due, err = s.UsersDue(ctx, now.Add(time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
	if err := s.MarkBlocked(ctx, u.ID, "blocked", now.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	due, err = s.UsersDue(ctx, now)
	if err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
	due, err = s.UsersDue(ctx, now.Add(7*time.Hour))
	if err != nil || len(due) != 1 {
		t.Fatal(due, err)
	}
}
