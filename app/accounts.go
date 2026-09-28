package app

import (
	"context"

	"journal/lj"
)

// LoginKind classifies a service login without an HTTP status.
type LoginKind string

const (
	LoginOK      LoginKind = "ok"
	LoginDigest  LoginKind = "digest"
	LoginAuth    LoginKind = "auth"
	LoginBlocked LoginKind = "blocked"
	LoginDown    LoginKind = "down"
)

// Authenticate checks a service password. The password is hashed before the call and not returned.
func Authenticate(ctx context.Context, src lj.LJSource, username, password string) (lj.Session, string, LoginKind) {
	pwMD5 := lj.PasswordMD5(password)
	sess, err := src.Login(ctx, username, pwMD5)
	if err == nil {
		return sess, pwMD5, LoginOK
	}
	switch {
	case lj.IsDigestDisabled(err):
		return lj.Session{}, "", LoginDigest
	case lj.IsAuth(err):
		return lj.Session{}, "", LoginAuth
	case lj.IsBlocked(err):
		return lj.Session{}, "", LoginBlocked
	default:
		return lj.Session{}, "", LoginDown
	}
}
