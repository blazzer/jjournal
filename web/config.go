package web

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// Config is process configuration.
type Config struct {
	DBPath     string
	ListenAddr string
	Secret     []byte
	BaseURL    string
	SiteName   string
	LJSource   string
	UserpicDir string
	ImageDir   string
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
		DBPath:     getenv("DB_PATH"),
		ListenAddr: getenv("LISTEN_ADDR"),
		BaseURL:    strings.TrimRight(getenv("BASE_URL"), "/"),
		SiteName:   getenv("SITE_NAME"),
		LJSource:   strings.ToLower(strings.TrimSpace(getenv("LJ_SOURCE"))),
		UserpicDir: getenv("USERPIC_DIR"),
		ImageDir:   getenv("IMAGE_CACHE_DIR"),
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
	switch cfg.LJSource {
	case "xmlrpc", "digest", "scrape":
	default:
		return Config{}, fmt.Errorf("LJ_SOURCE must be xmlrpc, digest, or scrape")
	}
	if cfg.UserpicDir == "" {
		cfg.UserpicDir = "data/userpics"
	}
	if cfg.ImageDir == "" {
		cfg.ImageDir = "data/images"
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
