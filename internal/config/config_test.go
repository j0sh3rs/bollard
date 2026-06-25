package config_test

import (
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/config"
)

func TestLoad_RequiredFieldsMissing(t *testing.T) {
	// t.Setenv with empty string ensures the env var is set but empty,
	// which triggers the notEmpty validation in the env struct tags.
	t.Setenv("UNIFI_HOST", "")
	t.Setenv("UNIFI_API_KEY", "")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when required fields are empty")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("UNIFI_SITE", "")
	t.Setenv("UNIFI_SKIP_TLS_VERIFY", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RECONCILE_INTERVAL", "")
	t.Setenv("LOG_FORMAT", "")
	t.Setenv("LOG_LEVEL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UnifiSite != "default" {
		t.Errorf("expected site=default, got %q", cfg.UnifiSite)
	}
	if cfg.ReconcileInterval != 5*time.Minute {
		t.Errorf("expected ReconcileInterval=5m, got %v", cfg.ReconcileInterval)
	}
	if cfg.LogFormat != "logfmt" {
		t.Errorf("expected LogFormat=logfmt, got %q", cfg.LogFormat)
	}
	if cfg.DatabaseURL != "file:bollard.db" {
		t.Errorf("expected DatabaseURL=file:bollard.db, got %q", cfg.DatabaseURL)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("expected LogLevel=info, got %q", cfg.LogLevel)
	}
	if !cfg.UnifiSkipTLSVerify {
		t.Errorf("expected UnifiSkipTLSVerify=true, got %v", cfg.UnifiSkipTLSVerify)
	}
}

func TestLoad_LogFormatValidation(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("LOG_FORMAT", "invalid")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid LOG_FORMAT")
	}
}

func TestLoad_LogLevelValidation(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("LOG_LEVEL", "invalid")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid LOG_LEVEL")
	}
}

func TestLoad_ReconcileIntervalValidation(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("RECONCILE_INTERVAL", "500ms")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for RECONCILE_INTERVAL < 1s")
	}
}
