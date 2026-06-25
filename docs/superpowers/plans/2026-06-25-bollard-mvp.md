# bollard MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Docker event-driven DNS controller that creates/deletes A records in a UniFi Network controller from container labels, with SQLite ownership tracking and a self-healing reconcile loop.

**Architecture:** Single Go binary. A Docker watcher feeds container events to a Reconciler, which owns all create/delete logic via a Store interface and a UniFi DNSProvider interface. A periodic ticker drives the reconcile loop. Config is 12-factor env-var driven.

**Tech Stack:** Go 1.23+, `github.com/docker/docker` SDK, `modernc.org/sqlite` (CGO-free SQLite), `github.com/golang-migrate/migrate/v4`, `github.com/lmittmann/tint` (logfmt), `log/slog` (structured logging), `github.com/caarlos0/env/v11` (config), `release-please` (releases).

## Global Constraints

- Go module: `github.com/j0sh3rs/bollard`
- Go version: `1.23`
- Label namespace: `dns.bollard/` — immutable, never rename
- Default reconcile interval: `5m` (env: `RECONCILE_INTERVAL`)
- Default log format: `logfmt` (env: `LOG_FORMAT`, values: `logfmt`, `json`)
- SQLite default state store DSN: `file:bollard.db` (env: `DATABASE_URL`)
- UniFi env vars: `UNIFI_HOST`, `UNIFI_API_KEY`, `UNIFI_SITE` (default `default`), `UNIFI_SKIP_TLS_VERIFY` (default `true`), `UNIFI_CA_CERT`
- MVP record type: `A` only — reject others with a logged warning, not a crash
- Ownership invariant: bollard NEVER deletes a UniFi record not in its state store
- `--adopt` flag is non-destructive; never add a destructive mode to it
- All commits use Conventional Commits format (`feat:`, `fix:`, `test:`, `chore:`, etc.)
- TDD: write failing test first, then implementation, then commit

---

## File Structure

```
bollard/
├── main.go                          # Entry point: parse flags, wire components, start
├── go.mod
├── go.sum
├── .release-please-manifest.json
├── release-please-config.json
├── .github/workflows/
│   └── release-please.yml
├── internal/
│   ├── config/
│   │   └── config.go                # Config struct, env parsing, validation
│   ├── unifi/
│   │   ├── types.go                 # DNSRecord, dnsPolicyEnvelope, apiPage, error types
│   │   ├── client.go                # httpClient: GetRecords, CreateRecord, DeleteRecord
│   │   ├── transport.go             # TLS setup, retry logic, backoff
│   │   └── provider.go              # DNSProvider interface + UnifiProvider impl
│   ├── store/
│   │   ├── store.go                 # Store interface
│   │   ├── sqlite.go                # SQLite implementation
│   │   └── migrations/
│   │       └── 001_init.sql         # Initial schema
│   ├── docker/
│   │   ├── watcher.go               # Docker event subscription, typed events
│   │   └── labels.go                # Label parsing → RecordSpec
│   ├── resolver/
│   │   └── ip.go                    # Host IP inference + override
│   └── reconciler/
│       └── reconciler.go            # Event handler + periodic reconcile loop + adopt
├── docker-compose.example.yml
└── README.md
```

---

## Task 1: Module scaffold + config

**Files:**
- Create: `go.mod`
- Create: `go.sum` (generated)
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Config` struct with fields: `UnifiHost string`, `UnifiAPIKey string`, `UnifiSite string`, `UnifiSkipTLSVerify bool`, `UnifiCACert string`, `DatabaseURL string`, `ReconcileInterval time.Duration`, `LogFormat string`, `LogLevel string`
- Produces: `config.Load() (*Config, error)` — reads env vars, validates, returns populated Config or error

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/josh.simmonds/Documents/github/j0sh3rs/bollard
go mod init github.com/j0sh3rs/bollard
```

Expected: `go.mod` created with `module github.com/j0sh3rs/bollard` and `go 1.23`

- [ ] **Step 2: Add dependencies**

```bash
go get github.com/caarlos0/env/v11
go get github.com/lmittmann/tint
```

- [ ] **Step 3: Write failing test**

Create `internal/config/config_test.go`:

```go
package config_test

import (
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/config"
)

func TestLoad_RequiredFieldsMissing(t *testing.T) {
	t.Setenv("UNIFI_HOST", "")
	t.Setenv("UNIFI_API_KEY", "")
	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when required fields are missing")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("UNIFI_HOST", "https://unifi.local")
	t.Setenv("UNIFI_API_KEY", "test-key")
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
```

- [ ] **Step 4: Run test to verify it fails**

```bash
cd /Users/josh.simmonds/Documents/github/j0sh3rs/bollard
go test ./internal/config/... 2>&1 | head -20
```

Expected: build error — package does not exist yet.

- [ ] **Step 5: Implement config**

Create `internal/config/config.go`:

```go
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
)

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
```

- [ ] **Step 6: Run tests to verify pass**

```bash
go test ./internal/config/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/config/
git commit -m "feat: add module scaffold and config package"
```

---

## Task 2: Logging setup

**Files:**
- Create: `internal/log/log.go`
- Test: `internal/log/log_test.go`

**Interfaces:**
- Consumes: `config.Config.LogFormat` (`"logfmt"` or `"json"`), `config.Config.LogLevel`
- Produces: `log.New(format, level string) (*slog.Logger, error)` — returns configured slog.Logger
- Produces: `log.NewWithWriter(format, level string, w io.Writer) (*slog.Logger, error)` — for testing

- [ ] **Step 1: Write failing test**

Create `internal/log/log_test.go`:

```go
package log_test

import (
	"bytes"
	"strings"
	"testing"

	bollardlog "github.com/j0sh3rs/bollard/internal/log"
)

func TestNew_LogfmtFormat(t *testing.T) {
	logger, err := bollardlog.New("logfmt", "info")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNew_JSONFormat(t *testing.T) {
	logger, err := bollardlog.New("json", "info")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNew_InvalidFormat(t *testing.T) {
	_, err := bollardlog.New("invalid", "info")
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestNew_JSONOutput(t *testing.T) {
	var buf bytes.Buffer
	logger, err := bollardlog.NewWithWriter("json", "info", &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	logger.Info("test message", "key", "value")
	out := buf.String()
	if !strings.Contains(out, `"msg":"test message"`) {
		t.Errorf("expected JSON output, got: %s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./internal/log/... 2>&1 | head -5
```

Expected: build error — package does not exist.

- [ ] **Step 3: Implement**

Create `internal/log/log.go`:

```go
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
)

func New(format, level string) (*slog.Logger, error) {
	return NewWithWriter(format, level, os.Stderr)
}

func NewWithWriter(format, level string, w io.Writer) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	var handler slog.Handler
	switch format {
	case "logfmt":
		handler = tint.NewHandler(w, &tint.Options{
			Level:      lvl,
			TimeFormat: time.RFC3339,
		})
	case "json":
		handler = slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})
	default:
		return nil, fmt.Errorf("invalid log format %q: must be logfmt or json", format)
	}
	return slog.New(handler), nil
}

func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q: must be debug, info, warn, or error", level)
	}
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/log/... -v
```

Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/log/
git commit -m "feat: add structured logging package (logfmt/json)"
```

---

## Task 3: State store interface + SQLite implementation

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/sqlite.go`
- Create: `internal/store/migrations/001_init.sql`
- Test: `internal/store/sqlite_test.go`

