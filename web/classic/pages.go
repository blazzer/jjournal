package classic

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"journal/app"
	"journal/lj"
	"journal/store"
	"journal/web"
)

func login(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server) {
	if u, ok := s.CurrentUser(r); ok && r.Method == http.MethodGet {
		http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		f.render(w, "login", struct {
			baseView
			Error string
		}{baseView: baseView{SiteName: s.Config.SiteName, Title: "Log in", CSRF: s.CSRFToken(w, r)}})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	sess, pwMD5, kind := app.Authenticate(ctx, s.Source, r.FormValue("username"), pw)
	pw = ""
	if kind == app.LoginDigest {
		http.Redirect(w, r, lj.DigestManageURL, http.StatusSeeOther)
		return
	}
	if kind != app.LoginOK {
		msg := "Could not reach LiveJournal."
		code := http.StatusBadGateway
		switch kind {
		case app.LoginAuth:
			msg = "Those credentials were not accepted."
			code = http.StatusUnauthorized
		case app.LoginBlocked:
			msg = "LiveJournal is blocking logins right now. Try again later."
			code = http.StatusServiceUnavailable
		}
		f.renderCode(w, code, "login", struct {
			baseView
			Error string
		}{baseView: baseView{SiteName: s.Config.SiteName, Title: "Log in", CSRF: s.CSRFToken(w, r)}, Error: msg})
		return
	}
	u, err := s.Store.UpsertLogin(ctx, sess.Username, sess.FullName, pwMD5, sess.Cookie)
	if err != nil {
		http.Error(w, "Could not save the login.", http.StatusInternalServerError)
		return
	}
	id, err := s.Store.CreateSession(ctx, u.ID, time.Now().Add(web.SessionTTL))
	if err != nil {
		http.Error(w, "Could not start a session.", http.StatusInternalServerError)
		return
	}
	s.WriteSession(w, r, id)
	if s.Worker != nil {
		s.Worker.Kick(u.ID)
	}
	http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
}

func logout(_ *Front, w http.ResponseWriter, r *http.Request, s *web.Server, _ store.User) {
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	if c, err := r.Cookie(web.SessionCookie); err == nil {
		id, _, _ := strings.Cut(c.Value, ".")
		_ = s.Store.DeleteSession(r.Context(), id)
	}
	s.ClearSession(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func home(w http.ResponseWriter, r *http.Request, u store.User) {
	http.Redirect(w, r, "/~"+u.Username+"/friends", http.StatusSeeOther)
}

func friends(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	feed, err := app.Reading(r.Context(), s.Store, app.ViewerFrom(viewer), journalUser, strings.TrimSpace(r.URL.Query().Get("filter")), atoi(r.URL.Query().Get("skip")))
	if err != nil {
		web.WriteError(w, r, err)
		return
	}
	path := "/~" + viewer.Username + "/friends"
	var links []linkView
	for _, g := range feed.Groups {
		links = append(links, linkView{Name: g.Name, URL: path + "?filter=" + url.QueryEscape(g.Name)})
	}
	prev, next := pagerURLs(path, strings.TrimSpace(r.URL.Query().Get("filter")), feed.Page.Skip, feed.Page.HasPrev, feed.Page.HasNext)
	f.render(w, "list", listView{
		baseView:   base(s, viewer, "Friends", s.CSRFToken(w, r)),
		Intro:      "Friends",
		FilterNote: feed.Note,
		AllURL:     path,
		Groups:     links,
		Entries:    entries(s, feed.Page.Entries, false),
		HasPrev:    feed.Page.HasPrev, HasNext: feed.Page.HasNext, PrevURL: prev, NextURL: next,
	})
}

func journal(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	page, err := app.Journal(r.Context(), s.Store, app.ViewerFrom(viewer), journalUser, atoi(r.URL.Query().Get("skip")))
	if err != nil {
		web.WriteError(w, r, err)
		return
	}
	path := "/~" + journalUser + "/"
	prev, next := pagerURLs(path, "", page.Skip, page.HasPrev, page.HasNext)
	f.render(w, "list", listView{
		baseView: base(s, viewer, journalUser, s.CSRFToken(w, r)),
		Intro:    journalUser + "'s journal",
		Entries:  entries(s, page.Entries, false),
		HasPrev:  page.HasPrev, HasNext: page.HasNext, PrevURL: prev, NextURL: next,
	})
}

func entry(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string, id int64) {
	e, err := app.OpenEntry(r.Context(), s.Store, app.ViewerFrom(viewer), journalUser, id)
	if err != nil {
		web.WriteError(w, r, err)
		return
	}
	action := "/~" + journalUser + "/" + strconv.FormatInt(id, 10) + ".html"
	if r.Method == http.MethodPost {
		postComment(w, r, s, viewer, e, action)
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
	csrf := s.CSRFToken(w, r)
	f.render(w, "entry", struct {
		baseView
		Entry    entryView
		Comments []*commentView
		Action   string
		ReplyTo  int64
		Native   bool
	}{
		baseView: base(s, viewer, e.Subject, csrf),
		Entry:    oneEntry(s, e, true, localUsers(s)),
		Comments: thread(s, comments, csrf, action, viewer, author.ID),
		Action:   action,
		ReplyTo:  atoi64(r.URL.Query().Get("replyto")),
		Native:   e.Source == "native",
	})
}

func postComment(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, e store.Entry, action string) {
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	id, err := app.Comment(r.Context(), s.Store, app.ViewerFrom(viewer), e, app.CommentDraft{
		Delete:    r.FormValue("action") == "delete",
		CommentID: atoi64(r.FormValue("comment_id")),
		Body:      r.FormValue("body"),
		ParentID:  atoi64(r.FormValue("parent_id")),
	}, time.Now())
	if err != nil {
		web.WriteError(w, r, err)
		return
	}
	if r.FormValue("action") == "delete" {
		http.Redirect(w, r, action+"#comments", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, action+"#c"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func update(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
	if r.Method == http.MethodGet {
		f.render(w, "update", struct {
			baseView
			Groups []store.Group
			Error  string
		}{baseView: base(s, viewer, "Update", s.CSRFToken(w, r)), Groups: groups})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
		http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
		return
	}
	id, err := app.CreatePost(r.Context(), s.Store, app.ViewerFrom(viewer), app.Post{
		Subject:  r.FormValue("subject"),
		Body:     r.FormValue("body"),
		Security: r.FormValue("security"),
		Groups:   app.ParseIDs(r.Form["group"]),
		Userpic:  r.FormValue("userpic"),
		Mood:     r.FormValue("mood"),
		Music:    r.FormValue("music"),
	}, time.Now())
	if err != nil {
		var inv app.Invalid
		if errors.As(err, &inv) {
			web.WriteError(w, r, err)
			return
		}
		http.Error(w, "Could not save the post.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/~"+viewer.Username+"/"+strconv.FormatInt(id, 10)+".html", http.StatusSeeOther)
}

func manage(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || !s.CheckCSRF(r) {
			http.Error(w, "The form expired. Go back and try again.", http.StatusBadRequest)
			return
		}
		err := app.ApplyList(r.Context(), s.Store, app.ViewerFrom(viewer), app.ListAction{
			Action:   r.FormValue("action"),
			Username: r.FormValue("username"),
			Name:     r.FormValue("name"),
			FriendID: atoi64(r.FormValue("friend_id")),
			GroupID:  atoi64(r.FormValue("group_id")),
			Groups:   app.ParseIDs(r.Form["group"]),
		})
		if err != nil {
			var inv app.Invalid
			if errors.As(err, &inv) {
				managePage(f, w, r, s, viewer, inv.Msg)
				return
			}
			web.WriteError(w, r, err)
			return
		}
		http.Redirect(w, r, "/manage/friends", http.StatusSeeOther)
		return
	}
	managePage(f, w, r, s, viewer, "")
}

func managePage(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, errMsg string) {
	ljFriends, _ := s.Store.ListLJFriends(r.Context(), viewer.ID)
	native, _ := s.Store.ListNativeFriends(r.Context(), viewer.ID)
	groups, _ := s.Store.ListGroups(r.Context(), viewer.ID)
	f.render(w, "manage", struct {
		baseView
		Error  string
		LJ     []store.LJFriendRow
		Native []store.NativeFriendRow
		Groups []store.Group
	}{baseView: base(s, viewer, "Friends", s.CSRFToken(w, r)), Error: errMsg, LJ: ljFriends, Native: native, Groups: groups})
}

func profile(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, name string) {
	p, err := app.LoadProfile(r.Context(), s.Store, name)
	if err != nil {
		web.WriteError(w, r, err)
		return
	}
	u := p.User
	f.render(w, "profile", struct {
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
		baseView:    base(s, viewer, u.DisplayName, s.CSRFToken(w, r)),
		ProfileName: u.Username,
		Display:     u.DisplayName,
		Joined:      formatWhen(u.CreatedAt),
		Migrated:    formatWhen(u.MigratedAt),
		LJCount:     p.LJCount,
		NativeCount: p.NativeCount,
		Pic:         pictureURL(s, u.Username),
		JournalURL:  "/~" + u.Username + "/",
	})
}

func admin(f *Front, w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	users, err := app.Users(r.Context(), s.Store, app.ViewerFrom(viewer))
	if err != nil {
		web.WriteError(w, r, err)
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
	f.render(w, "admin", struct {
		baseView
		Rows []row
	}{baseView: base(s, viewer, "Sync", s.CSRFToken(w, r)), Rows: rows})
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
