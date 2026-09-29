// Package vault wraps a member's data key with a passphrase.
package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	// MinPassphrase is the shortest accepted passphrase.
	MinPassphrase = 12
	saltLen       = 16
	dekLen        = 32
	timeCost      = 2
	memoryKiB     = 19 * 1024
	threads       = 1
)

// Params are the argon2id settings stored with a vault.
type Params struct {
	Time    uint32 `json:"t"`
	Memory  uint32 `json:"m"`
	Threads uint8  `json:"p"`
}

// DefaultParams is the SPEC derivation cost.
func DefaultParams() Params {
	return Params{Time: timeCost, Memory: memoryKiB, Threads: threads}
}

// Wrap is a passphrase-encrypted data key.
type Wrap struct {
	Salt    []byte
	Params  Params
	Wrapped []byte
}

// BusyError means the derivation queue was full for too long.
type BusyError struct{ RetryAfter time.Duration }

func (e BusyError) Error() string { return "vault: busy" }

// ErrPassphrase means the passphrase did not unwrap the data key, or it is too short.
var ErrPassphrase = errors.New("vault: passphrase")

var (
	slots     = make(chan struct{}, 2)
	dummySalt = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
)

// NewDEK returns a random 32-byte data key.
func NewDEK() ([]byte, error) {
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, err
	}
	return dek, nil
}

// SealPassphrase wraps dek under passphrase. aad binds the ciphertext to the profile.
func SealPassphrase(ctx context.Context, passphrase string, dek []byte, aad string) (Wrap, error) {
	if len(passphrase) < MinPassphrase || len(dek) != dekLen {
		return Wrap{}, ErrPassphrase
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return Wrap{}, err
	}
	p := DefaultParams()
	key, err := Derive(ctx, passphrase, salt, p)
	if err != nil {
		return Wrap{}, err
	}
	defer clear(key)
	blob, err := seal(key, dek, aad)
	if err != nil {
		return Wrap{}, err
	}
	return Wrap{Salt: salt, Params: p, Wrapped: blob}, nil
}

// OpenPassphrase unwraps a data key. A wrong passphrase returns ErrPassphrase.
func OpenPassphrase(ctx context.Context, passphrase string, w Wrap, aad string) ([]byte, error) {
	if len(passphrase) < MinPassphrase {
		return nil, ErrPassphrase
	}
	key, err := Derive(ctx, passphrase, w.Salt, w.Params)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	dek, err := open(key, w.Wrapped, aad)
	if err != nil {
		return nil, ErrPassphrase
	}
	if len(dek) != dekLen {
		return nil, ErrPassphrase
	}
	return dek, nil
}

// Dummy runs a full derivation so an unknown handle costs the same as a real one.
func Dummy(ctx context.Context) error {
	_, err := Derive(ctx, "journal dummy vault passphrase", dummySalt, DefaultParams())
	return err
}

// Derive runs argon2id, waiting at most 5 seconds for a slot.
func Derive(ctx context.Context, passphrase string, salt []byte, p Params) ([]byte, error) {
	if p.Time == 0 || p.Memory == 0 || p.Threads == 0 {
		p = DefaultParams()
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-timer.C:
		return nil, BusyError{RetryAfter: 5 * time.Second}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if deriveHook != nil {
		return deriveHook(passphrase, salt, p), nil
	}
	return argon2.IDKey([]byte(passphrase), salt, p.Time, p.Memory, p.Threads, dekLen), nil
}

// deriveHook replaces argon2 in tests.
var deriveHook func(passphrase string, salt []byte, p Params) []byte

// MarshalParams encodes parameters for the users.kdf_params column.
func MarshalParams(p Params) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UnmarshalParams decodes kdf_params.
func UnmarshalParams(s string) (Params, error) {
	var p Params
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return Params{}, err
	}
	if p.Time == 0 || p.Memory == 0 || p.Threads == 0 {
		return Params{}, fmt.Errorf("vault: bad params")
	}
	return p, nil
}

// DEKAAD binds a wrapped data key to a profile.
func DEKAAD(userID int64) string {
	return fmt.Sprintf("journal/v1/dek/%d", userID)
}

// SecretAAD binds a service secret to a profile, account, and purpose.
func SecretAAD(purpose string, userID, accountID int64) string {
	return fmt.Sprintf("journal/v1/%s/%d/%d", purpose, userID, accountID)
}

// Seal encrypts plaintext with the data key.
func Seal(dek []byte, aad, plaintext string) ([]byte, error) {
	return seal(dek, []byte(plaintext), aad)
}

// Open decrypts a data-key ciphertext.
func Open(dek []byte, aad string, blob []byte) ([]byte, error) {
	return open(dek, blob, aad)
}

func seal(key, plaintext []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, []byte(aad)), nil
}

func open(key, blob []byte, aad string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize() {
		return nil, errors.New("vault: short ciphertext")
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, []byte(aad))
}
