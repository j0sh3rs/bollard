package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config holds the application configuration loaded from environment variables.
// Note: UnifiSkipTLSVerify defaults to true for homelab use with self-signed certs.
// For production, set UNIFI_SKIP_TLS_VERIFY=false and provide UNIFI_CA_CERT if needed.
type Config struct {
	UnifiHost          string        `env:"UNIFI_HOST,notEmpty"`
	UnifiAPIKey        string        `env:"UNIFI_API_KEY,notEmpty"`
	UnifiSite          string        `env:"UNIFI_SITE"            envDefault:"default"`
	UnifiSkipTLSVerify bool          `env:"UNIFI_SKIP_TLS_VERIFY" envDefault:"true"`
	UnifiCACert        string        `env:"UNIFI_CA_CERT"         envDefault:""`
	DatabaseURL        string        `env:"DATABASE_URL"          envDefault:"file:bollard.db"`
	ReconcileInterval  time.Duration `env:"RECONCILE_INTERVAL"    envDefault:"5m"`
	LogFormat          string        `env:"LOG_FORMAT"            envDefault:"logfmt"`
	LogLevel           string        `env:"LOG_LEVEL"             envDefault:"info"`
	MetricsAddr        string        `env:"METRICS_ADDR"          envDefault:":9090"`
}

func Load() (*Config, error) {
	cfg := &Config{}
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: parse env: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
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
	return nil
}
