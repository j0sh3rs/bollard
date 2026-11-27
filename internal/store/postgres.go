package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations_pg/*.sql
var migrationsPgFS embed.FS

type postgresStore struct {
	db  *sql.DB
	tag string
}

// NewPostgres opens a Postgres database at the given DSN, runs migrations, and
// returns a Store implementation.
func NewPostgres(dsn, instanceTag string) (Store, error) {
	if err := validateTag(instanceTag); err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	db.SetMaxOpenConns(10)
	if err := runPostgresMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate postgres: %w", err)
	}
	return &postgresStore{db: db, tag: instanceTag}, nil
}

func runPostgresMigrations(db *sql.DB) error {
	src, err := iofs.New(migrationsPgFS, "migrations_pg")
	if err != nil {
		return err
	}
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		return err
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx", driver)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

// nullTime maps the zero time to SQL NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func (s *postgresStore) Create(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO records (`+recordCols+`, instance_tag)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.ContainerID, r.Hostname, r.IP, r.RecordType, r.TTL, r.UnifiRecordID, r.Target,
		nullTime(r.DrainUntil), r.CreatedAt.UTC(), r.UpdatedAt.UTC(), s.tag,
	)
	if err != nil {
		return fmt.Errorf("store: create record: %w", err)
	}
	return nil
}

func (s *postgresStore) Update(ctx context.Context, r Record) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE records SET container_id = $1, ip = $2, ttl = $3, unifi_record_id = $4, target = $5, drain_until = $6, updated_at = $7
		 WHERE id = $8 AND instance_tag = $9`,
		r.ContainerID, r.IP, r.TTL, r.UnifiRecordID, r.Target, nullTime(r.DrainUntil), r.UpdatedAt.UTC(), r.ID, s.tag,
	)
	if err != nil {
		return fmt.Errorf("store: update record: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: update record: %w", err)
	} else if n == 0 {
		return errNoRow(r.ID)
	}
	return nil
}

func (s *postgresStore) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE id = $1 AND instance_tag = $2`, id, s.tag)
	return err
}

func (s *postgresStore) ListByContainerID(ctx context.Context, containerID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+recordCols+` FROM records WHERE container_id = $1 AND instance_tag = $2 ORDER BY hostname`,
		containerID, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: list by container id: %w", err)
	}
	return collect(rows, scanPgRecord)
}

func (s *postgresStore) DeleteByContainerID(ctx context.Context, containerID string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM records WHERE container_id = $1 AND instance_tag = $2 RETURNING `+recordCols,
		containerID, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: delete by container id: %w", err)
	}
	return collect(rows, scanPgRecord)
}

func (s *postgresStore) ListAll(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+recordCols+` FROM records WHERE instance_tag = $1 ORDER BY hostname, container_id`, s.tag)
	if err != nil {
		return nil, fmt.Errorf("store: list all: %w", err)
	}
	return collect(rows, scanPgRecord)
}

func (s *postgresStore) Close() error {
	return s.db.Close()
}

// scanPgRecord scans a Postgres row directly into time.Time fields (TIMESTAMPTZ
// round-trips cleanly without string intermediaries).
func scanPgRecord(s scanner) (Record, error) {
	var r Record
	var drain sql.NullTime
	var createdAt, updatedAt time.Time
	err := s.Scan(
		&r.ID, &r.ContainerID, &r.Hostname, &r.IP, &r.RecordType,
		&r.TTL, &r.UnifiRecordID, &r.Target, &drain, &createdAt, &updatedAt,
	)
	if err != nil {
		return Record{}, fmt.Errorf("store: scan record: %w", err)
	}
	if drain.Valid {
		r.DrainUntil = drain.Time.UTC()
	}
	r.CreatedAt = createdAt.UTC()
	r.UpdatedAt = updatedAt.UTC()
	return r, nil
}
