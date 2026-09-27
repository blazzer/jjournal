package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"journal/lj"
	"journal/render"
	"journal/store"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.currentUser(r); ok && r.Method == http.MethodGet {
		http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		s.render(w, "login", struct {
			baseView
			Error string
		}{baseView: baseView{SiteName: s.Config.SiteName, Title: "Log in", CSRF: s.csrfToken(w, r)}, Error: ""})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	pwMD5 := lj.PasswordMD5(pw)
	pw = ""
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	sess, err := s.Source.Login(ctx, r.FormValue("username"), pwMD5)
	if lj.IsDigestDisabled(err) {
		http.Redirect(w, r, lj.DigestManageURL, http.StatusSeeOther)
		return
	}
	if err != nil {
		msg := "Could not reach LiveJournal."
		code := http.StatusBadGateway
		switch {
		case lj.IsAuth(err):
			msg = "Those credentials were not accepted."
			code = http.StatusUnauthorized
		case lj.IsBlocked(err):
			msg = "LiveJournal is blocking logins right now. Try again later."
			code = http.StatusServiceUnavailable
		}
		s.renderCode(w, code, "login", struct {
			baseView
			Error string
		}{baseView: baseView{SiteName: s.Config.SiteName, Title: "Log in", CSRF: s.csrfToken(w, r)}, Error: msg})
		return
	}
	u, err := s.Store.UpsertLogin(ctx, sess.Username, sess.FullName, pwMD5, sess.Cookie)
	if err != nil {
		http.Error(w, "Could not save the login.", http.StatusInternalServerError)
		return
	}
	id, err := s.Store.CreateSession(ctx, u.ID, time.Now().Add(sessionTTL))
	if err != nil {
		http.Error(w, "Could not start a session.", http.StatusInternalServerError)
		return
	}
	s.writeSession(w, r, id)
	if s.Worker != nil {
		s.Worker.Kick(u.ID)
	}
	http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, u store.User) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		id, _, _ := strings.Cut(c.Value, ".")
		_ = s.Store.DeleteSession(r.Context(), id)
	}
	s.clearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
	_ = u
}

func (s *Server) home(w http.ResponseWriter, r *http.Request, u store.User) {
	http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
}

