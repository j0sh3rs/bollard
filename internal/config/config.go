package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds the application configuration loaded from environment variables.
// Note: UnifiSkipTLSVerify defaults to true for homelab use with self-signed certs.
// For production, set UNIFI_SKIP_TLS_VERIFY=false and provide UNIFI_CA_CERT if needed.
type Config struct {
	UnifiHost          string `env:"UNIFI_HOST,notEmpty"`
	UnifiAPIKey        string `env:"UNIFI_API_KEY"`
	UnifiAPIKeyFile    string `env:"UNIFI_API_KEY_FILE"`
	UnifiSite          string `env:"UNIFI_SITE"            envDefault:"default"`
	UnifiSkipTLSVerify bool   `env:"UNIFI_SKIP_TLS_VERIFY" envDefault:"true"`
	UnifiCACert        string `env:"UNIFI_CA_CERT"         envDefault:""`
	DatabaseURL        string `env:"DATABASE_URL"          envDefault:"file:bollard.db"`
	// InstanceTag scopes this instance's ownership rows, so several instances can share one
	// database and one controller without seeing or deleting each other's records.
	InstanceTag       string        `env:"BOLLARD_INSTANCE_TAG" envDefault:""`
	ReconcileInterval time.Duration `env:"RECONCILE_INTERVAL"   envDefault:"5m"`
	LogFormat         string        `env:"LOG_FORMAT"           envDefault:"logfmt"`
	LogLevel          string        `env:"LOG_LEVEL"            envDefault:"info"`
	// MetricsAddr defaults to loopback because /metrics and /healthz are unauthenticated.
	MetricsAddr string `env:"METRICS_ADDR" envDefault:"127.0.0.1:9090"`
	// DryRun logs the creates and deletes bollard would make without changing UniFi.
	DryRun bool `env:"DRY_RUN" envDefault:"false"`
	// StopDrain keeps a stopped container's record, with a short TTL, for this long before
	// deleting it, so a restart does not cause a resolution gap. Zero deletes immediately.
	StopDrain time.Duration `env:"STOP_DRAIN" envDefault:"0s"`
	// InferTraefik derives hostnames from Traefik router rule labels for containers without a
	// dns.bollard/hostname label.
	InferTraefik bool `env:"INFER_TRAEFIK_HOSTS" envDefault:"false"`
}

var instanceTagPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: parse env: %w", err)
	}
	if err := cfg.resolveAPIKey(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// resolveAPIKey loads the key from UNIFI_API_KEY_FILE (a Docker secret path such as
// /run/secrets/unifi_api_key) when set. Exactly one source may be used.
func (c *Config) resolveAPIKey() error {
	if c.UnifiAPIKeyFile != "" {
		if c.UnifiAPIKey != "" {
			return fmt.Errorf("set only one of UNIFI_API_KEY and UNIFI_API_KEY_FILE")
		}
		data, err := os.ReadFile(c.UnifiAPIKeyFile)
		if err != nil {
			return fmt.Errorf("read UNIFI_API_KEY_FILE: %w", err)
		}
		c.UnifiAPIKey = strings.TrimSpace(string(data))
	}
	if c.UnifiAPIKey == "" {
		return fmt.Errorf("a UniFi API key is required: set UNIFI_API_KEY or UNIFI_API_KEY_FILE")
	}
	if strings.ContainsAny(c.UnifiAPIKey, "\r\n") {
		return fmt.Errorf("the UniFi API key must be a single line")
	}
	return nil
}

func (c *Config) validate() error {
	switch c.LogFormat {
	case "logfmt", "json":
	default:
		return fmt.Errorf("invalid LOG_FORMAT %q: must be logfmt or json", c.LogFormat)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid LOG_LEVEL %q: must be debug, info, warn, or error", c.LogLevel)
	}
	if c.ReconcileInterval < time.Second {
		return fmt.Errorf("RECONCILE_INTERVAL must be >= 1s, got %v", c.ReconcileInterval)
	}
	if !instanceTagPattern.MatchString(c.InstanceTag) {
		return fmt.Errorf("invalid BOLLARD_INSTANCE_TAG %q: use up to 64 of A-Z a-z 0-9 . _ -", c.InstanceTag)
	}
	if c.StopDrain < 0 || (c.StopDrain > 0 && c.StopDrain < time.Second) {
		return fmt.Errorf("STOP_DRAIN must be 0 or >= 1s, got %v", c.StopDrain)
	}
	return nil
}
