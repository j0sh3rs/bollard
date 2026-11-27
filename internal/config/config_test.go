package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_MetricsAddrDefaultsToLoopback(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
	t.Setenv("METRICS_ADDR", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetricsAddr != "127.0.0.1:9090" {
		t.Fatalf("MetricsAddr = %q, want a loopback default", cfg.MetricsAddr)
	}
}

func TestLoad_APIKeyFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "key")
	multi := filepath.Join(dir, "multi")
	if err := os.WriteFile(good, []byte("  file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(multi, []byte("a\nb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, file string
		want            string
		wantErr         bool
	}{
		{"file only, trimmed", "", good, "file-key", false},
		{"env only", "env-key", "", "env-key", false},
		{"both set", "env-key", good, "", true},
		{"neither set", "", "", "", true},
		{"missing file", "", filepath.Join(dir, "absent"), "", true},
		{"multi-line file", "", multi, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNIFI_HOST", "https://unifi.local")
			t.Setenv("UNIFI_API_KEY", tc.key)
			t.Setenv("UNIFI_API_KEY_FILE", tc.file)
			cfg, err := config.Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && cfg.UnifiAPIKey != tc.want {
				t.Fatalf("key = %q, want %q", cfg.UnifiAPIKey, tc.want)
			}
		})
	}
}

func TestLoad_InstanceTagAndDrainValidation(t *testing.T) {
	for _, tc := range []struct {
		name, tag, drain string
		wantErr          bool
	}{
		{"default", "", "", false},
		{"valid tag", "nas-1.home", "", false},
		{"bad tag", "has space", "", true},
		{"long tag", strings.Repeat("a", 65), "", true},
		{"drain ok", "", "30s", false},
		{"drain too short", "", "500ms", true},
		{"drain negative", "", "-5s", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UNIFI_HOST", "https://unifi.local")
			t.Setenv("UNIFI_API_KEY", "k")
			t.Setenv("BOLLARD_INSTANCE_TAG", tc.tag)
			t.Setenv("STOP_DRAIN", tc.drain)
			if _, err := config.Load(); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
