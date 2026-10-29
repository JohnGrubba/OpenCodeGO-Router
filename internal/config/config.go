package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the complete runtime configuration. Everything is sourced from
// environment variables so the same binary works in Docker and bare metal.
type Config struct {
	Addr            string
	DataDir         string
	DatabasePath    string
	UpstreamBaseURL string
	APIKey          string
	AdminPassword   string
	AdminReset      bool
	PublicURL       string
	Plan            string
	InstanceName    string
	RetentionDays   int
	TrustProxy      bool
	MaxBodyBytes    int64
	SessionTTL      time.Duration
	SessionIdle     time.Duration
	ModelsRefresh   time.Duration
	LogFormat       string
	Version         string
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// Load reads configuration and applies safe defaults.
func Load(version string) (Config, error) {
	dataDir := env("DATA_DIR", "./data")
	dbPath := env("DATABASE_PATH", filepath.Join(dataDir, "router.db"))
	plan := strings.ToLower(env("PLAN", "go"))
	if plan != "go" && plan != "go-plus" {
		return Config{}, fmt.Errorf("PLAN must be go or go-plus")
	}

	cfg := Config{
		Addr:            env("ADDR", ":4000"),
		DataDir:         dataDir,
		DatabasePath:    dbPath,
		UpstreamBaseURL: strings.TrimRight(env("UPSTREAM_BASE_URL", "https://opencode.ai/inference"), "/"),
		APIKey:          strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")),
		AdminPassword:   os.Getenv("ADMIN_PASSWORD"),
		AdminReset:      envBool("ADMIN_PASSWORD_RESET", false),
		PublicURL:       strings.TrimRight(env("PUBLIC_URL", ""), "/"),
		Plan:            plan,
		InstanceName:    env("INSTANCE_NAME", "OpenCode Go Router"),
		RetentionDays:   envInt("RETENTION_DAYS", 180),
		TrustProxy:      envBool("TRUST_PROXY", false),
		MaxBodyBytes:    int64(envInt("MAX_BODY_MB", 64)) << 20,
		SessionTTL:      time.Duration(envInt("SESSION_TTL_HOURS", 168)) * time.Hour,
		SessionIdle:     time.Duration(envInt("SESSION_IDLE_HOURS", 24)) * time.Hour,
		ModelsRefresh:   time.Duration(envInt("MODELS_REFRESH_MINUTES", 15)) * time.Minute,
		LogFormat:       strings.ToLower(env("LOG_FORMAT", "text")),
		Version:         version,
	}
	if cfg.RetentionDays < 1 {
		cfg.RetentionDays = 1
	}
	if cfg.RetentionDays > 3650 {
		cfg.RetentionDays = 3650
	}
	if cfg.MaxBodyBytes < 1<<20 {
		cfg.MaxBodyBytes = 64 << 20
	}
	if cfg.SessionTTL < time.Hour {
		cfg.SessionTTL = 168 * time.Hour
	}
	if cfg.SessionTTL > 365*24*time.Hour {
		cfg.SessionTTL = 365 * 24 * time.Hour
	}
	if cfg.SessionIdle < time.Hour {
		cfg.SessionIdle = 24 * time.Hour
	}
	if cfg.SessionIdle > cfg.SessionTTL {
		cfg.SessionIdle = cfg.SessionTTL
	}
	if cfg.ModelsRefresh < time.Minute {
		cfg.ModelsRefresh = 15 * time.Minute
	}
	if !strings.HasPrefix(cfg.UpstreamBaseURL, "https://") && !strings.HasPrefix(cfg.UpstreamBaseURL, "http://") {
		return Config{}, fmt.Errorf("UPSTREAM_BASE_URL must start with http:// or https://")
	}
	if cfg.PublicURL != "" {
		u, err := url.Parse(cfg.PublicURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return Config{}, fmt.Errorf("PUBLIC_URL must be an absolute http(s) URL")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return Config{}, fmt.Errorf("PUBLIC_URL must use http or https")
		}
		if u.Path != "" && u.Path != "/" {
			return Config{}, fmt.Errorf("PUBLIC_URL must not contain a path prefix")
		}
	}
	return cfg, nil
}

// PublicBase returns the configured public URL or a fallback derived from a request.
func (c Config) PublicBase(scheme, host string) string {
	if c.PublicURL != "" {
		return c.PublicURL
	}
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + host
}