func (s *Server) friends(w http.ResponseWriter, r *http.Request, viewer store.User, journalUser string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if journalUser != viewer.Username {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.list(w, r, viewer, "/~"+viewer.Username+"/friends", "friends", "Friends")
}

func (s *Server) journal(w http.ResponseWriter, r *http.Request, viewer store.User, journalUser string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	skip := store.NormalizeSkip(atoi(r.URL.Query().Get("skip")))
	page, err := s.Store.JournalPage(r.Context(), viewer.ID, journalUser, skip, store.PageSize)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	path := "/~" + journalUser + "/"
	prev, next := pagerURLs(path, "", page.Skip, page.HasPrev, page.HasNext)
	s.render(w, "list", listView{
		baseView: s.base(viewer, journalUser, s.csrfToken(w, r)),
		Intro:    journalUser + "'s journal",
		Entries:  s.entries(page.Entries, false),
		HasPrev:  page.HasPrev, HasNext: page.HasNext, PrevURL: prev, NextURL: next,
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, viewer store.User, path, kind, title string) {
	skip := store.NormalizeSkip(atoi(r.URL.Query().Get("skip")))
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	mask, err := s.Store.GroupMask(r.Context(), viewer.ID, filter)
	note := ""
	if errors.Is(err, store.ErrUnknownGroup) {
		note = "No such group."
		mask = 1<<31 - 1 // show nothing: no friend has every high bit; use impossible mask
		mask = 0x80000000
	} else if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	var page store.Page
	if note == "" {
		page, err = s.Store.FriendsPage(r.Context(), viewer.ID, mask, skip, store.PageSize)
		if err != nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
	}
	groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
	var links []linkView
	for _, g := range groups {
		links = append(links, linkView{Name: g.Name, URL: path + "?filter=" + url.QueryEscape(g.Name)})
	}
	prev, next := pagerURLs(path, filter, page.Skip, page.HasPrev, page.HasNext)
	s.render(w, "list", listView{
		baseView:   s.base(viewer, title, s.csrfToken(w, r)),
		Intro:      "Friends",
		FilterNote: note,
		AllURL:     path,
		Groups:     links,
		Entries:    s.entries(page.Entries, false),
		HasPrev:    page.HasPrev, HasNext: page.HasNext, PrevURL: prev, NextURL: next,
	})
	_ = kind
}

func (s *Server) entry(w http.ResponseWriter, r *http.Request, viewer store.User, journalUser string, id int64) {
	e, err := s.Store.VisibleEntry(r.Context(), viewer.ID, journalUser, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	action := "/~" + journalUser + "/" + strconv.FormatInt(id, 10) + ".html"
	if r.Method == http.MethodPost {
		s.postComment(w, r, viewer, e, action)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	comments, err := s.Store.ListComments(r.Context(), e.ID)
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	author, _ := s.Store.UserByUsername(r.Context(), e.Author)
	csrf := s.csrfToken(w, r)
	replyTo := atoi64(r.URL.Query().Get("replyto"))
	s.render(w, "entry", struct {
		baseView
		Entry    entryView
		Comments []*commentView
		Action   string
		ReplyTo  int64
		Native   bool
	}{
		baseView: s.base(viewer, e.Subject, csrf),
		Entry:    s.oneEntry(e, true, s.localUsers()),
		Comments: s.thread(comments, csrf, action, viewer, author.ID),
		Action:   action,
		ReplyTo:  replyTo,
		Native:   e.Source == "native",
	})
}

func (s *Server) postComment(w http.ResponseWriter, r *http.Request, viewer store.User, e store.Entry, action string) {
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	if r.FormValue("action") == "delete" {
		id := atoi64(r.FormValue("comment_id"))
		if err := s.Store.DeleteComment(r.Context(), viewer.ID, id); err != nil {
			http.Error(w, "Could not delete that comment.", http.StatusForbidden)
			return
		}
		http.Redirect(w, r, action+"#comments", http.StatusSeeOther)
		return
	}
	if e.Source != "native" {
		http.Error(w, "Comments on mirrored entries stay on the original site.", http.StatusBadRequest)
		return
	}
	body := r.FormValue("body")
	if len(body) > 20000 {
		http.Error(w, "Comment is too long.", http.StatusBadRequest)
		return
	}
	clean := render.Sanitize(body)
	if !render.HasText(clean) {
		http.Error(w, "Write a comment first.", http.StatusBadRequest)
		return
	}
	parent := atoi64(r.FormValue("parent_id"))
	id, err := s.Store.AddComment(r.Context(), e.ID, parent, viewer.ID, clean, time.Now())
	if err != nil {
		http.Error(w, "Could not save the comment.", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, action+"#c"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request, viewer store.User) {
	groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
	if r.Method == http.MethodGet {
		s.render(w, "update", struct {
			baseView
			Groups []store.Group
			Error  string
		}{baseView: s.base(viewer, "Update", s.csrfToken(w, r)), Groups: groups})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	subject := strings.TrimSpace(r.FormValue("subject"))
	body := r.FormValue("body")
	if len(subject) > 200 || len(body) > 100000 {
		http.Error(w, "That post is too long.", http.StatusBadRequest)
		return
	}
	clean := render.Sanitize(body)
	if !render.HasText(clean) {
		http.Error(w, "Write something first.", http.StatusBadRequest)
		return
	}
	sec := r.FormValue("security")
	switch sec {
	case "public", "friends", "private", "custom":
	default:
		sec = "public"
	}
	var mask uint32
	if sec == "friends" {
		mask = 1
	}
	if sec == "custom" {
		for _, raw := range r.Form["group"] {
			id := atoi64(raw)
			for _, g := range groups {
				if g.ID == id {
					mask |= store.Mask(g.Bit)
				}
			}
		}
		if mask == 0 {
			http.Error(w, "Choose at least one group.", http.StatusBadRequest)
			return
		}
	}
	pic := strings.TrimSpace(r.FormValue("userpic"))
	if pic != "" {
		if u, err := url.Parse(pic); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "Userpic must be an http or https URL.", http.StatusBadRequest)
			return
		}
	}
	mood := trimMax(r.FormValue("mood"), 80)
	music := trimMax(r.FormValue("music"), 80)
	id, err := s.Store.CreateNativeEntry(r.Context(), viewer.ID, store.LJEntryIn{
		Subject: subject, BodyHTML: clean, Security: sec, AllowMask: mask,
		EventTime: time.Now().UTC(), UserpicURL: pic, Mood: mood, Music: music,
	})
	if err != nil {
		http.Error(w, "Could not save the post.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/~"+viewer.Username+"/"+strconv.FormatInt(id, 10)+".html", http.StatusSeeOther)
}

func (s *Server) manage(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || !s.checkCSRF(r) {
			http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
			return
		}
		msg := s.managePost(r, viewer)
		if msg != "" {
			s.managePage(w, r, viewer, msg)
			return
		}
		http.Redirect(w, r, "/manage/friends", http.StatusSeeOther)
		return
	}
	s.managePage(w, r, viewer, "")
}

func (s *Server) managePost(r *http.Request, viewer store.User) string {
	switch r.FormValue("action") {
	case "add_friend":
		name, err := lj.NormalizeUsername(r.FormValue("username"))
		if err != nil {
			return "Enter a valid username."
		}
		friend, err := s.Store.UserByUsername(r.Context(), name)
		if err != nil {
			return "That person does not have an account here yet."
		}
		groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
		var mask uint32
		for _, raw := range r.Form["group"] {
			id := atoi64(raw)
			for _, g := range groups {
				if g.ID == id && g.Origin == "native" {
					mask |= store.Mask(g.Bit)
				}
			}
		}
		if mask == 0 {
			mask = 1
		}
		if err := s.Store.AddNativeFriend(r.Context(), viewer.ID, friend.ID, mask); err != nil {
			return "Could not add that friend."
		}
	case "remove_friend":
		id := atoi64(r.FormValue("friend_id"))
		if err := s.Store.RemoveNativeFriend(r.Context(), viewer.ID, id); err != nil {
			return "Could not remove that friend."
		}
	case "add_group":
		if _, err := s.Store.CreateNativeGroup(r.Context(), viewer.ID, r.FormValue("name")); err != nil {
			return "Could not add that group."
		}
	case "remove_group":
		if err := s.Store.DeleteNativeGroup(r.Context(), viewer.ID, atoi64(r.FormValue("group_id"))); err != nil {
			return "LiveJournal groups are read-only."
		}
	default:
		return "Unknown action."
	}
	return ""
}

func (s *Server) managePage(w http.ResponseWriter, r *http.Request, viewer store.User, errMsg string) {
	ljFriends, _ := s.Store.ListLJFriends(r.Context(), viewer.ID)
	native, _ := s.Store.ListNativeFriends(r.Context(), viewer.ID)
	groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
	s.render(w, "manage", struct {
		baseView
		Error  string
		LJ     []store.LJFriendRow
		Native []store.NativeFriendRow
		Groups []store.Group
	}{baseView: s.base(viewer, "Friends", s.csrfToken(w, r)), Error: errMsg, LJ: ljFriends, Native: native, Groups: groups})
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, viewer store.User, name string) {
	u, err := s.Store.UserByUsername(r.Context(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ljFriends, _ := s.Store.ListLJFriends(r.Context(), u.ID)
	native, _ := s.Store.ListNativeFriends(r.Context(), u.ID)
	s.render(w, "profile", struct {
		baseView
		ProfileName string
		Display     string
		Joined      string
		Migrated    string
		LJCount     int
		NativeCount int
		Pic         string
		JournalURL  string
	}{
		baseView:    s.base(viewer, u.DisplayName, s.csrfToken(w, r)),
		ProfileName: u.Username,
		Display:     u.DisplayName,
		Joined:      formatWhen(u.CreatedAt),
		Migrated:    formatWhen(u.MigratedAt),
		LJCount:     len(ljFriends),
		NativeCount: len(native),
		Pic:         s.pictureURL(u.Username),
		JournalURL:  "/~" + u.Username + "/",
	})
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request, viewer store.User) {
	if !store.IsAdmin(viewer) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	type row struct {
		Username string
		Status   string
		Error    string
		Synced   string
		Migrated string
	}
	var rows []row
	for _, u := range users {
		rows = append(rows, row{
			Username: u.Username,
			Status:   u.SyncStatus,
			Error:    u.SyncError,
			Synced:   formatWhen(u.LastSyncedAt),
			Migrated: formatWhen(u.MigratedAt),
		})
	}
	s.render(w, "admin", struct {
		baseView
		Rows []row
	}{baseView: s.base(viewer, "Sync", s.csrfToken(w, r)), Rows: rows})
}

func (s *Server) userpic(w http.ResponseWriter, r *http.Request, _ store.User) {
	path, err := render.LocalUserpicPath(s.Pics.Dir, r.URL.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func trimMax(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
