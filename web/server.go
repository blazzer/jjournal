package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"journal/lj"
	"journal/outbound"
	"journal/render"
	"journal/store"
	jsync "journal/sync"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server is the HTTP application.
type Server struct {
	Config Config
	Store  *store.Store
	Source lj.LJSource
	Worker *jsync.Worker
	Pics   *render.Proxy
	Images *render.Proxy
	tmpl   *template.Template
	files  http.Handler
}

// New builds a handler. Templates are parsed from the embedded files.
func New(cfg Config, st *store.Store, src lj.LJSource, worker *jsync.Worker, pics, images *render.Proxy) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, err
	}
	return &Server{
		Config: cfg,
		Store:  st,
		Source: src,
		Worker: worker,
		Pics:   pics,
		Images: images,
		tmpl:   tmpl,
		files:  http.StripPrefix("/static/", http.FileServer(http.FS(sub))),
	}, nil
}

// Run opens the database, starts sync, and serves until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	st, err := store.Open(cfg.DBPath, cfg.Secret)
	if err != nil {
		return err
	}
	defer st.Close()
	contact := os.Getenv("OPERATOR_CONTACT")
	if contact == "" {
		contact = "unset"
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	picDir, imgDir := cfg.UserpicDir, cfg.ImageDir
	if os.Getenv("USERPIC_DIR") == "" {
		picDir = filepath.Join(dataDir, "cache", "userpics")
	}
	if os.Getenv("IMAGE_CACHE_DIR") == "" {
		imgDir = filepath.Join(dataDir, "cache", "images")
	}
	oc := outbound.New(outbound.Config{Contact: contact, Pauses: storePauses{st}})
	if err := oc.Load(ctx); err != nil {
		return err
	}
	src, err := lj.NewSource(cfg.LJSource, oc.HTTP(outbound.LaneAPI))
	if err != nil {
		return err
	}
	pics := &render.Proxy{Key: cfg.Secret, Dir: picDir, Client: oc.HTTP(outbound.LaneImage)}
	images := &render.Proxy{Key: cfg.Secret, Dir: imgDir, Client: oc.HTTP(outbound.LaneImage)}
	jobs := make(chan string, 500)
	worker := jsync.New(st, src, pics, 2)
	worker.Pauses = oc
	worker.Images = jobs
	go fetchImages(ctx, jobs, pics, images)
	go cacheJanitor(ctx, []string{picDir, imgDir})
	go worker.Run(ctx)
	h, err := New(cfg, st, src, worker, pics, images)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; img-src 'self'; style-src 'self'; frame-src https://www.youtube.com https://www.youtube-nocookie.com https://player.vimeo.com; form-action 'self'; base-uri 'self'")
	path := r.URL.Path
	switch {
	case path == "/static" || stringsHasPrefix(path, "/static/"):
		s.files.ServeHTTP(w, r)
		return
	case path == "/login":
		s.login(w, r)
		return
	}
	if path == "/logout" {
		s.withUser(w, r, s.logout)
		return
	}
	if path == "/img" {
		s.withUser(w, r, func(w http.ResponseWriter, r *http.Request, _ store.User) {
			s.Images.ServeHTTP(w, r)
		})
		return
	}
	if stringsHasPrefix(path, "/userpics/") {
		s.withUser(w, r, s.userpic)
		return
	}
	switch path {
	case "/":
		s.withUser(w, r, s.home)
	case "/update":
		s.withUser(w, r, s.update)
	case "/manage/friends":
		s.withUser(w, r, s.manage)
	case "/admin":
		s.withUser(w, r, s.admin)
	default:
		user, kind, id, ok := ParseUserPath(path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.withUser(w, r, func(w http.ResponseWriter, r *http.Request, viewer store.User) {
			switch kind {
			case "friends":
				s.friends(w, r, viewer, user)
			case "journal":
				s.journal(w, r, viewer, user)
			case "profile":
				s.profile(w, r, viewer, user)
			case "entry":
				s.entry(w, r, viewer, user, id)
			default:
				http.NotFound(w, r)
			}
		})
	}
}

func stringsHasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func (s *Server) withUser(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request, store.User)) {
	u, ok := s.currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	fn(w, r, u)
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	s.renderCode(w, http.StatusOK, name, data)
}

func (s *Server) renderCode(w http.ResponseWriter, code int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		return
	}
}
