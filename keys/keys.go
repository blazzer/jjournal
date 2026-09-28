// Package keys derives subkeys from the root and seals short secrets.
package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	LabelCookie = "journal/v1/cookie"
	LabelCSRF   = "journal/v1/csrf"
	LabelImage  = "journal/v1/image"
	LabelToken  = "journal/v1/token"
	LabelLegacy = "journal/v1/legacy-secret"
)

// Root is one 32-byte SECRET_KEY and the id recorded on ciphertexts.
type Root struct {
	ID  [4]byte
	Key []byte
}

// NewRoot returns the id and key. The key must be 32 bytes.
func NewRoot(key []byte) (Root, error) {
	if len(key) != 32 {
		return Root{}, errors.New("keys: root must be 32 bytes")
	}
	sum := sha256.Sum256(append([]byte("journal key id"), key...))
	var id [4]byte
	copy(id[:], sum[:4])
	return Root{ID: id, Key: key}, nil
}

// Derive returns a 32-byte subkey for label.
func Derive(root []byte, label string) ([]byte, error) {
	return hkdf.Key(sha256.New, root, nil, label, 32)
}

// Seal writes version 1, the key id, the nonce, and the ciphertext.
func Seal(root Root, label string, plaintext []byte) ([]byte, error) {
	sub, err := Derive(root.Key, label)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sub)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, 1+4+len(nonce)+len(ct))
	out = append(out, 0x01)
	out = append(out, root.ID[:]...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

// Open decrypts an envelope with the first root whose id matches.
func Open(roots []Root, label string, sealed []byte) ([]byte, error) {
	if len(sealed) < 1+4+12 || sealed[0] != 0x01 {
		return nil, errors.New("keys: not an envelope")
	}
	var id [4]byte
	copy(id[:], sealed[1:5])
	for _, root := range roots {
		if root.ID != id {
			continue
		}
		sub, err := Derive(root.Key, label)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(sub)
		if err != nil {
			return nil, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		nonce, ct := sealed[5:5+gcm.NonceSize()], sealed[5+gcm.NonceSize():]
		plain, err := gcm.Open(nil, nonce, ct, nil)
		if err != nil {
			return nil, fmt.Errorf("keys: decrypt: %w", err)
		}
		return plain, nil
	}
	return nil, errors.New("keys: unknown key id")
}

// IsEnvelope reports whether b starts with the envelope version byte.
func IsEnvelope(b []byte) bool { return len(b) > 0 && b[0] == 0x01 }

// SignCookie returns key-id hex and an HMAC over id using the cookie subkey.
func SignCookie(root Root, id string) (string, string, error) {
	sub, err := Derive(root.Key, LabelCookie)
	if err != nil {
		return "", "", err
	}
	mac := hmac.New(sha256.New, sub)
	mac.Write([]byte(id))
	return hex.EncodeToString(root.ID[:]), hex.EncodeToString(mac.Sum(nil)), nil
}

// VerifyCookie checks a signature made by SignCookie.
func VerifyCookie(root Root, id, sig string) bool {
	_, want, err := SignCookie(root, id)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(sig))
}

// SignImage MACs a URL with the image subkey.
func SignImage(root Root, raw string) (string, error) {
	sub, err := Derive(root.Key, LabelImage)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, sub)
	mac.Write(root.ID[:])
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil)), nil
}
