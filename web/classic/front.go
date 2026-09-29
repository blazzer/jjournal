package classic

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"sync"

	"journal/store"
	"journal/web"
)

//go:embed templates/*.html
var assets embed.FS

// Front is the classic HTML interface.
type Front struct {
	tmpl *template.Template
}

// New parses the classic templates.
func New() (*Front, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Front{tmpl: tmpl}, nil
}

func (f *Front) UI() string { return "classic" }

func (f *Front) Login(w http.ResponseWriter, r *http.Request, s *web.Server) {
	login(f, w, r, s)
}
func (f *Front) Logout(w http.ResponseWriter, r *http.Request, s *web.Server, u store.User) {
	logout(f, w, r, s, u)
}
func (f *Front) Home(w http.ResponseWriter, r *http.Request, s *web.Server, u store.User) {
	home(w, r, u)
}
func (f *Front) Friends(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string) {
	friends(f, w, r, s, viewer, journalUser)
}
func (f *Front) Journal(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string) {
	journal(f, w, r, s, viewer, journalUser)
}
func (f *Front) Entry(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, journalUser string, id int64) {
	entry(f, w, r, s, viewer, journalUser, id)
}
func (f *Front) Update(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	update(f, w, r, s, viewer)
}
func (f *Front) Manage(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	manage(f, w, r, s, viewer)
}
func (f *Front) Profile(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User, name string) {
	profile(f, w, r, s, viewer, name)
}
func (f *Front) Admin(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	admin(f, w, r, s, viewer)
}
func (f *Front) Signup(w http.ResponseWriter, r *http.Request, s *web.Server) {
	signup(f, w, r, s)
}
func (f *Front) Recover(w http.ResponseWriter, r *http.Request, s *web.Server) {
	recoverAccount(f, w, r, s)
}
func (f *Front) Settings(w http.ResponseWriter, r *http.Request, s *web.Server, viewer store.User) {
	settings(f, w, r, s, viewer)
}

func (f *Front) render(w http.ResponseWriter, name string, data any) {
	f.renderCode(w, http.StatusOK, name, data)
}

var renderPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func (f *Front) renderCode(w http.ResponseWriter, code int, name string, data any) {
	buf := renderPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer renderPool.Put(buf)
	if err := f.tmpl.ExecuteTemplate(buf, name, data); err != nil {
		slog.Error("template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}