**Interfaces:**
- Produces: `store.Record` struct: `ID string`, `ContainerID string`, `Hostname string`, `IP string`, `RecordType string`, `TTL int`, `UnifiRecordID string`, `CreatedAt time.Time`, `UpdatedAt time.Time`
- Produces: `store.Store` interface:
  ```go
  type Store interface {
      Create(ctx context.Context, r Record) error
      Delete(ctx context.Context, id string) error
      DeleteByContainerID(ctx context.Context, containerID string) (*Record, error)
      GetByContainerID(ctx context.Context, containerID string) (*Record, error)
      ListAll(ctx context.Context) ([]Record, error)
      Close() error
  }
  ```
- Produces: `store.NewSQLite(dsn string) (Store, error)`

- [ ] **Step 1: Add dependencies**

```bash
go get modernc.org/sqlite
go get github.com/golang-migrate/migrate/v4
go get github.com/golang-migrate/migrate/v4/database/sqlite3
go get github.com/golang-migrate/migrate/v4/source/iofs
go get github.com/google/uuid
```

- [ ] **Step 2: Create migration file**

Create `internal/store/migrations/001_init.sql`:

```sql
CREATE TABLE IF NOT EXISTS records (
    id              TEXT PRIMARY KEY,
    container_id    TEXT NOT NULL UNIQUE,
    hostname        TEXT NOT NULL,
    ip              TEXT NOT NULL,
    record_type     TEXT NOT NULL,
    ttl             INTEGER NOT NULL,
    unifi_record_id TEXT NOT NULL,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_records_container_id ON records(container_id);
CREATE INDEX IF NOT EXISTS idx_records_hostname ON records(hostname);
```

- [ ] **Step 3: Write failing tests**

Create `internal/store/sqlite_test.go`:

```go
package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/store"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLite("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLite_CreateAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID:            "test-uuid-1",
		ContainerID:   "container-abc123",
		Hostname:      "myapp.home.arpa",
		IP:            "192.168.1.10",
		RecordType:    "A",
		TTL:           300,
		UnifiRecordID: "unifi-id-xyz",
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		UpdatedAt:     time.Now().UTC().Truncate(time.Second),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.GetByContainerID(ctx, "container-abc123")
	if err != nil {
		t.Fatalf("GetByContainerID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil record")
	}
	if got.Hostname != r.Hostname {
		t.Errorf("expected hostname %q, got %q", r.Hostname, got.Hostname)
	}
	if got.UnifiRecordID != r.UnifiRecordID {
		t.Errorf("expected unifi ID %q, got %q", r.UnifiRecordID, got.UnifiRecordID)
	}
}

func TestSQLite_DeleteByContainerID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "test-uuid-2", ContainerID: "container-del123",
		Hostname: "del.home.arpa", IP: "192.168.1.11", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-del",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deleted, err := s.DeleteByContainerID(ctx, "container-del123")
	if err != nil {
		t.Fatalf("DeleteByContainerID: %v", err)
	}
	if deleted == nil || deleted.UnifiRecordID != "unifi-del" {
		t.Errorf("expected deleted record unifi ID unifi-del, got %v", deleted)
	}
	got, err := s.GetByContainerID(ctx, "container-del123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestSQLite_ListAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for i, cid := range []string{"c1", "c2", "c3"} {
		if err := s.Create(ctx, store.Record{
			ID: fmt.Sprintf("uuid-%d", i), ContainerID: cid,
			Hostname: cid + ".home.arpa", IP: "192.168.1.1", RecordType: "A",
			TTL: 300, UnifiRecordID: "unifi-" + cid,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("Create %s: %v", cid, err)
		}
	}
	all, err := s.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 records, got %d", len(all))
	}
}

func TestSQLite_DuplicateContainerID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := store.Record{
		ID: "uuid-dup", ContainerID: "dup-container",
		Hostname: "dup.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-dup",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	r.ID = "uuid-dup2"
	if err := s.Create(ctx, r); err == nil {
		t.Fatal("expected error on duplicate container_id")
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

```bash
go test ./internal/store/... 2>&1 | head -10
```

Expected: build error — package does not exist.

- [ ] **Step 5: Implement store interface**

Create `internal/store/store.go`:

```go
package store

import (
	"context"
	"time"
)

type Record struct {
	ID            string
	ContainerID   string
	Hostname      string
	IP            string
	RecordType    string
	TTL           int
	UnifiRecordID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Store interface {
	Create(ctx context.Context, r Record) error
	Delete(ctx context.Context, id string) error
	DeleteByContainerID(ctx context.Context, containerID string) (*Record, error)
	GetByContainerID(ctx context.Context, containerID string) (*Record, error)
	ListAll(ctx context.Context) ([]Record, error)
	Close() error
}
```

- [ ] **Step 6: Implement SQLite backend**

Create `internal/store/sqlite.go`:

```go
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type sqliteStore struct {
	db *sql.DB
}

func NewSQLite(dsn string) (Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func runMigrations(db *sql.DB) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	driver, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "sqlite3", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func (s *sqliteStore) Create(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO records (id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ContainerID, r.Hostname, r.IP, r.RecordType, r.TTL, r.UnifiRecordID,
		r.CreatedAt.UTC().Format(time.RFC3339),
		r.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("store: create record: %w", err)
	}
	return nil
}

func (s *sqliteStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE id = ?`, id)
	return err
}

func (s *sqliteStore) DeleteByContainerID(ctx context.Context, containerID string) (*Record, error) {
	r, err := s.GetByContainerID(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	if err := s.Delete(ctx, r.ID); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *sqliteStore) GetByContainerID(ctx context.Context, containerID string) (*Record, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at
		 FROM records WHERE container_id = ?`, containerID)
	return scanRecord(row)
}

func (s *sqliteStore) ListAll(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, container_id, hostname, ip, record_type, ttl, unifi_record_id, created_at, updated_at FROM records`)
	if err != nil {
		return nil, fmt.Errorf("store: list all: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *r)
	}
	return records, rows.Err()
}

func (s *sqliteStore) Close() error {
	return s.db.Close()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(s scanner) (*Record, error) {
	var r Record
	var createdStr, updatedStr string
	err := s.Scan(
		&r.ID, &r.ContainerID, &r.Hostname, &r.IP, &r.RecordType,
		&r.TTL, &r.UnifiRecordID, &createdStr, &updatedStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: scan record: %w", err)
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return &r, nil
}
```

- [ ] **Step 7: Run tests to verify pass**

```bash
go test ./internal/store/... -v
```

Expected: all 4 tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/ go.mod go.sum
git commit -m "feat: add store interface and SQLite backend with migrations"
```

---

## Task 4: Label parser + IP resolver

**Files:**
- Create: `internal/docker/labels.go`
- Create: `internal/resolver/ip.go`
- Test: `internal/docker/labels_test.go`
- Test: `internal/resolver/ip_test.go`

**Interfaces:**
- Produces: `docker.RecordSpec` struct: `Hostname string`, `RecordType string`, `TTL int`, `IPOverride string`, `Enabled bool`
- Produces: `docker.ParseLabels(labels map[string]string) (*RecordSpec, error)` — nil, nil if `dns.bollard/hostname` absent
- Produces: `resolver.HostIP(override string) (string, error)`

- [ ] **Step 1: Write failing tests**

Create `internal/docker/labels_test.go`:

```go
package docker_test

import (
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
)

func TestParseLabels_OptIn(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec == nil {
		t.Fatal("expected non-nil spec")
	}
	if spec.Hostname != "myapp.home.arpa" {
		t.Errorf("expected hostname myapp.home.arpa, got %q", spec.Hostname)
	}
	if spec.RecordType != "A" {
		t.Errorf("expected default record type A, got %q", spec.RecordType)
	}
	if spec.TTL != 300 {
		t.Errorf("expected default TTL 300, got %d", spec.TTL)
	}
	if !spec.Enabled {
		t.Error("expected enabled=true by default")
	}
}

func TestParseLabels_OptOut(t *testing.T) {
	labels := map[string]string{"unrelated": "value"}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec != nil {
		t.Fatal("expected nil spec when hostname label absent")
	}
}

func TestParseLabels_Disabled(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/enabled":  "false",
	}
	spec, err := docker.ParseLabels(labels)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Enabled {
		t.Error("expected enabled=false")
	}
}

func TestParseLabels_InvalidTTL(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname": "myapp.home.arpa",
		"dns.bollard/ttl":      "notanumber",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for invalid TTL")
	}
}

