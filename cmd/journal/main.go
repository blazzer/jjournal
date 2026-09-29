package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"journal/lj"
	"journal/metrics"
	"journal/outbound"
	"journal/render"
	"journal/store"
	jsync "journal/sync"
	"journal/web"
	"journal/web/classic"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "serve":
		return cmdServe(args)
	case "invite":
		return cmdInvite(args)
	case "backup":
		return cmdBackup(args)
	case "restore":
		return cmdRestore(args)
	case "rotate-keys":
		return cmdRotate(args)
	case "demo":
		return cmdDemo(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		return 2
	}
}

func cmdInvite(args []string) int {
	fs := flag.NewFlagSet("invite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	admin := fs.Bool("admin", false, "grant admin on the new profile")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.OpenWithDataDir(cfg.DBPath, cfg.Secret, cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	token, err := st.CreateInvite(context.Background(), 0, *admin, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(cfg.BaseURL + "/signup?invite=" + token)
	return 0
}

func cmdDemo(args []string) int {
	if err := ensureDemoEnv(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	url, err := demoInviteURL()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(url)
	return cmdServe(args)
}

func ensureDemoEnv() error {
	if strings.TrimSpace(os.Getenv("OPERATOR_CONTACT")) == "" {
		if err := os.Setenv("OPERATOR_CONTACT", "demo@localhost"); err != nil {
			return err
		}
	}
	data := os.Getenv("DATA_DIR")
	if data == "" {
		data = "data"
		if err := os.Setenv("DATA_DIR", data); err != nil {
			return err
		}
	}
	if os.Getenv("DB_PATH") == "" {
		if err := os.Setenv("DB_PATH", filepath.Join(data, "demo.db")); err != nil {
			return err
		}
	}
	if strings.TrimSpace(os.Getenv("SECRET_KEY")) != "" {
		return nil
	}
	path := filepath.Join(data, "demo.key")
	b, err := os.ReadFile(path)
	if err != nil {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		b = []byte(base64.StdEncoding.EncodeToString(raw))
		if err := os.MkdirAll(data, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return err
		}
	}
	return os.Setenv("SECRET_KEY", strings.TrimSpace(string(b)))
}

func demoInviteURL() (string, error) {
	cfg, err := loadConfig(false)
	if err != nil {
		return "", err
	}
	st, err := store.OpenWithDataDir(cfg.DBPath, cfg.Secret, cfg.DataDir)
	if err != nil {
		return "", err
	}
	defer st.Close()
	token, err := st.CreateInvite(context.Background(), 0, true, time.Now())
	if err != nil {
		return "", err
	}
	return cfg.BaseURL + "/signup?invite=" + token, nil
}

func loadConfig(requireContact bool) (web.Config, error) {
	cfg, err := web.ConfigFrom(os.Getenv)
	if err != nil {
		return web.Config{}, err
	}
	setupLog(cfg.LogLevel)
	for _, name := range cfg.Deprecated {
		slog.Warn("deprecated environment variable; use DATA_DIR", "name", name)
	}
	if requireContact && cfg.OperatorContact == "" {
		return web.Config{}, errors.New("OPERATOR_CONTACT is required")
	}
	return cfg, nil
}

func setupLog(level string) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, cfg); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "err", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, cfg web.Config) error {
	st, err := store.OpenWithDataDir(cfg.DBPath, cfg.Secret, cfg.DataDir)
	if err != nil {
		return err
	}
	contact := cfg.OperatorContact
	if contact == "" {
		contact = "unset"
	}
	oc := outbound.New(outbound.Config{Contact: contact, Pauses: pauseBridge{st}})
	if err := oc.Load(ctx); err != nil {
		st.Close()
		return err
	}
	src, err := lj.NewSource(cfg.LJSource, oc.HTTP(outbound.LaneAPI))
	if err != nil {
		st.Close()
		return err
	}
	pics := &render.Proxy{Key: cfg.Secret, Dir: cfg.UserpicDir, Client: oc.HTTP(outbound.LaneImage)}
	images := &render.Proxy{Key: cfg.Secret, Dir: cfg.ImageDir, Client: oc.HTTP(outbound.LaneImage)}
	jobs := make(chan string, 500)
	worker := jsync.New(st, src, pics, 2)
	worker.Pauses = oc
	worker.Images = jobs
	reg := metrics.New()
	front, err := classic.New()
	if err != nil {
		st.Close()
		return err
	}
	h, err := web.New(cfg, st, src, worker, pics, images, front)
	if err != nil {
		st.Close()
		return err
	}
	h.Metrics = reg
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" && cfg.MetricsAddr != "off" {
		metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: web.MetricsHandler(reg), ReadHeaderTimeout: 5 * time.Second}
		go metricsSrv.ListenAndServe()
	}
	backupCtx, backupStop := context.WithCancel(context.Background())
	go backupLoop(backupCtx, st, filepath.Join(cfg.DataDir, "backups"))
	go web.FetchImages(ctx, jobs, pics, images)
	go web.CacheJanitor(ctx, []string{cfg.UserpicDir, cfg.ImageDir}, int64(cfg.CacheMaxMB)<<20)
	go worker.Run(ctx)

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-errCh:
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	schedCtx, schedCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = shutdown(context.Background(), []step{
		{"http", func(context.Context) error { return srv.Shutdown(shutCtx) }},
		{"scheduler", func(context.Context) error { return worker.Stop(schedCtx) }},
		{"backup", func(context.Context) error { backupStop(); return nil }},
		{"store", func(context.Context) error {
			if metricsSrv != nil {
				_ = metricsSrv.Close()
			}
			pics.Close()
			images.Close()
			return st.Close()
		}},
	})
	cancel()
	schedCancel()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return err
}

func backupLoop(ctx context.Context, st *store.Store, dir string) {
	timer := time.NewTimer(time.Duration(time.Now().UnixNano()%int64(time.Hour)) + time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if _, err := st.Backup(ctx, dir); err != nil {
				slog.Error("backup", "err", err)
			} else {
				slog.Info("backup", "dir", dir)
			}
			timer.Reset(24 * time.Hour)
		}
	}
}

func cmdBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.OpenWithDataDir(cfg.DBPath, cfg.Secret, cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	path, err := st.Backup(context.Background(), filepath.Join(cfg.DataDir, "backups"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(path)
	return 0
}

func cmdRotate(args []string) int {
	fs := flag.NewFlagSet("rotate-keys", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.OpenWithDataDir(cfg.DBPath, cfg.Secret, cfg.DataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	if len(cfg.SecretPrevious) == 32 {
		st.SetPrevious(cfg.SecretPrevious)
	}
	if err := st.RotateSecrets(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("rotated")
	return 0
}

func cmdRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: journal restore <file>")
		return 2
	}
	cfg, err := loadConfig(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := store.Restore(fs.Arg(0), cfg.DBPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("restored", cfg.DBPath)
	return 0
}

type pauseBridge struct{ s *store.Store }

func (p pauseBridge) List(ctx context.Context) (map[string]time.Time, error) {
	return p.s.ListHostPauses(ctx)
}

func (p pauseBridge) Put(ctx context.Context, host string, until time.Time, reason string) error {
	return p.s.PutHostPause(ctx, host, until, reason)
}
