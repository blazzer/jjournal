package classic

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"journal/app"
	"journal/store"
	"journal/vault"
	"journal/web"
)

func signup(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server) {
	now := time.Now()
	if raw := r.URL.Query().Get("invite"); raw != "" && r.Method == http.MethodGet {
		if err := s.Store.InviteOpen(r.Context(), raw, now); err != nil {
			f.renderCode(w, http.StatusBadRequest, "signup", signupView(s, w, r, "That invite is not valid."))
			return
		}
		s.WriteInvite(w, r, raw)
		http.Redirect(w, r, "/signup", http.StatusSeeOther)
		return
	}
	token, ok := s.InviteHash(r)
	if r.Method == http.MethodGet {
		msg := ""
		if !ok {
			msg = "An invite is required."
		}
		f.render(w, "signup", signupView(s, w, r, msg))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) || !ok {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	ip := web.ClientIP(r)
	if !s.Limits.AllowSignup(now, ip) {
		web.WriteError(w, r, app.RateLimited{RetryAfter: time.Hour})
		return
	}
	s.Limits.Signup(now, ip)
	pass := r.FormValue("passphrase")
	if pass != r.FormValue("confirm") {
		f.renderCode(w, http.StatusBadRequest, "signup", signupView(s, w, r, "The passphrases did not match."))
		return
	}
	u, err := s.Store.RedeemInvite(r.Context(), token, r.FormValue("handle"), r.FormValue("display"), pass, s.Config.MaxUsers, now)
	pass = ""
	if err != nil {
		msg := "Could not create the profile."
		code := http.StatusBadRequest
		switch {
		case errors.Is(err, store.ErrFull):
			msg = "This site is not accepting new members."
		case errors.Is(err, store.ErrInvite):
			msg = "That invite is not valid."
		case errors.Is(err, vault.ErrPassphrase):
			msg = "Use a passphrase of at least 12 characters."
		case errors.Is(err, store.ErrHandle):
			msg = "Use a handle of 2 to 30 letters, digits, or underscores."
		default:
			if strings.Contains(err.Error(), "UNIQUE") {
				msg = "That handle is taken."
			} else {
				code = http.StatusInternalServerError
			}
		}
		f.renderCode(w, code, "signup", signupView(s, w, r, msg))
		return
	}
	s.ClearInvite(w, r)
	id, err := s.Store.CreateSession(r.Context(), u.ID, now.Add(web.SessionTTL))
	if err != nil {
		http.Error(w, "Could not start a session.", http.StatusInternalServerError)
		return
	}
	s.WriteSession(w, r, id)
	http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
}

type signupPage struct {
	baseView
	Error string
}

func signupView(s *web.Server, w http.ResponseWriter, r *http.Request, msg string) signupPage {
	return signupPage{
		baseView: baseView{SiteName: s.Config.SiteName, Title: "Sign up", CSRF: s.CSRFToken(w, r)},
		Error:    msg,
	}
}

func recoverAccount(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server) {
	now := time.Now()
	if raw := r.URL.Query().Get("token"); raw != "" && r.Method == http.MethodGet {
		if _, err := s.Store.RecoveryUser(r.Context(), raw, now); err != nil {
			f.renderCode(w, http.StatusBadRequest, "recover", recoverView(s, w, r, "That recovery link is not valid."))
			return
		}
		s.WriteRecovery(w, r, raw)
		http.Redirect(w, r, "/recover", http.StatusSeeOther)
		return
	}
	token, ok := s.RecoveryToken(r)
	if r.Method == http.MethodGet {
		msg := ""
		if !ok {
			msg = "A recovery link is required."
		}
		f.render(w, "recover", recoverView(s, w, r, msg))
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) || !ok {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	userID, err := s.Store.RecoveryUser(r.Context(), token, now)
	if err != nil {
		f.renderCode(w, http.StatusBadRequest, "recover", recoverView(s, w, r, "That recovery link is not valid."))
		return
	}
	n, err := s.Store.LinkedAccounts(r.Context(), userID)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		ip := web.ClientIP(r)
		if !s.Limits.AllowService(now, ip) {
			web.WriteError(w, r, app.RateLimited{RetryAfter: time.Hour})
			return
		}
		_, _, kind := app.Authenticate(r.Context(), s.Source, r.FormValue("service_user"), r.FormValue("service_password"))
		if kind != app.LoginOK {
			s.Limits.ServiceFail(now, ip)
			f.renderCode(w, http.StatusUnauthorized, "recover", recoverView(s, w, r, "Sign in to a linked service account first."))
			return
		}
	}
	pass := r.FormValue("passphrase")
	if pass != r.FormValue("confirm") || len(pass) < vault.MinPassphrase {
		f.renderCode(w, http.StatusBadRequest, "recover", recoverView(s, w, r, "Choose a new passphrase of at least 12 characters."))
		return
	}
	if err := s.Store.ResetVault(r.Context(), token, pass, now); err != nil {
		f.renderCode(w, http.StatusBadRequest, "recover", recoverView(s, w, r, "Could not reset the passphrase."))
		return
	}
	s.ClearRecovery(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func recoverView(s *web.Server, w http.ResponseWriter, r *http.Request, msg string) signupPage {
	return signupPage{baseView: baseView{SiteName: s.Config.SiteName, Title: "Recover", CSRF: s.CSRFToken(w, r)}, Error: msg}
}

func settings(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	if r.Method == http.MethodGet {
		f.render(w, "settings", signupPage{baseView: base(s, viewer, "Settings", s.CSRFToken(w, r))})
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	switch r.FormValue("action") {
	case "delete":
		if _, err := s.Store.OpenVault(r.Context(), viewer.ID, r.FormValue("passphrase")); err != nil {
			f.renderCode(w, http.StatusUnauthorized, "settings", signupPage{baseView: base(s, viewer, "Settings", s.CSRFToken(w, r)), Error: "Passphrase was not accepted."})
			return
		}
		if err := s.Store.DeleteProfile(r.Context(), viewer.ID); err != nil {
			http.Error(w, "Could not delete the profile.", http.StatusInternalServerError)
			return
		}
		s.ClearSession(w, r)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	default:
		id := ""
		if c, err := r.Cookie(web.SessionCookie); err == nil {
			id, _, _ = strings.Cut(c.Value, ".")
		}
		if err := s.Store.ChangePassphrase(r.Context(), viewer.ID, r.FormValue("passphrase"), r.FormValue("next"), id); err != nil {
			f.renderCode(w, http.StatusBadRequest, "settings", signupPage{baseView: base(s, viewer, "Settings", s.CSRFToken(w, r)), Error: "Could not change the passphrase."})
			return
		}
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}