func TestParseLabels_UnsupportedRecordType(t *testing.T) {
	labels := map[string]string{
		"dns.bollard/hostname":    "myapp.home.arpa",
		"dns.bollard/record-type": "CNAME",
	}
	_, err := docker.ParseLabels(labels)
	if err == nil {
		t.Fatal("expected error for unsupported record type")
	}
}
```

Create `internal/resolver/ip_test.go`:

```go
package resolver_test

import (
	"testing"

	"github.com/j0sh3rs/bollard/internal/resolver"
)

func TestHostIP_Override(t *testing.T) {
	ip, err := resolver.HostIP("10.0.0.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ip != "10.0.0.1" {
		t.Errorf("expected 10.0.0.1, got %q", ip)
	}
}

func TestHostIP_Inference(t *testing.T) {
	ip, err := resolver.HostIP("")
	if err != nil {
		t.Skipf("inference unavailable in this environment: %v", err)
	}
	if ip == "" {
		t.Fatal("expected non-empty IP from inference")
	}
	if ip == "127.0.0.1" || ip == "::1" {
		t.Errorf("expected non-loopback IP, got %q", ip)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/docker/... ./internal/resolver/... 2>&1 | head -10
```

Expected: build errors.

- [ ] **Step 3: Implement label parser**

Create `internal/docker/labels.go`:

```go
package docker

import (
	"fmt"
	"strconv"
)

const (
	labelHostname   = "dns.bollard/hostname"
	labelRecordType = "dns.bollard/record-type"
	labelTTL        = "dns.bollard/ttl"
	labelIPOverride = "dns.bollard/ip-override"
	labelEnabled    = "dns.bollard/enabled"

	defaultRecordType = "A"
	defaultTTL        = 300
)

// RecordSpec is the parsed, validated intent from container labels.
type RecordSpec struct {
	Hostname   string
	RecordType string
	TTL        int
	IPOverride string
	Enabled    bool
}

// ParseLabels returns nil, nil if the container is not opted in.
// Returns an error if labels are present but invalid.
func ParseLabels(labels map[string]string) (*RecordSpec, error) {
	hostname, ok := labels[labelHostname]
	if !ok || hostname == "" {
		return nil, nil
	}
	spec := &RecordSpec{
		Hostname:   hostname,
		RecordType: defaultRecordType,
		TTL:        defaultTTL,
		Enabled:    true,
	}
	if rt, ok := labels[labelRecordType]; ok {
		if rt != "A" {
			return nil, fmt.Errorf("label %s: unsupported record type %q (MVP supports A only)", labelRecordType, rt)
		}
		spec.RecordType = rt
	}
	if ttlStr, ok := labels[labelTTL]; ok {
		ttl, err := strconv.Atoi(ttlStr)
		if err != nil {
			return nil, fmt.Errorf("label %s: invalid TTL %q: %w", labelTTL, ttlStr, err)
		}
		if ttl < 0 {
			return nil, fmt.Errorf("label %s: TTL must be non-negative, got %d", labelTTL, ttl)
		}
		spec.TTL = ttl
	}
	if ip, ok := labels[labelIPOverride]; ok {
		spec.IPOverride = ip
	}
	if enabledStr, ok := labels[labelEnabled]; ok {
		switch enabledStr {
		case "true", "1", "yes":
			spec.Enabled = true
		case "false", "0", "no":
			spec.Enabled = false
		default:
			return nil, fmt.Errorf("label %s: invalid value %q (use true/false)", labelEnabled, enabledStr)
		}
	}
	return spec, nil
}
```

- [ ] **Step 4: Implement IP resolver**

Create `internal/resolver/ip.go`:

```go
package resolver

import (
	"fmt"
	"net"
)

// HostIP returns override if non-empty. Otherwise infers the host's
// primary non-loopback unicast IPv4 address. Returns an error if
// inference finds no suitable address or finds more than one routable
// candidate (use dns.bollard/ip-override in that case).
func HostIP(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return inferHostIP()
}

func inferHostIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("resolver: list interfaces: %w", err)
	}
	var candidates []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				candidates = append(candidates, ip4.String())
			}
		}
	}
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("resolver: no routable IPv4 address found; set dns.bollard/ip-override")
	case 1:
		return candidates[0], nil
	default:
		return "", fmt.Errorf("resolver: multiple routable IPv4 addresses found (%v); set dns.bollard/ip-override explicitly", candidates)
	}
}
```

- [ ] **Step 5: Run tests to verify pass**

```bash
go test ./internal/docker/... ./internal/resolver/... -v
```

Expected: all tests PASS (inference test skips if no network).

- [ ] **Step 6: Commit**

```bash
git add internal/docker/ internal/resolver/ go.mod go.sum
git commit -m "feat: add label parser and host IP resolver"
```

---

## Task 5: UniFi DNS provider

**Files:**
- Create: `internal/unifi/types.go`
- Create: `internal/unifi/transport.go`
- Create: `internal/unifi/provider.go`
- Test: `internal/unifi/provider_test.go`

**Interfaces:**
- Produces: `unifi.DNSRecord` struct: `ID string`, `Hostname string`, `IP string`, `RecordType string`, `TTL int`
- Produces: `unifi.Config` struct: `Host string`, `APIKey string`, `Site string`, `SkipTLSVerify bool`, `CACertPath string`, `RetryAttempts int`, `RetryInitialDelay time.Duration`, `RetryMaxDelay time.Duration`
- Produces: `unifi.DNSProvider` interface with `ListRecords`, `CreateRecord`, `DeleteRecord`
- Produces: `unifi.New(cfg *Config) (DNSProvider, error)` — probes for modern API, falls back to legacy

- [ ] **Step 1: Add Docker SDK dependency**

```bash
go get github.com/docker/docker@latest
```

- [ ] **Step 2: Write failing tests**

Create `internal/unifi/provider_test.go`:

```go
package unifi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/j0sh3rs/bollard/internal/unifi"
)

func newTestConfig(url string) *unifi.Config {
	return &unifi.Config{
		Host:          url,
		APIKey:        "test-key",
		Site:          "default",
		SkipTLSVerify: true,
	}
}

func TestNew_ModernAPIDetection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy/network/integration/v1/sites" {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestListRecords_Modern(t *testing.T) {
	ttl := 300
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case "/proxy/network/integration/v1/sites/site-uuid/dns/policies":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id": "rec-1", "type": "A_RECORD",
					"domain": "myapp.home.arpa", "ipv4Address": "192.168.1.10",
					"ttlSeconds": ttl, "enabled": true,
				}},
				"count": 1, "totalCount": 1,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	records, err := provider.ListRecords(context.Background())
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Hostname != "myapp.home.arpa" {
		t.Errorf("expected hostname myapp.home.arpa, got %q", records[0].Hostname)
	}
}

