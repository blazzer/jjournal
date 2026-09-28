package keys

import (
	"bytes"
	"testing"
)

func TestSealRoundTripAndKeyID(t *testing.T) {
	a, err := NewRoot(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRoot(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("ids collided")
	}
	sealed, err := Seal(a, LabelLegacy, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open([]Root{b}, LabelLegacy, sealed); err == nil {
		t.Fatal("opened with the wrong root")
	}
	if _, err := Open([]Root{a}, LabelToken, sealed); err == nil {
		t.Fatal("opened with the wrong label")
	}
	plain, err := Open([]Root{b, a}, LabelLegacy, sealed)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("%q %v", plain, err)
	}
	kid, sig, err := SignCookie(a, "sess")
	if err != nil || !VerifyCookie(a, "sess", sig) || len(kid) != 8 {
		t.Fatal(kid, err)
	}
}
