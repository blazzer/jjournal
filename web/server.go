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

	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"

	"journal/lj"
	"journal/metrics"
	"journal/outbound"
	"journal/render"
	"journal/store"
	jsync "journal/sync"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server is the HTTP application.
type Server struct {
	Config  Config
	Store   *store.Store
	Source  lj.LJSource
	Worker  *jsync.Worker
	Pics    *render.Proxy
	Images  *render.Proxy
	Metrics *metrics.Registry
	tmpl    *template.Template
	files   http.Handler
	handler http.Handler
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
	s := &Server{
		Config: cfg,
		Store:  st,
		Source: src,
		Worker: worker,
		Pics:   pics,
		Images: images,
		tmpl:   tmpl,
		files:  http.StripPrefix("/static/", http.FileServer(http.FS(sub))),
	}
	s.handler = s.routes()
	return s, nil
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
	go FetchImages(ctx, jobs, pics, images)
	go CacheJanitor(ctx, []string{picDir, imgDir}, 256<<20)
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
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", s.files)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /logout", s.authed(s.logout))
	mux.HandleFunc("POST /logout", s.authed(s.logout))
	mux.HandleFunc("GET /img", s.authed(func(w http.ResponseWriter, r *http.Request, _ store.User) {
		s.Images.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /userpics/{name}", s.authed(s.userpic))
	mux.HandleFunc("GET /{$}", s.authed(s.home))
	mux.HandleFunc("GET /update", s.authed(s.update))
	mux.HandleFunc("POST /update", s.authed(s.update))
	mux.HandleFunc("GET /manage/friends", s.authed(s.manage))
	mux.HandleFunc("POST /manage/friends", s.authed(s.manage))
	mux.HandleFunc("GET /admin", s.authed(s.admin))
	mux.HandleFunc("POST /admin", s.authed(s.admin))
	mux.HandleFunc("GET /{path...}", s.userPath)
	mux.HandleFunc("POST /{path...}", s.userPath)
	return s.wrap(mux)
}

func (s *Server) authed(fn func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.withUser(w, r, fn) }
}

func (s *Server) userPath(w http.ResponseWriter, r *http.Request) {
	user, kind, id, ok := ParseUserPath(r.URL.Path)
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

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ready(r.Context()); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; img-src 'self'; style-src 'self'; frame-src https://www.youtube.com https://www.youtube-nocookie.com https://player.vimeo.com; form-action 'self'; base-uri 'self'")
		id := r.Header.Get("X-Request-ID")
		if !saneRequestID(id) {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Request-ID", id)
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(sw, r)
		attrs := []any{"route", r.Pattern, "status", sw.code, "duration", time.Since(start).String(), "request_id", id}
		if u, ok := s.currentUser(r); ok {
			attrs = append(attrs, "user", u.ID)
		}
		slog.Info("request", attrs...)
		if s.Metrics != nil && r.Pattern != "" {
			s.Metrics.AddCounter("http_requests_total", "HTTP requests.", 1, "route", r.Pattern, "status", http.StatusText(sw.code))
			s.Metrics.Observe("http_request_duration_seconds", "HTTP request latency.", time.Since(start).Seconds(), "route", r.Pattern)
		}
	})
}

func saneRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
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

var renderPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func (s *Server) renderCode(w http.ResponseWriter, code int, name string, data any) {
	buf := renderPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer renderPool.Put(buf)
	if err := s.tmpl.ExecuteTemplate(buf, name, data); err != nil {
		slog.Error("template", "name", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

// MetricsHandler serves the Prometheus text exposition.
func MetricsHandler(reg *metrics.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if reg == nil {
			return
		}
		_, _ = reg.WriteTo(w)
	})
}