func TestCreateAndDeleteRecord_Modern(t *testing.T) {
	var created map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/proxy/network/integration/v1/sites":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "site-uuid", "name": "default"}},
			})
		case r.URL.Path == "/proxy/network/integration/v1/sites/site-uuid/dns/policies" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&created)
			created["id"] = "new-rec-id"
			json.NewEncoder(w).Encode(created)
		case r.URL.Path == "/proxy/network/integration/v1/sites/site-uuid/dns/policies/new-rec-id" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	provider, err := unifi.New(newTestConfig(srv.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id, err := provider.CreateRecord(context.Background(), unifi.DNSRecord{
		Hostname: "test.home.arpa", IP: "192.168.1.5", RecordType: "A", TTL: 300,
	})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if id != "new-rec-id" {
		t.Errorf("expected id new-rec-id, got %q", id)
	}
	if err := provider.DeleteRecord(context.Background(), id); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify fail**

```bash
go test ./internal/unifi/... 2>&1 | head -10
```

Expected: build error.

- [ ] **Step 4: Implement types**

Create `internal/unifi/types.go`:

```go
package unifi

import (
	"fmt"
	"time"
)

// DNSRecord is bollard's internal representation of a managed DNS record.
type DNSRecord struct {
	ID         string
	Hostname   string
	IP         string
	RecordType string
	TTL        int
}

// Config holds UniFi connection parameters.
type Config struct {
	Host              string
	APIKey            string
	Site              string
	SkipTLSVerify     bool
	CACertPath        string
	RetryAttempts     int
	RetryInitialDelay time.Duration
	RetryMaxDelay     time.Duration
}

func (c *Config) setDefaults() {
	if c.Site == "" {
		c.Site = "default"
	}
	if c.RetryAttempts == 0 {
		c.RetryAttempts = 3
	}
	if c.RetryInitialDelay == 0 {
		c.RetryInitialDelay = 500 * time.Millisecond
	}
	if c.RetryMaxDelay == 0 {
		c.RetryMaxDelay = 10 * time.Second
	}
}

// Modern API wire types.
type dnsPolicyEnvelope struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Domain      string `json:"domain,omitempty"`
	IPv4Address string `json:"ipv4Address,omitempty"`
	TTLSeconds  *int   `json:"ttlSeconds,omitempty"`
}

const policyTypeA = "A_RECORD"

type apiPage[T any] struct {
	Count      int `json:"count"`
	TotalCount int `json:"totalCount"`
	Data       []T `json:"data"`
}

// Legacy static-DNS wire types.
type staticDNSEntry struct {
	ID         string `json:"_id,omitempty"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	RecordType string `json:"record_type"`
	TTL        int    `json:"ttl,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type staticDNSResponse struct {
	Data []staticDNSEntry `json:"data"`
	Meta struct {
		RC string `json:"rc"`
	} `json:"meta"`
}

type apiErrorResponse struct {
	StatusCode int    `json:"statusCode"`
	StatusName string `json:"statusName"`
	Message    string `json:"message"`
}

func (e *apiErrorResponse) Error() string {
	return fmt.Sprintf("unifi api error %d (%s): %s", e.StatusCode, e.StatusName, e.Message)
}
```

- [ ] **Step 5: Implement transport**

Create `internal/unifi/transport.go`:

```go
package unifi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

const maxResponseBody = 25 * 1024 * 1024

type httpTransport struct {
	client *http.Client
	apiKey string
	cfg    *Config
}

func newHTTPTransport(cfg *Config) (*httpTransport, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.CACertPath != "" {
		pem, err := os.ReadFile(cfg.CACertPath)
		if err != nil {
			return nil, fmt.Errorf("unifi: read CA cert: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("unifi: no valid certs in %s", cfg.CACertPath)
		}
		tlsCfg.RootCAs = pool
	} else if cfg.SkipTLSVerify {
		tlsCfg.InsecureSkipVerify = true //nolint:gosec
	}
	return &httpTransport{
		client: &http.Client{Transport: &http.Transport{
			TLSClientConfig:     tlsCfg,
			MaxConnsPerHost:     10,
			MaxIdleConnsPerHost: 5,
		}},
		apiKey: cfg.APIKey,
		cfg:    cfg,
	}, nil
}

func (t *httpTransport) do(ctx context.Context, method, url string, body []byte) ([]byte, int, error) {
	var lastErr error
	for attempt := 0; attempt < t.cfg.RetryAttempts; attempt++ {
		if attempt > 0 {
			wait := t.backoff(attempt)
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			case <-time.After(wait):
			}
		}
		data, status, err := t.doOnce(ctx, method, url, body)
		if err == nil {
			return data, status, nil
		}
		lastErr = err
		if !t.shouldRetry(method, status, err) {
			return nil, status, err
		}
	}
	return nil, 0, lastErr
}

func (t *httpTransport) doOnce(ctx context.Context, method, url string, body []byte) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("unifi: new request: %w", err)
	}
	req.Header.Set("X-API-KEY", t.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("unifi: request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("unifi: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr apiErrorResponse
		if jsonErr := json.Unmarshal(data, &apiErr); jsonErr == nil && apiErr.Message != "" {
			return nil, resp.StatusCode, &apiErr
		}
		limit := 512
		if len(data) < limit {
			limit = len(data)
		}
		return nil, resp.StatusCode, fmt.Errorf("unifi: HTTP %d: %s", resp.StatusCode, strconv.Quote(string(data[:limit])))
	}
	return data, resp.StatusCode, nil
}

func (t *httpTransport) shouldRetry(method string, status int, _ error) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if method == http.MethodGet || method == http.MethodDelete {
		return status >= 500
	}
	return false
}

func (t *httpTransport) backoff(attempt int) time.Duration {
	base := t.cfg.RetryInitialDelay
	exp := time.Duration(math.Pow(2, float64(attempt-1))) * base
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	wait := exp + jitter
	if wait > t.cfg.RetryMaxDelay {
		return t.cfg.RetryMaxDelay
	}
	return wait
}
```

- [ ] **Step 6: Implement provider**

Create `internal/unifi/provider.go`:

```go
package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// DNSProvider is the interface bollard uses to manage UniFi DNS records.
type DNSProvider interface {
	ListRecords(ctx context.Context) ([]DNSRecord, error)
	CreateRecord(ctx context.Context, r DNSRecord) (string, error)
	DeleteRecord(ctx context.Context, unifiID string) error
}

// New probes the UniFi controller and returns the appropriate DNSProvider.
// Tries modern Integration API first; falls back to legacy static-DNS.
func New(cfg *Config) (DNSProvider, error) {
	cfg.setDefaults()
	t, err := newHTTPTransport(cfg)
	if err != nil {
		return nil, err
	}
	siteID, err := resolveSiteID(context.Background(), t, cfg)
	if err == nil {
		return &modernProvider{transport: t, cfg: cfg, siteID: siteID}, nil
	}
	return &legacyProvider{transport: t, cfg: cfg}, nil
}

func resolveSiteID(ctx context.Context, t *httpTransport, cfg *Config) (string, error) {
	url := fmt.Sprintf("%s/proxy/network/integration/v1/sites", cfg.Host)
	data, status, err := t.do(ctx, http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return "", fmt.Errorf("unifi: site probe failed (status=%d): %w", status, err)
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", fmt.Errorf("unifi: parse sites: %w", err)
	}
	for _, site := range resp.Data {
		if site.Name == cfg.Site || site.ID == cfg.Site {
			return site.ID, nil
		}
	}
	return "", fmt.Errorf("unifi: site %q not found", cfg.Site)
}

type modernProvider struct {
	transport *httpTransport
	cfg       *Config
	siteID    string
}

func (p *modernProvider) policiesURL() string {
	return fmt.Sprintf("%s/proxy/network/integration/v1/sites/%s/dns/policies", p.cfg.Host, p.siteID)
}

func (p *modernProvider) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	const pageLimit = 200
	var all []DNSRecord
	offset := 0
	for {
		url := fmt.Sprintf("%s?offset=%d&limit=%d", p.policiesURL(), offset, pageLimit)
		data, _, err := p.transport.do(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("unifi: list records: %w", err)
		}
		var page apiPage[dnsPolicyEnvelope]
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, fmt.Errorf("unifi: parse records: %w", err)
		}
		for _, env := range page.Data {
			if !env.Enabled || env.Type != policyTypeA {
				continue
			}
			ttl := 0
			if env.TTLSeconds != nil {
				ttl = *env.TTLSeconds
			}
			all = append(all, DNSRecord{
				ID: env.ID, Hostname: env.Domain, IP: env.IPv4Address,
				RecordType: "A", TTL: ttl,
			})
		}
		if offset+page.Count >= page.TotalCount {
			break
		}
		offset += page.Count
	}
	return all, nil
}

func (p *modernProvider) CreateRecord(ctx context.Context, r DNSRecord) (string, error) {
	ttl := r.TTL
	env := dnsPolicyEnvelope{
		Type: policyTypeA, Enabled: true,
		Domain: r.Hostname, IPv4Address: r.IP, TTLSeconds: &ttl,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("unifi: marshal record: %w", err)
	}
	data, _, err := p.transport.do(ctx, http.MethodPost, p.policiesURL(), body)
	if err != nil {
		return "", fmt.Errorf("unifi: create record: %w", err)
	}
	var created dnsPolicyEnvelope
	if err := json.Unmarshal(data, &created); err != nil {
		return "", fmt.Errorf("unifi: parse create response: %w", err)
	}
	return created.ID, nil
}

func (p *modernProvider) DeleteRecord(ctx context.Context, unifiID string) error {
	url := fmt.Sprintf("%s/%s", p.policiesURL(), unifiID)
	_, status, err := p.transport.do(ctx, http.MethodDelete, url, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("unifi: delete record %s: %w", unifiID, err)
	}
	return nil
}

type legacyProvider struct {
	transport *httpTransport
	cfg       *Config
}

func (p *legacyProvider) baseURL() string {
	return fmt.Sprintf("%s/proxy/network/v2/api/site/%s/static-dns", p.cfg.Host, p.cfg.Site)
}

func (p *legacyProvider) ListRecords(ctx context.Context) ([]DNSRecord, error) {
	data, _, err := p.transport.do(ctx, http.MethodGet, p.baseURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("unifi: legacy list: %w", err)
	}
	var resp staticDNSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unifi: parse legacy records: %w", err)
	}
	var records []DNSRecord
	for _, e := range resp.Data {
		if !e.Enabled || e.RecordType != "A" {
			continue
		}
		records = append(records, DNSRecord{
			ID: e.ID, Hostname: e.Key, IP: e.Value, RecordType: "A", TTL: e.TTL,
		})
	}
	return records, nil
}

func (p *legacyProvider) CreateRecord(ctx context.Context, r DNSRecord) (string, error) {
	body, err := json.Marshal(staticDNSEntry{
		Key: r.Hostname, Value: r.IP, RecordType: "A", TTL: r.TTL, Enabled: true,
	})
	if err != nil {
		return "", fmt.Errorf("unifi: marshal legacy record: %w", err)
	}
	data, _, err := p.transport.do(ctx, http.MethodPost, p.baseURL(), body)
	if err != nil {
		return "", fmt.Errorf("unifi: legacy create: %w", err)
	}
	var resp staticDNSResponse
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Data) == 0 {
		return "", fmt.Errorf("unifi: parse legacy create response")
	}
	return resp.Data[0].ID, nil
}

func (p *legacyProvider) DeleteRecord(ctx context.Context, unifiID string) error {
	url := fmt.Sprintf("%s/%s", p.baseURL(), unifiID)
	_, status, err := p.transport.do(ctx, http.MethodDelete, url, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("unifi: legacy delete %s: %w", unifiID, err)
	}
	return nil
}
```

- [ ] **Step 7: Run tests to verify pass**

```bash
go test ./internal/unifi/... -v
```

Expected: all tests PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/unifi/ go.mod go.sum
git commit -m "feat: add UniFi DNS provider (modern + legacy API)"
```

---

## Task 6: Docker watcher

**Files:**
- Create: `internal/docker/watcher.go`
- Test: `internal/docker/watcher_test.go`

**Interfaces:**
- Produces: `docker.Event` struct: `Type string` (`"start"` or `"stop"`), `ContainerID string`, `Labels map[string]string`
- Produces: `docker.Watcher` struct
- Produces: `docker.NewWatcher() (*Watcher, error)`
- Produces: `(*Watcher).Watch(ctx context.Context) (<-chan Event, <-chan error)`
- Produces: `(*Watcher).Close() error`

- [ ] **Step 1: Write failing test**

Create `internal/docker/watcher_test.go`:

```go
package docker_test

import (
	"testing"

	"github.com/j0sh3rs/bollard/internal/docker"
)

func TestNewWatcher_ConnectsToSocket(t *testing.T) {
	w, err := docker.NewWatcher()
	if err != nil {
		t.Skipf("Docker socket unavailable: %v", err)
	}
	if w == nil {
		t.Fatal("expected non-nil watcher")
	}
	w.Close()
}
```

- [ ] **Step 2: Run test to verify fail**

```bash
go test ./internal/docker/... -run TestNewWatcher 2>&1 | head -5
```

Expected: build error.

- [ ] **Step 3: Implement watcher**

Create `internal/docker/watcher.go`:

```go
package docker

import (
	"context"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	dockerclient "github.com/docker/docker/client"
)

// Event is a simplified Docker container lifecycle event.
type Event struct {
	Type        string // "start" or "stop"
	ContainerID string
	Labels      map[string]string
}

// Watcher subscribes to Docker container events.
type Watcher struct {
	client *dockerclient.Client
}

// NewWatcher creates a Watcher connected to the default Docker socket.
func NewWatcher() (*Watcher, error) {
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &Watcher{client: c}, nil
}

// Watch returns a channel of Events and a channel of errors.
// Closes when ctx is cancelled.
func (w *Watcher) Watch(ctx context.Context) (<-chan Event, <-chan error) {
	eventCh := make(chan Event, 64)
	errCh := make(chan error, 1)

	f := filters.NewArgs()
	f.Add("type", "container")
	f.Add("event", "start")
	f.Add("event", "die")
	f.Add("event", "destroy")

	msgCh, dockerErrCh := w.client.Events(ctx, events.ListOptions{Filters: f})

	go func() {
		defer close(eventCh)
		defer close(errCh)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-dockerErrCh:
				if !ok {
					return
				}
				errCh <- err
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				e := Event{ContainerID: msg.Actor.ID, Labels: msg.Actor.Attributes}
				switch msg.Action {
				case "start":
					e.Type = "start"
				case "die", "destroy":
					e.Type = "stop"
				default:
					continue
				}
				select {
				case eventCh <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return eventCh, errCh
}

// Close shuts down the Docker client.
func (w *Watcher) Close() error {
	return w.client.Close()
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/docker/... -v
```

Expected: PASS or SKIP (Docker socket required).

- [ ] **Step 5: Commit**

```bash
git add internal/docker/ go.mod go.sum
git commit -m "feat: add Docker event watcher"
```

---

## Task 7: Reconciler

**Files:**
- Create: `internal/reconciler/reconciler.go`
- Test: `internal/reconciler/reconciler_test.go`

**Interfaces:**
- Consumes: `store.Store`, `unifi.DNSProvider`, `docker.ParseLabels`, `resolver.HostIP`
- Produces: `reconciler.Reconciler` struct
- Produces: `reconciler.New(s store.Store, p unifi.DNSProvider, hostIP string, log *slog.Logger) *Reconciler`
- Produces: `(*Reconciler).HandleEvent(ctx context.Context, e docker.Event) error`
- Produces: `(*Reconciler).Reconcile(ctx context.Context) error`
- Produces: `(*Reconciler).Adopt(ctx context.Context, running map[string]map[string]string) error`

- [ ] **Step 1: Write failing tests**

Create `internal/reconciler/reconciler_test.go`:

```go
package reconciler_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

type fakeStore struct{ records map[string]store.Record }

func newFakeStore() *fakeStore { return &fakeStore{records: map[string]store.Record{}} }

func (f *fakeStore) Create(_ context.Context, r store.Record) error {
	if _, exists := f.records[r.ContainerID]; exists {
		return fmt.Errorf("duplicate container_id %s", r.ContainerID)
	}
	f.records[r.ContainerID] = r
	return nil
}
func (f *fakeStore) Delete(_ context.Context, id string) error {
	for k, r := range f.records {
		if r.ID == id {
			delete(f.records, k)
		}
	}
	return nil
}
func (f *fakeStore) DeleteByContainerID(_ context.Context, cid string) (*store.Record, error) {
	r, ok := f.records[cid]
	if !ok {
		return nil, nil
	}
	delete(f.records, cid)
	return &r, nil
}
func (f *fakeStore) GetByContainerID(_ context.Context, cid string) (*store.Record, error) {
	r, ok := f.records[cid]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (f *fakeStore) ListAll(_ context.Context) ([]store.Record, error) {
	var out []store.Record
	for _, r := range f.records {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeStore) Close() error { return nil }

type fakeProvider struct {
	records map[string]unifi.DNSRecord
	nextID  int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{records: map[string]unifi.DNSRecord{}}
}
func (f *fakeProvider) ListRecords(_ context.Context) ([]unifi.DNSRecord, error) {
	var out []unifi.DNSRecord
	for _, r := range f.records {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeProvider) CreateRecord(_ context.Context, r unifi.DNSRecord) (string, error) {
	f.nextID++
	id := fmt.Sprintf("unifi-%d", f.nextID)
	r.ID = id
	f.records[id] = r
	return id, nil
}
func (f *fakeProvider) DeleteRecord(_ context.Context, id string) error {
	delete(f.records, id)
	return nil
}

func TestHandleEvent_Start(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	err := r.HandleEvent(context.Background(), docker.Event{
		Type:        "start",
		ContainerID: "container-123",
		Labels:      map[string]string{"dns.bollard/hostname": "myapp.home.arpa"},
	})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	rec, err := s.GetByContainerID(context.Background(), "container-123")
	if err != nil || rec == nil {
		t.Fatal("expected record in store after start event")
	}
	if len(p.records) != 1 {
		t.Fatalf("expected 1 unifi record, got %d", len(p.records))
	}
}

func TestHandleEvent_Stop(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	_ = s.Create(context.Background(), store.Record{
		ID: "rec-1", ContainerID: "container-456",
		Hostname: "old.home.arpa", IP: "192.168.1.1", RecordType: "A",
		TTL: 300, UnifiRecordID: "unifi-old",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	})
	p.records["unifi-old"] = unifi.DNSRecord{
		ID: "unifi-old", Hostname: "old.home.arpa", IP: "192.168.1.1",
	}

	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "stop", ContainerID: "container-456",
	})
	if err != nil {
		t.Fatalf("HandleEvent stop: %v", err)
	}
	rec, _ := s.GetByContainerID(context.Background(), "container-456")
	if rec != nil {
		t.Error("expected record removed from store after stop")
	}
	if len(p.records) != 0 {
		t.Error("expected unifi record deleted after stop")
	}
}

func TestHandleEvent_DuplicateHostname(t *testing.T) {
	s := newFakeStore()
	p := newFakeProvider()
	r := reconciler.New(s, p, "192.168.1.1", slog.Default())

	_ = r.HandleEvent(context.Background(), docker.Event{
		Type: "start", ContainerID: "c1",
		Labels: map[string]string{"dns.bollard/hostname": "shared.home.arpa"},
	})
	err := r.HandleEvent(context.Background(), docker.Event{
		Type: "start", ContainerID: "c2",
		Labels: map[string]string{"dns.bollard/hostname": "shared.home.arpa"},
	})
	if err == nil {
		t.Fatal("expected error on duplicate hostname")
	}
	if len(p.records) != 1 {
		t.Errorf("expected exactly 1 unifi record, got %d", len(p.records))
	}
}
```

- [ ] **Step 2: Run test to verify fail**

```bash
go test ./internal/reconciler/... 2>&1 | head -10
```

Expected: build error.

- [ ] **Step 3: Implement reconciler**

Create `internal/reconciler/reconciler.go`:

```go
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/j0sh3rs/bollard/internal/docker"
	"github.com/j0sh3rs/bollard/internal/resolver"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

// Reconciler orchestrates DNS record lifecycle.
type Reconciler struct {
	store    store.Store
	provider unifi.DNSProvider
	hostIP   string
	log      *slog.Logger
}

// New creates a Reconciler. hostIP may be empty (inferred on first use).
func New(s store.Store, p unifi.DNSProvider, hostIP string, log *slog.Logger) *Reconciler {
	return &Reconciler{store: s, provider: p, hostIP: hostIP, log: log}
}

func (r *Reconciler) resolvedIP(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if r.hostIP != "" {
		return r.hostIP, nil
	}
	ip, err := resolver.HostIP("")
	if err != nil {
		return "", err
	}
	r.hostIP = ip
	return ip, nil
}

// HandleEvent processes a single Docker container event.
func (r *Reconciler) HandleEvent(ctx context.Context, e docker.Event) error {
	switch e.Type {
	case "start":
		return r.handleStart(ctx, e)
	case "stop":
		return r.handleStop(ctx, e)
	default:
		return nil
	}
}

func (r *Reconciler) handleStart(ctx context.Context, e docker.Event) error {
	spec, err := docker.ParseLabels(e.Labels)
	if err != nil {
		r.log.Warn("invalid labels on container", "container", e.ContainerID, "err", err)
		return nil
	}
	if spec == nil || !spec.Enabled {
		return nil
	}

	all, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list store: %w", err)
	}
	for _, rec := range all {
		if rec.Hostname == spec.Hostname {
			return fmt.Errorf("reconciler: hostname %q already owned by container %s", spec.Hostname, rec.ContainerID)
		}
	}

	ip, err := r.resolvedIP(spec.IPOverride)
	if err != nil {
		return fmt.Errorf("reconciler: resolve IP: %w", err)
	}

	unifiID, err := r.provider.CreateRecord(ctx, unifi.DNSRecord{
		Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType, TTL: spec.TTL,
	})
	if err != nil {
		return fmt.Errorf("reconciler: create unifi record: %w", err)
	}

	now := time.Now().UTC()
	if err := r.store.Create(ctx, store.Record{
		ID:            uuid.New().String(),
		ContainerID:   e.ContainerID,
		Hostname:      spec.Hostname,
		IP:            ip,
		RecordType:    spec.RecordType,
		TTL:           spec.TTL,
		UnifiRecordID: unifiID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}); err != nil {
		_ = r.provider.DeleteRecord(ctx, unifiID)
		return fmt.Errorf("reconciler: write store: %w", err)
	}

	r.log.Info("created DNS record", "hostname", spec.Hostname, "ip", ip, "container", e.ContainerID)
	return nil
}

func (r *Reconciler) handleStop(ctx context.Context, e docker.Event) error {
	rec, err := r.store.DeleteByContainerID(ctx, e.ContainerID)
	if err != nil {
		return fmt.Errorf("reconciler: delete from store: %w", err)
	}
	if rec == nil {
		return nil
	}
	if err := r.provider.DeleteRecord(ctx, rec.UnifiRecordID); err != nil {
		r.log.Error("failed to delete unifi record", "unifi_id", rec.UnifiRecordID, "err", err)
		return err
	}
	r.log.Info("deleted DNS record", "hostname", rec.Hostname, "container", e.ContainerID)
	return nil
}

// Reconcile performs one full reconcile tick.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	storeRecords, err := r.store.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list store: %w", err)
	}
	unifiRecords, err := r.provider.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("reconciler: list unifi records: %w", err)
	}
	unifiIndex := map[string]struct{}{}
	for _, ur := range unifiRecords {
		unifiIndex[ur.ID] = struct{}{}
	}
	for _, sr := range storeRecords {
		if _, exists := unifiIndex[sr.UnifiRecordID]; !exists {
			r.log.Warn("orphaned store record, cleaning up",
				"hostname", sr.Hostname, "unifi_id", sr.UnifiRecordID)
			_ = r.store.Delete(ctx, sr.ID)
		}
	}
	return nil
}

// Adopt reclaims ownership of existing UniFi records for running containers.
// running is a map of containerID → labels. Non-destructive.
func (r *Reconciler) Adopt(ctx context.Context, running map[string]map[string]string) error {
	unifiRecords, err := r.provider.ListRecords(ctx)
	if err != nil {
		return fmt.Errorf("adopt: list unifi records: %w", err)
	}
	type key struct{ hostname, ip string }
	unifiByKey := map[key]unifi.DNSRecord{}
	for _, ur := range unifiRecords {
		unifiByKey[key{ur.Hostname, ur.IP}] = ur
	}
	for containerID, labels := range running {
		spec, err := docker.ParseLabels(labels)
		if err != nil || spec == nil || !spec.Enabled {
			continue
		}
		ip, err := r.resolvedIP(spec.IPOverride)
		if err != nil {
			r.log.Warn("adopt: cannot resolve IP", "container", containerID, "err", err)
			continue
		}
		existing, _ := r.store.GetByContainerID(ctx, containerID)
		if existing != nil {
			continue
		}
		k := key{spec.Hostname, ip}
		unifiRec, found := unifiByKey[k]
		now := time.Now().UTC()
		if found {
			if err := r.store.Create(ctx, store.Record{
				ID: uuid.New().String(), ContainerID: containerID,
				Hostname: spec.Hostname, IP: ip, RecordType: spec.RecordType,
				TTL: spec.TTL, UnifiRecordID: unifiRec.ID,
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				r.log.Error("adopt: store create failed", "container", containerID, "err", err)
				continue
			}
			r.log.Info("adopted existing unifi record", "hostname", spec.Hostname, "container", containerID)
		} else {
			if err := r.handleStart(ctx, docker.Event{
				Type: "start", ContainerID: containerID, Labels: labels,
			}); err != nil {
				r.log.Error("adopt: create record failed", "container", containerID, "err", err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify pass**

```bash
go test ./internal/reconciler/... -v
```

Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/reconciler/ go.mod go.sum
git commit -m "feat: add reconciler with event handling, reconcile loop, and adopt"
```

---

## Task 8: Main entrypoint + wiring

**Files:**
- Create: `main.go`

**Interfaces:**
- Consumes: all internal packages above
- Produces: runnable binary with `--adopt` flag, graceful shutdown on SIGTERM/SIGINT

- [ ] **Step 1: Implement main**

Create `main.go`:

```go
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/j0sh3rs/bollard/internal/config"
	dockerwatcher "github.com/j0sh3rs/bollard/internal/docker"
	bollardlog "github.com/j0sh3rs/bollard/internal/log"
	"github.com/j0sh3rs/bollard/internal/reconciler"
	"github.com/j0sh3rs/bollard/internal/store"
	"github.com/j0sh3rs/bollard/internal/unifi"
)

func main() {
	adopt := flag.Bool("adopt", false, "adopt existing UniFi records before starting normal operation")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger, err := bollardlog.New(cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		slog.Error("logger init error", "err", err)
		os.Exit(1)
	}
	slog.SetDefault(logger)

	db, err := store.NewSQLite(cfg.DatabaseURL)
	if err != nil {
		logger.Error("state store unavailable", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	provider, err := unifi.New(&unifi.Config{
		Host:          cfg.UnifiHost,
		APIKey:        cfg.UnifiAPIKey,
		Site:          cfg.UnifiSite,
		SkipTLSVerify: cfg.UnifiSkipTLSVerify,
		CACertPath:    cfg.UnifiCACert,
	})
	if err != nil {
		logger.Error("unifi client init error", "err", err)
		os.Exit(1)
	}

	watcher, err := dockerwatcher.NewWatcher()
	if err != nil {
		logger.Error("docker socket unavailable", "err", err)
		os.Exit(1)
	}
	defer watcher.Close()

	rec := reconciler.New(db, provider, "", logger)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if *adopt {
		logger.Info("starting adopt phase")
		running, err := listRunningContainers(ctx)
		if err != nil {
			logger.Error("adopt: list containers failed", "err", err)
			os.Exit(1)
		}
		if err := rec.Adopt(ctx, running); err != nil {
			logger.Error("adopt phase failed", "err", err)
			os.Exit(1)
		}
		logger.Info("adopt phase complete, starting normal operation")
	}

	eventCh, errCh := watcher.Watch(ctx)
	ticker := time.NewTicker(cfg.ReconcileInterval)
	defer ticker.Stop()

	logger.Info("bollard started", "reconcile_interval", cfg.ReconcileInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case err := <-errCh:
			if err != nil {
				logger.Error("docker event stream error", "err", err)
			}
			return
		case e, ok := <-eventCh:
			if !ok {
				return
			}
			if err := rec.HandleEvent(ctx, e); err != nil {
				logger.Error("handle event failed", "container", e.ContainerID, "err", err)
			}
		case <-ticker.C:
			if err := rec.Reconcile(ctx); err != nil {
				logger.Error("reconcile failed", "err", err)
			}
		}
	}
}

func listRunningContainers(ctx context.Context) (map[string]map[string]string, error) {
	c, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	containers, err := c.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]string, len(containers))
	for _, ctr := range containers {
		result[ctr.ID] = ctr.Labels
	}
	return result, nil
}
```

- [ ] **Step 2: Verify it builds**

```bash
go build ./...
```

Expected: binary produced, no errors.

- [ ] **Step 3: Run all tests**

```bash
go test ./... 2>&1
```

Expected: all packages PASS.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: add main entrypoint with adopt flag and graceful shutdown"
```

---

## Task 9: Docker compose example + README

**Files:**
- Create: `docker-compose.example.yml`
- Create: `README.md`

- [ ] **Step 1: Write compose example**

Create `docker-compose.example.yml`:

```yaml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    environment:
      UNIFI_HOST: https://unifi.home.arpa
      UNIFI_API_KEY: ${UNIFI_API_KEY}
      UNIFI_SITE: default
      UNIFI_SKIP_TLS_VERIFY: "true"
      DATABASE_URL: file:/data/bollard.db
      RECONCILE_INTERVAL: 5m
      LOG_FORMAT: logfmt
      LOG_LEVEL: info

volumes:
  bollard-data:
```

- [ ] **Step 2: Write README.md**

Create `README.md`:

````markdown
# bollard

Docker label-driven DNS controller for UniFi Network controllers. Watches Docker container events and creates/deletes A records automatically — no manual static-DNS editing required.

## How it works

bollard subscribes to the Docker event stream. When a container with a `dns.bollard/hostname` label starts, bollard creates a matching A record in your UniFi controller. When the container stops, the record is deleted. A periodic reconcile loop self-heals missed events. Ownership is tracked in a local SQLite database so bollard never modifies records it did not create.

## Label reference

| Label | Required | Default | Description |
|---|---|---|---|
| `dns.bollard/hostname` | Yes | — | FQDN for the DNS record. Opts the container in. |
| `dns.bollard/record-type` | No | `A` | Record type. A only in current release. |
| `dns.bollard/ttl` | No | `300` | TTL in seconds. |
| `dns.bollard/ip-override` | No | Host IP | Override the inferred NAS host IP. |
| `dns.bollard/enabled` | No | `true` | Set to `false` to suppress DNS management without removing labels. |

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `UNIFI_HOST` | required | UniFi controller URL, e.g. `https://unifi.home.arpa` |
| `UNIFI_API_KEY` | required | API key for a dedicated local UniFi account (see below) |
| `UNIFI_SITE` | `default` | UniFi site name |
| `UNIFI_SKIP_TLS_VERIFY` | `true` | Skip TLS verification |
| `UNIFI_CA_CERT` | — | Path to custom CA certificate PEM file |
| `DATABASE_URL` | `file:bollard.db` | SQLite DSN. Use an absolute path in production. |
| `RECONCILE_INTERVAL` | `5m` | How often the reconcile loop runs |
| `LOG_FORMAT` | `logfmt` | `logfmt` or `json` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

## Quickstart

```yaml
# docker-compose.yml
services:
  bollard:
    image: ghcr.io/j0sh3rs/bollard:latest
    restart: unless-stopped
    network_mode: host
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - bollard-data:/data
    environment:
      UNIFI_HOST: https://unifi.home.arpa
      UNIFI_API_KEY: ${UNIFI_API_KEY}
      DATABASE_URL: file:/data/bollard.db

volumes:
  bollard-data:
```

Label a container:

```yaml
services:
  myapp:
    image: myapp:latest
    labels:
      dns.bollard/hostname: myapp.home.arpa
```

## UniFi credential setup

> **Security caveat:** UniFi does not offer a DNS-only role. bollard requires a local account with the **Network Admin** role. Create a dedicated local account (e.g. `bollard`) rather than using your primary admin credential. Do not use a Ubiquiti SSO account.

1. UniFi Network → Settings → Admins & Users → Add local admin
2. Role: Network Admin
3. Generate an API key in the account settings
4. Set `UNIFI_API_KEY` in your compose environment or `.env` file

## Recovering after state loss (`--adopt`)

If the bollard database is lost but containers are still running:

```yaml
services:
  bollard:
    command: ["--adopt"]
    # rest of config unchanged
```

bollard scans running containers, matches existing UniFi records by hostname + IP, and reclaims ownership. After adopt completes it transitions to normal operation automatically. `--adopt` never deletes records.

## Failure modes

| Failure | Behavior |
|---|---|
| UniFi unreachable at startup | Retries with exponential backoff. Does not crash. |
| UniFi write fails | Logged. Reconcile loop retries on next tick. |
| Container dies without a stop event | Reconcile loop cleans up the orphaned record within one interval. |
| State database unavailable | Fatal — bollard exits. Fix the `DATABASE_URL` and restart. |
| Docker socket unavailable | Fatal — bollard exits. |

## Known limitations

- A records only. CNAME and other types are planned post-MVP.
- Duplicate hostnames across two containers are not supported. The second container is left unregistered with a logged error.
- Record value is the NAS host IP (host networking). Use `dns.bollard/ip-override` for other values.
````

- [ ] **Step 3: Commit**

```bash
git add docker-compose.example.yml README.md
git commit -m "docs: add README and docker compose example"
```

---

## Task 10: release-please setup

**Files:**
- Create: `.release-please-manifest.json`
- Create: `release-please-config.json`
- Create: `.github/workflows/release-please.yml`

- [ ] **Step 1: Create release-please config files**

Create `release-please-config.json`:

```json
{
  "$schema": "https://raw.githubusercontent.com/googleapis/release-please/main/schemas/config.json",
  "release-type": "go",
  "packages": {
    ".": {
      "release-type": "go",
      "changelog-sections": [
        {"type": "feat", "section": "Features"},
        {"type": "fix", "section": "Bug Fixes"},
        {"type": "perf", "section": "Performance Improvements"},
        {"type": "deps", "section": "Dependencies"},
        {"type": "docs", "section": "Documentation"}
      ]
    }
  }
}
```

Create `.release-please-manifest.json`:

```json
{
  ".": "0.1.0"
}
```

- [ ] **Step 2: Create GitHub Actions workflow**

```bash
mkdir -p .github/workflows
```

Create `.github/workflows/release-please.yml`:

```yaml
name: release-please
on:
  push:
    branches:
      - main

permissions:
  contents: write
  pull-requests: write

jobs:
  release-please:
    runs-on: ubuntu-latest
    steps:
      - uses: googleapis/release-please-action@v4
        id: release
        with:
          config-file: release-please-config.json
          manifest-file: .release-please-manifest.json

      - uses: actions/checkout@v4
        if: ${{ steps.release.outputs.release_created }}

      - uses: actions/setup-go@v5
        if: ${{ steps.release.outputs.release_created }}
        with:
          go-version-file: go.mod

      - name: Build release binaries
        if: ${{ steps.release.outputs.release_created }}
        run: |
          GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bollard-linux-amd64 .
          GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bollard-linux-arm64 .

      - name: Upload release artifacts
        if: ${{ steps.release.outputs.release_created }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          gh release upload ${{ steps.release.outputs.tag_name }} \
            bollard-linux-amd64 \
            bollard-linux-arm64
```

- [ ] **Step 3: Commit**

```bash
git add .release-please-manifest.json release-please-config.json .github/
git commit -m "chore: add release-please configuration"
```

---

## Self-Review Against Spec

| Spec requirement | Task |
|---|---|
| Watch Docker events (start/die/destroy) | Task 6 |
| Modern + legacy UniFi API, auto-detect | Task 5 |
| Label-driven opt-in | Task 4 |
| SQLite ownership tracking | Task 3 |
| Store interface for future Postgres | Task 3 |
| Reconcile loop, configurable interval | Task 7 + Task 8 |
| `--adopt` flag, non-destructive | Task 7 + Task 8 |
| Exponential backoff on UniFi unreachable | Task 5 (transport) |
| All config via env vars | Task 1 |
| logfmt + JSON logging | Task 2 |
| Host IP inference, ip-override label | Task 4 |
| Duplicate hostname rejection | Task 7 |
| Fatal on state store unavailable | Task 8 |
| Fatal on Docker socket unavailable | Task 8 |
| README with install, labels, UniFi setup, failure modes | Task 9 |
| release-please semantic versioning | Task 10 |
| golang-migrate from commit 1 | Task 3 |
