package vault

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"testing/synctest"
	"time"
)

func TestWrapAndAAD(t *testing.T) {
	deriveHook = func(pass string, _ []byte, _ Params) []byte {
		sum := sha256.Sum256([]byte(pass))
		return sum[:]
	}
	t.Cleanup(func() { deriveHook = nil })
	ctx := t.Context()
	dek, err := NewDEK()
	if err != nil {
		t.Fatal(err)
	}
	w, err := SealPassphrase(ctx, "correct horse battery", dek, DEKAAD(4))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenPassphrase(ctx, "correct horse battery", w, DEKAAD(4))
	if err != nil || !bytes.Equal(got, dek) {
		t.Fatal(err)
	}
	if _, err := OpenPassphrase(ctx, "wrong horse battery", w, DEKAAD(4)); err != ErrPassphrase {
		t.Fatal(err)
	}
	if _, err := OpenPassphrase(ctx, "correct horse battery", w, DEKAAD(5)); err != ErrPassphrase {
		t.Fatal("aad swap", err)
	}
	blob, err := Seal(dek, SecretAAD("password", 4, 9), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dek, SecretAAD("password", 4, 8), blob); err == nil {
		t.Fatal("account swap")
	}
	plain, err := Open(dek, SecretAAD("password", 4, 9), blob)
	if err != nil || string(plain) != "secret" {
		t.Fatal(err, string(plain))
	}
}

func TestShortPassphrase(t *testing.T) {
	_, err := SealPassphrase(t.Context(), "too-short", bytes.Repeat([]byte{1}, 32), DEKAAD(1))
	if err != ErrPassphrase {
		t.Fatal(err)
	}
}

func TestCacheExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := NewCache()
		dek := bytes.Repeat([]byte{3}, 32)
		c.Put("sess", dek, time.Now())
		time.Sleep(14 * time.Minute)
		if _, ok := c.Get("sess", time.Now()); !ok {
			t.Fatal("still valid")
		}
		time.Sleep(2 * time.Minute)
		if _, ok := c.Get("sess", time.Now()); ok {
			t.Fatal("expired")
		}
		c.Put("sess", dek, time.Now())
		c.Drop("sess")
		if _, ok := c.Get("sess", time.Now()); ok {
			t.Fatal("dropped")
		}
	})
}
