package web

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"journal/store"
)

const (
	sessionCookie = "journal_session"
	csrfCookie    = "journal_csrf"
	sessionTTL    = 30 * 24 * time.Hour
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

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id + "." + store.Sign(s.Config.Secret, id),
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return store.User{}, false
	}
	id, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !store.Verify(s.Config.Secret, id, sig) {
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

func (s *Server) csrfToken(w http.ResponseWriter, r *http.Request) string {
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

func (s *Server) checkCSRF(r *http.Request) bool {
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
