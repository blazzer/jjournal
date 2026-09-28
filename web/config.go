package web

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is process configuration.
type Config struct {
	DBPath           string
	ListenAddr       string
	Secret           []byte
	SecretPrevious   []byte
	BaseURL          string
	SiteName         string
	LJSource         string
	DataDir          string
	UserpicDir       string
	ImageDir         string
	OperatorContact  string
	MaxUsers         int
	CacheMaxMB       int
	BackupKeepDaily  int
	BackupKeepWeekly int
	RetentionLocked  int
	RetentionPublic  int
	Credentialed     string
	MetricsAddr      string
	LogLevel         string
	Deprecated       []string
}

// DecodeSecret decodes a 32-byte base64 SECRET_KEY.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("SECRET_KEY must be 32 bytes of base64")
	}
	return b, nil
}

// ConfigFrom reads configuration. getenv is os.Getenv in production.
func ConfigFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		DBPath:           getenv("DB_PATH"),
		ListenAddr:       getenv("LISTEN_ADDR"),
		BaseURL:          strings.TrimRight(getenv("BASE_URL"), "/"),
		SiteName:         getenv("SITE_NAME"),
		LJSource:         strings.ToLower(strings.TrimSpace(getenv("LJ_SOURCE"))),
		DataDir:          getenv("DATA_DIR"),
		UserpicDir:       getenv("USERPIC_DIR"),
		ImageDir:         getenv("IMAGE_CACHE_DIR"),
		OperatorContact:  strings.TrimSpace(getenv("OPERATOR_CONTACT")),
		Credentialed:     getenv("CREDENTIALED_SERVICES"),
		MetricsAddr:      getenv("METRICS_ADDR"),
		LogLevel:         getenv("LOG_LEVEL"),
		MaxUsers:         atoiDefault(getenv("MAX_USERS"), 30),
		CacheMaxMB:       atoiDefault(getenv("CACHE_MAX_MB"), 256),
		BackupKeepDaily:  atoiDefault(getenv("BACKUP_KEEP_DAILY"), 7),
		BackupKeepWeekly: atoiDefault(getenv("BACKUP_KEEP_WEEKLY"), 4),
		RetentionLocked:  atoiDefault(getenv("RETENTION_LOCKED_DAYS"), 30),
		RetentionPublic:  atoiDefault(getenv("RETENTION_PUBLIC_DAYS"), 90),
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "journal.db"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:8080"
	}
	if cfg.SiteName == "" {
		cfg.SiteName = "Journal"
	}
	if cfg.LJSource == "" {
		cfg.LJSource = "xmlrpc"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.Credentialed == "" {
		cfg.Credentialed = "livejournal"
	}
	if cfg.MetricsAddr == "" && getenv("METRICS_ADDR") == "" {
		cfg.MetricsAddr = "127.0.0.1:9091"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	switch cfg.LJSource {
	case "xmlrpc", "digest", "scrape":
	default:
		return Config{}, fmt.Errorf("LJ_SOURCE must be xmlrpc, digest, or scrape")
	}
	if cfg.UserpicDir != "" {
		cfg.Deprecated = append(cfg.Deprecated, "USERPIC_DIR")
	} else {
		cfg.UserpicDir = filepath.Join(cfg.DataDir, "cache", "userpics")
	}
	if cfg.ImageDir != "" {
		cfg.Deprecated = append(cfg.Deprecated, "IMAGE_CACHE_DIR")
	} else {
		cfg.ImageDir = filepath.Join(cfg.DataDir, "cache", "images")
	}
	if prev := strings.TrimSpace(getenv("SECRET_KEY_PREVIOUS")); prev != "" {
		b, err := DecodeSecret(prev)
		if err != nil {
			return Config{}, fmt.Errorf("SECRET_KEY_PREVIOUS: %w", err)
		}
		cfg.SecretPrevious = b
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("BASE_URL must be an absolute http or https URL")
	}
	secret, err := DecodeSecret(getenv("SECRET_KEY"))
	if err != nil {
		return Config{}, err
	}
	cfg.Secret = secret
	return cfg, nil
}

func atoiDefault(s string, def int) int {
	if strings.TrimSpace(s) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
