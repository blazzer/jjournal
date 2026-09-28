package web

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"journal/keys"
	"journal/store"
)

const (
	// SessionCookie is the signed session cookie name.
	SessionCookie = "journal_session"
	csrfCookie    = "journal_csrf"
	// SessionTTL is how long a session cookie stays valid.
	SessionTTL = 30 * 24 * time.Hour
)

func (s *Server) secure(r *http.Request) bool {
	if r != nil && r.TLS != nil {
		return true
	}
	if r != nil && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(s.Config.BaseURL), "https://")
}

// WriteSession sets the signed session cookie.
func (s *Server) WriteSession(w http.ResponseWriter, r *http.Request, id string) {
	value, err := signSession(s.Config.Secret, id)
	if err != nil {
		value = id + "." + store.Sign(s.Config.Secret, id)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionTTL.Seconds()),
	})
}

// ClearSession removes the session cookie.
func (s *Server) ClearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// CurrentUser returns the signed-in member when the session cookie is valid.
func (s *Server) CurrentUser(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return store.User{}, false
	}
	id, ok := sessionID(s.Config.Secret, s.Config.SecretPrevious, c.Value)
	if !ok {
		return store.User{}, false
	}
	sess, err := s.Store.LookupSession(r.Context(), id)
	if err != nil {
		return store.User{}, false
	}
	u, err := s.Store.UserByID(r.Context(), sess.UserID)
	if err != nil {
		return store.User{}, false
	}
	return u, true
}

// CSRFToken returns the form token, creating the cookie when it is missing.
func (s *Server) CSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	tok := hex.EncodeToString(buf[:])
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: tok, Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
	})
	return tok
}

// CheckCSRF reports whether the form token matches the cookie.
func (s *Server) CheckCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || len(c.Value) < 32 {
		return false
	}
	form := r.FormValue("csrf")
	if len(form) != len(c.Value) {
		return false
	}
	return hmac.Equal([]byte(form), []byte(c.Value))
}

func signSession(secret []byte, id string) (string, error) {
	root, err := keys.NewRoot(secret)
	if err != nil {
		return "", err
	}
	kid, sig, err := keys.SignCookie(root, id)
	if err != nil {
		return "", err
	}
	return id + "." + kid + "." + sig, nil
}

func sessionID(current, previous []byte, cookie string) (string, bool) {
	parts := strings.Split(cookie, ".")
	switch len(parts) {
	case 2:
		if store.Verify(current, parts[0], parts[1]) || (len(previous) == 32 && store.Verify(previous, parts[0], parts[1])) {
			return parts[0], true
		}
	case 3:
		for _, key := range [][]byte{current, previous} {
			if len(key) != 32 {
				continue
			}
			root, err := keys.NewRoot(key)
			if err != nil {
				continue
			}
			if hexID(root.ID[:]) == parts[1] && keys.VerifyCookie(root, parts[0], parts[2]) {
				return parts[0], true
			}
		}
	}
	return "", false
}

func hexID(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
