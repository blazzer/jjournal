package store

import (
	"bytes"
	"os"
	"testing"
	"time"

	"journal/lj"
	"journal/vault"
)

func TestProfileVault(t *testing.T) {
	s := openProfile(t)
	ctx := t.Context()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	token, err := s.CreateInvite(ctx, 0, true, now)
	if err != nil {
		t.Fatal(err)
	}
	const pass = "sentinel passphrase 9c1e"
	u, err := s.RedeemInvite(ctx, token, "Ada", "Ada", pass, 30, now)
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasVault || !u.IsAdmin || u.Username != "ada" {
		t.Fatalf("%+v", u)
	}
	if _, err := s.RedeemInvite(ctx, token, "bob", "Bob", pass, 30, now); err != ErrInvite {
		t.Fatal("invite reused")
	}
	dek, err := s.OpenVault(ctx, u.ID, pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenVault(ctx, u.ID, "wrong passphrase here"); err != vault.ErrPassphrase {
		t.Fatal(err)
	}
	var wrapped []byte
	if err := s.db.QueryRow(`SELECT dek_wrapped FROM users WHERE id=?`, u.ID).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), wrapped...)
	if err := s.RotateSecrets(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT dek_wrapped FROM users WHERE id=?`, u.ID).Scan(&wrapped); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, wrapped) {
		t.Fatal("key rotation touched the vault")
	}
	sess, err := s.CreateSession(ctx, u.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateSession(ctx, u.ID, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassphrase(ctx, u.ID, pass, "a newer passphrase", sess); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, other); err == nil {
		t.Fatal("other session survived")
	}
	if _, err := s.OpenVault(ctx, u.ID, "a newer passphrase"); err != nil {
		t.Fatal(err)
	}
	pw := "Sentinel-Pa55word-7f3a"
	md5hex := lj.PasswordMD5(pw)
	res, err := s.db.ExecContext(ctx, `INSERT INTO accounts(user_id, service, username, secret_scheme) VALUES (?, 'livejournal', 'ada', 'vault')`, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := res.LastInsertId()
	if err := s.PutRememberedPassword(ctx, u.ID, accountID, dek, md5hex, true); err != nil {
		t.Fatal(err)
	}
	clear(dek)
	raw, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{pass, "a newer passphrase", pw, md5hex} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("database contains %q", secret)
		}
	}
	if err := s.MarkAuthFailed(ctx, u.ID, "nope"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT secret_state FROM accounts WHERE id=?`, accountID).Scan(&state); err != nil || state != "needs_unlock" {
		t.Fatal(state, err)
	}
}

func TestDeleteProfileKeepsCommentTombstone(t *testing.T) {
	s := openProfile(t)
	ctx := t.Context()
	now := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	token, err := s.CreateInvite(ctx, 0, true, now)
	if err != nil {
		t.Fatal(err)
	}
	ada, err := s.RedeemInvite(ctx, token, "ada", "Ada", "passphrase one", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err = s.CreateInvite(ctx, ada.ID, false, now)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.RedeemInvite(ctx, token, "bob", "Bob", "passphrase two", 30, now)
	if err != nil {
		t.Fatal(err)
	}
	entryID, err := s.CreateNativeEntry(ctx, ada.ID, LJEntryIn{
		Subject: "hi", BodyHTML: "<p>hi</p>", Security: "public", EventTime: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddComment(ctx, entryID, 0, bob.ID, "<p>note</p>", now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	comments, err := s.ListComments(ctx, entryID)
	if err != nil || len(comments) != 1 || !comments[0].Deleted || comments[0].BodyHTML != "[deleted]" {
		t.Fatalf("%+v %v", comments, err)
	}
	if _, err := s.UserByID(ctx, bob.ID); err == nil {
		t.Fatal("bob remains")
	}
}

func openProfile(t *testing.T) *Store {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := storeOpen(t, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func storeOpen(t *testing.T, key []byte) (*Store, error) {
	t.Helper()
	return Open(t.TempDir()+"/t.db", key)
}
